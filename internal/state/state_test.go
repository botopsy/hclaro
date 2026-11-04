package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"hclaro/fixtures"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func TestFixtureIndexAndRedaction(t *testing.T) {
	idx, err := Parse(fixtures.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Resources) != 316 || idx.Managed != 315 || idx.Data != 1 || len(idx.Modules) != 8 {
		t.Fatalf("wrong counts: %d %d %d %d", len(idx.Resources), idx.Managed, idx.Data, len(idx.Modules))
	}
	encoded, _ := json.Marshal(idx)
	if strings.Contains(string(encoded), canary) {
		t.Fatal("secret crossed parser boundary")
	}
	var found bool
	for _, r := range idx.Resources {
		if r.Address == `module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]` {
			found = true
			if r.Module != `module.compute.module.workers["blue.eu"]` {
				t.Fatal(r.Module)
			}
		}
		if r.Address == "aws_s3_bucket.artifacts" {
			v := r.Attributes.(map[string]any)
			if v["id"] != "hclaro-demo-artifacts" || v["name"] != Redacted || v["marked"] != Redacted {
				t.Fatalf("unexpected redaction: %v", v)
			}
		}
	}
	if !found {
		t.Fatal("canonical quoted address lost")
	}
}
func TestEmptyAndMalformed(t *testing.T) {
	idx, err := Parse(fixtures.Empty)
	if err != nil || len(idx.Resources) != 0 {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{"format_version":"1.0"}`)); !errors.Is(err, ErrNoState) {
		t.Fatalf("missing state: %v", err)
	}
	for _, input := range []string{`{}`, `null`, `{"format_version":"2.0"}`, `{"format_version":"1.0","planned_values":{}}`, `{"format_version":"1.0"} {}`, `{"format_version":"1.0","values":{"root_module":{"resources":[{"address":"x"}]}}}`} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
func TestConservativeRedaction(t *testing.T) {
	v := map[string]any{"name": "demo", "id": "https://user:password@host", "unknown": canary, "password": 1234, "nested": []any{map[string]any{"name": canary}}, "tokens": []any{canary}, canary: "key", "arn": "safe-but-marked"}
	out := redact(v, map[string]any{"arn": true}, "", 0)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), canary) || strings.Contains(string(b), "1234") || strings.Contains(string(b), "password@host") || strings.Contains(string(b), "safe-but-marked") {
		t.Fatalf("leak: %s", b)
	}
}

func TestSensitivityArrayAndEscapedKeys(t *testing.T) {
	var values, masks any
	_ = json.Unmarshal([]byte(`{"items":[{"id":"normal-looking-private-id"},{"name":"public-name"}],"pass\u0077ord":"private-value"}`), &values)
	_ = json.Unmarshal([]byte(`{"items":[{"id":true},false]}`), &masks)
	b, _ := json.Marshal(redact(values, masks, "", 0))
	if strings.Contains(string(b), "normal-looking-private-id") || strings.Contains(string(b), "private-value") || !strings.Contains(string(b), "public-name") {
		t.Fatal("incorrect nested sensitivity handling")
	}
}

func BenchmarkParseFixture(b *testing.B) {
	for range b.N {
		if _, err := Parse(fixtures.State); err != nil {
			b.Fatal(err)
		}
	}
}
func TestThousandInstances(t *testing.T) {
	resources := make([]string, 1000)
	for i := range resources {
		resources[i] = fmt.Sprintf(`{"address":"example_resource.main[%d]","mode":"managed","type":"example_resource","name":"main","index":%d,"values":{"id":"demo-%d"}}`, i, i, i)
	}
	input := []byte(`{"format_version":"1.0","values":{"root_module":{"resources":[` + strings.Join(resources, ",") + `]}}}`)
	idx, err := Parse(input)
	if err != nil || len(idx.Resources) != 1000 {
		t.Fatalf("count=%d err=%v", len(idx.Resources), err)
	}
	duplicate := strings.Replace(string(input), `main[999]`, `main[0]`, 1)
	if _, err := Parse([]byte(duplicate)); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestLongCanonicalModuleAddress(t *testing.T) {
	module := strings.TrimSuffix(strings.Repeat("module.deep.", 60), ".")
	address := module + `.aws_instance.main["blue.eu"]`
	input := fmt.Sprintf(`{"format_version":"1.0","values":{"root_module":{"child_modules":[{"address":%q,"resources":[{"address":%q,"mode":"managed","type":"aws_instance","name":"main","values":{"id":"public-instance"}}]}]}}}`, module, address)
	idx, err := Parse([]byte(input))
	if err != nil || len(idx.Resources) != 1 || idx.Resources[0].Address != address || idx.Resources[0].Module != module {
		t.Fatal("long canonical address was altered or rejected")
	}
}
func FuzzParse(f *testing.F) {
	f.Add(fixtures.Empty)
	f.Add([]byte(`{"format_version":"1.0"}`))
	f.Fuzz(func(t *testing.T, input []byte) { _, _ = Parse(input) })
}
