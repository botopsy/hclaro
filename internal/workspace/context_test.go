package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintInputs(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "main.tf"), `resource "terraform_data" "a" {}`)
	write(t, filepath.Join(root, ".terraform", "terraform.tfstate"), `{"backend":{"type":"local"}}`)
	write(t, filepath.Join(root, ".terraform", "modules", "modules.json"), `{"Modules":[]}`)
	write(t, filepath.Join(root, ".terraform", "modules", "net", "main.tf"), `variable "x" {}`)
	env := []string{"TF_VAR_region=eu-west-1", "HOME=/home/someone"}
	base := Fingerprint(root, env, "terraform", "1.9.8", "default")
	if base == "" || base != Fingerprint(root, env, "terraform", "1.9.8", "default") {
		t.Fatal("fingerprint not reproducible")
	}
	if Fingerprint(root, []string{"HOME=/elsewhere", "TF_VAR_region=eu-west-1", "PATH=/bin"}, "terraform", "1.9.8", "default") != base {
		t.Fatal("unrelated environment or ordering changed the fingerprint")
	}
	// Each input below changes what a plan would contain.
	changes := map[string]func(){
		"TF_VAR value":       func() { env[0] = "TF_VAR_region=us-east-1" },
		"TF_WORKSPACE":       func() { env = append(env, "TF_WORKSPACE=prod") },
		"selected workspace": func() { write(t, filepath.Join(root, ".terraform", "environment"), "prod") },
		"backend config":     func() { write(t, filepath.Join(root, ".terraform", "terraform.tfstate"), `{"backend":{"type":"s3"}}`) },
		"module manifest":    func() { write(t, filepath.Join(root, ".terraform", "modules", "modules.json"), `{"Modules":[{}]}`) },
		"installed module":   func() { write(t, filepath.Join(root, ".terraform", "modules", "net", "main.tf"), `variable "y" {}`) },
		"configuration":      func() { write(t, filepath.Join(root, "main.tf"), `# changed`) },
	}
	for name, change := range changes {
		before := Fingerprint(root, env, "terraform", "1.9.8", "default")
		change()
		if Fingerprint(root, env, "terraform", "1.9.8", "default") == before {
			t.Errorf("%s: change not reflected", name)
		}
	}
	// Provider binaries are covered by the lock file, not hashed directly.
	before := Fingerprint(root, env, "terraform", "1.9.8", "default")
	write(t, filepath.Join(root, ".terraform", "providers", "registry", "plugin"), "binary")
	if Fingerprint(root, env, "terraform", "1.9.8", "default") != before {
		t.Error("provider binaries should not be hashed")
	}
}

func TestFingerprintDataDir(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "main.tf"), "")
	env := []string{"TF_DATA_DIR=.tfdata"}
	before := Fingerprint(root, env)
	write(t, filepath.Join(root, ".tfdata", "environment"), "staging")
	if Fingerprint(root, env) == before {
		t.Fatal("TF_DATA_DIR not followed")
	}
	before = Fingerprint(root, env)
	write(t, filepath.Join(root, ".terraform", "environment"), "ignored")
	if Fingerprint(root, env) != before {
		t.Fatal("default data directory read although TF_DATA_DIR is set")
	}
}

func TestFingerprintNamesWhatCouldNotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced here")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "main.tf"), "")
	write(t, filepath.Join(root, "modules", "net", "main.tf"), "")
	dir := filepath.Join(root, "modules", "net")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	fp, err := FingerprintExcept(root, nil, nil)
	if fp != "" || err == nil || err.Error() != `"modules/net" could not be read (permission denied)` {
		t.Fatalf("%q %v", fp, err)
	}
}

func TestFingerprintWithholdsSecretLikeNames(t *testing.T) {
	key := "AKIAABCDEFGHIJKLMNOP"
	err := readError("/r", "/r/.terraform", &fs.PathError{Op: "open", Path: "/r/" + key + "/main.tf", Err: fs.ErrPermission})
	if strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "withheld") {
		t.Fatal(err)
	}
}

func TestFingerprintLimitsAreNamed(t *testing.T) {
	for err, want := range map[error]string{errFiles: "more than 10000 files", errBytes: "larger than 256 MiB"} {
		if got := readError("/r", "/r/.terraform", err).Error(); !strings.Contains(got, want) {
			t.Errorf("%s: %s", want, got)
		}
	}
}
