package assist

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// versioned reports a version, which saved conversations compare by major version.
type versioned struct {
	*stub
	version string
}

func (v versioned) Detect(ctx context.Context) ProviderStatus {
	st := v.stub.Detect(ctx)
	st.Version = v.version
	return st
}

func savedSession(t *testing.T, dir string, a Assistant) *Session {
	t.Helper()
	s := NewSession(false, a)
	s.Store = OpenStore(dir, time.Now())
	enable(t, s)
	if err := s.Accept(DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	return s
}

var savedScope = RunScope{Project: "net", Root: "/synthetic/net", Workspace: "default", ProjectKey: ProjectKey("/synthetic/net")}

func TestSavedConversationRetentionAndSettings(t *testing.T) {
	dir := t.TempDir()
	s := savedSession(t, dir, versioned{&stub{id: "good", mode: ModeHeadless}, "2.1.0"})
	settings, err := os.ReadFile(filepath.Join(dir, "ai-conversations", "settings.json"))
	if err != nil || !strings.Contains(string(settings), `"retentionDays": 30`) {
		t.Fatalf("default settings: %s %v", settings, err)
	}
	c, err := s.StartChat("launch", DisclosureVersion, savedScope, "keep me", nil)
	if err != nil {
		t.Fatal(err)
	}
	settle(t, s, "launch", c.ID)
	if _, err := s.Save("launch", c.ID, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ai-conversations", savedScope.ProjectKey, c.ID+".json")
	// Within the retention it stays; past it, opening the store (startup) deletes it and the
	// emptied project folder.
	OpenStore(dir, time.Now().Add(29*24*time.Hour))
	if _, err := os.Stat(path); err != nil {
		t.Fatal("deleted before the retention")
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), ".conversation-left.tmp"), []byte("x"), 0o600)
	OpenStore(dir, time.Now().Add(31*24*time.Hour))
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("expired conversation left: %v", err)
	}

	// A shorter retention from the settings file applies.
	if err := os.WriteFile(filepath.Join(dir, "ai-conversations", "settings.json"), []byte(`{"version":1,"retentionDays":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s = savedSession(t, dir, versioned{&stub{id: "good", mode: ModeHeadless}, "2.1.0"})
	if s.Store.RetentionDays() != 2 {
		t.Fatalf("retention %d", s.Store.RetentionDays())
	}
	c, _ = s.StartChat("launch", DisclosureVersion, savedScope, "short", nil)
	settle(t, s, "launch", c.ID)
	_, _ = s.Save("launch", c.ID, true)
	if len(s.SavedList("launch", savedScope.ProjectKey)) != 1 {
		t.Fatal("not listed")
	}
	s.Store.Sweep(time.Now().Add(3 * 24 * time.Hour))
	if len(s.SavedList("launch", savedScope.ProjectKey)) != 0 {
		t.Fatal("expired conversation listed")
	}
	// An invalid value falls back to the default.
	_ = os.WriteFile(filepath.Join(dir, "ai-conversations", "settings.json"), []byte(`{"retentionDays":-4}`), 0o600)
	if d := OpenStore(dir, time.Now()).RetentionDays(); d != DefaultRetentionDays {
		t.Fatalf("retention %d", d)
	}
}

func TestSavedConversationPaths(t *testing.T) {
	dir := t.TempDir()
	st := OpenStore(dir, time.Now())
	key, id := ProjectKey(`C:\Users\dev\infra`), strings.Repeat("a", 32)
	if p, ok := st.path(key, id); !ok || p != filepath.Join(key, id+".json") {
		t.Fatalf("%s %v", p, ok)
	}
	for _, bad := range [][2]string{{key, `..\..\x`}, {key, "../" + id[:29]}, {`..\x`, id}, {"", id}, {key, strings.ToUpper(id)}} {
		if _, ok := st.path(bad[0], bad[1]); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if ProjectKey(`C:\Users\dev\infra`) == ProjectKey(`C:\Users\dev\other`) {
		t.Fatal("keys collide")
	}
}

// A running turn's message is never written alone; the file follows when the reply arrives.
// A different major version reopens read-only, never claiming a resume.
func TestSavedConversationRunningAndVersion(t *testing.T) {
	dir := t.TempDir()
	a := &stub{id: "good", mode: ModeHeadless}
	s := savedSession(t, dir, versioned{a, "2.1.0"})
	c, _ := s.StartChat("launch", DisclosureVersion, savedScope, "first", nil)
	settle(t, s, "launch", c.ID)
	_, _ = s.Save("launch", c.ID, true)
	a.set(true, nil)
	if _, err := s.Send("launch", c.ID, "unanswered"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "ai-conversations", savedScope.ProjectKey, c.ID+".json"))
	if strings.Contains(string(raw), "unanswered") {
		t.Fatal("a running turn's message was written")
	}
	s.Close()

	same := savedSession(t, dir, versioned{&stub{id: "good", mode: ModeHeadless}, "2.4.0"})
	r := Resume{Scope: savedScope, Bound: func() bool { return true }}
	got, err := same.Reopen("launch", c.ID, r)
	if err != nil || got.ReadOnly != "" || len(got.Turns) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	newer := savedSession(t, dir, versioned{&stub{id: "good", mode: ModeHeadless}, "3.0.0"})
	got, _ = newer.Reopen("launch", c.ID, r)
	if !strings.Contains(got.ReadOnly, "saved with good 2.1.0; version 3.0.0") {
		t.Fatalf("%q", got.ReadOnly)
	}
	_, err = newer.Send("launch", c.ID, "hello")
	refused(t, err, RefuseReadOnly)
	// AI off: nothing is listed, opened or deleted; the file stays.
	newer.SetEnabled(false)
	if len(newer.SavedList("launch", savedScope.ProjectKey)) != 0 {
		t.Fatal("listed while AI is off")
	}
	_, err = newer.Reopen("launch", c.ID, r)
	refused(t, err, RefuseDisabled)
}

// Without a configuration folder nothing can be saved.
func TestSaveWithoutStore(t *testing.T) {
	s := NewSession(false, &stub{id: "good", mode: ModeHeadless})
	enable(t, s)
	_ = s.Accept(DisclosureVersion)
	c, _ := s.StartChat("launch", DisclosureVersion, savedScope, "hi", nil)
	settle(t, s, "launch", c.ID)
	_, err := s.Save("launch", c.ID, true)
	refused(t, err, RefuseSaveOff)
}

// replier answers every turn with the reply it holds.
type replier struct {
	stub
	reply string
}

func (r *replier) Run(ctx context.Context, payload []byte) (string, error) {
	if _, err := r.stub.Run(ctx, payload); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reply, nil
}
func (r *replier) Detect(context.Context) ProviderStatus {
	return ProviderStatus{ID: "good", Name: "good", Mode: ModeHeadless, Version: "2.1.0"}
}
func (r *replier) say(text string) { r.mu.Lock(); r.reply = text; r.mu.Unlock() }

func savedFile(dir, id string) string {
	return filepath.Join(dir, "ai-conversations", savedScope.ProjectKey, id+".json")
}

// Replies are not scanned when they arrive: one that looks secret never reaches the disk. The
// file keeps its last safe state and the conversation says so; saving it later is refused.
func TestSavedUnsafeReplyIsNeverWritten(t *testing.T) {
	dir := t.TempDir()
	a := &replier{stub: stub{id: "good", mode: ModeHeadless}, reply: "safe answer"}
	s := savedSession(t, dir, a)
	c, _ := s.StartChat("launch", DisclosureVersion, savedScope, "first", nil)
	settle(t, s, "launch", c.ID)
	if _, err := s.Save("launch", c.ID, true); err != nil {
		t.Fatal(err)
	}
	a.say("use key AKIAIOSFODNN7EXAMPLE")
	if _, err := s.Send("launch", c.ID, "second"); err != nil {
		t.Fatal(err)
	}
	got := settle(t, s, "launch", c.ID)
	raw, _ := os.ReadFile(savedFile(dir, c.ID))
	if strings.Contains(string(raw), "AKIA") || strings.Contains(string(raw), "second") || !strings.Contains(string(raw), "safe answer") || got.SaveError != unsafeSave {
		t.Fatalf("%q\n%s", got.SaveError, raw)
	}
	// A conversation already holding such a reply cannot be saved at all.
	c2, _ := s.StartChat("launch", DisclosureVersion, savedScope, "other", nil)
	settle(t, s, "launch", c2.ID)
	_, err := s.Save("launch", c2.ID, true)
	refused(t, err, RefuseSaveUnsafe)
	if _, err := os.Stat(savedFile(dir, c2.ID)); !os.IsNotExist(err) {
		t.Fatal("an unsafe conversation was written")
	}
}

// The store never follows a symbolic link out of its folder and never deletes a file it did
// not write: sweep, delete-all, write and read all refuse a linked project folder.
func TestSavedStoreRefusesLinksAndForeignFiles(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	st := OpenStore(dir, time.Now())
	id := strings.Repeat("b", 32)
	old := time.Now().Add(-400 * 24 * time.Hour)
	for _, name := range []string{id + ".json", "notes.txt"} {
		_ = os.WriteFile(filepath.Join(outside, name), []byte(`{}`), 0o644)
		_ = os.Chtimes(filepath.Join(outside, name), old, old)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "ai-conversations", savedScope.ProjectKey)); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	st.Sweep(time.Now())
	if n, _ := st.removeAll(savedScope.ProjectKey); n != 0 {
		t.Fatal("delete-all followed a link")
	}
	_ = st.remove(savedScope.ProjectKey, id)
	if err := st.write(savedDoc{Version: savedVersion, ID: id, ProjectKey: savedScope.ProjectKey}); err == nil {
		t.Fatal("wrote through a link")
	}
	if _, err := st.read(savedScope.ProjectKey, id); err == nil {
		t.Fatal("read through a link")
	}
	for _, name := range []string{id + ".json", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(outside, name)); err != nil {
			t.Fatalf("%s deleted through a link", name)
		}
	}
	if info, _ := os.Stat(outside); info.Mode().Perm() == 0o700 && os.PathSeparator == '/' {
		t.Fatal("chmod followed a link")
	}

	// In a real project folder, an old file the store did not write survives sweep and delete-all.
	other := ProjectKey("/synthetic/other")
	pdir := filepath.Join(dir, "ai-conversations", other)
	_ = os.MkdirAll(pdir, 0o755)
	_ = os.WriteFile(filepath.Join(pdir, "notes.txt"), []byte("x"), 0o600)
	_ = os.Chtimes(filepath.Join(pdir, "notes.txt"), old, old)
	st.Sweep(time.Now())
	_, _ = st.removeAll(other)
	if _, err := os.Stat(filepath.Join(pdir, "notes.txt")); err != nil {
		t.Fatal("a foreign file was deleted")
	}
	// An existing 0755 folder is made owner-only on the next write.
	if err := st.write(savedDoc{Version: savedVersion, ID: id, ProjectKey: other}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(pdir); os.PathSeparator == '/' && info.Mode().Perm() != 0o700 {
		t.Fatalf("folder %v", info.Mode().Perm())
	}
	// Too large to be read back: refused, nothing written.
	big := savedDoc{Version: savedVersion, ID: strings.Repeat("c", 32), ProjectKey: other, Package: strings.Repeat("x", maxSavedFile)}
	if err := st.write(big); err != errTooLarge {
		t.Fatalf("%v", err)
	}
}

// Deleting while a turn runs: the reply arriving afterwards does not write the file again.
func TestSavedDeleteDuringTurnStaysDeleted(t *testing.T) {
	dir := t.TempDir()
	a := &stub{id: "good", mode: ModeHeadless}
	s := savedSession(t, dir, versioned{a, "2.1.0"})
	c, _ := s.StartChat("launch", DisclosureVersion, savedScope, "first", nil)
	settle(t, s, "launch", c.ID)
	_, _ = s.Save("launch", c.ID, true)
	a.set(true, nil)
	_, _ = s.Send("launch", c.ID, "second")
	if err := s.DeleteSaved(savedScope.ProjectKey, c.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = s.CancelTurn("launch", c.ID)
	settle(t, s, "launch", c.ID)
	if _, err := os.Stat(savedFile(dir, c.ID)); !os.IsNotExist(err) {
		t.Fatal("deleted conversation written again")
	}
	if _, err := s.Reopen("launch2", c.ID, Resume{Scope: savedScope, Bound: func() bool { return true }}); err == nil {
		t.Fatal("reopened a deleted conversation")
	}
}

// Edited or broken files: a review marked as resumable stays read-only, a broken file or an
// unknown schema is refused alone and the others still list, and delete-all removes them all.
func TestSavedTamperedAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	s := savedSession(t, dir, versioned{&stub{id: "good", mode: ModeHeadless}, "2.1.0"})
	review := strings.Repeat("d", 32)
	if err := s.Store.write(savedDoc{Version: savedVersion, ID: review, ProjectKey: savedScope.ProjectKey, Workspace: "default", Provider: "good", ProviderVersion: "2.1.0",
		Kind: KindPlan, Resume: resumeWorkspace, Package: "# plan", Turns: []savedTurn{{Role: "user", Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Reopen("launch", review, Resume{Scope: savedScope, Bound: func() bool { return true }})
	if err != nil || !strings.Contains(got.ReadOnly, "earlier Hclaro session") {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = s.Send("launch", review, "go on")
	refused(t, err, RefuseReadOnly)

	pdir := filepath.Dir(savedFile(dir, review))
	broken, newer := strings.Repeat("e", 32), strings.Repeat("f", 32)
	_ = os.WriteFile(filepath.Join(pdir, broken+".json"), []byte(`{"version":1,"id":"`+broken+`",`), 0o600)
	_ = os.WriteFile(filepath.Join(pdir, newer+".json"), []byte(`{"version":2,"id":"`+newer+`","projectKey":"`+savedScope.ProjectKey+`"}`), 0o600)
	if l := s.SavedList("launch", savedScope.ProjectKey); len(l) != 1 || l[0].ID != review {
		t.Fatalf("%+v", l)
	}
	for _, id := range []string{broken, newer} {
		_, err := s.Reopen("launch", id, Resume{Scope: savedScope, Bound: func() bool { return true }})
		refused(t, err, RefuseNoChat)
	}
	if n, err := s.DeleteAllSaved(savedScope.ProjectKey); n != 3 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
}

// Save in dev, open from prod (read-only), untick and tick Save there, reopen from prod: still
// read-only, Send and Attach refused; the file keeps dev and dev continues it.
func TestSavedConversationStaysInItsWorkspace(t *testing.T) {
	dir := t.TempDir()
	newSession := func() *Session { return savedSession(t, dir, versioned{&stub{id: "good", mode: ModeHeadless}, "2.1.0"}) }
	dev, prod := savedScope, savedScope
	dev.Workspace, prod.Workspace = "dev", "prod"
	bound := func() bool { return true }
	s := newSession()
	c, _ := s.StartChat("launch", DisclosureVersion, dev, "dev question", bound)
	settle(t, s, "launch", c.ID)
	if _, err := s.Save("launch", c.ID, true); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = newSession()
	got, err := s.Reopen("launch", c.ID, Resume{Scope: prod, Bound: bound})
	if err != nil || !strings.Contains(got.ReadOnly, "workspace dev") {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.Save("launch", c.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("launch", c.ID, true); err != nil {
		t.Fatal(err)
	}
	if l := s.SavedList("launch", savedScope.ProjectKey); len(l) != 1 || l[0].Workspace != "dev" {
		t.Fatalf("re-saving from prod moved it: %+v", l)
	}
	s.Close()

	s = newSession()
	got, err = s.Reopen("launch", c.ID, Resume{Scope: prod, Bound: bound})
	if err != nil || !strings.Contains(got.ReadOnly, "workspace dev") {
		t.Fatalf("reopened from prod after re-saving: %+v %v", got, err)
	}
	_, err = s.Send("launch", c.ID, "continue in prod")
	refused(t, err, RefuseReadOnly)
	pkg := Package{Kind: KindProject, Markdown: "# project\n", SHA256: hashOf("# project\n"), Fingerprint: "fp"}
	s.Put("launch", pkg, bound)
	_, err = s.Attach("launch", c.ID, pkg.SHA256)
	refused(t, err, RefuseReadOnly)
	s.Close()

	s = newSession()
	got, err = s.Reopen("launch", c.ID, Resume{Scope: dev, Bound: bound})
	if err != nil || got.ReadOnly != "" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.Send("launch", c.ID, "continue in dev"); err != nil {
		t.Fatal(err)
	}
	if got = settle(t, s, "launch", c.ID); got.State != StateReady || len(got.Turns) != 4 {
		t.Fatalf("%+v", got)
	}
}

// A symbolic link swapped in after a folder check, at the store folder or at a project folder,
// cannot make any operation reach outside the store: os.Root resolves every path inside it.
func TestSavedStoreSurvivesLinkSwaps(t *testing.T) {
	id := strings.Repeat("a", 32)
	key := savedScope.ProjectKey
	old := time.Now().Add(-400 * 24 * time.Hour)
	setup := func(t *testing.T) (*Store, string, string) {
		dir, outside := t.TempDir(), t.TempDir()
		st := OpenStore(dir, time.Now())
		if err := st.write(savedDoc{Version: savedVersion, ID: id, ProjectKey: key}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{id + ".json", ".conversation-x.tmp"} {
			_ = os.WriteFile(filepath.Join(outside, name), []byte(`{"version":1,"id":"`+id+`","projectKey":"`+key+`"}`), 0o644)
			_ = os.Chtimes(filepath.Join(outside, name), old, old)
		}
		return st, filepath.Join(dir, "ai-conversations"), outside
	}
	// swapAt replaces folder name (relative to the store, "." for the store) by a link to
	// outside, once, right after the store checked it.
	swapAt := func(t *testing.T, st *Store, store, name, outside string) {
		done := false
		st.hook = func(checked string) {
			path := filepath.Join(store, name)
			if name == "." {
				checked, path = filepath.Join(".", checked), store
				if checked != filepath.Join(".", "ai-conversations") {
					return
				}
			} else if checked != name {
				return
			}
			if done {
				return
			}
			done = true
			if err := os.Rename(path, path+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skip("symbolic links unavailable:", err)
			}
		}
	}
	untouched := func(t *testing.T, outside string) {
		t.Helper()
		entries, _ := os.ReadDir(outside)
		if len(entries) != 2 {
			t.Fatalf("outside changed: %v", entries)
		}
		info, _ := os.Stat(outside)
		if os.PathSeparator == '/' && info.Mode().Perm() == 0o700 {
			t.Fatal("chmod reached outside")
		}
	}
	ops := map[string]func(st *Store) error{
		"write": func(st *Store) error {
			return st.write(savedDoc{Version: savedVersion, ID: strings.Repeat("b", 32), ProjectKey: key})
		},
		"read":      func(st *Store) error { _, err := st.read(key, id); return err },
		"remove":    func(st *Store) error { return st.remove(key, id) },
		"removeAll": func(st *Store) error { _, err := st.removeAll(key); return err },
		"sweep":     func(st *Store) error { st.Sweep(time.Now()); return nil },
		"list":      func(st *Store) error { st.list(key, time.Now()); return nil },
	}
	for _, at := range []string{".", key} {
		for name, op := range ops {
			t.Run(name+" swap at "+at, func(t *testing.T) {
				st, store, outside := setup(t)
				swapAt(t, st, store, at, outside)
				err := op(st)
				if name == "write" && err == nil {
					t.Fatal("write followed a swapped link")
				}
				if name == "read" && err == nil {
					t.Fatal("read followed a swapped link")
				}
				untouched(t, outside)
			})
		}
	}
	// A conversation file replaced by a link is never read and its target never written.
	st, store, outside := setup(t)
	path := filepath.Join(store, key, id+".json")
	_ = os.Remove(path)
	if err := os.Symlink(filepath.Join(outside, id+".json"), path); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	if _, err := st.read(key, id); err == nil || st.exists(key, id) {
		t.Fatal("read a linked file")
	}
	if err := st.write(savedDoc{Version: savedVersion, ID: id, ProjectKey: key, Title: "new"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, id+".json")); strings.Contains(string(b), "new") {
		t.Fatal("wrote through a linked file")
	}
	untouched(t, outside)
}

// ai.json is written in the configuration folder through the same temporary files; one left
// there by an interrupted settings write is deleted at startup once it is an hour old, and
// nothing else is.
func TestStrayTempInConfigRootIsSwept(t *testing.T) {
	dir := t.TempDir()
	stray, other := filepath.Join(dir, ".conversation-left.tmp"), filepath.Join(dir, "other.tmp")
	for _, p := range []string{stray, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh one may be another process's write in progress: it stays.
	OpenStore(dir, time.Now())
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("temporary file of a running write removed: %v", err)
	}
	OpenStore(dir, time.Now().Add(2*time.Hour))
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray temporary file kept: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}
