package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/internal/operations"
)

// These tests pin the history.json compatibility contract (docs/config-compatibility.md)
// against versioned fixtures in testdata/compat.

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
	body := fixture(t, "v1.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir)
	if status, notice, _ := s.Status(); status != Ready || notice != "" {
		t.Fatalf("%s %q", status, notice)
	}
	// Loading a current file that needs no change never rewrites it.
	if string(mustRead(t, path)) != string(body) {
		t.Fatal("a current file was rewritten on load")
	}
	e, ok := s.Get(id(1))
	if !ok || e.Kind != "plan" || e.ProjectName != "network" || e.Context == nil || e.Context.ProfileRevision != 3 || e.ExitCode == nil || *e.ExitCode != 2 || len(e.Excerpt) != 1 {
		t.Fatalf("%+v", e)
	}
	finished(s, 2, "plan", operations.Succeeded, t0)
	var doc document
	if err := json.Unmarshal(mustRead(t, path), &doc); err != nil || doc.Version != 1 || len(doc.Entries) != 2 {
		t.Fatalf("rewritten file: %+v %v", doc, err)
	}
	if matches, _ := filepath.Glob(path + ".*"); len(matches) != 0 {
		t.Fatalf("unexpected backup for a current file: %v", matches)
	}
}

func TestNewerSchemaFixtureIsNeverChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	newer := fixture(t, "v2-newer.json")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, dir)
	if status, notice, _ := s.Status(); status != ReadOnly || !strings.Contains(notice, "newer Hclaro (schema 2)") {
		t.Fatalf("%s %q", status, notice)
	}
	if len(s.List(Filter{})) != 0 {
		t.Fatal("decoded a newer file")
	}
	finished(s, 1, "plan", operations.Succeeded, t0)
	if string(mustRead(t, path)) != string(newer) {
		t.Fatal("newer history changed")
	}
	if matches, _ := filepath.Glob(path + ".*"); len(matches) != 0 {
		t.Fatalf("newer history moved aside: %v", matches)
	}
}

// A newer Hclaro that replaces the file while this one runs must not be downgraded: the
// next write would otherwise put schema 1 back over it.
func TestNewerSchemaWrittenDuringSessionIsNotDowngraded(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	finished(s, 1, "plan", operations.Succeeded, t0)
	newer := fixture(t, "v2-newer.json")
	if err := os.WriteFile(s.path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	finished(s, 2, "plan", operations.Succeeded, t0)
	if string(mustRead(t, s.path)) != string(newer) {
		t.Fatal("newer history downgraded by Record")
	}
	if status, notice, _ := s.Status(); status != ReadOnly || !strings.Contains(notice, "newer Hclaro (schema 2)") {
		t.Fatalf("%s %q", status, notice)
	}
	if err := s.SetRetention(DefaultPolicy); err != ErrUnavailable {
		t.Fatal(err)
	}
	if _, err := s.Clear(Filter{}); err != ErrUnavailable {
		t.Fatal(err)
	}
	if string(mustRead(t, s.path)) != string(newer) {
		t.Fatal("newer history downgraded")
	}
}

// legacy is a hypothetical schema 0 that named the kind "action" and stored no retention.
const legacy = `{"version":0,"entries":[{"id":"00000000000000000000000000000001","action":"plan","status":"succeeded","startedAt":"2026-11-05T10:00:00Z","durationMs":5,"engine":"terraform","projectId":"00000000000000000000000000000007","projectName":"net","registered":true,"command":["terraform","plan"],"outcome":{"result":"planned","summary":"x","verified":false},"excerpt":[]}]}`

func upgradeFrom0() map[int]Migration {
	return map[int]Migration{0: func(raw json.RawMessage) (json.RawMessage, error) {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		for _, e := range doc["entries"].([]any) {
			m := e.(map[string]any)
			m["kind"] = m["action"]
			delete(m, "action")
		}
		doc["version"] = 1
		doc["retention"] = DefaultPolicy
		return json.Marshal(doc)
	}}
}

func TestSchemaUpgradeKeepsABackupFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	_ = os.WriteFile(path, []byte(legacy), 0o600)
	s := OpenWith(dir, t0, upgradeFrom0())
	backup := path + ".schema-0-20261106T100000Z"
	if status, notice, _ := s.Status(); status != Ready || !strings.Contains(notice, "upgraded from schema 0 to schema 1") || !strings.Contains(notice, filepath.Base(backup)) {
		t.Fatalf("%s %q", status, notice)
	}
	if string(mustRead(t, backup)) != legacy {
		t.Fatal("backup differs from the original file")
	}
	if fi, err := os.Stat(backup); err != nil || (os.PathSeparator == '/' && fi.Mode().Perm() != 0o600) {
		t.Fatalf("backup mode: %v %v", fi.Mode(), err)
	}
	var doc document
	if err := json.Unmarshal(mustRead(t, path), &doc); err != nil || doc.Version != 1 {
		t.Fatal("upgraded file not written", err)
	}
}

// Without a backup copy an upgrade does not happen and the file is left unchanged.
func TestSchemaUpgradeWithoutBackupLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	_ = os.WriteFile(path, []byte(legacy), 0o600)
	// An existing entry at the backup name is never replaced, so the copy fails.
	if err := os.Mkdir(path+".schema-0-20261106T100000Z", 0o700); err != nil {
		t.Fatal(err)
	}
	s := OpenWith(dir, t0, upgradeFrom0())
	if status, notice, _ := s.Status(); status != Unavailable || !strings.Contains(notice, "backup copy could not be written") {
		t.Fatalf("%s %q", status, notice)
	}
	finished(s, 2, "plan", operations.Succeeded, t0)
	if string(mustRead(t, path)) != legacy {
		t.Fatal("file changed without a backup")
	}
}

// An older Hclaro rewriting the file during a session is not upgraded behind its back.
func TestOlderSchemaWrittenDuringSessionIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	s := OpenWith(dir, t0, upgradeFrom0())
	s.Now, s.Sync = func() time.Time { return t0 }, false
	finished(s, 1, "plan", operations.Succeeded, t0)
	_ = os.WriteFile(s.path, []byte(legacy), 0o600)
	finished(s, 2, "plan", operations.Succeeded, t0)
	if string(mustRead(t, s.path)) != legacy {
		t.Fatal("older history rewritten during the session")
	}
	if status, notice, _ := s.Status(); status != ReadOnly || !strings.Contains(notice, "older Hclaro (schema 0)") {
		t.Fatalf("%s %q", status, notice)
	}
}

func TestInvalidSchemaVersionsAreKeptAside(t *testing.T) {
	for name, body := range map[string]string{
		"zero without upgrade": `{"version":0,"entries":[]}`,
		"negative":             `{"version":-1,"entries":[]}`,
		"string":               `{"version":"1","entries":[]}`,
		"null":                 `{"version":null,"entries":[]}`,
		"fraction":             `{"version":1.5,"entries":[]}`,
		"array":                `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, fileName)
			_ = os.WriteFile(path, []byte(body), 0o600)
			s := open(t, dir)
			if status, notice, _ := s.Status(); status != Ready || !strings.Contains(notice, "history.json.corrupt-20261106T100000Z") {
				t.Fatalf("%s %q", status, notice)
			}
			if string(mustRead(t, path+".corrupt-20261106T100000Z")) != body {
				t.Fatal("original bytes not kept")
			}
		})
	}
}
