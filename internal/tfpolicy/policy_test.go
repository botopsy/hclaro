package tfpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestVersionCompatibility(t *testing.T) {
	for _, v := range []string{"0.12.0", "2.0.0", "1.18.0", "1.9.0-beta", "bogus"} {
		if Supported("terraform", v) {
			t.Fatal(v)
		}
	}
	if !Supported("tofu", "1.6.0") || !Supported("terraform", "1.5.0") || Supported("tofu", "1.5.0") {
		t.Fatal("incorrect compatibility")
	}
	// Exact accepted edges, documented in docs/support-matrix.md, README.md and the version
	// error. Widening either range needs real-engine evidence in the support matrix first.
	for v, want := range map[string]bool{
		"terraform 1.4.7": false, "terraform 1.5.0": true, "terraform 1.17.99": true, "terraform 1.18.0": false,
		"tofu 1.5.9": false, "tofu 1.6.0": true, "tofu 1.13.99": true, "tofu 1.14.0": false,
		"terraform 1.5.0-rc1": false, "tofu 1.9.0-beta1": false, "opentofu 1.6.0": false,
		"terraform v1.5.0": false, "terraform 1.5": false, "terraform 1.5.0.1": false,
	} {
		name, version, _ := strings.Cut(v, " ")
		if Supported(name, version) != want {
			t.Errorf("Supported(%s) = %v", v, !want)
		}
	}
}

func TestImportSupport(t *testing.T) {
	for _, c := range []struct {
		name, version string
		want          bool
	}{
		{"terraform", "1.5.0", true}, {"terraform", "1.9.8", true}, {"tofu", "1.6.0", true},
		{"terraform", "1.4.6", false}, {"tofu", "1.5.0", false}, {"terraform", "1.10.0-rc1", false}, {"terraform", "2.0.0", false},
	} {
		if got := Supports(c.name, c.version, ImportBlocks); got != c.want {
			t.Errorf("%s %s: %v", c.name, c.version, got)
		}
	}
}

// Graph, refresh-only and init -json edges.
func TestSupports(t *testing.T) {
	for _, c := range []struct {
		name, version string
		feature       Feature
		want          bool
	}{
		{"terraform", "1.5.0", Graph, true}, {"terraform", "1.17.0", Graph, true}, {"tofu", "1.13.1", Graph, true}, {"terraform", "1.4.0", Graph, false}, {"tofu", "", Graph, false},
		{"terraform", "1.5.0", RefreshOnly, true}, {"terraform", "1.17.2", RefreshOnly, true}, {"tofu", "1.6.0", RefreshOnly, true}, {"tofu", "1.13.0", RefreshOnly, true},
		{"terraform", "1.4.6", RefreshOnly, false}, {"tofu", "1.5.0", RefreshOnly, false}, {"terraform", "1.9.0-beta1", RefreshOnly, false}, {"terraform", "", RefreshOnly, false},
		{"terraform", "1.9.0", InitJSON, true}, {"terraform", "1.17.2", InitJSON, true}, {"terraform", "1.8.5", InitJSON, false}, {"tofu", "1.10.0", InitJSON, false},
		{"terraform", "1.99.0", InitJSON, false}, {"terraform", "2.0.0", InitJSON, false},
	} {
		if got := Supports(c.name, c.version, c.feature); got != c.want {
			t.Errorf("%s %s feature %d: %v", c.name, c.version, c.feature, got)
		}
	}
}

func TestUnsafeArgs(t *testing.T) {
	for _, c := range []struct {
		env      string
		commands []string
		want     bool
	}{
		{"TF_CLI_ARGS=-lock=false", []string{"plan"}, true},
		{"TF_CLI_ARGS_plan=-target=x", []string{"plan"}, true},
		{"tf_cli_args_plan=-target=x", []string{"plan"}, true},
		{"TF_CLI_ARGS_apply=-auto-approve", []string{"plan"}, false},
		{"TF_CLI_ARGS_plan=-no-color", []string{"plan"}, false},
		{"TF_CLI_ARGS_plan=  -no-color  -no-color ", []string{"plan"}, false},
		{"TF_CLI_ARGS_plan=-no-color -refresh=false", []string{"plan"}, true},
		{"TF_CLI_ARGS_plan=", []string{"plan"}, false},
		{"TF_CLI_ARGSX=-x", []string{"plan"}, false},
		{"TF_CLI_ARGS_workspace=-force", []string{"workspace delete"}, true},
		{"TF_CLI_ARGS_workspace_delete=-force", []string{"workspace delete"}, true},
		{"TF_CLI_ARGS_workspace_delete=-force", []string{"workspace new", "show"}, false},
		{"TF_CLI_ARGS_workspace_deletex=-force", []string{"workspace delete"}, false},
		{"TF_CLI_ARGS_providers_lock=-x", []string{"providers lock"}, true},
		{"TF_CLI_ARGS_force_unlock=-force", []string{"force-unlock"}, true},
		{"TF_CLI_ARGS_show=-x", []string{""}, false},
		// No commands means every TF_CLI_ARGS* variable.
		{"TF_CLI_ARGS_anything=-x", nil, true},
		{"TF_CLI_ARGSX=-x", nil, true},
		{"TF_CLI_ARGS_anything=-no-color", nil, false},
		{"TF_LOG=-x", nil, false},
	} {
		if got := UnsafeArgs([]string{c.env}, c.commands...); got != c.want {
			t.Errorf("UnsafeArgs(%q, %q) = %v", c.env, c.commands, got)
		}
		// The help names exactly the variables the check refuses.
		if got := UnsafeArgsHelp([]string{c.env}, c.commands...) != ""; got != c.want {
			t.Errorf("UnsafeArgsHelp(%q, %q) non-empty = %v", c.env, c.commands, got)
		}
	}
}

// Root files are listed even when a folder that sorts first fills the listing limit, and a
// link into node_modules (which the plan fingerprint skips) is never selectable.
func TestVarFilesListsRootFirstAndRefusesNodeModules(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("a = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 600 {
		write(fmt.Sprintf("a/f%03d.tfvars", i))
	}
	write("z.tfvars")
	write("node_modules/cfg/prod.tfvars")
	if err := os.Symlink(filepath.Join(root, "node_modules", "cfg", "prod.tfvars"), filepath.Join(root, "linked.tfvars")); err != nil {
		t.Fatal(err)
	}
	files, truncated, err := VarFiles(root)
	if err != nil || !truncated || len(files) != 500 {
		t.Fatalf("%d files, truncated %v, %v", len(files), truncated, err)
	}
	if !slices.ContainsFunc(files, func(f VarFile) bool { return f.Ref == "z.tfvars" }) {
		t.Fatal("root file crowded out by a large folder")
	}
	if slices.ContainsFunc(files, func(f VarFile) bool { return f.Ref == "linked.tfvars" }) {
		t.Fatal("link into node_modules listed")
	}
	for _, ref := range []string{"linked.tfvars", "node_modules/cfg/prod.tfvars"} {
		if _, err := ResolveVarFile(root, ref); err == nil {
			t.Errorf("%s resolved", ref)
		}
	}
}

func TestSupportTextMatchesSupported(t *testing.T) {
	for _, c := range []struct {
		name, lo, hi, label string
	}{{"terraform", "1.5.0", "1.17.0", "Terraform 1.5–1.17"}, {"tofu", "1.6.0", "1.13.0", "OpenTofu 1.6–1.13"}} {
		if !Supported(c.name, c.lo) || !Supported(c.name, c.hi) || !strings.Contains(SupportText, c.label) {
			t.Errorf("%s: SupportText %q disagrees with Supported", c.name, SupportText)
		}
	}
}
