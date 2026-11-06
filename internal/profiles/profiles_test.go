package profiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"hclaro/internal/engine"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

var (
	projectA = strings.Repeat("a", 32)
	projectB = strings.Repeat("b", 32)
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s := Open(dir, time.Now())
	if status, notice := s.Status(); status != Ready {
		t.Fatalf("status %s: %s", status, notice)
	}
	return s
}

func TestProfilesSurviveRestartWithRevisions(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	created, err := s.Create(projectA, Input{Name: "Production EU", Workspace: "prod", VarFiles: []string{"env/prod.tfvars"}, Options: Options{Parallelism: 4, LockTimeoutSeconds: 60}, AWSProfile: "prod-admin", AWSRegion: "eu-west-1", EnvNames: []string{"VAULT_ADDR"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || !ValidID(created.ID) {
		t.Fatalf("created %+v", created)
	}
	if err := s.SetDefault(projectA, created.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Update(projectA, created.ID, 1, Input{Name: "Production EU", Workspace: "prod", AWSProfile: "prod-readonly"})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	// A stale revision (another tab edited it) is refused, not merged.
	if _, err := s.Update(projectA, created.ID, 1, Input{Name: "Other"}); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale update: %v", err)
	}
	restarted := open(t, dir)
	list, def, err := restarted.List(projectA)
	if err != nil || len(list) != 1 || def != created.ID || list[0].Revision != 2 || list[0].AWSProfile != "prod-readonly" || len(list[0].VarFiles) != 0 {
		t.Fatalf("after restart: %+v %q %v", list, def, err)
	}
	info, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	clone, err := restarted.Clone(projectA, created.ID, "Production EU (copy)")
	if err != nil || clone.ID == created.ID || clone.Revision != 1 || clone.AWSProfile != "prod-readonly" {
		t.Fatalf("clone %+v %v", clone, err)
	}
	if err := restarted.Delete(projectA, created.ID, 1); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := restarted.Delete(projectA, created.ID, 2); err != nil {
		t.Fatal(err)
	}
	list, def, _ = restarted.List(projectA)
	if len(list) != 1 || def != "" {
		t.Fatalf("after delete: %+v default %q", list, def)
	}
}

func TestProjectsSharingALabelAreIsolated(t *testing.T) {
	s := open(t, t.TempDir())
	a, err := s.Create(projectA, Input{Name: "staging", AWSProfile: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(projectB, Input{Name: "staging", AWSProfile: "team-b"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("shared ID")
	}
	if _, err := s.Get(projectA, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project get: %v", err)
	}
	if _, err := s.Update(projectA, b.ID, 1, Input{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project update: %v", err)
	}
	if err := s.Delete(projectA, b.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project delete: %v", err)
	}
	if err := s.SetDefault(projectA, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project default: %v", err)
	}
	listA, _, _ := s.List(projectA)
	listB, _, _ := s.List(projectB)
	if len(listA) != 1 || listA[0].AWSProfile != "team-a" || len(listB) != 1 || listB[0].AWSProfile != "team-b" {
		t.Fatalf("lists %+v %+v", listA, listB)
	}
	if _, err := s.Create(projectA, Input{Name: "STAGING"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate name within a project: %v", err)
	}
	if err := s.RemoveProject(projectA); err != nil {
		t.Fatal(err)
	}
	listA, _, _ = s.List(projectA)
	listB, _, _ = s.List(projectB)
	if len(listA) != 0 || len(listB) != 1 {
		t.Fatalf("remove project: %+v %+v", listA, listB)
	}
}

func TestValidationRefusesSecretsAndControlledNames(t *testing.T) {
	s := open(t, t.TempDir())
	for _, in := range []Input{
		{Name: ""},
		{Name: " padded"},
		{Name: canary},
		{Name: "AKIAIOSFODNN7EXAMPLE"},
		{Name: "ok", Engine: "bash"},
		{Name: "ok", Workspace: "../prod"},
		{Name: "ok", VarFiles: []string{"../outside.tfvars"}},
		{Name: "ok", VarFiles: []string{"/etc/passwd.tfvars"}},
		{Name: "ok", VarFiles: []string{".secrets/prod.tfvars"}},
		{Name: "ok", VarFiles: []string{"-destroy.tfvars"}},
		{Name: "ok", VarFiles: []string{"main.tf"}},
		{Name: "ok", VarFiles: []string{"terraform.tfvars"}},
		{Name: "ok", VarFiles: []string{"a.tfvars", "a.tfvars"}},
		{Name: "ok", Options: Options{Parallelism: 257}},
		{Name: "ok", Options: Options{LockTimeoutSeconds: -1}},
		{Name: "ok", AWSProfile: "AKIAIOSFODNN7EXAMPLE"},
		{Name: "ok", AWSProfile: canary},
		{Name: "ok", AWSProfile: "has space"},
		{Name: "ok", AWSRegion: "moon-1"},
		{Name: "ok", EnvNames: []string{"AWS_SECRET_ACCESS_KEY=" + canary}},
		{Name: "ok", EnvNames: []string{"TF_CLI_ARGS_plan"}},
		{Name: "ok", EnvNames: []string{"tf_cli_args"}},
		{Name: "ok", EnvNames: []string{"TF_WORKSPACE"}},
		{Name: "ok", EnvNames: []string{"AWS_PROFILE"}},
		{Name: "ok", EnvNames: []string{"TF_LOG_PATH"}},
		{Name: "ok", EnvNames: []string{"1BAD"}},
	} {
		var field *FieldError
		if _, err := s.Create(projectA, in); !errors.As(err, &field) {
			t.Errorf("accepted %+v: %v", in, err)
		}
	}
	list, _, _ := s.List(projectA)
	if len(list) != 0 {
		t.Fatalf("stored invalid profiles: %+v", list)
	}
}

// The profile file has no value fields: the canary held in the environment never reaches it,
// and a hand-edited file with a value field is refused instead of being loaded.
func TestNoSecretValuesPersisted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_SECRET_ACCESS_KEY", canary)
	t.Setenv("TF_VAR_db_password", canary)
	root := t.TempDir()
	write(t, root, "prod.tfvars", `db_password = "`+canary+`"`)
	s := open(t, dir)
	p, err := s.Create(projectA, Input{Name: "prod", VarFiles: []string{"prod.tfvars"}, AWSProfile: "prod", EnvNames: []string{"TF_VAR_db_password", "AWS_SECRET_ACCESS_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	r := Resolve(p, root, os.Environ())
	b, _ := json.Marshal(r)
	disk, _ := os.ReadFile(filepath.Join(dir, fileName))
	for name, text := range map[string]string{"resolved": string(b), "disk": string(disk)} {
		if strings.Contains(text, canary) {
			t.Fatalf("%s contains the canary: %s", name, text)
		}
	}
	if len(r.Env) != 2 || !r.Env[0].Present || !r.Env[1].Present {
		t.Fatalf("env status %+v", r.Env)
	}
	tampered := strings.Replace(string(disk), `"aws_profile": "prod"`, `"aws_profile": "prod", "aws_secret_access_key": "x"`, 1)
	if tampered == string(disk) {
		t.Fatal("fixture did not change")
	}
	write(t, dir, fileName, tampered)
	reopened := Open(dir, time.Now())
	if _, notice := reopened.Status(); !strings.Contains(notice, "moved") {
		t.Fatalf("tampered file loaded: %q", notice)
	}
	if list, _, _ := reopened.List(projectA); len(list) != 0 {
		t.Fatalf("tampered profiles: %+v", list)
	}
}

func TestNewerSchemaIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, fileName, `{"version": 99, "profiles": []}`)
	s := Open(dir, time.Now())
	if status, _ := s.Status(); status != Unavailable {
		t.Fatalf("status %s", status)
	}
	if _, err := s.Create(projectA, Input{Name: "x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write to newer schema: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, fileName)); !strings.Contains(string(b), "99") {
		t.Fatal("newer file overwritten")
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func canonical(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestVarFileResolutionIsSymlinkSafe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra privileges on Windows")
	}
	root := canonical(t, t.TempDir())
	outside := canonical(t, t.TempDir())
	write(t, root, "env/prod.tfvars", `region = "eu-west-1"`)
	write(t, root, ".private/hidden.tfvars", `x = 1`)
	write(t, outside, "stolen.tfvars", `x = 1`)
	links := map[string]string{
		"escape.tfvars":     filepath.Join(outside, "stolen.tfvars"),
		"env/up.tfvars":     filepath.Join("..", "..", filepath.Base(outside), "stolen.tfvars"),
		"linked.tfvars":     filepath.Join("env", "prod.tfvars"),
		"hidden.tfvars":     filepath.Join(".private", "hidden.tfvars"),
		"dangling.tfvars":   filepath.Join(root, "gone.tfvars"),
		"env/folder.tfvars": root,
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	for ref, code := range map[string]string{
		"escape.tfvars":     "var_file_outside_root",
		"hidden.tfvars":     "var_file_outside_root",
		"missing.tfvars":    "var_file_missing",
		"dangling.tfvars":   "var_file_missing",
		"env/folder.tfvars": "var_file_outside_root",
		"../x.tfvars":       "var_file_invalid",
	} {
		_, err := ResolveVarFile(root, ref)
		var vf *VarFileError
		if !errors.As(err, &vf) || vf.Code != code {
			t.Errorf("%s: got %v, want %s", ref, err, code)
			continue
		}
		if code == "var_file_missing" && !strings.Contains(vf.Message, "Restore") {
			t.Errorf("%s: message is not actionable: %q", ref, vf.Message)
		}
	}
	// The ../.. link resolves into another temp directory: outside the root.
	if _, err := ResolveVarFile(root, "env/up.tfvars"); err == nil {
		t.Error("relative escape accepted")
	}
	f, err := ResolveVarFile(root, "linked.tfvars")
	if err != nil || f.Path != "env/prod.tfvars" || !f.Linked || f.Size == 0 {
		t.Fatalf("linked inside root: %+v %v", f, err)
	}
	files, truncated := Candidates(root)
	refs := []string{}
	for _, f := range files {
		refs = append(refs, f.Ref)
	}
	if truncated || !slices.Equal(refs, []string{"env/prod.tfvars", "linked.tfvars"}) {
		t.Fatalf("candidates %v (truncated %v)", refs, truncated)
	}
	p := Profile{ID: NewID(), ProjectID: projectA, Name: "x", Revision: 1, VarFiles: []string{"env/prod.tfvars", "escape.tfvars"}}
	r := Resolve(p, root, nil)
	if issue := r.Blocking(); issue == nil || issue.Code != "var_file_outside_root" || len(r.VarFiles) != 1 {
		t.Fatalf("resolve with an escaping file: %+v", r)
	}
}

// TF_CLI_ARGS* can add flags to any command. Composition refuses every injected flag, in any
// letter case, instead of trying to remove individual ones.
func TestComposeRefusesInjectedCLIArgs(t *testing.T) {
	r := Resolve(Profile{ID: NewID(), ProjectID: projectA, Name: "x", Revision: 1}, t.TempDir(), nil)
	for _, item := range []string{
		"TF_CLI_ARGS=-lock=false",
		"TF_CLI_ARGS_plan=-lock=false",
		"TF_CLI_ARGS_plan=-no-color -destroy",
		"TF_CLI_ARGS_apply=-auto-approve",
		"TF_CLI_ARGS_plan=-refresh=false",
		"TF_CLI_ARGS_plan=-out=/tmp/elsewhere",
		"tf_cli_args_apply=-auto-approve",
		"Tf_Cli_Args=-lock=false",
	} {
		if _, err := Compose([]string{"PATH=/bin", item}, r); !errors.Is(err, engine.ErrUnsafeArgs) {
			t.Errorf("%s accepted: %v", item, err)
		}
		if issue := Resolve(Profile{ID: NewID(), ProjectID: projectA, Name: "x", Revision: 1}, t.TempDir(), []string{item}).Blocking(); issue == nil || issue.Code != "unsafe_cli_args" {
			t.Errorf("%s not reported as an issue", item)
		}
	}
	env, err := Compose([]string{"PATH=/bin", "TF_CLI_ARGS=-no-color", "TF_WORKSPACE=prod", "TF_LOG=TRACE", "TF_LOG_PATH=/tmp/log"}, r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env, []string{"PATH=/bin"}) {
		t.Fatalf("env %v", env)
	}
}

func TestComposeUsesTheChosenAWSProfile(t *testing.T) {
	base := []string{"PATH=/bin", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", "AWS_SECRET_ACCESS_KEY=" + canary, "AWS_SESSION_TOKEN=" + canary, "AWS_PROFILE=other", "AWS_REGION=us-east-1", "AWS_CONFIG_FILE=/home/me/.aws/config", "TF_VAR_region=eu"}
	p := Profile{ID: NewID(), ProjectID: projectA, Name: "prod", Revision: 1, AWSProfile: "prod-sso", AWSRegion: "eu-west-1"}
	r := Resolve(p, t.TempDir(), base)
	env, err := Compose(base, r)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/bin", "AWS_CONFIG_FILE=/home/me/.aws/config", "TF_VAR_region=eu", "AWS_PROFILE=prod-sso", "AWS_REGION=eu-west-1", "AWS_DEFAULT_REGION=eu-west-1"}
	if !slices.Equal(env, want) {
		t.Fatalf("env\n got %v\nwant %v", env, want)
	}
	notices := strings.Join(r.Notices, "\n")
	if !strings.Contains(notices, "AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN are not passed") || strings.Contains(notices, canary) || !strings.Contains(notices, "does not verify") {
		t.Fatalf("notices %q", notices)
	}
	if r.Identity != IdentityNotVerified {
		t.Fatalf("identity %q", r.Identity)
	}
	// Without an AWS profile the inherited credential chain is left alone.
	plain := Resolve(Profile{ID: NewID(), ProjectID: projectA, Name: "plain", Revision: 1}, t.TempDir(), base)
	env, _ = Compose(base, plain)
	if !slices.Contains(env, "AWS_PROFILE=other") || !slices.Contains(env, "AWS_SECRET_ACCESS_KEY="+canary) {
		t.Fatalf("inherited chain changed: %v", env)
	}
}

func TestFingerprintChangesWithRevisionAndContext(t *testing.T) {
	root := t.TempDir()
	p := Profile{ID: NewID(), ProjectID: projectA, Name: "x", Revision: 1, AWSProfile: "a"}
	first := Resolve(p, root, nil).Fingerprint
	if again := Resolve(p, root, nil).Fingerprint; again != first {
		t.Fatal("fingerprint is not deterministic")
	}
	p.Revision = 2
	if Resolve(p, root, nil).Fingerprint == first {
		t.Fatal("revision not covered")
	}
	p.Revision = 1
	p.ProjectID = projectB
	if Resolve(p, root, nil).Fingerprint == first {
		t.Fatal("project not covered")
	}
	p.ProjectID = projectA
	p.EnvNames = []string{"VAULT_ADDR"}
	missing := Resolve(p, root, nil)
	present := Resolve(p, root, []string{"VAULT_ADDR=https://vault"})
	if missing.Fingerprint == present.Fingerprint || missing.Blocking() == nil || missing.Blocking().Code != "env_missing" || present.Blocking() != nil {
		t.Fatalf("env presence: %+v %+v", missing, present)
	}
}
