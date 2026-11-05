package plan

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/fixtures"
)

var update = flag.Bool("update", false, "rewrite golden files")

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func TestClassify(t *testing.T) {
	for actions, want := range map[string]string{
		"create": Create, "update": Update, "delete": Delete, "read": Read, "no-op": NoOp, "forget": Forget,
		"delete,create": Replace, "create,delete": Replace,
		"update,delete": Unknown, "delete,delete": Unknown, "create,create": Unknown, "": Unknown, "import": Unknown,
	} {
		var list []string
		if actions != "" {
			list = strings.Split(actions, ",")
		}
		if got := Classify(list); got != want {
			t.Errorf("%q: got %s want %s", actions, got, want)
		}
	}
}

// The golden file pins the complete redacted review, including per-change details.
func TestGoldenReview(t *testing.T) {
	for name, input := range map[string][]byte{"all-actions": fixtures.PlanAllActions, "collateral": fixtures.PlanCollateral, "no-changes": fixtures.PlanNoChanges, "errored": fixtures.PlanErrored} {
		review, err := Parse(input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := json.MarshalIndent(struct {
			Review
			Details map[string]Detail `json:"details"`
		}{review, review.Details}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(got, []byte(canary)) {
			t.Fatalf("%s: canary in normalized review", name)
		}
		path := filepath.Join("..", "..", "fixtures", "plan", name+".golden.json")
		if *update {
			if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.TrimSpace(want), got) {
			t.Fatalf("%s: review differs from golden; rerun with -update and review the diff", name)
		}
	}
}

func byAddress(t *testing.T, r Review) map[string]Detail {
	t.Helper()
	out := map[string]Detail{}
	for _, c := range r.Changes {
		out[c.Address+c.Deposed] = r.Details[c.ID]
	}
	return out
}

func TestAllActions(t *testing.T) {
	r, err := Parse(fixtures.PlanAllActions)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Changes) != 13 || len(r.Drift) != 1 || len(r.Outputs) != 5 {
		t.Fatalf("every change must be kept: %d changes, %d drift, %d outputs", len(r.Changes), len(r.Drift), len(r.Outputs))
	}
	want := map[string]int{Create: 1, Update: 3, Delete: 2, Replace: 2, Read: 1, NoOp: 2, Forget: 1, Unknown: 1}
	for k, v := range want {
		if r.Summary[k] != v {
			t.Errorf("summary %s = %d, want %d (%v)", k, r.Summary[k], v, r.Summary)
		}
	}
	// CLI order is preserved, not re-sorted.
	if r.Changes[0].Address != "aws_s3_bucket.logs" || r.Changes[len(r.Changes)-1].Address != "aws_lb.edge" {
		t.Fatal("change order not preserved")
	}
	c := byAddress(t, r)
	cbd := c["module.compute.aws_instance.main[0]"]
	if cbd.Action != Replace || strings.Join(cbd.Actions, ",") != "delete,create" || cbd.Reason != "replace_because_cannot_update" {
		t.Fatalf("destroy-before-create replacement: %+v", cbd.Change)
	}
	cbdRows := map[string]AttributeChange{}
	for _, a := range cbd.Attributes {
		cbdRows[a.Path] = a
	}
	if !cbdRows["ami"].ForcesReplacement || cbdRows["ami"].Before != "[redacted]" || cbdRows["ami"].Kind != "changed" {
		t.Fatalf("replace path not marked: %+v", cbdRows["ami"])
	}
	if cbdRows["id"].After != KnownAfterApply || !cbdRows["id"].Unknown || cbdRows["id"].Before != "i-0aa" {
		t.Fatalf("unknown id: %+v", cbdRows["id"])
	}
	if _, ok := cbdRows["user_data"]; ok {
		t.Fatal("unchanged sensitive attribute reported as changed")
	}
	cbc := c[`module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]`]
	if cbc.Action != Replace || strings.Join(cbc.Actions, ",") != "create,delete" || cbc.Index != `quote"key` {
		t.Fatalf("create-before-destroy replacement with quoted key: %+v", cbc.Change)
	}
	db := c["aws_db_instance.primary"]
	rows := map[string]AttributeChange{}
	for _, a := range db.Attributes {
		rows[a.Path] = a
	}
	if rows["engine_version"].Before != "15.4" || rows["engine_version"].After != "16.1" || rows["allocated_storage"].After != json.Number("40") {
		t.Fatalf("safe values should be visible: %+v", db.Attributes)
	}
	if len(db.Attributes) != 2 {
		t.Fatalf("unchanged sensitive values must not appear as changes: %+v", db.Attributes)
	}
	subnet := c["module.network.aws_subnet.main[0]"]
	if len(subnet.Attributes) != 1 || subnet.Attributes[0].Path != "tags.owner" || subnet.Attributes[0].Kind != "added" || subnet.Attributes[0].After != "[redacted]" {
		t.Fatalf("nested map addition: %+v", subnet.Attributes)
	}
	bucket := c["aws_s3_bucket.logs"]
	for _, a := range bucket.Attributes {
		if a.Path == "policy" && (!a.Sensitive || a.After != "[redacted]") {
			t.Fatalf("sensitive marker ignored: %+v", a)
		}
		if a.Path == "secret_access_key" && a.After != "[redacted]" {
			t.Fatal("risky key revealed")
		}
	}
	if bucket.After.(map[string]any)["arn"] != KnownAfterApply {
		t.Fatalf("unknown overlay missing: %+v", bucket.After)
	}
	if d := c["aws_instance.retired00000001"]; d.Deposed != "00000001" || d.Action != Delete {
		t.Fatalf("deposed object: %+v", d.Change)
	}
	if c["aws_security_group.web"].PreviousAddress != "aws_security_group.legacy_web" {
		t.Fatal("moved resource lost its previous address")
	}
	if !c["aws_vpc.imported"].Importing {
		t.Fatal("import not flagged")
	}
	if c["aws_lb.edge"].Action != Unknown || len(r.Warnings) == 0 {
		t.Fatal("unknown action combination must be flagged")
	}
	if r.Drift[0].Address != "aws_s3_bucket.artifacts" || r.Drift[0].ID == c["aws_s3_bucket.artifacts"].ID {
		t.Fatal("drift must be kept separately")
	}
	outputs := map[string]Output{}
	for _, o := range r.Outputs {
		outputs[o.Name] = o
	}
	if !outputs["db_password"].Sensitive || outputs["db_password"].After != "[redacted]" || outputs["instance_ids"].After != KnownAfterApply || outputs["legacy_role"].Action != Delete {
		t.Fatalf("outputs: %+v", r.Outputs)
	}
}

func TestCollateral(t *testing.T) {
	r, err := Parse(fixtures.PlanCollateral)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary[Replace] != 2 || r.Summary[Delete] != 1 || r.Summary[NoOp] != 1 {
		t.Fatalf("two replacements plus a collateral deletion: %v", r.Summary)
	}
	if c := byAddress(t, r)["aws_lb_target_group_attachment.web[2]"]; c.Reason != "delete_because_count_index" {
		t.Fatalf("collateral reason lost: %+v", c.Change)
	}
}

func TestErroredAndEmpty(t *testing.T) {
	r, err := Parse(fixtures.PlanErrored)
	if err != nil || !r.Errored || len(r.Warnings) == 0 {
		t.Fatalf("errored plan must be flagged: %v %+v", err, r)
	}
	r, err = Parse(fixtures.PlanNoChanges)
	if err != nil || r.Summary[NoOp] != 1 || len(r.Outputs) != 0 {
		t.Fatalf("no-changes plan: %v %+v", err, r)
	}
}

func TestFailClosed(t *testing.T) {
	for name, input := range map[string]string{
		"state document":  string(fixtures.State),
		"empty":           ``,
		"trailing":        `{"format_version":"1.2","planned_values":{}} {}`,
		"major format":    `{"format_version":"2.0","planned_values":{}}`,
		"missing planned": `{"format_version":"1.2","resource_changes":[]}`,
		"no actions":      `{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b","mode":"managed","type":"a","name":"b","change":{"actions":[]}}]}`,
		"bad mode":        `{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b","mode":"x","type":"a","name":"b","change":{"actions":["create"]}}]}`,
		"secret address":  `{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b[\"DO_NOT_LEAK_HCLARO_TEST_SECRET\"]","mode":"managed","type":"a","name":"b","change":{"actions":["create"]}}]}`,
		"duplicate":       `{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b","mode":"managed","type":"a","name":"b","change":{"actions":["create"]}},{"address":"a.b","mode":"managed","type":"a","name":"b","change":{"actions":["delete"]}}]}`,
		"secret output":   `{"format_version":"1.2","planned_values":{},"output_changes":{"token=abc":{"actions":["create"]}}}`,
		"bad deposed":     `{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b","mode":"managed","type":"a","name":"b","deposed":"../x","change":{"actions":["delete"]}}]}`,
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAttributeDiffEdges(t *testing.T) {
	input := `{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b","mode":"managed","type":"a","name":"b","change":{"actions":["update"],
	"before":{"id":"x","list":["a","b","c"],"nested":{"deep":{"name":"n1"}},"token_ref":"DO_NOT_LEAK_HCLARO_TEST_SECRET","weird key!":"v","DO_NOT_LEAK_HCLARO_TEST_SECRET":"k"},
	"after":{"id":"x","list":["a","z"],"nested":{"deep":{"name":"n2"}},"token_ref":"other","weird key!":"w","DO_NOT_LEAK_HCLARO_TEST_SECRET":"k2"},
	"after_unknown":{"list":[false,false]},"before_sensitive":{"nested":{"deep":true}},"after_sensitive":{}}}]}`
	r, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	d := r.Details[r.Changes[0].ID]
	paths := map[string]AttributeChange{}
	for _, a := range d.Attributes {
		paths[a.Path] = a
	}
	for _, want := range []string{"list[1]", "list[2]", "nested.deep.name", "token_ref", `["weird key!"]`, "[redacted key]"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("missing path %s in %v", want, d.Attributes)
		}
	}
	if paths["list[2]"].Kind != "removed" || !paths["nested.deep.name"].Sensitive || paths["nested.deep.name"].Before != "[redacted]" {
		t.Fatalf("edges: %+v", d.Attributes)
	}
	encoded, _ := json.Marshal(d)
	if bytes.Contains(encoded, []byte(canary)) {
		t.Fatal("canary leaked through a key or value")
	}
}

// Real Terraform 1.5 output: a list of new IDs is marked unknown per element, not as a whole.
func TestOutputPartialUnknown(t *testing.T) {
	r, err := Parse([]byte(`{"format_version":"1.2","planned_values":{},"output_changes":{
		"ids":{"actions":["create"],"before":null,"after":[null,null],"after_unknown":[true,true],"before_sensitive":false,"after_sensitive":false},
		"all":{"actions":["create"],"before":null,"after":null,"after_unknown":true,"before_sensitive":false,"after_sensitive":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(r.Outputs)
	if string(got) != `[{"name":"all","actions":["create"],"action":"create","before":null,"after":"[known after apply]","unknown":true},{"name":"ids","actions":["create"],"action":"create","before":null,"after":["[known after apply]","[known after apply]"]}]` {
		t.Fatal(string(got))
	}
}

func FuzzParse(f *testing.F) {
	f.Add(fixtures.PlanAllActions)
	f.Add(fixtures.PlanCollateral)
	f.Add([]byte(`{"format_version":"1.2","planned_values":{},"resource_changes":[{"address":"a.b","mode":"managed","type":"a","name":"b","change":{"actions":["update"],"before":[[[[1]]]],"after":{"a":[{"b":null}]},"after_unknown":[true,{"x":[true]}]}}]}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		r, err := Parse(input)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range r.Details {
			if _, err := json.Marshal(d); err != nil {
				t.Fatal(err)
			}
		}
		if bytes.Contains(encoded, []byte(canary)) && !bytes.Contains(input, []byte(canary)) {
			t.Fatal("canary appeared from nowhere")
		}
	})
}
