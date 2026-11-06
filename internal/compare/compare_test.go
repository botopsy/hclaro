package compare

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/state"
)

func parse(t testing.TB, b []byte) state.Index {
	t.Helper()
	idx, err := state.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func run(t testing.TB, left, right []byte, rules ...Rule) *Result {
	t.Helper()
	c, err := Validate(rules)
	if err != nil {
		t.Fatal(err)
	}
	return Compare(parse(t, left), parse(t, right), c)
}

func statusOf(t *testing.T, res *Result, address string) Match {
	t.Helper()
	for _, m := range res.Matches {
		if m.LeftAddress == address || m.RightAddress == address {
			return m
		}
	}
	t.Fatalf("no match for %s", address)
	return Match{}
}

// noCanary fails when the planted secret appears in the result, any detail or the report.
func noCanary(t *testing.T, res *Result) {
	t.Helper()
	b, _ := json.Marshal(res)
	all := string(b)
	for _, m := range res.Matches {
		d, _ := res.Detail(m.ID)
		b, _ := json.Marshal(d)
		all += string(b)
	}
	if strings.Contains(all, fixtures.Canary) {
		t.Fatal("canary in comparison output")
	}
}

func TestTwoWorkspacesWithoutRules(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareProd)
	want := map[string]string{
		"aws_vpc.main":                     Different,
		"aws_s3_bucket.logs":               Different,
		"aws_db_instance.main":             Different,
		"aws_sns_topic.alerts":             Uncomparable,
		"data.aws_ami.ubuntu":              Same,
		"aws_iam_role.debug":               OnlyLeft,
		"data.aws_caller_identity.current": OnlyRight,
		"module.app.aws_instance.web[0]":   Different,
		"module.app.aws_instance.web[2]":   OnlyRight,
		`module.monitoring.aws_cloudwatch_metric_alarm.cpu["worker.batch"]`: Different,
	}
	for address, status := range want {
		if got := statusOf(t, res, address); got.Status != status {
			t.Errorf("%s: %s, want %s (%s)", address, got.Status, status, got.Reason)
		}
	}
	if s := res.Summary; s.Data.Same != 1 || s.Data.OnlyRight != 1 || s.Managed.OnlyLeft != 1 || s.Managed.OnlyRight != 1 || s.Managed.Uncomparable != 1 {
		t.Fatalf("summary %+v", s)
	}
	if res.Summary.Managed.Total+res.Summary.Data.Total != len(res.Matches) {
		t.Fatal("counts do not add up")
	}
	vpc := statusOf(t, res, "aws_vpc.main")
	if vpc.LeftID == "" || vpc.LeftID != vpc.RightID || strings.Join(vpc.Paths, ",") != "cidr_block,id,tags.Environment" {
		t.Fatalf("vpc %+v", vpc)
	}
	if sns := statusOf(t, res, "aws_sns_topic.alerts"); !strings.Contains(sns.Reason, "acme/aws") {
		t.Fatalf("uncomparable reason %q", sns.Reason)
	}
	noCanary(t, res)
}

func TestRulesNormalizeExpectedDifferences(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareProd,
		Rule{Kind: RuleSubstitute, Left: "dev", Right: "prod"},
		Rule{Kind: RuleIgnore, Path: "region"},
		Rule{Kind: RuleIgnore, Path: "cidr_block", ResourceType: "aws_vpc"},
	)
	for address, status := range map[string]string{
		"aws_vpc.main":                   Same,
		"aws_s3_bucket.logs":             Same,
		"aws_db_instance.main":           Unknown,
		"module.app.aws_lb.front":        Same,
		"module.app.aws_instance.web[1]": Different,
		`module.monitoring.aws_cloudwatch_metric_alarm.cpu["api"]`:          Different,
		`module.monitoring.aws_cloudwatch_metric_alarm.cpu["worker.batch"]`: Same,
	} {
		if got := statusOf(t, res, address); got.Status != status {
			t.Errorf("%s: %s, want %s (%v)", address, got.Status, status, got.Paths)
		}
	}
	db := statusOf(t, res, "aws_db_instance.main")
	if db.NotCompared == 0 || !strings.Contains(db.Reason, "not compared") {
		t.Fatalf("db %+v", db)
	}
	if hits := res.Summary.RuleHits; hits[0] == 0 || hits[1] != 1 || hits[2] != 1 {
		t.Fatalf("rule hits %v", hits)
	}
	if res.Summary.IgnoredAttributes != 2 || res.Summary.Normalized == 0 {
		t.Fatalf("summary %+v", res.Summary)
	}
	web := statusOf(t, res, "module.app.aws_instance.web[1]")
	if strings.Join(web.Paths, ",") != "instance_type" {
		t.Fatalf("web paths %v", web.Paths)
	}
	d, ok := res.Detail(statusOf(t, res, "aws_s3_bucket.logs").ID)
	if !ok {
		t.Fatal("no detail")
	}
	byPath := map[string]Attribute{}
	for _, a := range d.Attributes {
		byPath[a.Path] = a
	}
	if a := byPath["region"]; a.Status != AttrIgnored || a.Left != nil || a.Right != nil || a.Rule != 2 {
		t.Fatalf("ignored row %+v", a)
	}
	if a := byPath["bucket"]; a.Status != AttrSame || !a.Normalized || a.Left != "hclaro-dev-logs" || a.Right != "hclaro-prod-logs" {
		t.Fatalf("normalized row %+v", a)
	}
	if a := byPath["force_destroy"]; a.Status != AttrSame {
		t.Fatalf("same row %+v", a)
	}
	// Detail views never count rule hits again.
	before := append([]int{}, res.Summary.RuleHits...)
	res.Detail(statusOf(t, res, "aws_s3_bucket.logs").ID)
	if jsonOf(before) != jsonOf(res.Summary.RuleHits) {
		t.Fatal("detail changed rule hits")
	}
	noCanary(t, res)
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSensitiveValuesAreNeverCompared(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareDev)
	db := statusOf(t, res, "aws_db_instance.main")
	if db.Status != Unknown {
		t.Fatalf("identical states with redacted values: %s", db.Status)
	}
	d, _ := res.Detail(db.ID)
	sensitive := 0
	for _, a := range d.Attributes {
		if a.Status == AttrSensitive {
			sensitive++
			if a.Left != nil || a.Right != nil {
				t.Fatalf("sensitive row has values: %+v", a)
			}
		}
	}
	if sensitive != 3 || db.NotCompared != 3 {
		t.Fatalf("sensitive rows %d, not compared %d", sensitive, db.NotCompared)
	}
	if statusOf(t, res, "aws_vpc.main").Status != Same {
		t.Fatal("identical visible values must be same")
	}
	noCanary(t, res)
}

func TestMixedEnginesCompareOneProvider(t *testing.T) {
	res := run(t, fixtures.CompareProd, fixtures.CompareProdTofu)
	for _, m := range res.Matches {
		if m.Status != Same && m.Status != Unknown {
			t.Errorf("%s: %s (%s)", m.LeftAddress, m.Status, m.Reason)
		}
		if m.Status != Unknown && m.ProviderNote == "" {
			t.Errorf("%s: no registry note", m.LeftAddress)
		}
	}
	found := false
	for _, p := range res.Providers {
		if p.Source == "hashicorp/aws" {
			found = true
			if p.Status != "both" || len(p.Notes) != 1 || p.Left != p.Right {
				t.Fatalf("provider row %+v", p)
			}
		}
	}
	if !found || len(res.Providers) != 2 {
		t.Fatalf("providers %+v", res.Providers)
	}
}

func TestModuleRenameNeedsExplicitMapping(t *testing.T) {
	res := run(t, fixtures.CompareProd, fixtures.CompareProdRenamed)
	if res.Summary.Managed.OnlyLeft != 4 || res.Summary.Managed.OnlyRight != 4 || res.Summary.Mapped != 0 {
		t.Fatalf("without a rule: %+v", res.Summary.Managed)
	}
	res = run(t, fixtures.CompareProd, fixtures.CompareProdRenamed, Rule{Kind: RuleMap, Left: "module.app", Right: "module.application"})
	if res.Summary.Managed.OnlyLeft != 0 || res.Summary.Managed.OnlyRight != 0 || res.Summary.Mapped != 4 || res.Summary.RuleHits[0] != 4 {
		t.Fatalf("with a rule: %+v %+v", res.Summary.Managed, res.Summary)
	}
	m := statusOf(t, res, "module.application.aws_instance.web[2]")
	if m.LeftAddress != "module.app.aws_instance.web[2]" || m.Rule != 1 || m.Status != Unknown || m.NotCompared != 1 || m.LeftID == m.RightID {
		t.Fatalf("mapped %+v", m)
	}
	var row *ModuleRow
	for i := range res.Modules {
		if res.Modules[i].Address == "module.application" {
			row = &res.Modules[i]
		}
	}
	if row == nil || row.LeftAddress != "module.app" || row.Status != "both" || row.Left != 4 || row.Right != 4 {
		t.Fatalf("module rows %+v", res.Modules)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestIndexedAddressesMatchExactly(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareProd)
	for _, m := range res.Matches {
		if m.LeftAddress != "" && m.RightAddress != "" && m.LeftAddress != m.RightAddress {
			t.Fatalf("paired different addresses without a rule: %+v", m)
		}
	}
	m := statusOf(t, res, `module.monitoring.aws_cloudwatch_metric_alarm.cpu["worker.batch"]`)
	if m.LeftAddress != m.RightAddress {
		t.Fatal("quoted for_each key did not match")
	}
}

func TestMapAddressBoundaries(t *testing.T) {
	for _, tc := range []struct {
		address, left, right, want string
		ok                         bool
	}{
		{"module.app.aws_instance.web[0]", "module.app", "module.application", "module.application.aws_instance.web[0]", true},
		{"module.apps.aws_instance.web[0]", "module.app", "module.application", "", false},
		{`module.app["a"].aws_instance.web`, "module.app", "module.b", `module.b["a"].aws_instance.web`, true},
		{"aws_instance.web[0]", "aws_instance.web", "aws_instance.front", "aws_instance.front[0]", true},
		{"aws_instance.webx", "aws_instance.web", "aws_instance.front", "", false},
		{"aws_instance.web[1]", "aws_instance.web[0]", "aws_instance.front[0]", "", false},
		{"aws_instance.web[0]", "aws_instance.web[0]", "aws_instance.front[3]", "aws_instance.front[3]", true},
	} {
		got, ok := mapAddress(tc.address, tc.left, tc.right)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s with %s: %q %v", tc.address, tc.left, got, ok)
		}
	}
}

func TestMappingConflictIsUncomparable(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareProd, Rule{Kind: RuleMap, Left: "aws_iam_role.debug", Right: "aws_vpc.main"})
	m := statusOf(t, res, "aws_iam_role.debug")
	if m.Status != Uncomparable || m.Rule != 1 || !strings.Contains(m.Reason, "already matched") {
		t.Fatalf("conflict %+v", m)
	}
	if statusOf(t, res, "aws_vpc.main").LeftAddress != "aws_vpc.main" || len(res.Warnings) != 1 {
		t.Fatalf("exact pair must win: %v", res.Warnings)
	}
}

func TestMappingToAnotherTypeIsUncomparable(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareProd, Rule{Kind: RuleMap, Left: "aws_iam_role.debug", Right: "data.aws_caller_identity.current"})
	m := statusOf(t, res, "aws_iam_role.debug")
	if m.Status != Uncomparable || !strings.Contains(m.Reason, "data source") {
		t.Fatalf("%+v", m)
	}
}

func TestUnmatchedMapRuleWarns(t *testing.T) {
	res := run(t, fixtures.CompareDev, fixtures.CompareProd, Rule{Kind: RuleMap, Left: "module.nothing", Right: "module.other"})
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "matched no left address") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

func TestDataSourceOnlyDifference(t *testing.T) {
	var doc map[string]any
	_ = json.Unmarshal(fixtures.CompareProd, &doc)
	root := doc["values"].(map[string]any)["root_module"].(map[string]any)
	kept := []any{}
	for _, r := range root["resources"].([]any) {
		if r.(map[string]any)["address"] != "data.aws_caller_identity.current" {
			kept = append(kept, r)
		}
	}
	root["resources"] = kept
	right, _ := json.Marshal(doc)
	res := run(t, fixtures.CompareProd, right, Rule{Kind: RuleIgnore, Path: "description"})
	if s := res.Summary; s.Data.OnlyLeft != 1 || s.Data.Same != 1 || s.Managed.Different != 0 || s.Managed.OnlyLeft != 0 || s.Managed.OnlyRight != 0 {
		t.Fatalf("summary %+v / %+v", s.Data, s.Managed)
	}
	m := statusOf(t, res, "data.aws_caller_identity.current")
	if m.Status != OnlyLeft || m.Mode != "data" {
		t.Fatalf("%+v", m)
	}
}

func TestEmptySideIsEverythingOneSided(t *testing.T) {
	res := run(t, fixtures.CompareDev, []byte(`{"format_version":"1.0","values":{"root_module":{}}}`))
	if res.Summary.Managed.OnlyLeft != res.Summary.Left.Managed || res.Summary.Managed.Same != 0 {
		t.Fatalf("%+v", res.Summary)
	}
}

func TestRuleValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule Rule
	}{
		{"unknown kind", Rule{Kind: "rename"}},
		{"secret token", Rule{Kind: RuleSubstitute, Left: fixtures.Canary, Right: "x"}},
		{"secret-shaped token", Rule{Kind: RuleSubstitute, Left: "AKIAABCDEFGHIJKLMNOP", Right: "x"}},
		{"same tokens", Rule{Kind: RuleSubstitute, Left: "dev", Right: "dev"}},
		{"empty token", Rule{Kind: RuleSubstitute, Left: "dev"}},
		{"bad path", Rule{Kind: RuleIgnore, Path: "tags..x"}},
		{"path with spaces", Rule{Kind: RuleIgnore, Path: "tags name"}},
		{"empty path", Rule{Kind: RuleIgnore}},
		{"ignore with addresses", Rule{Kind: RuleIgnore, Path: "id", Left: "x"}},
		{"bad type", Rule{Kind: RuleIgnore, Path: "id", ResourceType: "aws instance"}},
		{"map without right", Rule{Kind: RuleMap, Left: "module.a"}},
		{"map same", Rule{Kind: RuleMap, Left: "module.a", Right: "module.a"}},
		{"map partial", Rule{Kind: RuleMap, Left: "module", Right: "module.b"}},
		{"map attribute", Rule{Kind: RuleMap, Left: "aws_instance.web.id", Right: "aws_instance.b"}},
		{"map open quote", Rule{Kind: RuleMap, Left: `module.a["x`, Right: "module.b"}},
		{"map with path", Rule{Kind: RuleMap, Left: "module.a", Right: "module.b", Path: "id"}},
		{"control character", Rule{Kind: RuleIgnore, Path: "id‮"}},
	} {
		if _, err := Validate([]Rule{tc.rule}); err == nil {
			t.Errorf("%s accepted", tc.name)
		}
	}
	valid := []Rule{
		{Kind: RuleMap, Left: `module.app["blue"]`, Right: "module.app_green"},
		{Kind: RuleMap, Left: "data.aws_ami.old", Right: "data.aws_ami.new"},
		{Kind: RuleMap, Left: "module.a.module.b.aws_instance.web[0]", Right: "aws_instance.web"},
		{Kind: RuleIgnore, Path: `tags["cost.center"]`},
		{Kind: RuleIgnore, Path: "ingress[*].cidr_blocks"},
		{Kind: RuleIgnore, Path: "tags.*", ResourceType: "aws_instance"},
		{Kind: RuleSubstitute, Left: "us-east-1", Right: "eu-west-1", Path: "arn"},
	}
	if _, err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	many := make([]Rule, MaxRules+1)
	for i := range many {
		many[i] = Rule{Kind: RuleIgnore, Path: "id"}
	}
	if _, err := Validate(many); err == nil {
		t.Fatal("too many rules accepted")
	}
}

func TestPatternMatching(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		path    []any
		want    bool
	}{
		{"tags", []any{"tags", "Name"}, true},
		{"tags.Name", []any{"tags", "Name"}, true},
		{"tags.Name", []any{"tags", "Owner"}, false},
		{"ingress[*].cidr_blocks", []any{"ingress", 3, "cidr_blocks", 0}, true},
		{"ingress[0]", []any{"ingress", 1}, false},
		{`tags["cost.center"]`, []any{"tags", "cost.center"}, true},
		{"*.Name", []any{"tags", "Name"}, true},
		{"tags.Name.x", []any{"tags", "Name"}, false},
	} {
		p, ok := parsePattern(tc.pattern)
		if !ok {
			t.Fatalf("pattern %s invalid", tc.pattern)
		}
		if got := matches(p, tc.path); got != tc.want {
			t.Errorf("%s on %v: %v", tc.pattern, tc.path, got)
		}
	}
}

func TestThousandResourcePairIsFast(t *testing.T) {
	left, right := parse(t, fixtures.CompareLarge("dev", 1000)), parse(t, fixtures.CompareLarge("prod", 1000))
	rules, _ := Validate([]Rule{{Kind: RuleSubstitute, Left: "dev", Right: "prod"}})
	start := time.Now()
	res := Compare(left, right, rules)
	elapsed := time.Since(start)
	if s := res.Summary.Managed; s.Total != 1000 || s.Different != 200 || s.Same != 800 {
		t.Fatalf("summary %+v", s)
	}
	for _, m := range res.Matches[:50] {
		res.Detail(m.ID)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("1,000-resource comparison took %s", elapsed)
	}
	noCanary(t, res)
}

func BenchmarkCompareThousand(b *testing.B) {
	left, right := parse(b, fixtures.CompareLarge("dev", 1000)), parse(b, fixtures.CompareLarge("prod", 1000))
	rules, _ := Validate([]Rule{{Kind: RuleSubstitute, Left: "dev", Right: "prod"}})
	for b.Loop() {
		Compare(left, right, rules)
	}
}
