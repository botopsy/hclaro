//go:build realengine

package qualify

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// re01Want is RE-01's plan summary (docs/qualification/terraform-1.5.5-local.md): 13 changes.
var re01Want = map[string]int{"create": 5, "update": 2, "replace": 2, "delete": 1, "read": 1, "no-op": 2}

// TestRealRE01Plan reproduces RE-01's plan through Hclaro's API on the reconstructed fixture,
// once per configured engine. Differences from RE-01 that are known are recorded in the run.
func TestRealRE01Plan(t *testing.T) {
	if run == nil {
		t.Skip(runSkip)
	}
	run.Result.Differences = append(run.Result.Differences,
		"RE-01 fixture is a reconstruction from the RE-01 description, not the original files (which were not kept).",
		"RE-01 canary locations kept (sensitive variable, var file, recorded state, remote-state file); no random_password keeper, because RE-01 used built-in resources only and a provider resource would change the 13-change summary.",
		"Fixture change after the first real run (2026-11-15): Hclaro showed the deleted resource's recorded value in its change detail because the state did not mark it sensitive. The fixture now marks it sensitive, reading RE-01's 'sensitive, deleted and output values' as a sensitive deleted value. Whether values the engine does not mark sensitive must also be redacted is an owner decision (CLAUDE.md: missing sensitivity flags are no proof of safety).")
	for _, name := range []string{"terraform", "tofu"} {
		if _, ok := run.Engine(name); !ok {
			continue
		}
		run.Scenario(t, "re01-plan-"+name, name, "launch", false, func(s *Scenario) {
			project := s.Project("re01")
			dirs, env, err := s.Process(name)
			if err != nil {
				s.Fatalf("%v", err)
			}
			s.SetupInit(dirs, env, name, project)
			h := s.StartLaunch(dirs, env, name, project)
			detail := s.LaunchPlan(h, project, name, []string{"prod.tfvars"})
			s.Artifact("plan-summary.txt", []byte(detail.String()+"\n"+changeList(detail)))
			if len(detail.Changes) != 13 {
				s.Fatalf("plan has %d changes, RE-01 had 13: %s", len(detail.Changes), detail)
			}
			var diff []string
			for action, n := range re01Want {
				if detail.Summary[action] != n {
					diff = append(diff, fmt.Sprintf("%s %d (RE-01 %d)", action, detail.Summary[action], n))
				}
			}
			sort.Strings(diff)
			if len(diff) > 0 {
				s.Fatalf("plan summary differs from RE-01: %s", strings.Join(diff, ", "))
			}
			// The RE-01 details: both replacement orders, the deletion and deferred-read reasons,
			// and the move kept as previousAddress.
			want := map[string]string{
				"terraform_data.rotated":               "delete,create",
				"terraform_data.rolling":               "create,delete",
				"terraform_data.retired":               "delete_because_no_resource_config",
				"data.terraform_remote_state.upstream": "read_because_dependency_pending",
				"terraform_data.renamed":               "from terraform_data.legacy",
			}
			for _, c := range detail.Changes {
				w, ok := want[c.Address]
				if !ok {
					continue
				}
				got := strings.Join(c.Actions, ",")
				switch {
				case strings.HasPrefix(w, "from "):
					got = "from " + c.PreviousAddress
				case strings.Contains(w, "_because_"):
					got = c.Reason
				}
				if got != w {
					s.Fatalf("%s: %s, RE-01 %s", c.Address, got, w)
				}
				delete(want, c.Address)
			}
			if len(want) > 0 {
				s.Fatalf("changes missing: %v", want)
			}
			s.Observe("plan: %s", detail)
		})
	}
}

func changeList(d PlanDetail) string {
	var b strings.Builder
	for _, c := range d.Changes {
		fmt.Fprintf(&b, "%s %s %v\n", c.Address, c.Action, c.Actions)
	}
	return b.String()
}
