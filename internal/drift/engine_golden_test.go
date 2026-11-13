package drift

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real engine recordings (scripts/fixtures/record-engine.sh). Built-in resources never drift,
// so these pin the document shape, the output-change count and refusal of a normal plan.
func TestRealEngineScans(t *testing.T) {
	for _, engine := range []string{"terraform-1.5.5", "terraform-1.17.2", "tofu-1.13.9"} {
		t.Run(engine, func(t *testing.T) {
			dir := filepath.Join("..", "..", "fixtures", "engines", engine)
			scan, err := os.ReadFile(filepath.Join(dir, "drift.json"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := Analyze(scan)
			if err != nil {
				t.Fatalf("real refresh-only plan rejected: %v", err)
			}
			if r.Errored || r.Incomplete || r.Summary.Total != 0 || r.Summary.OutputChanges != 2 {
				t.Fatalf("result %+v", r)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), canary) {
				t.Fatal("canary visible")
			}
			normal, err := os.ReadFile(filepath.Join(dir, "plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Analyze(normal); !errors.Is(err, ErrNotRefreshOnly) {
				t.Fatalf("normal plan accepted as a scan: %v", err)
			}
		})
	}
}
