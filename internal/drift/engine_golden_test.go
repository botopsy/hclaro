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

// Real hashicorp/local 2.9.1 drift (M27.04, local-drift.json): out/gone.txt deleted and
// out/edited.txt edited outside the engine. Every CORE engine reports both as deleted (the
// provider drops an object whose file checksum no longer matches), so no approved provider
// yields attribute-level drift; out/same.txt is not listed.
func TestRealEngineLocalFileDrift(t *testing.T) {
	for _, engine := range []string{"terraform-1.5.5", "terraform-1.17.2", "tofu-1.13.9"} {
		t.Run(engine, func(t *testing.T) {
			scan, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", engine, "local-drift.json"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := Analyze(scan)
			if err != nil {
				t.Fatalf("real refresh-only plan rejected: %v", err)
			}
			got := map[string]string{}
			for _, c := range r.Resources {
				got[c.Address] = c.Classification + " " + strings.Join(c.Actions, ",")
			}
			want := map[string]string{"local_file.gone": Missing + " delete", "local_file.edited": Missing + " delete"}
			if r.Errored || r.Incomplete || r.Summary.Missing != 2 || r.Summary.Changed != 0 || r.Summary.Total != 2 || len(got) != 2 || got["local_file.gone"] != want["local_file.gone"] || got["local_file.edited"] != want["local_file.edited"] {
				t.Fatalf("summary %+v resources %v", r.Summary, got)
			}
		})
	}
}
