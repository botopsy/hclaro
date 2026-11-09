package profiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestSchema1FixtureLoadsAndStaysSchema1(t *testing.T) {
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
	if err := json.Unmarshal(mustRead(t, path), &doc); err != nil || doc.Version != 1 || len(doc.Profiles) != 2 || !reflect.DeepEqual(doc.Profiles[0], want) || doc.Defaults[fixtureProject] != want.ID {
		t.Fatalf("rewritten file: %+v %v", doc, err)
	}
}

func TestNewerSchemaFixtureIsNeverChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	newer := fixture(t, "v2-newer.json")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir, compatNow)
	if status, notice := s.Status(); status != Unavailable || !strings.Contains(notice, "newer Hclaro (schema 2)") {
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
	newer := fixture(t, "v2-newer.json")
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
