package discovery

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func parseFixture(t *testing.T, typ, file string) []CloudResourceCandidate {
	t.Helper()
	rt, ok := TypeByName(typ)
	if !ok {
		t.Fatalf("%s is not a supported type", typ)
	}
	out, next, err := rt.parse(fixture(file), parseContext{Region: "eu-west-1", Account: account, Partition: "aws", Observed: "2026-11-09T09:00:00Z"})
	if err != nil || next != "" {
		t.Fatalf("%s: %v %q", typ, err, next)
	}
	return out
}

func TestLaterTypesParseIdentityAndSafeFactsOnly(t *testing.T) {
	cases := []struct {
		typ, file string
		count     int
		importID  string
		detail    [2]string
	}{
		{"aws_instance", "ec2-describe-instances.json", 1, "i-0a1b2c3d4e5f60001", [2]string{"instanceType", "t3.small"}}, // terminated one is skipped
		{"aws_ebs_volume", "ec2-describe-volumes.json", 1, "vol-0a1b2c3d4e5f60001", [2]string{"sizeGiB", "50"}},
		{"aws_internet_gateway", "ec2-describe-internet-gateways.json", 1, "igw-0a1b2c3d4e5f60001", [2]string{"vpc", "vpc-0a1b2c3d4e5f60001"}},
		{"aws_route_table", "ec2-describe-route-tables.json", 1, "rtb-0a1b2c3d4e5f60001", [2]string{"vpc", "vpc-0a1b2c3d4e5f60001"}},
		{"aws_nat_gateway", "ec2-describe-nat-gateways.json", 1, "nat-0a1b2c3d4e5f60001", [2]string{"connectivity", "public"}}, // deleted one is skipped
		{"aws_lb", "elbv2-describe-load-balancers.json", 1, "arn:aws:elasticloadbalancing:eu-west-1:111122223333:loadbalancer/app/core-alb/50dc6c495c0c9188", [2]string{"type", "application"}},
		{"aws_lambda_function", "lambda-list-functions.json", 1, "orders-webhook", [2]string{"runtime", "python3.12"}},
		{"aws_dynamodb_table", "dynamodb-list-tables.json", 2, "orders", [2]string{}},
		{"aws_iam_role", "iam-list-roles.json", 2, "orders-task-role", [2]string{}},
		{"aws_sqs_queue", "sqs-list-queues.json", 2, "https://sqs.eu-west-1.amazonaws.com/111122223333/orders-events", [2]string{}},
		{"aws_sns_topic", "sns-list-topics.json", 2, "arn:aws:sns:eu-west-1:111122223333:orders-alerts", [2]string{}},
		{"aws_kms_key", "kms-list-aliases.json", 1, "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9", [2]string{}}, // AWS-managed, duplicate and unattached aliases are skipped
		{"aws_cloudwatch_log_group", "logs-describe-log-groups.json", 2, "/aws/lambda/orders-webhook", [2]string{"retentionDays", "30"}},
		{"aws_ecr_repository", "ecr-describe-repositories.json", 2, "orders/api", [2]string{"tagMutability", "IMMUTABLE"}},
		{"aws_eks_cluster", "eks-list-clusters.json", 2, "core-eks", [2]string{}},
		{"aws_ecs_cluster", "ecs-list-clusters.json", 2, "orders", [2]string{}},
		{"aws_route53_zone", "route53-list-hosted-zones.json", 2, "Z0123456789ABCDEFGHIJ", [2]string{"records", "12"}},
		{"aws_acm_certificate", "acm-list-certificates.json", 2, "arn:aws:acm:eu-west-1:111122223333:certificate/0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9", [2]string{"status", "ISSUED"}},
		{"aws_secretsmanager_secret", "secretsmanager-list-secrets.json", 1, "arn:aws:secretsmanager:eu-west-1:111122223333:secret:orders/db-AbCdEf", [2]string{}}, // the secret scheduled for deletion is skipped
		{"aws_ssm_parameter", "ssm-describe-parameters.json", 2, "/orders/api/endpoint", [2]string{"type", "String"}},
		{"aws_sfn_state_machine", "stepfunctions-list-state-machines.json", 2, "arn:aws:states:eu-west-1:111122223333:stateMachine:orders-workflow", [2]string{"type", "STANDARD"}},
		{"aws_api_gateway_rest_api", "apigateway-get-rest-apis.json", 2, "a1b2c3d4e5", [2]string{"endpoint", "REGIONAL"}},
		{"aws_cloudfront_distribution", "cloudfront-list-distributions.json", 2, "E1A2B3C4D5E6F7", [2]string{"status", "Deployed"}},
		{"aws_elastic_beanstalk_application", "elasticbeanstalk-describe-applications.json", 2, "orders-web", [2]string{}},
		{"aws_elastic_beanstalk_environment", "elasticbeanstalk-describe-environments.json", 1, "e-abcd1efghi", [2]string{"health", "Green"}}, // the terminated one is skipped
		{"aws_kinesis_stream", "kinesis-list-streams.json", 2, "orders-events", [2]string{}},
	}
	for _, c := range cases {
		got := parseFixture(t, c.typ, c.file)
		if len(got) != c.count {
			t.Fatalf("%s: %d candidates, want %d", c.typ, len(got), c.count)
		}
		first := got[0]
		if first.ImportStatus != ImportIDVerified || first.ImportID != c.importID || first.Type != c.typ {
			t.Errorf("%s: %+v", c.typ, first)
		}
		if c.detail[0] != "" && first.Details[c.detail[0]] != c.detail[1] {
			t.Errorf("%s: details %v, want %s=%s", c.typ, first.Details, c.detail[0], c.detail[1])
		}
		// The fixtures plant a canary in fields that must never be decoded (user data, DNS
		// name, environment variables, trust policy).
		raw, _ := json.Marshal(got)
		for _, leak := range []string{"DO_NOT_LEAK", "UserData", "10.0.1.10", "Environment", "AssumeRolePolicy"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s leaks %q: %s", c.typ, leak, raw)
			}
		}
	}
}

func TestIAMRolesAreGlobalAndServiceLinkedRolesAreMarked(t *testing.T) {
	roles := parseFixture(t, "aws_iam_role", "iam-list-roles.json")
	if len(roles[0].Regions) != 0 || roles[0].Details["serviceLinked"] != "" {
		t.Fatalf("%+v", roles[0])
	}
	if roles[1].Details["serviceLinked"] != "true" || roles[1].Account != account {
		t.Fatalf("%+v", roles[1])
	}
}

func TestLaterTypesWithoutADocumentedImportIDStayUnresolved(t *testing.T) {
	rt, _ := TypeByName("aws_instance")
	page := `{"Reservations":[{"Instances":[{"InstanceId":"not-an-instance","State":{"Name":"running"}}]}]}`
	out, _, err := rt.parse([]byte(page), parseContext{Region: "eu-west-1", Account: account, Partition: "aws"})
	if err != nil || len(out) != 1 || out[0].ImportStatus != ImportIDUnresolved || out[0].ImportID != "" {
		t.Fatalf("%+v %v", out, err)
	}
	lb, _ := TypeByName("aws_lb")
	// A classic-style or malformed ARN is not an aws_lb import ID.
	out, _, err = lb.parse([]byte(`{"LoadBalancers":[{"LoadBalancerArn":"arn:aws:elasticloadbalancing:eu-west-1:111122223333:loadbalancer/classic","Type":"application"}]}`), parseContext{Region: "eu-west-1", Account: account, Partition: "aws"})
	if err != nil || len(out) != 1 || out[0].ImportStatus != ImportIDUnresolved {
		t.Fatalf("%+v %v", out, err)
	}
	if _, _, err := rt.parse([]byte(`{`), parseContext{}); err == nil {
		t.Fatal("malformed page accepted")
	}
}

func TestLaterTypesMatchStateByDocumentedIDs(t *testing.T) {
	for typ, attrs := range map[string]map[string]any{
		"aws_instance":        {"id": "i-0a1b2c3d4e5f60001"},
		"aws_lb":              {"id": "arn:aws:elasticloadbalancing:eu-west-1:111122223333:loadbalancer/app/core-alb/50dc6c495c0c9188"},
		"aws_lambda_function": {"id": "orders-webhook", "function_name": "orders-webhook"},
		"aws_iam_role":        {"id": "orders-task-role", "name": "orders-task-role"},
	} {
		ids, ok := StateIDs(typ, attrs)
		if !ok || len(ids) == 0 {
			t.Errorf("%s: %v %v", typ, ids, ok)
		}
	}
}

func TestKMSKeysComeFromCustomerAliasesOnly(t *testing.T) {
	keys := parseFixture(t, "aws_kms_key", "kms-list-aliases.json")
	if len(keys) != 1 || keys[0].Name != "orders-data" || keys[0].ARN != "arn:aws:kms:eu-west-1:111122223333:key/0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9" {
		t.Fatalf("%+v", keys)
	}
}

func TestQueuesTopicsAndLogGroupsFillAccountARNAndFifo(t *testing.T) {
	q := parseFixture(t, "aws_sqs_queue", "sqs-list-queues.json")
	if q[0].ARN != "arn:aws:sqs:eu-west-1:111122223333:orders-events" || q[0].Name != "orders-events" || q[1].Details["fifo"] != "true" {
		t.Fatalf("%+v", q)
	}
	n := parseFixture(t, "aws_sns_topic", "sns-list-topics.json")
	if n[0].Account != account || n[1].Details["fifo"] != "true" {
		t.Fatalf("%+v", n)
	}
	// The CLI reports a log group ARN with a trailing ":*", which is not part of the ARN.
	g := parseFixture(t, "aws_cloudwatch_log_group", "logs-describe-log-groups.json")
	if g[0].ARN != "arn:aws:logs:eu-west-1:111122223333:log-group:/aws/lambda/orders-webhook" {
		t.Fatalf("%+v", g[0])
	}
}

func TestBatchThreeRefusesUnexpectedIdentifiers(t *testing.T) {
	ctx := parseContext{Region: "eu-west-1", Account: account, Partition: "aws"}
	for typ, page := range map[string]string{
		"aws_sqs_queue":            `{"QueueUrls":["https://example.com/111122223333/q"]}`,
		"aws_sns_topic":            `{"Topics":[{"TopicArn":"not-an-arn"}]}`,
		"aws_cloudwatch_log_group": `{"logGroups":[{"logGroupName":"bad name with spaces"}]}`,
	} {
		rt, _ := TypeByName(typ)
		out, _, err := rt.parse([]byte(page), ctx)
		if err != nil || len(out) != 1 || out[0].ImportStatus != ImportIDUnresolved {
			t.Errorf("%s: %+v %v", typ, out, err)
		}
	}
}

func TestBatchFourDerivesImportIDsAndKeepsZonesGlobal(t *testing.T) {
	// aws_ecs_cluster is imported by name, but ecs list-clusters returns ARNs.
	c := parseFixture(t, "aws_ecs_cluster", "ecs-list-clusters.json")
	if c[0].ImportID != "orders" || c[0].ARN != "arn:aws:ecs:eu-west-1:111122223333:cluster/orders" || c[0].Name != "orders" {
		t.Fatalf("%+v", c[0])
	}
	// The provider records the ARN as the id and the name as `name`; only the name is compared.
	if ids, ok := StateIDs("aws_ecs_cluster", map[string]any{"id": "arn:aws:ecs:eu-west-1:111122223333:cluster/orders", "name": "orders"}); !ok || len(ids) != 1 || ids[0] != "orders" {
		t.Fatalf("%v %v", ids, ok)
	}
	z := parseFixture(t, "aws_route53_zone", "route53-list-hosted-zones.json")
	if len(z[0].Regions) != 0 || z[0].Name != "example.com" || z[0].Details["private"] != "" || z[1].Details["private"] != "true" {
		t.Fatalf("%+v", z)
	}
	if z[0].ARN != "arn:aws:route53:::hostedzone/Z0123456789ABCDEFGHIJ" {
		t.Fatalf("%+v", z[0])
	}
}

func TestBatchFourRefusesUnexpectedIdentifiers(t *testing.T) {
	ctx := parseContext{Region: "eu-west-1", Account: account, Partition: "aws"}
	for typ, page := range map[string]string{
		"aws_ecr_repository": `{"repositories":[{"repositoryName":"Upper/Case"}]}`,
		"aws_eks_cluster":    `{"clusters":["-bad"]}`,
		"aws_ecs_cluster":    `{"clusterArns":["arn:aws:ecs:eu-west-1:111122223333:service/x/y"]}`,
		"aws_route53_zone":   `{"HostedZones":[{"Id":"/hostedzone/lowercase"}]}`,
	} {
		rt, _ := TypeByName(typ)
		out, _, err := rt.parse([]byte(page), ctx)
		if err != nil || len(out) != 1 || out[0].ImportStatus != ImportIDUnresolved {
			t.Errorf("%s: %+v %v", typ, out, err)
		}
	}
}

func TestBatchFiveNamesARNsAndSkipsDeletedSecrets(t *testing.T) {
	sec := parseFixture(t, "aws_secretsmanager_secret", "secretsmanager-list-secrets.json")
	if len(sec) != 1 || sec[0].Name != "orders-db" || sec[0].Account != account {
		t.Fatalf("%+v", sec)
	}
	p := parseFixture(t, "aws_ssm_parameter", "ssm-describe-parameters.json")
	if p[0].ARN != "arn:aws:ssm:eu-west-1:111122223333:parameter/orders/api/endpoint" || p[1].ARN != "arn:aws:ssm:eu-west-1:111122223333:parameter/orders-flag" || p[1].Details["type"] != "SecureString" {
		t.Fatalf("%+v", p)
	}
	acm := parseFixture(t, "aws_acm_certificate", "acm-list-certificates.json")
	if acm[0].Name != "orders.example.com" || acm[1].Details["type"] != "IMPORTED" {
		t.Fatalf("%+v", acm)
	}
}

func TestBatchFiveRefusesUnexpectedIdentifiers(t *testing.T) {
	ctx := parseContext{Region: "eu-west-1", Account: account, Partition: "aws"}
	for typ, page := range map[string]string{
		"aws_acm_certificate":       `{"CertificateSummaryList":[{"CertificateArn":"not-an-arn"}]}`,
		"aws_secretsmanager_secret": `{"SecretList":[{"ARN":"arn:aws:secretsmanager:eu-west-1:111122223333:secret:nosuffix","Name":"x"}]}`,
		"aws_ssm_parameter":         `{"Parameters":[{"Name":"has space"}]}`,
		"aws_sfn_state_machine":     `{"stateMachines":[{"stateMachineArn":"arn:aws:states:eu-west-1:111122223333:execution:x:y"}]}`,
	} {
		rt, _ := TypeByName(typ)
		out, _, err := rt.parse([]byte(page), ctx)
		if err != nil || len(out) != 1 || out[0].ImportStatus != ImportIDUnresolved {
			t.Errorf("%s: %+v %v", typ, out, err)
		}
	}
}

func TestBatchSixGlobalDistributionsAndEnvironmentFacts(t *testing.T) {
	d := parseFixture(t, "aws_cloudfront_distribution", "cloudfront-list-distributions.json")
	if len(d[0].Regions) != 0 || d[0].Account != account || d[0].Details["enabled"] != "true" || d[1].Details["enabled"] != "false" || d[0].Name != "d1a2b3c4d5e6f7.cloudfront.net" {
		t.Fatalf("%+v", d)
	}
	e := parseFixture(t, "aws_elastic_beanstalk_environment", "elasticbeanstalk-describe-environments.json")
	if e[0].Details["application"] != "orders-web" || e[0].Name != "orders-web-prod" {
		t.Fatalf("%+v", e)
	}
	// Distributions need no list in `Items` when there are none.
	rt, _ := TypeByName("aws_cloudfront_distribution")
	if out, _, err := rt.parse([]byte(`{"DistributionList":{"Quantity":0}}`), parseContext{}); err != nil || len(out) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestBatchSixRefusesUnexpectedIdentifiers(t *testing.T) {
	ctx := parseContext{Region: "eu-west-1", Account: account, Partition: "aws"}
	for typ, page := range map[string]string{
		"aws_api_gateway_rest_api":          `{"items":[{"id":"UPPER"}]}`,
		"aws_cloudfront_distribution":       `{"DistributionList":{"Items":[{"Id":"not-a-dist"}]}}`,
		"aws_elastic_beanstalk_application": `{"Applications":[{"ApplicationName":"bad/name"}]}`,
		"aws_elastic_beanstalk_environment": `{"Environments":[{"EnvironmentId":"env-1","Status":"Ready"}]}`,
		"aws_kinesis_stream":                `{"StreamNames":["has space"]}`,
	} {
		rt, _ := TypeByName(typ)
		out, _, err := rt.parse([]byte(page), ctx)
		if err != nil || len(out) != 1 || out[0].ImportStatus != ImportIDUnresolved {
			t.Errorf("%s: %+v %v", typ, out, err)
		}
	}
}

// Beanstalk's list calls have no paginator, so --max-items must not be sent; a list longer
// than the per-type limit is cut and marked incomplete.
func TestUnpagedTypesSkipMaxItemsAndMarkACutListIncomplete(t *testing.T) {
	r := &fakeRunner{}
	v := scanAll(t, newService(r), DiscoveryScope{Regions: []string{"eu-west-1"}, Types: []string{"aws_elastic_beanstalk_application"}, MaxPerType: 1}, snapshot())
	for _, args := range r.recorded() {
		if len(args) > 1 && args[0] == "elasticbeanstalk" && slices.Contains(args, "--max-items") {
			t.Fatalf("unpaged call sent --max-items: %v", args)
		}
	}
	if v.Status != ScanPartial || !v.Tasks[0].Truncated || v.Tasks[0].Count != 1 || len(v.Results) != 1 {
		t.Fatalf("status %s task %+v results %d", v.Status, v.Tasks[0], len(v.Results))
	}
	for _, typ := range Types {
		if typ.noPaging && (typ.Service != "elasticbeanstalk") {
			t.Errorf("%s is unpaged; only Beanstalk is expected to be", typ.Type)
		}
	}
}
