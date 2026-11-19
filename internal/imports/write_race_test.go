package imports

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// AI-01.12: a replacement never loses a version another program saves while Hclaro writes, and
// the replaced file is kept as a recovery artifact. The hook stands in for an editor saving, or
// for Hclaro stopping, at each step of the protocol.

const (
	base   = "locals { a = 1 }\n"
	ours   = "locals { a = 2 }\n"
	editor = "locals { a = 3 } # saved by an editor\n"
	keep   = "0123456789abcdef0123456789abcdef"
)

func raceRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(base), 0o640); err != nil {
		t.Fatal(err)
	}
	return root
}

func at(t *testing.T, stage string, act func()) {
	t.Helper()
	hook = func(s string) {
		if s == stage {
			act()
		}
	}
	t.Cleanup(func() { hook = func(string) {} })
}

// left lists Hclaro's files in root after a call: recovery artifacts only, never a temporary file.
func left(t *testing.T, root string) []string {
	t.Helper()
	entries, _ := os.ReadDir(root)
	out := []string{}
	for _, e := range entries {
		switch {
		case e.Name() == AsideName(keep):
			out = append(out, e.Name())
		case strings.HasPrefix(e.Name(), ".hclaro-"):
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
	return out
}

func content(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func isBase(b []byte) bool { return bytes.Equal(b, []byte(base)) }

// saveByRename is how most editors save: a new file renamed over the name.
func saveByRename(t *testing.T, target string) {
	tmp := target + ".editor~"
	if err := os.WriteFile(tmp, []byte(editor), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceKeepsTheReplacedFile(t *testing.T) {
	root := raceRoot(t)
	if err := ReplaceAt(root, "main.tf", []byte(ours), isBase, keep); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(root, "main.tf"))
	// Windows has no Unix permission bits (it reports 0666 for any writable file).
	if content(t, filepath.Join(root, "main.tf")) != ours || runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("replace: %q %v", content(t, filepath.Join(root, "main.tf")), info.Mode().Perm())
	}
	if a := left(t, root); len(a) != 1 || content(t, filepath.Join(root, a[0])) != base {
		t.Fatalf("recovery artifact: %v", a)
	}
	if err := ReplaceAt(root, "main.tf", []byte(ours), isBase, "not-an-id"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("invalid keep ID: %v", err)
	}
}

// A program that opened the file before Hclaro replaced it and writes into it afterwards (in
// place, through its open descriptor) writes into the moved file: it is kept, so the write is
// recoverable after the call returned.
func TestInPlaceWriteThroughAnOpenFileStaysRecoverable(t *testing.T) {
	root := raceRoot(t)
	f, err := os.OpenFile(filepath.Join(root, "main.tf"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = ReplaceAt(root, "main.tf", []byte(ours), isBase, keep)
	if runtime.GOOS == "windows" {
		// Windows refuses to move a file another process holds open: the replace fails and
		// changes nothing (no write, no recovery artifact, no temporary file).
		if err == nil || content(t, filepath.Join(root, "main.tf")) != base || len(left(t, root)) != 0 {
			t.Fatalf("replace of an open file on Windows: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(editor), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if content(t, filepath.Join(root, "main.tf")) != ours || content(t, filepath.Join(root, AsideName(keep))) != editor {
		t.Fatal("the late in-place write is not recoverable")
	}
}

func TestEditorSaveDuringReplaceIsNeverLost(t *testing.T) {
	cases := []struct {
		stage string
		act   func(t *testing.T, root string)
		// aside is what the recovery artifact holds afterwards ("" when none is left).
		aside string
	}{
		// Saved after the early check, before the move: the moved file is the editor's, it fails
		// verification and is linked back under its name.
		{"before-aside", func(t *testing.T, root string) { saveByRename(t, filepath.Join(root, "main.tf")) }, ""},
		// Written in place into the moved file before it is verified: the same.
		{"after-aside", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, AsideName(keep)), []byte(editor), 0o644); err != nil {
				t.Fatal(err)
			}
		}, ""},
		// Saved under the name after the move: Hclaro's link fails, the editor's file stays and the
		// verified original is kept as the artifact.
		{"before-place", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(editor), 0o644); err != nil {
				t.Fatal(err)
			}
		}, base},
	}
	for _, c := range cases {
		t.Run(c.stage, func(t *testing.T) {
			root := raceRoot(t)
			at(t, c.stage, func() { c.act(t, root) })
			if err := ReplaceAt(root, "main.tf", []byte(ours), isBase, keep); !errors.Is(err, ErrChanged) {
				t.Fatalf("want ErrChanged, got %v", err)
			}
			if got := content(t, filepath.Join(root, "main.tf")); got != editor {
				t.Fatalf("the editor's save was lost: %q", got)
			}
			a := left(t, root)
			if c.aside == "" && len(a) != 0 || c.aside != "" && (len(a) != 1 || content(t, filepath.Join(root, a[0])) != c.aside) {
				t.Fatalf("artifacts %v", a)
			}
		})
	}
}

// Written in place into the moved file after Hclaro's file is in place: reported, kept.
func TestInPlaceWriteAfterPlaceIsReported(t *testing.T) {
	root := raceRoot(t)
	at(t, "after-place", func() {
		if err := os.WriteFile(filepath.Join(root, AsideName(keep)), []byte(editor), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	err := ReplaceAt(root, "main.tf", []byte(ours), isBase, keep)
	var aside *AsideError
	if !errors.As(err, &aside) || !aside.Written || aside.Name != AsideName(keep) {
		t.Fatalf("want AsideError written, got %v", err)
	}
	if content(t, filepath.Join(root, "main.tf")) != ours || content(t, filepath.Join(root, aside.Name)) != editor {
		t.Fatal("the kept file or the write is wrong")
	}
}

// Taken again under the name and changed in the moved file: both versions survive.
func TestTwoConcurrentWritersBothSurvive(t *testing.T) {
	root := raceRoot(t)
	at(t, "after-aside", func() {
		_ = os.WriteFile(filepath.Join(root, AsideName(keep)), []byte(editor), 0o644)
		_ = os.WriteFile(filepath.Join(root, "main.tf"), []byte("second\n"), 0o644)
	})
	err := ReplaceAt(root, "main.tf", []byte(ours), isBase, keep)
	var aside *AsideError
	if !errors.As(err, &aside) || aside.Written {
		t.Fatalf("want AsideError not written, got %v", err)
	}
	if content(t, filepath.Join(root, "main.tf")) != "second\n" || content(t, filepath.Join(root, aside.Name)) != editor {
		t.Fatal("a version was lost")
	}
}

// Hclaro stopping between the move and the placement leaves the name empty and the original as
// the artifact (the caller's record rolls it back at the next start), and no temporary file.
func TestInterruptionAfterMoveLeavesTheOriginalRecoverable(t *testing.T) {
	root := raceRoot(t)
	at(t, "before-place", func() { panic("stopped") })
	func() {
		defer func() { _ = recover() }()
		_ = ReplaceAt(root, "main.tf", []byte(ours), isBase, keep)
	}()
	if _, err := os.Lstat(filepath.Join(root, "main.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the name was not empty: the interruption was not at the step under test")
	}
	if a := left(t, root); len(a) != 1 || content(t, filepath.Join(root, a[0])) != base {
		t.Fatalf("artifacts %v", a)
	}
}

// A failed placement (here the new file vanished) puts the original back.
func TestFailedPlacementPutsTheOriginalBack(t *testing.T) {
	root := raceRoot(t)
	at(t, "before-place", func() {
		matches, _ := filepath.Glob(filepath.Join(root, ".hclaro-write-*.tmp"))
		for _, m := range matches {
			os.Remove(m)
		}
	})
	if err := ReplaceAt(root, "main.tf", []byte(ours), isBase, keep); !errors.Is(err, ErrChanged) {
		t.Fatalf("got %v", err)
	}
	if content(t, filepath.Join(root, "main.tf")) != base || len(left(t, root)) != 0 {
		t.Fatal("the original is not back")
	}
}

// Without hard links nothing moves and nothing is created: no partial change.
func TestNoHardLinksChangesNothing(t *testing.T) {
	root := raceRoot(t)
	link = func(string, string) error {
		return &os.LinkError{Op: "link", Err: errors.New("hard links not supported")}
	}
	t.Cleanup(func() { link = os.Link })
	if err := ReplaceAt(root, "main.tf", []byte(ours), isBase, keep); !errors.Is(err, ErrNoLinks) {
		t.Fatalf("replace: %v", err)
	}
	if err := RemoveAt(root, "main.tf", isBase, keep); !errors.Is(err, ErrNoLinks) {
		t.Fatalf("remove: %v", err)
	}
	if err := CreateAt(root, "new.tf", []byte(ours)); !errors.Is(err, ErrNoLinks) {
		t.Fatalf("create: %v", err)
	}
	if content(t, filepath.Join(root, "main.tf")) != base || len(left(t, root)) != 0 {
		t.Fatal("changed without hard links")
	}
	if _, err := os.Lstat(filepath.Join(root, "new.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created without hard links")
	}
}

func TestCreateNeverOverwrites(t *testing.T) {
	root := raceRoot(t)
	if err := CreateAt(root, "main.tf", []byte(ours)); !errors.Is(err, ErrFileExists) || content(t, filepath.Join(root, "main.tf")) != base {
		t.Fatalf("create over a file: %v", err)
	}
	at(t, "never", func() {})
	if err := CreateAt(root, "new.tf", []byte(ours)); err != nil || content(t, filepath.Join(root, "new.tf")) != ours || len(left(t, root)) != 0 {
		t.Fatalf("create: %v", err)
	}
}

func TestRemoveNeverDeletesAnotherVersion(t *testing.T) {
	root := raceRoot(t)
	at(t, "before-aside", func() { saveByRename(t, filepath.Join(root, "main.tf")) })
	if err := RemoveAt(root, "main.tf", isBase, keep); !errors.Is(err, ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
	if content(t, filepath.Join(root, "main.tf")) != editor || len(left(t, root)) != 0 {
		t.Fatal("the editor's file was removed")
	}
	hook = func(string) {}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAt(root, "main.tf", isBase, keep); err != nil {
		t.Fatal(err)
	}
	// Removed under its name, kept as the artifact.
	if _, err := os.Lstat(filepath.Join(root, "main.tf")); !errors.Is(err, os.ErrNotExist) || content(t, filepath.Join(root, AsideName(keep))) != base {
		t.Fatal("not removed, or not kept")
	}
}

func TestReplaceRefusesLinksAndEscapes(t *testing.T) {
	root := raceRoot(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.tf"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.tf"), filepath.Join(root, "link.tf")); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"link.tf", "dir/x.tf", "../x.tf", "/x.tf", `a\x.tf`, "C:x.tf"} {
		if err := ReplaceAt(root, rel, []byte(ours), isBase, keep); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if content(t, filepath.Join(outside, "x.tf")) != base {
		t.Fatal("written through a link")
	}
}
