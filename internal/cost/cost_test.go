package cost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/cost"
	"hclaro/internal/cost/costtest"
	"hclaro/internal/plan"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func allActionsChanges(t *testing.T) []cost.PlanChange {
	t.Helper()
	review, err := plan.Parse(fixtures.PlanAllActions)
	if err != nil {
		t.Fatal(err)
	}
	return cost.ChangesOf(review)
}

// richPlan is a synthetic plan document with every part Minimize must filter.
const richPlan = `{
  "format_version": "1.2",
  "terraform_version": "1.9.5",
  "variables": {"db_password": {"value": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}},
  "planned_values": {
    "outputs": {"conn": {"sensitive": false, "value": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}},
    "root_module": {
      "resources": [{
        "address": "aws_instance.web", "mode": "managed", "type": "aws_instance", "name": "web",
        "provider_name": "registry.terraform.io/hashicorp/aws", "schema_version": 1,
        "values": {
          "instance_type": "m5.large", "ami": "ami-0abc1234", "owner_id": "444455556666",
          "iam_instance_profile": "arn:aws:iam::444455556666:instance-profile/web", "placement_group": "arn:aws:ec2:eu-west-1:444455556666:placement-group/pg",
          "image_uri": "444455556666.dkr.ecr.eu-west-1.amazonaws.com/app:1", "queue": "https://sqs.eu-west-1.amazonaws.com/444455556666/q", "bucket": "logs-444455556666-777788889999", "build": "1234567890123",
          "tags": {"Owner": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}, "description": "free text DO_NOT_LEAK",
          "user_data": "DO_NOT_LEAK_HCLARO_TEST_SECRET", "marked": "hidden-by-marker-value",
          "random": "kX9fQ2LmZp7RtY4vBn8WcE3s", "long": "` + "%LONG%" + `",
          "root_block_device": [{"volume_size": 50, "volume_type": "gp3"}],
          "setting": [{"name": "DB_PASSWORD", "value": "plain-pair-value"}, {"name": "InstanceType", "value": "t3.large"}],
          "ebs_block_device": [{"volume_size": 10, "kms_key_id": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}]
        },
        "sensitive_values": {"marked": true}
      }],
      "child_modules": [{
        "address": "module.db",
        "resources": [{"address": "module.db.aws_db_instance.main", "mode": "managed", "type": "aws_db_instance", "name": "main",
          "values": {"instance_class": "db.r6g.large", "allocated_storage": 100, "password": "DO_NOT_LEAK_HCLARO_TEST_SECRET", "engine": "postgres"},
          "sensitive_values": {}}]
      }]
    }
  },
  "prior_state": {
    "format_version": "1.0", "terraform_version": "1.9.5",
    "values": {
      "outputs": {"conn": {"value": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}},
      "root_module": {"resources": [{"address": "aws_instance.web", "mode": "managed", "type": "aws_instance", "name": "web",
        "values": {"instance_type": "t3.large", "secret_string": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}, "sensitive_values": {}}]}
    }
  },
  "resource_changes": [{
    "address": "aws_instance.web", "mode": "managed", "type": "aws_instance", "name": "web",
    "provider_name": "registry.terraform.io/hashicorp/aws",
    "change": {"actions": ["update"],
      "before": {"instance_type": "t3.large", "connection": {"host": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}},
      "after": {"instance_type": "m5.large", "marked": "hidden-by-marker-value"},
      "after_sensitive": {"marked": true}, "before_sensitive": {},
      "after_unknown": {"id": true}}
  }],
  "output_changes": {"conn": {"actions": ["update"], "after": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}},
  "resource_drift": [{"address": "aws_instance.web", "change": {"after": {"x": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}}}],
  "relevant_attributes": [{"resource": "aws_instance.web", "attribute": ["DO_NOT_LEAK_HCLARO_TEST_SECRET"]}],
  "checks": [{"address": {"to_display": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}}],
  "configuration": {
    "provider_config": {"aws": {"name": "aws", "full_name": "registry.terraform.io/hashicorp/aws",
      "expressions": {"region": {"constant_value": "eu-west-1"}, "access_key": {"constant_value": "DO_NOT_LEAK_HCLARO_TEST_SECRET"},
        "assume_role": [{"role_arn": {"constant_value": "arn:aws:iam::111122223333:role/DO_NOT_LEAK"}}]}}},
    "root_module": {
      "resources": [{"address": "aws_eip.web", "mode": "managed", "type": "aws_eip", "name": "web", "provider_config_key": "aws",
        "expressions": {"instance": {"references": ["aws_instance.web.id", "aws_instance.web"]}, "tags": {"constant_value": {"x": "DO_NOT_LEAK_HCLARO_TEST_SECRET"}}}}],
      "module_calls": {"db": {"source": "git::https://user:DO_NOT_LEAK_HCLARO_TEST_SECRET@example.com/db.git", "module": {"resources": []}}}
    }
  }
}`

func TestMinimizeKeepsPricingDataAndDropsSecrets(t *testing.T) {
	doc := strings.Replace(richPlan, "%LONG%", strings.Repeat("a", 2000), 1)
	out, err := cost.Minimize([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, leak := range []string{canary, "DO_NOT_LEAK", "hidden-by-marker-value", "kX9fQ2LmZp7RtY4vBn8WcE3s", "aaaaaaaaaa", "example.com", "variables", "output_changes", "resource_drift", "relevant_attributes", "checks", "outputs", "assume_role", "access_key", "kms_key_id", "free text", "plain-pair-value", "444455556666", "777788889999"} {
		if strings.Contains(text, leak) {
			t.Errorf("minimized input contains %q:\n%s", leak, text)
		}
	}
	for _, keep := range []string{`"m5.large"`, `"t3.large"`, `"db.r6g.large"`, `"eu-west-1"`, `"aws_instance.web.id"`, `"volume_size":50`, `"gp3"`, `"postgres"`, `"module.db.aws_db_instance.main"`, `"actions":["update"]`, `"ami-0abc1234"`, `"owner_id":"000000000000"`, `"arn:aws:ec2:eu-west-1:000000000000:placement-group/pg"`, `"000000000000.dkr.ecr.eu-west-1.amazonaws.com/app:1"`, `"https://sqs.eu-west-1.amazonaws.com/000000000000/q"`, `"logs-000000000000-000000000000"`, `"1234567890123"`} {
		if !strings.Contains(text, keep) {
			t.Errorf("minimized input lost %s:\n%s", keep, text)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	// A dropped list element keeps its index: the second block has only its size.
	if !strings.Contains(text, `"ebs_block_device":[{"volume_size":10}]`) {
		t.Errorf("nested block not filtered element-wise: %s", text)
	}
}

// Name/value pairs take their risk from the sibling name in any letter case and at any
// depth; pairs with ordinary names keep the pricing value.
func TestMinimizeNameValuePairs(t *testing.T) {
	setting := `"setting": [{"name": "DB_PASSWORD", "value": "plain-pair-value"}, {"name": "InstanceType", "value": "t3.large"}]`
	cases := map[string]struct {
		setting    string
		leak, keep []string
	}{
		"case variants": {
			setting: `"setting": [{"Name": "db-password", "Value": "leak-a"}, {"NAME": "ApiToken", "VALUE": "leak-b"}, {"Key": "signingKey", "value": "leak-c"}]`,
			leak:    []string{"leak-a", "leak-b", "leak-c"},
			keep:    []string{`"Name":"db-password"`, `"NAME":"ApiToken"`},
		},
		"nested pairs and object values": {
			setting: `"setting": [{"block": [{"options": {"name": "client_secret", "value": {"inner": "leak-d"}}}]}, {"name": "license_key", "value": ["leak-e"]}]`,
			leak:    []string{"leak-d", "leak-e"},
			keep:    []string{`"name":"client_secret"`, `"name":"license_key"`},
		},
		"pairs inside JSON-encoded strings": {
			setting: `"container_definitions": "[{\"environment\":[{\"name\":\"DB_PASSWORD\",\"value\":\"leak-f\"}]}]", "task_spec": "{\"env\":[{\"Key\":\"ApiToken\",\"Value\":\"leak-g\"}]}", "options": "[{\"name\":\"Region\",\"value\":\"eu-north-1\"}]"`,
			leak:    []string{"leak-f", "leak-g"},
			keep:    []string{`"options":"[{\"name\":\"Region\",\"value\":\"eu-north-1\"}]"`},
		},
		// Text that only looks like JSON cannot be checked for embedded secrets, so it is dropped.
		"JSON-looking strings that are not one document": {
			setting: `"a": "{\"name\":\"DB_PASSWORD\",\"value\":\"leak-h\"", "b": "[{\"name\":\"DB_PASSWORD\",\"value\":\"leak-i\"}", "c": "{\"x\":1} {\"name\":\"DB_PASSWORD\",\"value\":\"leak-j\"}", "d": " {\"cpu\":256} "`,
			leak:    []string{"leak-h", "leak-i", "leak-j"},
			keep:    []string{`"d":" {\"cpu\":256} "`},
		},
		"JSON-looking strings nested in a valid document": {
			setting: `"e": "{\"x\":\"{\\\"name\\\":\\\"DB_PASSWORD\\\",\\\"value\\\":\\\"leak-k\\\"\"}", "f": "{\"x\":\"[1,2]\"}"`,
			leak:    []string{"leak-k"},
			keep:    []string{`"f":"{\"x\":\"[1,2]\"}"`},
		},
		"harmless pricing pairs": {
			setting: `"setting": [{"namespace": "aws:autoscaling:launchconfiguration", "name": "InstanceType", "value": "c5.xlarge"}, {"key": "MinSize", "value": "2"}, {"name": "Region", "value": "eu-west-1"}]`,
			keep:    []string{`"value":"c5.xlarge"`, `"value":"2"`, `"name":"Region","value":"eu-west-1"`},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := strings.Replace(strings.Replace(richPlan, "%LONG%", "x", 1), setting, c.setting, 1)
			if !strings.Contains(doc, c.setting) {
				t.Fatal("fixture replacement failed")
			}
			out, err := cost.Minimize([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			for _, leak := range c.leak {
				if strings.Contains(text, leak) {
					t.Errorf("minimized input contains %q:\n%s", leak, text)
				}
			}
			for _, keep := range c.keep {
				if !strings.Contains(text, keep) {
					t.Errorf("minimized input lost %s:\n%s", keep, text)
				}
			}
		})
	}
}

func TestMinimizeAllActionsFixtureHasNoCanary(t *testing.T) {
	out, err := cost.Minimize(fixtures.PlanAllActions)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(canary)) || bytes.Contains(out, []byte("DO_NOT_LEAK")) {
		t.Fatalf("canary in minimized all-actions plan: %s", out)
	}
	if !bytes.Contains(out, []byte(`"t3.small"`)) {
		t.Fatalf("instance type missing: %s", out)
	}
}

func TestMinimizeRefusesOtherDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"state":    string(fixtures.State),
		"trailing": `{"format_version":"1.2","resource_changes":[]} {}`,
		"format":   `{"format_version":"2.0","resource_changes":[]}`,
		"array":    `[]`,
		"empty":    ``,
	} {
		if _, err := cost.Minimize([]byte(doc)); !errors.Is(err, cost.ErrInputFormat) {
			t.Errorf("%s: err = %v, want ErrInputFormat", name, err)
		}
	}
}

func TestAllowlist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hclaro-cost-12345")
	good := filepath.Join(dir, "plan.json")
	if !cost.Allowed([]string{"--version"}) {
		t.Error("--version refused")
	}
	if !cost.Allowed([]string{"breakdown", "--path", good, "--format", "json", "--no-color", "--log-level=warn"}) {
		t.Error("breakdown refused")
	}
	for _, args := range [][]string{
		{},
		{"upload", "--path", good},
		{"comment", "github", "--path", good},
		{"auth", "login"},
		{"configure", "set", "api_key", "x"},
		{"breakdown", "--path", good, "--format", "json", "--no-color"},
		{"breakdown", "--path", good, "--format", "table", "--no-color", "--log-level=warn"},
		{"breakdown", "--path", good, "--format", "json", "--no-color", "--usage-file=u.yml"},
		{"breakdown", "--path", "plan.json", "--format", "json", "--no-color", "--log-level=warn"},
		{"breakdown", "--path", filepath.Join(t.TempDir(), "plan.json"), "--format", "json", "--no-color", "--log-level=warn"},
		{"breakdown", "--path", filepath.Join(dir, "..", "x", "plan.json"), "--format", "json", "--no-color", "--log-level=warn"},
		{"breakdown", "--path", filepath.Join(dir, "state.json"), "--format", "json", "--no-color", "--log-level=warn"},
		{"breakdown", "--path", good, "--format", "json", "--no-color", "--log-level=warn", "--sync-usage-file"},
		{"diff", "--path", good, "--format", "json", "--no-color", "--log-level=warn"},
	} {
		if cost.Allowed(args) {
			t.Errorf("allowed %q", args)
		}
	}
}

func TestEnvironPassesOnlyInfracostSettings(t *testing.T) {
	env := cost.Environ([]string{
		"PATH=/bin", "HOME=/home/u", "AWS_SECRET_ACCESS_KEY=" + canary, "AWS_PROFILE=prod", "TF_VAR_password=" + canary,
		"GOOGLE_CREDENTIALS=" + canary, "ARM_CLIENT_SECRET=" + canary, "INFRACOST_TERRAFORM_CLOUD_TOKEN=" + canary,
		"INFRACOST_API_KEY=ico-synthetic", "INFRACOST_ENABLE_CLOUD=true", "HTTPS_PROXY=http://proxy:3128", "TF_TOKEN_app_terraform_io=" + canary,
	})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, canary) || strings.Contains(joined, "AWS_PROFILE") || strings.Contains(joined, "INFRACOST_ENABLE_CLOUD=true") {
		t.Fatalf("environment leaks: %v", env)
	}
	for _, want := range []string{"PATH=/bin", "HOME=/home/u", "INFRACOST_API_KEY=ico-synthetic", "HTTPS_PROXY=http://proxy:3128", "INFRACOST_ENABLE_CLOUD=false", "INFRACOST_SKIP_UPDATE_CHECK=true", "INFRACOST_ENABLE_DASHBOARD=false"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
}

func TestEndpoint(t *testing.T) {
	if got := cost.Endpoint([]string{}); got != cost.DefaultEndpoint {
		t.Errorf("default endpoint %q", got)
	}
	if got := cost.Endpoint([]string{"INFRACOST_PRICING_API_ENDPOINT=https://user:" + canary + "@prices.example.test:8443/graphql?k=" + canary}); got != "prices.example.test:8443" {
		t.Errorf("custom endpoint %q", got)
	}
}

func TestParseVersion(t *testing.T) {
	if v, err := cost.ParseVersion([]byte("Infracost v0.10.39\n")); err != nil || v != "0.10.39" {
		t.Errorf("v0.10.39: %q %v", v, err)
	}
	for _, out := range []string{"Infracost v0.9.24", "Infracost v1.0.0", "Infracost v0.11.0"} {
		if _, err := cost.ParseVersion([]byte(out)); cost.KindOf(err) != cost.KindUnsupported {
			t.Errorf("%s: %v", out, err)
		}
	}
	if _, err := cost.ParseVersion([]byte("hello")); cost.KindOf(err) != cost.KindMalformed {
		t.Errorf("garbage: %v", err)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"No INFRACOST_API_KEY environment variable is set":          cost.KindAuth,
		"Invalid API key. Run infracost auth login":                 cost.KindAuth,
		"dial tcp: lookup pricing.api.infracost.io: no such host":   cost.KindPricing,
		"Error: something unexpected " + canary:                     cost.KindFailed,
		"Post https://pricing.api.infracost.io: connection refused": cost.KindPricing,
		"": cost.KindFailed,
	}
	for in, want := range cases {
		if got := cost.Classify(in); got != want {
			t.Errorf("Classify(%q) = %s, want %s", in, got, want)
		}
	}
}

func itemByAddress(b cost.Breakdown, address string) *cost.CostLineItem {
	for i := range b.Items {
		if b.Items[i].Address == address {
			return &b.Items[i]
		}
	}
	return nil
}

func strp(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestAggregateFixture(t *testing.T) {
	b, err := cost.Aggregate(fixtures.CostInfracost, allActionsChanges(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.Currency != "USD" || b.MonthlyDelta != "9.89" || b.AddedMonthly != "0.00" || b.RemovedMonthly != "0.00" || b.ChangedDelta != "9.89" || b.PastMonthly != "31.49" || b.ProjectedMonthly != "41.39" {
		t.Fatalf("totals = %+v", b.Totals)
	}
	want := map[string][2]string{
		"module.compute.aws_instance.main[0]":                                      {cost.StatusEstimated, "7.59"},
		`module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]`: {cost.StatusEstimated, "0.00"},
		"aws_db_instance.primary":                                                  {cost.StatusPartial, "2.30"},
		"aws_s3_bucket.logs":                                                       {cost.StatusUsageUnknown, "<nil>"},
		"module.network.aws_subnet.main[0]":                                        {cost.StatusFree, "<nil>"},
		"aws_iam_role.legacy":                                                      {cost.StatusFree, "<nil>"},
		"aws_security_group.web":                                                   {cost.StatusFree, "<nil>"},
		"random_password.unused":                                                   {cost.StatusNotEstimated, "<nil>"},
	}
	if len(b.Items) != len(want) {
		t.Logf("items: %+v", b.Items)
		t.Fatalf("items = %d, want %d", len(b.Items), len(want))
	}
	for address, w := range want {
		item := itemByAddress(b, address)
		if item == nil {
			t.Fatalf("missing %s", address)
		}
		if item.Status != w[0] || strp(item.Delta) != w[1] {
			t.Errorf("%s: status %s delta %s, want %v", address, item.Status, strp(item.Delta), w)
		}
	}
	// No-op, deposed and data resources are not line items.
	if itemByAddress(b, "aws_instance.retired") != nil || itemByAddress(b, "module.network.aws_subnet.main[1]") != nil || itemByAddress(b, "data.aws_caller_identity.current") != nil {
		t.Error("no-op or data source listed")
	}
	// Largest absolute delta first.
	if b.Items[0].Address != "module.compute.aws_instance.main[0]" {
		t.Errorf("first item %s", b.Items[0].Address)
	}
	c := b.Coverage
	if c.Changed != 8 || c.Free != 3 || c.CostBearing != 5 || c.Estimated != 2 || c.Partial != 1 || c.UsageUnknown != 1 || c.NotEstimated != 1 || c.Percent == nil || *c.Percent != 60 {
		t.Errorf("coverage = %+v", c)
	}
	codes := []string{}
	for _, cv := range b.Caveats {
		codes = append(codes, cv.Code)
	}
	for _, code := range []string{"not_a_bill", "not_estimated", "usage_based", "free", "forget", "unattributed"} {
		if !slices.Contains(codes, code) {
			t.Errorf("missing caveat %s in %v", code, codes)
		}
	}
	if slices.Contains(codes, "exchange_rate") {
		t.Error("exchange rate caveat for USD")
	}
	encoded, _ := json.Marshal(b)
	if bytes.Contains(encoded, []byte(canary)) || bytes.Contains(encoded, []byte("aws_nat_gateway.ghost")) {
		t.Errorf("breakdown leaks canary or unattributed address: %s", encoded)
	}
	db := itemByAddress(b, "aws_db_instance.primary")
	if db.UnpricedComponents != 2 || db.Monthly == nil || *db.Monthly != "17.01" || db.PastMonthly == nil || *db.PastMonthly != "14.71" {
		t.Errorf("db item = %+v", db)
	}
	if !slices.ContainsFunc(db.Components, func(c cost.CostComponent) bool { return c.Name == "[withheld]" && c.MonthlyCost == nil }) {
		t.Errorf("canary component not withheld: %+v", db.Components)
	}
	for _, ty := range b.Types {
		if ty.ResourceType == "aws_instance" && (ty.Count != 2 || strp(ty.Delta) != "7.59" || ty.Estimated != 2) {
			t.Errorf("aws_instance summary = %+v (%s)", ty, strp(ty.Delta))
		}
		if ty.ResourceType == "aws_s3_bucket" && (ty.Delta != nil || ty.NotEstimated != 1) {
			t.Errorf("aws_s3_bucket summary = %+v", ty)
		}
	}
}

func report(currency string, resources string, summary string) []byte {
	return []byte(`{"version":"0.2","currency":"` + currency + `","projects":[{"pastBreakdown":{"resources":[]},"breakdown":{"resources":[` + resources + `]},"summary":` + summary + `}]}`)
}

func TestAggregateExactDecimals(t *testing.T) {
	changes := []cost.PlanChange{{Address: "aws_instance.a", Type: "aws_instance", Mode: "managed", Action: plan.Create}, {Address: "aws_instance.b", Type: "aws_instance", Mode: "managed", Action: plan.Create}}
	r := report("USD", `{"name":"aws_instance.a","costComponents":[{"name":"x","unit":"h","monthlyQuantity":"1","price":"0.335","monthlyCost":"0.335"},{"name":"y","unit":"h","monthlyQuantity":"1","price":"0.335","monthlyCost":"0.335"},{"name":"z","unit":"h","monthlyQuantity":"1","price":"0.335","monthlyCost":"0.335"}]},
	{"name":"aws_instance.b","costComponents":[{"name":"x","unit":"h","monthlyQuantity":"1","price":"0.1","monthlyCost":"0.1"},{"name":"y","unit":"h","monthlyQuantity":"1","price":"0.2","monthlyCost":"0.2"}]}`, `{}`)
	b, err := cost.Aggregate(r, changes)
	if err != nil {
		t.Fatal(err)
	}
	// 1.005 rounds half away from zero; float64 sums give 1.0049999….
	if got := strp(itemByAddress(b, "aws_instance.a").Monthly); got != "1.01" {
		t.Errorf("a = %s", got)
	}
	if b.MonthlyDelta != "1.31" || b.AddedMonthly != "1.31" {
		t.Errorf("totals = %+v", b.Totals)
	}
	if b.Coverage.Percent == nil || *b.Coverage.Percent != 100 {
		t.Errorf("coverage = %+v", b.Coverage)
	}
}

func TestAggregateRemovedAndReplaced(t *testing.T) {
	changes := []cost.PlanChange{
		{Address: "aws_instance.old", Type: "aws_instance", Mode: "managed", Action: plan.Delete},
		{Address: "aws_instance.swap", Type: "aws_instance", Mode: "managed", Action: plan.Replace},
		{Address: "aws_instance.half", Type: "aws_instance", Mode: "managed", Action: plan.Update},
	}
	priced := func(name, monthly string) string {
		return `{"name":"` + name + `","costComponents":[{"name":"Instance","unit":"hours","monthlyQuantity":"730","price":"0.01","monthlyCost":"` + monthly + `"}]}`
	}
	r := []byte(`{"version":"0.2","currency":"USD","projects":[{"pastBreakdown":{"resources":[` + priced("aws_instance.old", "30.368") + `,` + priced("aws_instance.swap", "10") + `,` + priced("aws_instance.half", "5") + `]},"breakdown":{"resources":[` + priced("aws_instance.swap", "12.5") + `]}}]}`)
	b, err := cost.Aggregate(r, changes)
	if err != nil {
		t.Fatal(err)
	}
	if b.RemovedMonthly != "30.37" || b.ChangedDelta != "2.50" || b.MonthlyDelta != "-27.87" || b.PastMonthly != "45.37" || b.ProjectedMonthly != "12.50" {
		t.Errorf("totals = %+v", b.Totals)
	}
	if old := itemByAddress(b, "aws_instance.old"); old.Status != cost.StatusEstimated || strp(old.Monthly) != "<nil>" || strp(old.Delta) != "-30.37" {
		t.Errorf("old = %+v", old)
	}
	// An update priced on one side only would be a guess.
	if half := itemByAddress(b, "aws_instance.half"); half.Status != cost.StatusNotEstimated || half.Delta != nil {
		t.Errorf("half = %+v", half)
	}
}

func TestAggregateExcludesUnknownsFromTotals(t *testing.T) {
	changes := []cost.PlanChange{
		{Address: "aws_lambda_function.f", Type: "aws_lambda_function", Mode: "managed", Action: plan.Create},
		{Address: "aws_shield_protection.p", Type: "aws_shield_protection", Mode: "managed", Action: plan.Create},
		{Address: "aws_instance.a", Type: "aws_instance", Mode: "managed", Action: plan.Create},
	}
	r := report("EUR", `{"name":"aws_lambda_function.f","costComponents":[{"name":"Requests","unit":"1M requests","monthlyQuantity":null,"price":"0.2","monthlyCost":null,"usageBased":true}]},
	{"name":"aws_instance.a","costComponents":[{"name":"Instance","unit":"hours","monthlyQuantity":"730","price":"0.01","monthlyCost":"7.3"}]}`, `{"unsupportedResourceCounts":{"aws_shield_protection":1}}`)
	b, err := cost.Aggregate(r, changes)
	if err != nil {
		t.Fatal(err)
	}
	if b.MonthlyDelta != "7.30" || b.Currency != "EUR" {
		t.Errorf("totals = %+v", b.Totals)
	}
	f, p := itemByAddress(b, "aws_lambda_function.f"), itemByAddress(b, "aws_shield_protection.p")
	if f.Status != cost.StatusUsageUnknown || f.Delta != nil || f.Monthly != nil {
		t.Errorf("lambda = %+v", f)
	}
	if p.Status != cost.StatusUnsupported || p.Delta != nil || len(p.Components) != 0 {
		t.Errorf("shield = %+v", p)
	}
	if b.Coverage.UnsupportedType["aws_shield_protection"] != 1 || b.Coverage.Percent == nil || *b.Coverage.Percent != 33 {
		t.Errorf("coverage = %+v", b.Coverage)
	}
	if !slices.ContainsFunc(b.Caveats, func(c cost.Caveat) bool { return c.Code == "exchange_rate" && strings.Contains(c.Message, "EUR") }) {
		t.Errorf("caveats = %+v", b.Caveats)
	}
	// Nothing cost-bearing: no percentage rather than 0% or 100%.
	empty, err := cost.Aggregate(report("USD", ``, `{"noPriceResourceCounts":{"aws_iam_role":1}}`), []cost.PlanChange{{Address: "aws_iam_role.r", Type: "aws_iam_role", Mode: "managed", Action: plan.Create}})
	if err != nil || empty.Coverage.Percent != nil || empty.Coverage.Free != 1 {
		t.Errorf("free-only coverage = %+v %v", empty.Coverage, err)
	}
}

func TestAggregateRefusesMalformedReports(t *testing.T) {
	changes := []cost.PlanChange{{Address: "aws_instance.a", Type: "aws_instance", Mode: "managed", Action: plan.Create}}
	for name, r := range map[string][]byte{
		"currency lower": report("usd", ``, `{}`),
		"currency empty": report("", ``, `{}`),
		"no version":     []byte(`{"currency":"USD","projects":[]}`),
		"exponent":       report("USD", `{"name":"aws_instance.a","costComponents":[{"name":"x","unit":"h","monthlyCost":"1e9"}]}`, `{}`),
		"text cost":      report("USD", `{"name":"aws_instance.a","costComponents":[{"name":"x","unit":"h","monthlyCost":"lots"}]}`, `{}`),
		"trailing":       append(report("USD", ``, `{}`), []byte(" {}")...),
		"not json":       []byte("{"),
	} {
		if _, err := cost.Aggregate(r, changes); cost.KindOf(err) != cost.KindMalformed {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func waitDone(t *testing.T, s *cost.Service, id string) cost.CostEstimate {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e, _, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if e.Status != cost.Running {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("estimate did not finish")
	return cost.CostEstimate{}
}

func input(t *testing.T) cost.Input {
	doc, err := cost.Minimize(fixtures.PlanAllActions)
	if err != nil {
		t.Fatal(err)
	}
	return cost.Input{
		Binding:  cost.Binding{PlanID: strings.Repeat("a", 32), ArtifactSHA256: strings.Repeat("b", 64), Fingerprint: strings.Repeat("c", 64), Engine: "terraform", Workspace: "default"},
		Document: doc, Changes: allActionsChanges(t), ProjectKey: cost.ProjectKey("/synthetic/project"), ProjectName: "project",
	}
}

func service(t *testing.T, runner *costtest.Runner, strict bool) *cost.Service {
	s := cost.New(runner, strict)
	s.TempDir = t.TempDir()
	s.Environ = func() []string { return []string{"PATH=/bin", "AWS_SECRET_ACCESS_KEY=" + canary} }
	t.Cleanup(func() { s.Close(5 * time.Second) })
	return s
}

func TestNoProcessBeforeOptIn(t *testing.T) {
	runner := costtest.New()
	s := service(t, runner, false)
	caps := s.Capabilities()
	if caps.State != cost.StateOff || caps.Enabled || caps.CanEstimate || !caps.Installed || caps.Disclosure.Version != cost.DisclosureVersion || !caps.Disclosure.ExternalCalls || caps.Disclosure.Offline {
		t.Fatalf("caps = %+v", caps)
	}
	if _, err := s.Start(input(t)); cost.KindOf(err) != cost.KindOptIn {
		t.Fatalf("start before opt-in: %v", err)
	}
	if _, err := s.CanEstimate(context.Background()); cost.KindOf(err) != cost.KindOptIn {
		t.Fatalf("CanEstimate before opt-in: %v", err)
	}
	if _, err := s.OptIn(context.Background(), "0"); cost.KindOf(err) != cost.KindOptIn {
		t.Fatalf("opt-in with another disclosure version: %v", err)
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("provider ran before opt-in: %v", calls)
	}
}

func TestStrictLocalMode(t *testing.T) {
	runner := costtest.New()
	s := service(t, runner, true)
	caps := s.Capabilities()
	if caps.State != cost.StateStrictLocal || caps.CanEstimate || !caps.StrictLocal || !strings.Contains(caps.Reason, "strict local-only") {
		t.Fatalf("caps = %+v", caps)
	}
	if _, err := s.OptIn(context.Background(), cost.DisclosureVersion); cost.KindOf(err) != cost.KindStrictLocal {
		t.Fatalf("opt-in: %v", err)
	}
	if _, err := s.Start(input(t)); cost.KindOf(err) != cost.KindStrictLocal {
		t.Fatalf("start: %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatal("provider ran in strict mode")
	}
}

func TestMissingAndUnsupportedCLI(t *testing.T) {
	runner := costtest.New()
	runner.Installed = false
	s := service(t, runner, false)
	if caps := s.Capabilities(); caps.State != cost.StateMissing || caps.Installed {
		t.Fatalf("caps = %+v", caps)
	}
	if _, err := s.OptIn(context.Background(), cost.DisclosureVersion); cost.KindOf(err) != cost.KindMissingCLI {
		t.Fatalf("opt-in missing: %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Fatal("a missing CLI was run")
	}
	runner.Set(func(r *costtest.Runner) { r.Installed, r.Version = true, "Infracost v0.9.24\n" })
	if _, err := s.OptIn(context.Background(), cost.DisclosureVersion); cost.KindOf(err) != cost.KindUnsupported {
		t.Fatalf("opt-in unsupported: %v", err)
	}
	if caps := s.Capabilities(); caps.Enabled {
		t.Fatal("enabled after refused version")
	}
	if calls := runner.Calls(); len(calls) != 1 || calls[0][0] != "--version" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestEstimateEndToEnd(t *testing.T) {
	runner := costtest.New()
	s := service(t, runner, false)
	s.Store = cost.OpenStore(t.TempDir())
	caps, err := s.OptIn(context.Background(), cost.DisclosureVersion)
	if err != nil || caps.State != cost.StateReady || caps.CLIVersion != "0.10.39" {
		t.Fatalf("opt-in: %+v %v", caps, err)
	}
	in := input(t)
	started, err := s.Start(in)
	if err != nil || started.Status != cost.Running {
		t.Fatalf("start: %+v %v", started, err)
	}
	e := waitDone(t, s, started.ID)
	if e.Status != cost.Completed || e.Breakdown == nil || e.Breakdown.MonthlyDelta != "9.89" || e.ProviderVersion != "0.10.39" || e.PricingSource != cost.DefaultEndpoint {
		t.Fatalf("estimate = %+v", e)
	}
	if e.Fingerprint != strings.Repeat("c", 12) || e.ArtifactDigest != strings.Repeat("b", 12) {
		t.Errorf("binding labels = %s %s", e.Fingerprint, e.ArtifactDigest)
	}
	paths, inputs, modes := runner.Paths(), runner.Inputs(), runner.Modes()
	if len(paths) != 1 || !bytes.Equal(inputs[0], in.Document) || bytes.Contains(inputs[0], []byte(canary)) {
		t.Fatalf("input = %v", paths)
	}
	if runtime.GOOS != "windows" && (modes[0] != 0o600 || modes[1] != 0o700) {
		t.Errorf("input modes = %v", modes)
	}
	if _, err := os.Stat(filepath.Dir(paths[0])); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("input directory left behind: %v", err)
	}
	for _, env := range runner.Environs() {
		if strings.Contains(strings.Join(env, "\n"), canary) {
			t.Fatalf("cloud credential passed to provider: %v", env)
		}
	}
	latest, b, err := s.Latest(in.PlanID)
	if err != nil || latest.ID != e.ID || b.Fingerprint != in.Fingerprint {
		t.Fatalf("latest = %+v %+v %v", latest, b, err)
	}
	records := s.Store.List(in.ProjectKey)
	if len(records) != 1 || records[0].MonthlyDelta != "9.89" || records[0].Currency != "USD" || records[0].CoveragePercent == nil || *records[0].CoveragePercent != 60 {
		t.Fatalf("records = %+v", records)
	}
	if other := s.Store.List(cost.ProjectKey("/other")); len(other) != 0 {
		t.Fatalf("other project sees %v", other)
	}
	file, _ := os.ReadFile(filepath.Join(filepath.Dir(sPath(s)), "cost-estimates.json"))
	for _, leak := range []string{canary, "aws_instance", "aws_db_instance", "/synthetic/project", "t3.small"} {
		if bytes.Contains(file, []byte(leak)) {
			t.Errorf("aggregate file contains %q", leak)
		}
	}
	if n, err := s.Store.Clear(in.ProjectKey); err != nil || n != 1 || len(s.Store.List(in.ProjectKey)) != 0 {
		t.Fatalf("clear = %d %v", n, err)
	}
}

// sPath returns the aggregate file path of a store opened in a test directory.
func sPath(s *cost.Service) string { return s.Store.Path() }

func TestEstimateFailures(t *testing.T) {
	for mode, kind := range map[string]string{"auth": cost.KindAuth, "pricing": cost.KindPricing, "malformed": cost.KindMalformed} {
		runner := costtest.New()
		s := service(t, runner, false)
		if _, err := s.OptIn(context.Background(), cost.DisclosureVersion); err != nil {
			t.Fatal(err)
		}
		runner.Set(func(r *costtest.Runner) { r.Mode = mode })
		started, err := s.Start(input(t))
		if err != nil {
			t.Fatal(err)
		}
		e := waitDone(t, s, started.ID)
		if e.Status != cost.Failed || e.Error == nil || e.Error.Code != kind || e.Breakdown != nil || strings.Contains(e.Error.Message, "pricing.api.infracost.io/graphql") {
			t.Errorf("%s: %+v", mode, e)
		}
		if _, err := os.Stat(filepath.Dir(runner.Paths()[0])); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: input left behind", mode)
		}
	}
}

func TestCancelBusyOptOutAndClose(t *testing.T) {
	runner := costtest.New()
	runner.Mode = "block"
	s := service(t, runner, false)
	if _, err := s.OptIn(context.Background(), cost.DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	first, err := s.Start(input(t))
	if err != nil {
		t.Fatal(err)
	}
	<-runner.Started
	if _, err := s.Start(input(t)); cost.KindOf(err) != cost.KindBusy {
		t.Fatalf("second start: %v", err)
	}
	if _, err := s.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	e := waitDone(t, s, first.ID)
	if e.Status != cost.Cancelled || !e.CancelRequested || e.Error.Code != cost.KindCancelled {
		t.Fatalf("cancelled = %+v", e)
	}
	if _, err := os.Stat(filepath.Dir(runner.Paths()[0])); !errors.Is(err, os.ErrNotExist) {
		t.Error("input left behind after cancel")
	}
	if _, err := s.Cancel(strings.Repeat("f", 32)); !errors.Is(err, cost.ErrNotFound) {
		t.Errorf("cancel unknown: %v", err)
	}
	// Opting out cancels a running estimate and turns the feature off.
	second, err := s.Start(input(t))
	if err != nil {
		t.Fatal(err)
	}
	<-runner.Started
	if caps := s.OptOut(); caps.Enabled || caps.State != cost.StateOff {
		t.Fatalf("opt-out caps = %+v", caps)
	}
	if e := waitDone(t, s, second.ID); e.Status != cost.Cancelled {
		t.Fatalf("after opt-out = %+v", e)
	}
	if _, err := s.Start(input(t)); cost.KindOf(err) != cost.KindOptIn {
		t.Fatalf("start after opt-out: %v", err)
	}
	// Shutdown cancels and refuses new work.
	if _, err := s.OptIn(context.Background(), cost.DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	third, err := s.Start(input(t))
	if err != nil {
		t.Fatal(err)
	}
	<-runner.Started
	s.Close(5 * time.Second)
	if e, _, _ := s.Get(third.ID); e.Status != cost.Cancelled {
		t.Fatalf("after close = %+v", e)
	}
	if _, err := s.Start(input(t)); cost.KindOf(err) != cost.KindShutdown {
		t.Fatalf("start after close: %v", err)
	}
}

func TestEmptyInputRefused(t *testing.T) {
	s := service(t, costtest.New(), false)
	in := input(t)
	in.Document = nil
	if _, err := s.Start(in); cost.KindOf(err) != cost.KindNoInput {
		t.Fatalf("start: %v", err)
	}
}

func TestSweepStale(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "hclaro-cost-111")
	fresh := filepath.Join(dir, "hclaro-cost-222")
	other := filepath.Join(dir, "hclaro-plan-333")
	for _, d := range []string{old, fresh, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(other, past, past)
	if n := cost.SweepStale(dir, time.Now()); n != 1 {
		t.Fatalf("swept %d", n)
	}
	for d, exists := range map[string]bool{old: false, fresh: true, other: true} {
		if _, err := os.Stat(d); (err == nil) != exists {
			t.Errorf("%s exists=%v", d, err == nil)
		}
	}
}

func TestStoreNeverReplacesANewerFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cost-estimates.json")
	newer := `{"version":2,"records":[{"id":"future","extra":true}]}`
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	store := cost.OpenStore(dir)
	if got := store.List("x"); len(got) != 0 {
		t.Fatalf("records = %v", got)
	}
	if err := store.Add(cost.AggregateRecord{ID: "1", ProjectKey: "x", Currency: "USD"}); !errors.Is(err, cost.ErrNewerStore) {
		t.Fatalf("add over a newer file: %v", err)
	}
	if n, err := store.Clear("x"); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if b, _ := os.ReadFile(path); string(b) != newer {
		t.Fatal("newer cost-estimates.json replaced")
	}
}

func TestStoreIgnoresUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cost-estimates.json"), []byte(`{"version":1,"records":[{"unknown":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := cost.OpenStore(dir)
	if got := store.List("x"); len(got) != 0 {
		t.Fatalf("records = %v", got)
	}
	if err := store.Add(cost.AggregateRecord{ID: "1", ProjectKey: "x", Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if got := store.List("x"); len(got) != 1 {
		t.Fatalf("records after add = %v", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(store.Path()); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", info.Mode())
		}
	}
}
