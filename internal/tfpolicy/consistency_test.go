package tfpolicy_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"hclaro/internal/profiles"
	"hclaro/internal/tfpolicy"
	"hclaro/internal/workspace"
)

// Every controlled name is treated the same by the engine child environment and a profile's
// composed environment whatever its case, and by the plan fingerprint whenever the engine
// reads it under that name.
func TestControlledNamesAreConsistent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("# x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A profile that sets an AWS profile and region, so those inherited names are replaced.
	resolved := profiles.Resolve(profiles.Profile{ProjectID: "p", AWSProfile: "dev", AWSRegion: "eu-west-1"}, root, nil)
	for _, c := range []struct {
		name            string
		rule            tfpolicy.Rule
		child, composed bool // the inherited item reaches the engine child / the composed env
		fingerprinted   bool // its value changes the plan fingerprint
	}{
		{"TF_LOG", tfpolicy.EngineOwned, false, false, false},
		{"TF_LOG_PATH", tfpolicy.EngineOwned, false, false, false},
		{"TF_IN_AUTOMATION", tfpolicy.EngineOwned, false, false, false},
		{"CHECKPOINT_DISABLE", tfpolicy.EngineOwned, false, false, false},
		{"TF_WORKSPACE", tfpolicy.Workspace, false, false, true},
		{"TF_CLI_ARGS", tfpolicy.CLIArgs, true, false, true},
		{"TF_CLI_ARGS_plan", tfpolicy.CLIArgs, true, false, true},
		{"TF_DATA_DIR", tfpolicy.DataDir, true, true, true},
		{"AWS_PROFILE", tfpolicy.AWSProfile, true, false, false},
		{"AWS_DEFAULT_PROFILE", tfpolicy.AWSProfile, true, false, false},
		{"AWS_REGION", tfpolicy.AWSRegion, true, false, false},
		{"AWS_DEFAULT_REGION", tfpolicy.AWSRegion, true, false, false},
		{"TF_VAR_region", tfpolicy.Passed, true, true, true},
		{"HOME", tfpolicy.Passed, true, true, false},
	} {
		for _, name := range []string{c.name, strings.ToLower(c.name), strings.ToUpper(c.name[:1]) + strings.ToLower(c.name[1:])} {
			// -no-color is the only TF_CLI_ARGS value that does not refuse.
			item := func(v string) string { return name + "=-no-color" + v }
			if got := tfpolicy.Controlled(name); got != c.rule {
				t.Errorf("%s: rule %d, want %d", name, got, c.rule)
			}
			if got := slices.Contains(tfpolicy.ChildEnv([]string{item("")}, true), item("")); got != c.child {
				t.Errorf("%s: engine child keeps it = %v", name, got)
			}
			composed, err := profiles.Compose([]string{item("")}, resolved)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := slices.Contains(composed, item("")); got != c.composed {
				t.Errorf("%s: composed keeps it = %v", name, got)
			}
			// The fingerprint follows the engine's own lookup: any case on Windows, exact elsewhere.
			fingerprinted := c.fingerprinted && (name == c.name || runtime.GOOS == "windows")
			a, b := workspace.Fingerprint(root, []string{item("")}), workspace.Fingerprint(root, []string{item(" ")})
			if a == "" || (a != b) != fingerprinted {
				t.Errorf("%s: fingerprinted = %v", name, a != b)
			}
		}
	}
}
