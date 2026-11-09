package projects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the projects.json compatibility contract (docs/config-compatibility.md)
// against versioned fixtures in testdata/compat.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "compat", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSchema1FixtureLoadsAndStaysSchema1(t *testing.T) {
	config := t.TempDir()
	root := project(t, "network")
	quoted, _ := json.Marshal(root)
	body := strings.Replace(string(fixture(t, "v1.json")), `"{{ROOT}}"`, string(quoted), 1)
	path := filepath.Join(config, fileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Open(config, now)
	if status, notice, _ := r.Status(); status != RegistryReady || notice != "" {
		t.Fatalf("%s %q", status, notice)
	}
	want := Record{ID: "0123456789abcdef0123456789abcdef", Alias: "network", RootPath: root, EnginePreference: "terraform", Pinned: true, AddedAt: "2026-11-01T09:00:00Z", LastOpenedAt: "2026-11-05T09:30:00Z", LastWorkspace: "dev"}
	if list, err := r.List(); err != nil || len(list) != 1 || list[0] != want {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := NewHub(r, "").Add(project(t, "fresh"), "", ""); err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(mustRead(t, path), &doc); err != nil || doc.Version != 1 || len(doc.Projects) != 2 || doc.Projects[0] != want {
		t.Fatalf("rewritten file: %+v %v", doc, err)
	}
}

func TestNewerSchemaFixtureIsNeverChanged(t *testing.T) {
	config := t.TempDir()
	path := filepath.Join(config, fileName)
	newer := fixture(t, "v2-newer.json")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	r := Open(config, now)
	if status, notice, _ := r.Status(); status != RegistryUnavailable || !strings.Contains(notice, "newer Hclaro (schema 2)") {
		t.Fatalf("%s %q", status, notice)
	}
	if list, _ := r.List(); len(list) != 0 {
		t.Fatalf("decoded a newer file: %+v", list)
	}
	hub := NewHub(r, "")
	if _, err := hub.Add(project(t, "x"), "", ""); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("write allowed: %v", err)
	}
	if string(mustRead(t, path)) != string(newer) {
		t.Fatal("newer registry changed")
	}
	if matches, _ := filepath.Glob(path + ".*"); len(matches) != 0 {
		t.Fatalf("newer registry moved aside: %v", matches)
	}
}

// A newer Hclaro that replaces the file while this one runs must not be downgraded.
func TestNewerSchemaWrittenDuringSessionIsNotDowngraded(t *testing.T) {
	config := t.TempDir()
	r := Open(config, now)
	hub := NewHub(r, "")
	if _, err := hub.Add(project(t, "a"), "", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config, fileName)
	newer := fixture(t, "v2-newer.json")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Add(project(t, "b"), "", ""); err == nil {
		t.Fatal("write allowed over a newer file")
	}
	if string(mustRead(t, path)) != string(newer) {
		t.Fatal("newer registry downgraded")
	}
}

// Versions this build does not write are never decoded or overwritten in place: the exact
// bytes are kept in a .corrupt-<time> file next to the registry.
func TestInvalidSchemaVersionsAreKeptAside(t *testing.T) {
	for name, body := range map[string]string{
		"zero":     `{"version":0,"projects":[]}`,
		"negative": `{"version":-1,"projects":[]}`,
		"string":   `{"version":"1","projects":[]}`,
		"null":     `{"version":null,"projects":[]}`,
		"missing":  `{"projects":[]}`,
		"fraction": `{"version":1.5,"projects":[]}`,
		"array":    `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			config := t.TempDir()
			path := filepath.Join(config, fileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			r := Open(config, now)
			if status, notice, _ := r.Status(); status != RegistryReady || !strings.Contains(notice, "projects.json.corrupt-20261105T100000Z") {
				t.Fatalf("%s %q", status, notice)
			}
			if string(mustRead(t, path+".corrupt-20261105T100000Z")) != body {
				t.Fatal("original bytes not kept")
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
