package profiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests pin the profiles.json compatibility contract (docs/config-compatibility.md)
// against versioned fixtures in testdata/compat.

var compatNow = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

const fixtureProject = "0123456789abcdef0123456789abcdef"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "compat", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSchema1FixtureLoadsAndUpgradesWithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, fixture(t, "v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, compatNow)
	if status, notice := s.Status(); status != Ready || notice != "" {
		t.Fatalf("%s %q", status, notice)
	}
	want := Profile{ID: "fedcba9876543210fedcba9876543210", ProjectID: fixtureProject, Name: "dev", Engine: "terraform", Workspace: "dev", VarFiles: []string{"dev.tfvars"}, Options: Options{Parallelism: 4, LockTimeoutSeconds: 30}, AWSProfile: "sandbox", AWSRegion: "eu-west-1", EnvNames: []string{"TF_VAR_owner"}, Revision: 3, CreatedAt: "2026-11-01T09:00:00Z", UpdatedAt: "2026-11-04T12:00:00Z"}
	list, def, err := s.List(fixtureProject)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], want) || def != want.ID {
		t.Fatalf("%+v %q %v", list, def, err)
	}
	if _, err := s.Create(fixtureProject, Input{Name: "prod"}); err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(mustRead(t, path), &doc); err != nil || doc.Version != 2 || len(doc.Profiles) != 2 || !reflect.DeepEqual(doc.Profiles[0], want) || doc.Defaults[fixtureProject] != want.ID {
		t.Fatalf("rewritten file: %+v %v", doc, err)
	}
	// The schema 1 bytes were kept before the first schema 2 write.
	backups, _ := filepath.Glob(path + ".schema-1-*")
	if len(backups) != 1 || string(mustRead(t, backups[0])) != string(fixture(t, "v1.json")) {
		t.Fatalf("backup %v", backups)
	}
	if _, notice := s.Status(); !strings.Contains(notice, "upgraded from schema 1 to schema 2") {
		t.Fatalf("notice %q", notice)
	}
	// Further writes do not add backups.
	if _, err := s.Create(fixtureProject, Input{Name: "stage"}); err != nil {
		t.Fatal(err)
	}
	if backups, _ := filepath.Glob(path + ".schema-*"); len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
}

func TestNewerSchemaFixtureIsNeverChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	newer := fixture(t, "v3-newer.json")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, compatNow)
	if status, notice := s.Status(); status != Unavailable || !strings.Contains(notice, "newer Hclaro (schema 3)") {
		t.Fatalf("%s %q", status, notice)
	}
	if list, _, _ := s.List(fixtureProject); len(list) != 0 {
		t.Fatalf("decoded a newer file: %+v", list)
	}
	if _, err := s.Create(fixtureProject, Input{Name: "x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write allowed: %v", err)
	}
	if string(mustRead(t, path)) != string(newer) {
		t.Fatal("newer profiles changed")
	}
	if matches, _ := filepath.Glob(path + ".*"); len(matches) != 0 {
		t.Fatalf("newer profiles moved aside: %v", matches)
	}
}

// A newer Hclaro that replaces the file while this one runs must not be downgraded.
func TestNewerSchemaWrittenDuringSessionIsNotDowngraded(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, compatNow)
	if _, err := s.Create(fixtureProject, Input{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	newer := fixture(t, "v3-newer.json")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(fixtureProject, Input{Name: "b"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write allowed over a newer file: %v", err)
	}
	if string(mustRead(t, path)) != string(newer) {
		t.Fatal("newer profiles downgraded")
	}
}

// Versions this build does not write are never decoded or overwritten in place: the exact
// bytes are kept in a .corrupt-<time> file next to profiles.json.
func TestInvalidSchemaVersionsAreKeptAside(t *testing.T) {
	for name, body := range map[string]string{
		"zero":     `{"version":0,"profiles":[],"defaults":{}}`,
		"negative": `{"version":-1,"profiles":[],"defaults":{}}`,
		"string":   `{"version":"1","profiles":[],"defaults":{}}`,
		"null":     `{"version":null,"profiles":[],"defaults":{}}`,
		"missing":  `{"profiles":[],"defaults":{}}`,
		"fraction": `{"version":1.5,"profiles":[],"defaults":{}}`,
		"array":    `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, fileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			s := Open(dir, compatNow)
			if status, notice := s.Status(); status != Ready || !strings.Contains(notice, "profiles.json.corrupt-20261105T100000Z") {
				t.Fatalf("%s %q", status, notice)
			}
			if string(mustRead(t, path+".corrupt-20261105T100000Z")) != body {
				t.Fatal("original bytes not kept")
			}
		})
	}
}

func TestSchema2FixtureLoadsTheAuthBinding(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), fixture(t, "v2.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, compatNow)
	list, _, err := s.List(fixtureProject)
	if err != nil || len(list) != 1 || list[0].AWSAuth != AWSVault || list[0].AWSExpectedAccount != "111122223333" || list[0].AWSExpectedRole != "arn:aws:sts::111122223333:assumed-role/Deploy/*" {
		t.Fatalf("%+v %v", list, err)
	}
}

// A schema 1 file cannot carry schema 2 fields: it is treated as unrecognized.
func TestSchema1WithAuthFieldsIsUnrecognized(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(string(fixture(t, "v1.json")), `"aws_region": "eu-west-1",`, `"aws_region": "eu-west-1", "aws_expected_account": "111122223333",`, 1)
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, notice := Open(dir, compatNow).Status(); !strings.Contains(notice, "corrupt") {
		t.Fatalf("notice %q", notice)
	}
}

func TestSchemaUpgradeWithoutBackupLeavesFileUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, fixture(t, "v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, compatNow)
	// A read-only folder refuses the backup copy (and any new file).
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := s.Create(fixtureProject, Input{Name: "prod"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write allowed without backup: %v", err)
	}
	if string(mustRead(t, path)) != string(fixture(t, "v1.json")) {
		t.Fatal("file changed")
	}
	if status, notice := s.Status(); status != Unavailable || !strings.Contains(notice, "backup copy could not be written") {
		t.Fatalf("%s %q", status, notice)
	}
}

// A second upgrade backup in the same second (for example after an older Hclaro wrote schema
// 1 again) gets its own name instead of making profiles read-only.
func TestSchemaUpgradeBackupsNeverCollide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, fixture(t, "v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, compatNow)
	s.Now = func() time.Time { return compatNow }
	if _, err := s.Create(fixtureProject, Input{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture(t, "v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(fixtureProject, Input{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".schema-1-*")
	if len(backups) != 2 || !slices.Contains(backups, path+".schema-1-20261105T100000Z-2") {
		t.Fatalf("%v", backups)
	}
	if status, _ := s.Status(); status != Ready {
		t.Fatal(status)
	}
}

// A hand-edited schema 2 file that spells out the default method stays readable.
func TestSchema2AcceptsExplicitAWSCLI(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(string(fixture(t, "v2.json")), `"aws_auth": "aws-vault"`, `"aws_auth": "aws-cli"`, 1)
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, _, err := Open(dir, compatNow).List(fixtureProject); err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
}
