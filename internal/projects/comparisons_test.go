package projects

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/compare"
	"hclaro/internal/profiles"
)

func awaitComparison(t *testing.T, hub *Hub, c *Comparison) ComparisonView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := hub.View(c)
		if v.Status != "reading" || time.Now().After(deadline) {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The fake engine (a real subprocess) reads both sides with a per-command TF_WORKSPACE and
// the left side's profile environment; the right side keeps the inherited one.
func TestComparisonReadsEachSideInItsOwnContext(t *testing.T) {
	hub, ids, spy := profileFixture(t)
	t.Setenv("AWS_SECRET_ACCESS_KEY", fixtures.Canary)
	network := ids["network"]
	dev, err := hub.Profiles.Create(network, profiles.Input{Name: "dev", AWSProfile: "dev-sso"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := hub.StartComparison(ComparisonTarget{network, "dev", dev.ID}, ComparisonTarget{network, "prod", ""}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	v := awaitComparison(t, hub, c)
	if v.Status != "ready" || v.Result == nil {
		t.Fatalf("%+v", v)
	}
	// HubState names resources after the workspace, so nothing pairs; prod has one more.
	if s := v.Result.Summary.Managed; s.OnlyLeft != 2 || s.OnlyRight != 3 || s.Same+s.Different != 0 {
		t.Fatalf("%+v", s)
	}
	if v.Left.Execution == nil || v.Left.Execution.ProfileName != "dev" || v.Right.Execution != nil {
		t.Fatal("execution contexts not recorded per side")
	}
	if !spyContains(t, spy, "TF_WORKSPACE=dev", "AWS_PROFILE=dev-sso", "AWS_SECRET_ACCESS_KEY=<unset>", "show") || !spyContains(t, spy, "TF_WORKSPACE=prod", "AWS_PROFILE=<unset>", "show") {
		t.Fatal("sides did not read with their own workspace and profile")
	}
	for _, line := range spyLines(t, spy) {
		for _, forbidden := range []string{"plan", "apply", "refresh", "select", "import", "state"} {
			if strings.Contains(line, "\x00"+forbidden) {
				t.Fatalf("comparison ran %q", line)
			}
		}
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), fixtures.Canary) {
		t.Fatal("canary in comparison view")
	}

	// Another Hclaro process edits the profile: the context stays open, but the comparison
	// no longer matches its execution context.
	if _, err := hub.Profiles.Update(network, dev.ID, dev.Revision, profiles.Input{Name: "dev", AWSProfile: "other-sso"}); err != nil {
		t.Fatal(err)
	}
	v = hub.View(c)
	if v.Status != "stale" || v.Result != nil || !strings.Contains(v.StaleReason, "execution profile") {
		t.Fatalf("after profile edit %+v", v)
	}
}

func TestComparisonRefusesInvalidDefinitionsBeforeReading(t *testing.T) {
	hub, ids, spy := profileFixture(t)
	network := ids["network"]
	for _, tc := range []struct {
		left, right ComparisonTarget
		rules       []compare.Rule
		want        error
	}{
		{ComparisonTarget{network, "dev", ""}, ComparisonTarget{network, "dev", ""}, nil, ErrSameTarget},
		{ComparisonTarget{network, "dev", ""}, ComparisonTarget{network, "-state=x", ""}, nil, ErrInvalidWorkspace},
		{ComparisonTarget{"../x", "dev", ""}, ComparisonTarget{network, "prod", ""}, nil, ErrNotFound},
		{ComparisonTarget{network, "dev", "nope"}, ComparisonTarget{network, "prod", ""}, nil, ErrNotFound},
	} {
		if _, err := hub.StartComparison(tc.left, tc.right, tc.rules, false); !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	var rule *compare.RuleError
	if _, err := hub.StartComparison(ComparisonTarget{network, "dev", ""}, ComparisonTarget{network, "prod", ""}, []compare.Rule{{Kind: "ignore", Path: "a..b"}}, false); !errors.As(err, &rule) {
		t.Fatalf("rule: %v", err)
	}
	if lines := spyLines(t, spy); len(lines) > 1 || lines[0] != "" || len(hub.Comparisons()) != 0 {
		t.Fatal("a refused comparison ran the engine or was stored")
	}
}

func TestComparisonsAreBoundedAndExpire(t *testing.T) {
	hub, ids, _ := profileFixture(t)
	network := ids["network"]
	now := time.Now()
	hub.Now = func() time.Time { return now }
	var first *Comparison
	for i := 0; i < maxComparisons+2; i++ {
		c, err := hub.StartComparison(ComparisonTarget{network, "dev", ""}, ComparisonTarget{network, "prod", ""}, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = c
		}
	}
	if len(hub.Comparisons()) != maxComparisons {
		t.Fatalf("%d kept", len(hub.Comparisons()))
	}
	if _, err := hub.Comparison(first.ID); !errors.Is(err, ErrComparisonNotFound) {
		t.Fatal("oldest comparison kept")
	}
	now = now.Add(comparisonTTL + time.Minute)
	if len(hub.Comparisons()) != 0 {
		t.Fatal("expired comparisons kept")
	}
}
