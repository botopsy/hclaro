package drift

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hclaro/fixtures"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func analyze(t *testing.T, doc []byte) Result {
	t.Helper()
	r, err := Analyze(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(struct {
		R Result
		D map[string]Detail
	}{r, r.Details})
	for _, secret := range []string{canary, "AKIAABCDEFGHIJKLMNOP"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("secret %q reached the drift result: %s", secret, b)
		}
	}
	return r
}

func TestNoDrift(t *testing.T) {
	r := analyze(t, fixtures.DriftNone)
	if r.Summary.Total != 0 || len(r.Resources) != 0 || r.Incomplete || len(r.Warnings) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestChangedTag(t *testing.T) {
	r := analyze(t, fixtures.DriftChangedTag)
	if r.Summary.Changed != 1 || r.Summary.Total != 1 || r.Resources[0].Classification != Changed {
		t.Fatalf("%+v", r.Summary)
	}
	d := r.Details[r.Resources[0].ID]
	if len(d.Attributes) != 1 || d.Attributes[0].Path != `tags.owner` || d.Attributes[0].Kind != "added" || d.Attributes[0].Observed != "someone-in-console" || d.Attributes[0].Recorded != nil {
		t.Fatalf("%+v", d.Attributes)
	}
}

func TestSecurityGroupRedactsUnflaggedSecrets(t *testing.T) {
	r := analyze(t, fixtures.DriftSecurityGroup)
	d := r.Details[r.Resources[0].ID]
	paths := map[string]AttributeObservation{}
	for _, a := range d.Attributes {
		paths[a.Path] = a
	}
	// The provider did not flag the description, but the value is credential-shaped.
	if a := paths["description"]; a.Kind != "changed" || a.Observed != "[redacted]" || a.Recorded != "web ingress" {
		t.Fatalf("description %+v", a)
	}
	if a := paths["ingress[1].cidr_blocks[0]"]; a.Observed != "0.0.0.0/0" || a.Kind != "added" {
		t.Fatalf("new ingress %+v", paths)
	}
	if a := paths["ingress[1].description"]; a.Observed != "[redacted]" {
		t.Fatalf("canary description %+v", a)
	}
}

func TestDeletedRemoteObject(t *testing.T) {
	r := analyze(t, fixtures.DriftDeleted)
	c := r.Resources[0]
	if c.Classification != Missing || r.Summary.Missing != 1 || c.Module != "module.compute" {
		t.Fatalf("%+v", c)
	}
	if d := r.Details[c.ID]; d.Observed != nil {
		t.Fatalf("missing object has an observed value: %+v", d.Observed)
	}
}

func TestUnknownValueCannotBeVerified(t *testing.T) {
	r := analyze(t, fixtures.DriftUnknown)
	c := r.Resources[0]
	if c.Classification != CannotVerify || c.CannotVerify != 1 || r.Summary.CannotVerify != 1 {
		t.Fatalf("%+v", c)
	}
	if a := r.Details[c.ID].Attributes[0]; a.Kind != CannotVerify || a.Observed != nil || a.Recorded != false {
		t.Fatalf("%+v", a)
	}
	// Nothing is applied in a scan, so the full value never says "known after apply".
	if observed := r.Details[c.ID].Observed.(map[string]any); observed["map_public_ip_on_launch"] != UnverifiedMarker {
		t.Fatalf("%+v", observed)
	}
}

func TestInaccessibleIsIncomplete(t *testing.T) {
	r := analyze(t, fixtures.DriftInaccessible)
	if !r.Incomplete || !r.Errored || r.Summary.Changed != 1 || len(r.Warnings) == 0 {
		t.Fatalf("%+v", r)
	}
}

func TestMixedSkipsDataSources(t *testing.T) {
	r := analyze(t, fixtures.DriftMixed)
	if r.Summary.Total != 4 || r.Summary.Changed != 2 || r.Summary.Missing != 1 || r.Summary.CannotVerify != 1 {
		t.Fatalf("%+v", r.Summary)
	}
	for _, c := range r.Resources {
		if strings.HasPrefix(c.Address, "data.") {
			t.Fatal("data source listed as drift")
		}
	}
	if !strings.Contains(strings.Join(r.Warnings, " "), "data source") {
		t.Fatalf("%v", r.Warnings)
	}
}

func TestRefusesNonRefreshOnlyPlan(t *testing.T) {
	if _, err := Analyze(fixtures.DriftNotRefreshOnly); !errors.Is(err, ErrNotRefreshOnly) {
		t.Fatalf("got %v", err)
	}
	// A state document or garbage is refused by the plan parser.
	for _, doc := range [][]byte{fixtures.State, []byte(`{}`), []byte(`not json`)} {
		if _, err := Analyze(doc); err == nil {
			t.Fatal("accepted a non-plan document")
		}
	}
}

func TestUnrecognizedActionIsNotSafe(t *testing.T) {
	doc := strings.Replace(string(fixtures.DriftChangedTag), `"update"`, `"create"`, 1)
	r := analyze(t, []byte(doc))
	if r.Summary.Unrecognized != 1 || r.Resources[0].Classification != Unrecognized || len(r.Warnings) == 0 {
		t.Fatalf("%+v", r)
	}
}

// Drift rows come from plan rows: name/value pair secrets and values under a
// secret-shaped key stay hidden even though the provider did not flag them.
func TestPairValuesStayHidden(t *testing.T) {
	doc := `{"format_version":"1.2","planned_values":{"root_module":{}},"resource_changes":[],"resource_drift":[{"address":"aws_ecs_task_definition.app","mode":"managed","type":"aws_ecs_task_definition","name":"app","change":{"actions":["update"],
	"before":{"environment":[{"name":"DB_PASSWORD","value":"old-hunter2x"}]},
	"after":{"environment":[{"value":"hunter2x","name":"DB_PASSWORD"},{"key":"api_token","value":"hunter3x"}],"AKIAABCDEFGHIJKLMNOP":{"inner":"plainunder1"}},
	"after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}]}`
	r := analyze(t, []byte(doc))
	b, _ := json.Marshal(r.Details)
	for _, secret := range []string{"hunter2x", "hunter3x", "plainunder1"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("%q reached drift details: %s", secret, b)
		}
	}
	if d := r.Details[r.Resources[0].ID]; len(d.Attributes) != 4 {
		t.Fatalf("rows %+v", d.Attributes)
	}
}
