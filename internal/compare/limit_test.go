package compare

import (
	"strings"
	"testing"

	"hclaro/fixtures"
)

func limited(t *testing.T, left, right []byte, addresses []string, rules ...Rule) (*Result, []string) {
	t.Helper()
	c, err := Validate(rules)
	if err != nil {
		t.Fatal(err)
	}
	l, r, warnings := Limit(parse(t, left), parse(t, right), c, addresses)
	return Compare(l, r, c), warnings
}

func TestLimitKeepsSelectedAndCounterparts(t *testing.T) {
	res, warnings := limited(t, fixtures.CompareDev, fixtures.CompareProd, []string{"aws_vpc.main", "aws_iam_role.debug"})
	if len(warnings) != 0 || len(res.Matches) != 2 {
		t.Fatalf("%v %d", warnings, len(res.Matches))
	}
	if statusOf(t, res, "aws_vpc.main").Status != Different || statusOf(t, res, "aws_iam_role.debug").Status != OnlyLeft {
		t.Fatalf("%+v", res.Matches)
	}
	if s := res.Summary; s.Managed.Total != 2 || s.Data.Total != 0 || s.Left.Managed != 2 || s.Right.Managed != 1 {
		t.Fatalf("summary %+v", s)
	}
	noCanary(t, res)
}

func TestLimitFollowsMappingRules(t *testing.T) {
	res, _ := limited(t, fixtures.CompareProd, fixtures.CompareProdRenamed, []string{"module.app.aws_instance.web[2]"}, Rule{Kind: RuleMap, Left: "module.app", Right: "module.application"})
	if len(res.Matches) != 1 || res.Matches[0].RightAddress != "module.application.aws_instance.web[2]" {
		t.Fatalf("%+v", res.Matches)
	}
}

func TestLimitWarnsAboutMissingAddresses(t *testing.T) {
	res, warnings := limited(t, fixtures.CompareDev, fixtures.CompareProd, []string{"aws_vpc.main", "aws_nope.gone"})
	if len(res.Matches) != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "1 selected address is not in the left state") {
		t.Fatalf("%v %v", res.Matches, warnings)
	}
}

func TestNoAddressesIsUnfiltered(t *testing.T) {
	c, _ := Validate(nil)
	l, r := parse(t, fixtures.CompareDev), parse(t, fixtures.CompareProd)
	gl, gr, w := Limit(l, r, c, nil)
	if w != nil || len(gl.Resources) != len(l.Resources) || len(gr.Resources) != len(r.Resources) {
		t.Fatal("filter applied without addresses")
	}
}

func TestValidAddresses(t *testing.T) {
	many := make([]string, MaxAddresses+1)
	for i := range many {
		many[i] = "a.b" + strings.Repeat("x", i)
	}
	for name, tc := range map[string]struct {
		in []string
		ok bool
	}{
		"nil": {nil, true}, "one": {[]string{"aws_vpc.main", "data.aws_ami.x"}, true}, "too many": {many, false},
		"empty": {[]string{""}, false}, "dup": {[]string{"a.b", "a.b"}, false}, "flag": {[]string{"-target=x"}, false}, "control": {[]string{"a.b\n"}, false},
	} {
		if got := ValidAddresses(tc.in); got != tc.ok {
			t.Errorf("%s: %v", name, got)
		}
	}
}
