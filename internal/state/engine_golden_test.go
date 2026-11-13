package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real `show -json` state output recorded by scripts/fixtures/record-engine.sh from the
// hand-written synthetic state in fixtures/engines/project.
func TestRealEngineStates(t *testing.T) {
	for _, engine := range []string{"terraform-1.5.5", "terraform-1.17.2", "tofu-1.13.9"} {
		t.Run(engine, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "engines", engine, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			index, err := Parse(input)
			if err != nil {
				t.Fatalf("real state rejected: %v", err)
			}
			if index.Managed != 11 || index.Data != 0 || index.Types["terraform_data"] != 11 {
				t.Fatalf("managed %d data %d types %v", index.Managed, index.Data, index.Types)
			}
			found := false
			for _, r := range index.Resources {
				found = found || r.Address == `module.group["a"].terraform_data.this`
			}
			if !found {
				t.Fatal("module instance missing")
			}
			b, _ := json.Marshal(index.Resources)
			if strings.Contains(string(b), canary) {
				t.Fatal("canary visible")
			}
		})
	}
}
