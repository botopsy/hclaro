package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// engineGoldens are real engine recordings of fixtures/engines/project
// (scripts/fixtures/record-engine.sh), one per supported engine line.
var engineGoldens = []string{"terraform-1.5.5", "terraform-1.17.2", "tofu-1.13.9"}

func TestRealEnginePlans(t *testing.T) {
	wantActions := map[string]string{
		"data.terraform_remote_state.pending":   Read,
		"terraform_data.created[0]":             Create,
		"terraform_data.created[1]":             Create,
		"terraform_data.gone":                   Delete,
		"terraform_data.renamed":                NoOp,
		"terraform_data.replaced":               Replace,
		"terraform_data.replaced_first":         Replace,
		"terraform_data.secret":                 Update,
		"terraform_data.unchanged":              NoOp,
		"terraform_data.updated":                Update,
		`module.group["a"].terraform_data.this`: Update,
		`module.group["b"].terraform_data.this`: Create,
	}
	for _, engine := range engineGoldens {
		t.Run(engine, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", engine, "plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			review, err := Parse(input)
			if err != nil {
				t.Fatalf("real plan rejected: %v", err)
			}
			if !review.Applyable || review.Errored || review.Incomplete || review.Unsupported != 0 || len(review.Warnings) != 0 {
				t.Fatalf("flags: applyable %v errored %v incomplete %v unsupported %d warnings %v", review.Applyable, review.Errored, review.Incomplete, review.Unsupported, review.Warnings)
			}
			if review.EngineVersion != strings.SplitN(engine, "-", 2)[1] {
				t.Fatalf("engine version %q", review.EngineVersion)
			}
			got := map[string]string{}
			byAddress := map[string]Change{}
			for _, c := range review.Changes {
				got[c.Address] = c.Action
				byAddress[c.Address] = c
			}
			if !reflect.DeepEqual(got, wantActions) {
				t.Fatalf("actions:\n got %v\nwant %v", got, wantActions)
			}
			if c := byAddress["terraform_data.renamed"]; c.PreviousAddress != "terraform_data.old_name" {
				t.Fatalf("moved: previous address %q", c.PreviousAddress)
			}
			if a := byAddress["terraform_data.replaced"].Actions; !reflect.DeepEqual(a, []string{"delete", "create"}) {
				t.Fatalf("replace order %v", a)
			}
			if a := byAddress["terraform_data.replaced_first"].Actions; !reflect.DeepEqual(a, []string{"create", "delete"}) {
				t.Fatalf("create-before-destroy order %v", a)
			}
			if r := byAddress["data.terraform_remote_state.pending"].Reason; r != "read_because_dependency_pending" {
				t.Fatalf("deferred read reason %q", r)
			}
			forces := false
			for _, a := range review.Details[byAddress["terraform_data.replaced"].ID].Attributes {
				forces = forces || a.ForcesReplacement
			}
			if !forces {
				t.Fatal("no attribute marked as forcing replacement")
			}
			secret := false
			for _, a := range review.Details[byAddress["terraform_data.secret"].ID].Attributes {
				secret = secret || a.Sensitive
			}
			if !secret {
				t.Fatal("sensitive input update not marked sensitive")
			}
			outputs := map[string]Output{}
			for _, o := range review.Outputs {
				outputs[o.Name] = o
			}
			if o := outputs["ids"]; o.Action != Create {
				t.Fatalf("ids output %+v", o)
			}
			if o := outputs["label"]; o.Action != Update || o.Before != "old" || o.After != "new" {
				t.Fatalf("label output %+v", o)
			}
			if o := outputs["token"]; !o.Sensitive || o.Action != NoOp {
				t.Fatalf("token output %+v", o)
			}
			assertNoCanary(t, review)
		})
	}
}

// The refresh-only recording proposes no resource changes and lists the moved object as drift.
func TestRealEngineRefreshOnlyPlans(t *testing.T) {
	for _, engine := range engineGoldens {
		t.Run(engine, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", engine, "drift.json"))
			if err != nil {
				t.Fatal(err)
			}
			review, err := Parse(input)
			if err != nil {
				t.Fatalf("real refresh-only plan rejected: %v", err)
			}
			if len(review.Changes) != 0 || len(review.Drift) != 1 || review.Drift[0].Address != "terraform_data.renamed" || review.Drift[0].Action != NoOp {
				t.Fatalf("changes %v drift %v", review.Changes, review.Drift)
			}
			assertNoCanary(t, review)
		})
	}
}

func assertNoCanary(t *testing.T, review Review) {
	t.Helper()
	b, err := json.Marshal(struct {
		Review
		Details map[string]Detail
	}{review, review.Details})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), canary) {
		t.Fatal("canary visible after parsing a real plan")
	}
}
