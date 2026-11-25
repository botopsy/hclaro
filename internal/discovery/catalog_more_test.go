package discovery

import (
	"encoding/json"
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
