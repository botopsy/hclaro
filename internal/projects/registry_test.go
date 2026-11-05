package projects

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func project(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("terraform {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	return resolved
}

func TestRegistryPersistsThreeProjectsAcrossReopen(t *testing.T) {
	config := t.TempDir()
	hub := NewHub(Open(config, now), "")
	var ids []string
	for _, name := range []string{"dev", "qa", "prod"} {
		s, err := hub.Add(project(t, name), "", "")
		if err != nil {
			t.Fatal(err)
		}
		if s.Alias != name || s.Status != StatusReady {
			t.Fatalf("%+v", s)
		}
		ids = append(ids, s.ID)
	}
	alias := "Production infra"
	pinned := true
	if _, err := hub.Update(ids[2], Change{Alias: &alias, Pinned: &pinned}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Reorder([]string{ids[2], ids[0], ids[1]}); err != nil {
		t.Fatal(err)
	}
	reopened := Open(config, now)
	records, err := reopened.List()
	if err != nil || len(records) != 3 {
		t.Fatalf("%v %+v", err, records)
	}
	if records[0].ID != ids[2] || records[0].Alias != alias || !records[0].Pinned {
		t.Fatalf("%+v", records[0])
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(config, fileName))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("registry mode %v", fi.Mode())
		}
	}
	b, _ := os.ReadFile(filepath.Join(config, fileName))
	if !strings.Contains(string(b), `"version": 1`) {
		t.Fatalf("missing schema version: %s", b)
	}
	if err := hub.Reorder([]string{ids[0], ids[0], ids[1]}); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("duplicate order accepted: %v", err)
	}
}

func TestRegistryKeepsSameAliasForDistinctRoots(t *testing.T) {
	hub := NewHub(Open(t.TempDir(), now), "")
	a, err := hub.Add(project(t, "a"), "Infra", "terraform")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hub.Add(project(t, "b"), "Infra", "tofu")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.RootPath == b.RootPath || a.EnginePreference != "terraform" || b.EnginePreference != "tofu" {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestAddRejectsDuplicatesAndUnsafePaths(t *testing.T) {
	hub := NewHub(Open(t.TempDir(), now), "")
	root := project(t, "infra")
	first, err := hub.Add(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err == nil {
		dup, err := hub.Add(link, "", "")
		if !errors.Is(err, ErrDuplicate) || dup.ID != first.ID {
			t.Fatalf("symlinked duplicate accepted: %v", err)
		}
	}
	if _, err := hub.Add(root+string(filepath.Separator)+".", "", ""); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("cleaned duplicate accepted: %v", err)
	}
	empty := t.TempDir()
	for _, path := range []string{"", "relative/path", "~/infra", empty, filepath.Join(empty, "missing"), root + "\x00x"} {
		var pathErr *PathError
		if _, err := hub.Add(path, "", ""); !errors.As(err, &pathErr) {
			t.Fatalf("%q accepted: %v", path, err)
		}
	}
	for _, alias := range []string{" padded", "bad\nline", "bidi‮name", strings.Repeat("x", 81)} {
		if _, err := hub.Add(project(t, "other"), alias, ""); !errors.Is(err, ErrInvalidAlias) {
			t.Fatalf("alias %q accepted: %v", alias, err)
		}
	}
	if _, err := hub.Add(project(t, "eng"), "", "/usr/bin/terraform"); !errors.Is(err, ErrInvalidEngine) {
		t.Fatalf("engine path accepted: %v", err)
	}
}

func TestMissingProjectKeepsRecordAndRemoveKeepsFiles(t *testing.T) {
	hub := NewHub(Open(t.TempDir(), now), "")
	root := project(t, "moved")
	s, err := hub.Add(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	moved := root + "-new"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	list, _ := hub.List()
	if len(list) != 1 || list[0].Status != StatusMissing || list[0].Problem == "" {
		t.Fatalf("%+v", list)
	}
	if _, _, err := hub.Open(s.ID, "default", false); err == nil {
		t.Fatal("opened a missing project")
	}
	repaired, err := hub.Update(s.ID, Change{RootPath: &moved})
	if err != nil || repaired.Status != StatusReady || repaired.RootPath != moved {
		t.Fatalf("%v %+v", err, repaired)
	}
	if err := hub.Remove(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "main.tf")); err != nil {
		t.Fatal("unregister touched project files")
	}
	if list, _ := hub.List(); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestCorruptRegistryIsMovedAside(t *testing.T) {
	config := t.TempDir()
	path := filepath.Join(config, fileName)
	if err := os.WriteFile(path, []byte(`{"version":1,"projects":[{"id":"nope"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Open(config, now)
	status, notice, _ := r.Status()
	if status != RegistryReady || !strings.Contains(notice, "moved to projects.json.corrupt-20261105T100000Z") {
		t.Fatalf("%s %s", status, notice)
	}
	if b, err := os.ReadFile(path + ".corrupt-20261105T100000Z"); err != nil || !strings.Contains(string(b), "nope") {
		t.Fatal("backup not kept")
	}
	if _, err := NewHub(r, "").Add(project(t, "fresh"), "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestNewerOrUnreadableRegistryIsNeverOverwritten(t *testing.T) {
	config := t.TempDir()
	path := filepath.Join(config, fileName)
	newer := []byte(`{"version":2,"projects":[]}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	r := Open(config, now)
	if status, notice, _ := r.Status(); status != RegistryUnavailable || !strings.Contains(notice, "newer Hclaro") {
		t.Fatalf("%s %s", status, notice)
	}
	if _, err := NewHub(r, "").Add(project(t, "x"), "", ""); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("write allowed: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(newer) {
		t.Fatal("newer registry overwritten")
	}
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		return
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"projects":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := Open(config, now).Status(); status != RegistryUnavailable {
		t.Fatalf("unreadable registry status %s", status)
	}
}

func TestRegistryChangedByAnotherProcessIsReread(t *testing.T) {
	config := t.TempDir()
	one, two := NewHub(Open(config, now), ""), NewHub(Open(config, now), "")
	if _, err := one.Add(project(t, "a"), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Add(project(t, "b"), "", ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := one.List(); len(list) != 2 {
		t.Fatalf("lost update: %+v", list)
	}
	if err := os.WriteFile(filepath.Join(config, fileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := one.List(); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("corrupt file accepted mid-session: %v", err)
	}
	if _, err := one.Add(project(t, "c"), "", ""); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("overwrote a file changed by someone else: %v", err)
	}
}
