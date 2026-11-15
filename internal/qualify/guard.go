package qualify

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"hclaro/fixtures"
)

// Fixture guard (M27.02): a project may use only the local backend, the built-in
// terraform provider, hashicorp/random and hashicorp/local, and relative module sources.
// shortcut: a textual scan, not an HCL parser (go.mod has no dependencies); it fails closed on
// anything it cannot classify (JSON or override files, unknown sources, types or backends).
// Upgrade to hclsyntax if fixtures ever need constructs this rejects.
var (
	guardBackend      = regexp.MustCompile(`\bbackend\s+"([^"]*)"\s*\{`)
	guardBackendAttr  = regexp.MustCompile(`\bbackend\s*=\s*"([^"]*)"`)
	guardCloud        = regexp.MustCompile(`(^|[\s{])cloud\s*\{`)
	guardSource       = regexp.MustCompile(`\bsource\s*=\s*"([^"]*)"`)
	guardBlock        = regexp.MustCompile(`(?m)^\s*(resource|data|provider|module|ephemeral|import|check)\b\s*("[^"]*")?\s*("[^"]*")?`)
	guardLockProvider = regexp.MustCompile(`(?m)^provider\s+"([^"]*)"`)
	guardLockAny      = regexp.MustCompile(`(?m)^(\w+)`)
	guardProvisioner  = regexp.MustCompile(`\bprovisioner\b`)
)

// gateFiles are the only project files that may hold a provisioner: the OD-8 gated fixture
// (M27.03 A6, A7, A7b), byte-identical to fixtures/qualify/launch-gate.
var gateFiles = []string{"main.tf", "gate.sh"}

var allowedProviderSources = map[string]bool{
	"hashicorp/random": true, "hashicorp/local": true,
	"registry.terraform.io/hashicorp/random": true, "registry.terraform.io/hashicorp/local": true,
	"registry.opentofu.org/hashicorp/random": true, "registry.opentofu.org/hashicorp/local": true,
}

var allowedLockProviders = map[string]bool{
	"registry.terraform.io/hashicorp/random": true, "registry.terraform.io/hashicorp/local": true,
	"registry.opentofu.org/hashicorp/random": true, "registry.opentofu.org/hashicorp/local": true,
}

func allowedType(kind, typ string) bool {
	switch kind {
	case "resource", "ephemeral":
		return typ == "terraform_data" || strings.HasPrefix(typ, "random_") || strings.HasPrefix(typ, "local_")
	case "data":
		return typ == "terraform_remote_state" || strings.HasPrefix(typ, "local_")
	case "provider":
		return typ == "random" || typ == "local"
	}
	return true
}

// GuardFixture returns a non-nil error (reason fixture_scope) when the project at root uses
// anything outside the approved scope.
func GuardFixture(root string) error {
	var problems []string
	provisioner := false
	bad := func(rel, format string, args ...any) {
		problems = append(problems, rel+": "+fmt.Sprintf(format, args...))
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		switch {
		case name == ".terraform.lock.hcl":
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range guardLockAny.FindAllStringSubmatch(string(b), -1) {
				if m[1] != "provider" {
					bad(rel, "unexpected top-level %q", m[1])
				}
			}
			for _, m := range guardLockProvider.FindAllStringSubmatch(string(b), -1) {
				if !allowedLockProviders[m[1]] {
					bad(rel, "provider %q is not approved", m[1])
				}
			}
		case strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tofu.json"):
			bad(rel, "JSON configuration is not classified by the guard")
		case strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tofu"):
			base := strings.TrimSuffix(strings.TrimSuffix(name, ".tf"), ".tofu")
			if base == "override" || strings.HasSuffix(base, "_override") {
				bad(rel, "override files are not classified by the guard")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			guardHCL(rel, string(b), bad)
			if guardProvisioner.Match(b) {
				provisioner = true
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("fixture_scope: %v", err)
	}
	if provisioner && !isGateFixture(root) {
		bad(".", "provisioners are allowed only in the approved OD-8 gated fixture (byte-identical main.tf and gate.sh)")
	}
	if len(problems) > 0 {
		return fmt.Errorf("fixture_scope: %s", strings.Join(problems, "; "))
	}
	return nil
}

func guardHCL(rel, text string, bad func(string, string, ...any)) {
	for _, m := range guardBackend.FindAllStringSubmatch(text, -1) {
		if m[1] != "local" {
			bad(rel, "backend %q", m[1])
		}
	}
	for _, m := range guardBackendAttr.FindAllStringSubmatch(text, -1) {
		if m[1] != "local" {
			bad(rel, "remote state backend %q", m[1])
		}
	}
	if guardCloud.MatchString(text) {
		bad(rel, "cloud block")
	}
	for _, m := range guardSource.FindAllStringSubmatch(text, -1) {
		src := m[1]
		relative := strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../")
		if !relative && !allowedProviderSources[src] {
			bad(rel, "source %q is neither an approved provider nor a relative module path", src)
		}
	}
	for _, m := range guardBlock.FindAllStringSubmatch(text, -1) {
		typ := strings.Trim(m[2], `"`)
		if m[1] == "provider" || m[1] == "resource" || m[1] == "data" || m[1] == "ephemeral" {
			if !allowedType(m[1], typ) {
				bad(rel, "%s %q is not approved", m[1], typ)
			}
		}
	}
}

// GuardRemote is the guard for the M27.05 H12 project, the only one allowed a non-local
// backend: it passes only when main.tf is the committed hub-remote file byte for byte and the
// guard's only finding is that file's s3 backend (never initialized, never reached).
func GuardRemote(root string) error {
	err := GuardFixture(root)
	want, _ := fs.ReadFile(fixtures.Qualify, "qualify/hub-remote/main.tf")
	got, _ := os.ReadFile(filepath.Join(root, "main.tf"))
	if err == nil || !bytes.Equal(want, got) || err.Error() != `fixture_scope: main.tf: backend "s3"` {
		return fmt.Errorf("fixture_scope: the H12 backend exception does not apply: %v", err)
	}
	return nil
}

// isGateFixture reports whether root holds the approved gated fixture's main.tf and gate.sh
// unchanged.
func isGateFixture(root string) bool {
	for _, name := range gateFiles {
		want, err := fs.ReadFile(fixtures.Qualify, "qualify/launch-gate/"+name)
		got, err2 := os.ReadFile(filepath.Join(root, name))
		if err != nil || err2 != nil || !bytes.Equal(want, got) {
			return false
		}
	}
	return true
}
