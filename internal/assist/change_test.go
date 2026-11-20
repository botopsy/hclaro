package assist

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/internal/gitctx"
	"hclaro/internal/safefile"
)

// proposer is an Assistant whose reply is fixed text: a synthetic model proposing changes.
type proposer struct{ reply string }

func (r *proposer) ID() string { return "good" }
func (r *proposer) Detect(context.Context) ProviderStatus {
	return ProviderStatus{ID: "good", Name: "good", Mode: ModeHeadless}
}
func (r *proposer) Run(context.Context, []byte) (string, error) { return r.reply, nil }

func block(path, base, text string) string {
	return "Here is a change.\n```hclaro-change path=" + path + " base=" + base + "\n" + text + "```\n"
}

const mainTF = "resource \"null_resource\" \"a\" {\n  triggers = {\n    v = \"1\"\n  }\n}\n"

// changeRoot is a synthetic project with main.tf and a local module.
func changeRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "main.tf", mainTF)
	write(t, root, "modules/net/main.tf", "output \"id\" {\n  value = \"x\"\n}\n")
	write(t, root, "prod.tfvars", "region = \"us-east-1\"\n")
	return root
}

type recorded struct {
	mu   sync.Mutex
	list []ChangeRecord
}

func (r *recorded) add(c ChangeRecord) { r.mu.Lock(); r.list = append(r.list, c); r.mu.Unlock() }
func (r *recorded) all() []ChangeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ChangeRecord{}, r.list...)
}

// proposing starts a conversation in root whose reply is reply, and returns its ID.
func proposing(t *testing.T, root, reply string) (*Session, string, RunScope, *recorded) {
	t.Helper()
	s := NewSession(false, &proposer{reply: reply})
	t.Cleanup(s.Close)
	enable(t, s)
	if err := s.Accept(DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	s.Store = OpenStore(t.TempDir(), time.Now())
	rec := &recorded{}
	s.RecordChange = rec.add
	scope := RunScope{Project: "net", Root: root, Workspace: "default", ProjectKey: ProjectKey(root)}
	c, err := s.StartChat("launch", DisclosureVersion, scope, "propose a change", nil)
	if err != nil {
		t.Fatal(err)
	}
	settle(t, s, "launch", c.ID)
	return s, c.ID, scope, rec
}

// free runs fn as the workspace lock does when no operation is running.
func free(fn func() error) error { return fn() }

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProposalsParseOnlyWellFormedBlocks(t *testing.T) {
	reply := "intro\n```hclaro-change path=a.tf base=new\nx = 1\n```\n" +
		"```hclaro-change path=b.tf\nmissing base\n```\n" + // malformed header: inert
		"````hclaro-change path=c.tf base=new\ninner ```\n````\n" + // longer fence holds a shorter one
		"```hclaro-change path=d.tf base=new\nnever closed\n"
	got := proposals(reply)
	if len(got) != 2 || got[0].path != "a.tf" || got[0].text != "x = 1\n" || got[1].path != "c.tf" || got[1].text != "inner ```\n" {
		t.Fatalf("parsed %+v", got)
	}
	if len(proposals("plain text with ```hcl\nresource {}\n```")) != 0 {
		t.Fatal("an ordinary code block became a proposal")
	}
}

func TestPackageStatesHashOfUnchangedFilesOnly(t *testing.T) {
	root := changeRoot(t)
	write(t, root, "vars.tf", "variable \"token\" {\n  default = { a = 1 }\n}\n") // default omitted: edited
	p, err := Project(Scope{Project: "net", Root: root}, noRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Markdown, "# file: main.tf (sha256 "+sum(mainTF)+")") {
		t.Fatalf("main.tf hash missing:\n%s", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "# file: vars.tf (edited for sharing: cannot be replaced by a proposed change)") {
		t.Fatalf("edited file not marked:\n%s", p.Markdown)
	}
}

func TestChangeValidationRejectsUnsafeProposals(t *testing.T) {
	root := changeRoot(t)
	outside := t.TempDir()
	write(t, outside, "x.tf", mainTF)
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.tf"), filepath.Join(root, "link.tf")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "backend.tf", "terraform {\n  backend \"s3\" {}\n}\n")
	write(t, root, "vars.tf", "variable \"v\" {\n  default = [\"a\", \"b\"]\n}\n")
	newText := "resource \"null_resource\" \"b\" {}\n"
	cases := []struct{ path, base, text, code string }{
		{"../x.tf", "new", newText, "change_path"},
		{"/etc/x.tf", "new", newText, "change_path"},
		{`C:\x.tf`, "new", newText, "change_path"},
		{"C:/x.tf", "new", newText, "change_path"},
		{".terraform/x.tf", "new", newText, "change_path"},
		{"modules/../../x.tf", "new", newText, "change_path"},
		{"prod.tfvars", "new", newText, "change_path"},
		{".terraform.lock.hcl", "new", newText, "change_path"},
		{"main.tf.json", "new", newText, "change_path"},
		{"linkdir/x.tf", sum(mainTF), newText, "change_folder"},
		{"link.tf", sum(mainTF), newText, "change_scope"}, // a link is never in the package,
		{"missing/x.tf", "new", newText, "change_folder"},
		{"main.tf", "abc", newText, "change_base"},
		{"main.tf", "new", newText, "change_exists"},
		{"main.tf", sum("older text\n"), newText, "change_stale"},
		{"gone.tf", sum(mainTF), newText, "change_scope"},
		{"main.tf", sum(mainTF), "terraform {\n  backend \"local\" {}\n}\n", "change_backend"},
		{"main.tf", sum(mainTF), "terraform {\n  cloud {\n  }\n}\n", "change_backend"},
		{"backend.tf", sum("terraform {\n  backend \"s3\" {}\n}\n"), "terraform {}\n", "change_backend"},
		{"vars.tf", sum("variable \"v\" {\n  default = [\"a\", \"b\"]\n}\n"), "variable \"v\" {}\n", "change_edited"},
		{"main.tf", sum(mainTF), "resource \"x\" \"y\" {\n", "change_syntax"},
		{"main.tf", sum(mainTF), "locals {\n  a = \"open\n}\n", "change_syntax"},
		{"main.tf", sum(mainTF), "locals {\n  a = <<EOT\nnever ends\n}\n", "change_syntax"},
		{"main.tf", sum(mainTF), "/* open comment\n", "change_syntax"},
		{"main.tf", sum(mainTF), strings.Repeat("# x\n", MaxChangeBytes/4+1), "change_size"},
		{"main.tf", sum(mainTF), "\n", "change_size"},
		{"main.tf", sum(mainTF), "locals {\n  key = \"AKIAIOSFODNN7EXAMPLE\"\n}\n", "change_unsafe"},
		{"main.tf", sum(mainTF), "locals {\n  password = \"hunter2hunter2\"\n}\n", "change_unsafe"},
		{"main.tf", sum(mainTF), "locals {\n  v = \"[redacted]\"\n}\n", "change_unsafe"},
		{"main.tf", sum(mainTF), "locals {\n  p = \"" + root + "/x\"\n}\n", "change_unsafe"},
		{"main.tf", sum(mainTF), "locals {\n  a = \"\x1b[31m\"\n}\n", "change_text"},
		{"main.tf", sum(mainTF), mainTF, "change_applied"},
	}
	for _, c := range cases {
		v, _ := checkChange(root, proposal{path: c.path, base: c.base, text: c.text}, noRepo)
		if v.Status != ChangeInvalid || v.Code != c.code {
			t.Errorf("%s base=%s: got %s %s, want %s", c.path, c.base, v.Status, v.Code, c.code)
		}
		if v.Diff != "" {
			t.Errorf("%s: an invalid proposal has a diff", c.path)
		}
	}
	if read(t, root, "main.tf") != mainTF {
		t.Fatal("validation wrote a file")
	}
	// A swapped project root refuses everything.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if v, _ := checkChange(alias, proposal{path: "main.tf", base: sum(mainTF), text: newText}, noRepo); v.Code != "change_project" {
		t.Fatalf("swapped root: %+v", v)
	}
}

func TestChangeDiffIsComputedByHclaro(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `v = "1"`, `v = "2"`, 1)
	v, old := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: next}, noRepo)
	if v.Status != ChangeReady || string(old) != mainTF || v.Added != 1 || v.Removed != 1 {
		t.Fatalf("view %+v", v)
	}
	want := "--- a/main.tf\n+++ b/main.tf\n@@ -1,5 +1,5 @@\n resource \"null_resource\" \"a\" {\n   triggers = {\n-    v = \"1\"\n+    v = \"2\"\n   }\n }\n"
	if v.Diff != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", v.Diff, want)
	}
	created, _ := checkChange(root, proposal{path: "modules/net/extra.tf", base: "new", text: "locals {}\n"}, noRepo)
	if created.Status != ChangeReady || !strings.HasPrefix(created.Diff, "--- /dev/null\n+++ b/modules/net/extra.tf\n@@ -0,0 +1,1 @@\n+locals {}\n") {
		t.Fatalf("new file view %+v", created)
	}
	// Far-apart changes give two hunks.
	long := []string{}
	for i := range 30 {
		long = append(long, "# line "+string(rune('a'+i%26)))
	}
	changed := append([]string{}, long...)
	changed[2], changed[25] = "# two", "# twenty-five"
	diff, added, removed := unifiedDiff("x.tf", long, changed)
	if strings.Count(diff, "@@ -") != 2 || added != 2 || removed != 2 {
		t.Fatalf("hunks:\n%s", diff)
	}
}

func TestApplyRejectUndo(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `v = "1"`, `v = "2"`, 1)
	created := "locals {\n  name = \"net\"\n}\n"
	reply := block("main.tf", sum(mainTF), next) + block("modules/net/extra.tf", "new", created) + block("../escape.tf", "new", created)
	s, id, scope, rec := proposing(t, root, reply)

	views, err := s.Changes("launch", id, scope, files)
	if err != nil || len(views) != 3 || views[0].Status != ChangeReady || views[1].Status != ChangeReady || views[2].Code != "change_path" {
		t.Fatalf("views %+v %v", views, err)
	}
	if read(t, root, "main.tf") != mainTF {
		t.Fatal("listing wrote a file")
	}
	turn := views[0].Turn

	// The reviewed hashes must match what would be written.
	_, err = s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 0, BaseSHA256: sum(mainTF), NewSHA256: sum("other")}, scope, files, free)
	refused(t, err, RefuseChangeReviewed)
	// A running operation keeps the write out.
	busy := func(func() error) error { return errors.New("busy") }
	_, err = s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, busy)
	refused(t, err, RefuseChangeBusy)
	// Another workspace or scope cannot write.
	other := scope
	other.Workspace = "prod"
	_, err = s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, other, files, free)
	refused(t, err, RefuseStale)
	_, err = s.ApplyChange("hub-x", id, ChangeRequest{Turn: turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free)
	refused(t, err, RefuseNoChat)
	// An invalid proposal is refused and recorded.
	_, err = s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 2, NewSHA256: views[2].NewSHA256}, scope, files, free)
	refused(t, err, RefuseChangeInvalid)
	if read(t, root, "main.tf") != mainTF {
		t.Fatal("a refused apply wrote")
	}

	v, err := s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free)
	if err != nil || v.Status != ChangeApplied || !v.CanUndo {
		t.Fatalf("apply %+v %v", v, err)
	}
	if read(t, root, "main.tf") != next {
		t.Fatal("apply did not write exactly the proposed bytes")
	}
	_, err = s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free)
	refused(t, err, RefuseChangeDecided)

	// Reject writes nothing; a rejected proposal cannot be applied.
	if v, err := s.RejectChange("launch", id, turn, 1, scope, files); err != nil || v.Status != ChangeRejected {
		t.Fatalf("reject %+v %v", v, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "modules/net/extra.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reject wrote a file")
	}
	_, err = s.ApplyChange("launch", id, ChangeRequest{Turn: turn, Index: 1, NewSHA256: views[1].NewSHA256}, scope, files, free)
	refused(t, err, RefuseChangeDecided)

	// Undo restores the backup, only while the file still holds the applied text.
	write(t, root, "main.tf", next+"# edited by hand\n")
	_, err = s.UndoChange("launch", id, turn, 0, scope, free)
	refused(t, err, RefuseChangeNoUndo)
	write(t, root, "main.tf", next)
	if v, err := s.UndoChange("launch", id, turn, 0, scope, free); err != nil || v.Status != ChangeUndone {
		t.Fatalf("undo %+v %v", v, err)
	}
	if read(t, root, "main.tf") != mainTF {
		t.Fatal("undo did not restore the backup")
	}
	views, _ = s.Changes("launch", id, scope, files)
	if views[0].Status != ChangeUndone || views[1].Status != ChangeRejected {
		t.Fatalf("decisions not shown: %+v", views)
	}

	outcomes := []string{}
	for _, r := range rec.all() {
		outcomes = append(outcomes, r.Outcome+":"+r.Path+":"+r.Code)
		if r.Conversation != id || r.Root != root {
			t.Fatalf("record %+v", r)
		}
	}
	if strings.Join(outcomes, ",") != "refused::change_path,applied:main.tf:,rejected:modules/net/extra.tf:,undone:main.tf:" {
		t.Fatalf("records %v", outcomes)
	}
}

func TestUndoRemovesCreatedFileOnly(t *testing.T) {
	root := changeRoot(t)
	created := "locals {}\n"
	s, id, scope, _ := proposing(t, root, block("modules/net/extra.tf", "new", created))
	views, _ := s.Changes("launch", id, scope, files)
	if _, err := s.ApplyChange("launch", id, ChangeRequest{Turn: views[0].Turn, Index: 0, NewSHA256: views[0].NewSHA256}, scope, files, free); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "modules/net/extra.tf") != created {
		t.Fatal("new file not written")
	}
	if _, err := s.UndoChange("launch", id, views[0].Turn, 0, scope, free); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "modules/net/extra.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo kept the created file")
	}
}

func TestApplyRefusesWithoutBackupStore(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	s.Store = nil
	views, _ := s.Changes("launch", id, scope, files)
	_, err := s.ApplyChange("launch", id, ChangeRequest{Turn: views[0].Turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free)
	refused(t, err, RefuseChangeNoBackup)
	if read(t, root, "main.tf") != mainTF {
		t.Fatal("written without a backup")
	}
}

func TestApplyKeepsCRLFFileCheckedByNormalizedHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("line endings are the subject; covered on Unix")
	}
	root := changeRoot(t)
	write(t, root, "main.tf", strings.ReplaceAll(mainTF, "\n", "\r\n"))
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	views, _ := s.Changes("launch", id, scope, files)
	if views[0].Status != ChangeReady {
		t.Fatalf("CRLF file not matched by its shared hash: %+v", views[0])
	}
	if _, err := s.ApplyChange("launch", id, ChangeRequest{Turn: views[0].Turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free); err != nil {
		t.Fatal(err)
	}
	if read(t, root, "main.tf") != next {
		t.Fatal("not the proposed bytes")
	}
}

// files is the Git listing of a synthetic project outside any repository.
func files() GitFiles { return noRepo }

// Bypass attempts against the backend and structural checks, and tricky input that is valid.
func TestChangeHCLAdversarial(t *testing.T) {
	root := changeRoot(t)
	refusedAs := map[string]string{
		"backend after a block comment":      "terraform {\n  backend /* x */ \"s3\" {}\n}\n",
		"backend with an identifier label":   "terraform {\n  backend s3 {}\n}\n",
		"backend label on the next line":     "terraform {\n  backend\n  \"s3\"\n  {}\n}\n",
		"backend brace after a line comment": "terraform {\n  backend \"s3\" # c\n  {}\n}\n",
		"cloud after a block comment":        "terraform {\n  cloud /* x */ {}\n}\n",
		"cloud brace after a line comment":   "terraform {\n  cloud // c\n  {\n  }\n}\n",
		"cloud with tabs":                    "terraform {\n\tcloud\t{\n\t}\n}\n",
		"backend inside a heredoc":           "locals {\n  a = <<EOT\nbackend \"s3\" {}\nEOT\n}\n",
		"backend word in a comment":          "# backend \"s3\" {}\nlocals {}\n",
	}
	for name, text := range refusedAs {
		if v, _ := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: text}, noRepo); v.Code != "change_backend" {
			t.Errorf("%s: got %s %s", name, v.Status, v.Code)
		}
	}
	syntax := map[string]string{
		"extra closing brace":           "locals {}\n}\n",
		"mismatched brackets":           "locals {\n  a = [1, 2)\n}\n",
		"string across lines":           "locals {\n  a = \"one\ntwo\"\n}\n",
		"template never closed":         "locals {\n  a = \"${var.x\"\n}\n",
		"block comment never closed":    "locals {}\n/* open\n",
		"heredoc never closed":          "locals {\n  a = <<EOT\ntext\n}\n",
		"heredoc marker not at the end": "locals {\n  a = <<EOT x\n}\n",
		"brace hidden by a comment":     "locals {\n  # }\n",
		"bracket hidden in a string":    "locals {\n  a = \"{\"\n",
	}
	for name, text := range syntax {
		if v, _ := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: text}, noRepo); v.Code != "change_syntax" {
			t.Errorf("%s: got %s %s", name, v.Status, v.Code)
		}
	}
	textual := map[string]string{
		"bidi override (Trojan Source)": "locals {\n  a = \"x\u202e\"\n}\n",
		"byte order mark":               "\ufefflocals {}\n",
		"NUL byte":                      "locals {}\x00\n",
		"invalid UTF-8":                 "locals { a = \"\xff\" }\n",
	}
	for name, text := range textual {
		if v, _ := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: text}, noRepo); v.Code != "change_text" {
			t.Errorf("%s: got %s %s", name, v.Status, v.Code)
		}
	}
	valid := map[string]string{
		"braces in strings and comments": "locals {\n  a = \"}{][\" # }}}\n  b = \"//not a comment\" // {\n  /* { [ ( */\n}\n",
		"nested template strings":        "locals {\n  a = \"${lookup(var.m, \"k}\", \"d\")}-%{if true}x%{endif}\"\n}\n",
		"indented heredoc with brackets": "locals {\n  a = <<-EOT\n    } ] ) \" /*\n    EOT\n}\n",
		"nested blocks":                  "resource \"null_resource\" \"a\" {\n  triggers = {\n    v = jsonencode({ a = [1, (2)] })\n  }\n  lifecycle {\n    ignore_changes = [triggers]\n  }\n}\n",
		"backend as an attribute value":  "locals {\n  backend_name = \"none\"\n}\n",
	}
	for name, text := range valid {
		if v, _ := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: text}, noRepo); v.Status != ChangeReady {
			t.Errorf("%s: got %s %s", name, v.Status, v.Code)
		}
	}
	for _, p := range []string{"CON.tf", "modules/aux.tf", "nul.tofu", "lpt1/x.tf"} {
		if v, _ := checkChange(root, proposal{path: p, base: "new", text: "locals {}\n"}, noRepo); v.Code != "change_path" {
			t.Errorf("%s: got %s", p, v.Code)
		}
	}
}

// A proposal can only touch the package's own files: never a file Git ignores, a nested
// repository or submodule (absent from Git's listing), or anything when Git's listing is unknown.
func TestChangeStaysInPackageScope(t *testing.T) {
	root := changeRoot(t)
	write(t, root, "ignored/x.tf", mainTF)
	write(t, root, "vendor/sub/main.tf", mainTF) // a submodule: Git does not list its files
	repo := GitFiles{Repo: true, Known: true, Listed: map[string]bool{"main.tf": true, "modules/net/main.tf": true}, Ignored: notIgnored}
	cases := []struct{ path, base string }{
		{"ignored/x.tf", sum(mainTF)},
		{"ignored/new.tf", "new"},
		{"vendor/sub/main.tf", sum(mainTF)},
		{"vendor/sub/extra.tf", "new"},
	}
	for _, c := range cases {
		if v, _ := checkChange(root, proposal{path: c.path, base: c.base, text: "locals {}\n"}, repo); v.Code != "change_scope" {
			t.Errorf("%s: got %s %s", c.path, v.Status, v.Code)
		}
	}
	if v, _ := checkChange(root, proposal{path: "modules/net/extra.tf", base: "new", text: "locals {}\n"}, repo); v.Status != ChangeReady {
		t.Fatalf("listed folder: %+v", v)
	}
	if v, _ := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: "locals {}\n"}, GitFiles{Repo: true}); v.Code != "change_scope" {
		t.Fatalf("unknown Git listing: %+v", v)
	}
	write(t, root, ".gitignore", "*.secret\n") // outside a repository Hclaro cannot apply it
	if v, _ := checkChange(root, proposal{path: "main.tf", base: sum(mainTF), text: "locals {}\n"}, noRepo); v.Code != "change_scope" {
		t.Fatalf(".gitignore outside a repository: %+v", v)
	}
}

func applyFirst(t *testing.T, s *Session, id string, scope RunScope) ChangeView {
	t.Helper()
	views, _ := s.Changes("launch", id, scope, files)
	v, err := s.ApplyChange("launch", id, ChangeRequest{Turn: views[0].Turn, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func backups(t *testing.T, config, key string) []string {
	t.Helper()
	out, _ := filepath.Glob(filepath.Join(config, "ai-conversations", key, "*.backup"))
	return out
}

func TestBackupIsPrivateContainedAndSwept(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	config := t.TempDir()
	s.Store = OpenStore(config, time.Now())
	applyFirst(t, s, id, scope)
	files := backups(t, config, scope.ProjectKey)
	if len(files) != 1 || read(t, filepath.Dir(files[0]), filepath.Base(files[0])) != mainTF {
		t.Fatalf("backups %v", files)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{files[0]: 0o600, filepath.Dir(files[0]): 0o700, filepath.Dir(filepath.Dir(files[0])): 0o700} {
			if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
				t.Fatalf("%s: %v, want %v", p, info.Mode().Perm(), want)
			}
		}
	}
	// The replaced file stays next to it as the recovery artifact, owned by a record.
	artifact := filepath.Join(root, ".hclaro-aside-"+strings.TrimSuffix(filepath.Base(files[0]), ".backup")+".tmp")
	if read(t, root, filepath.Base(artifact)) != mainTF {
		t.Fatal("no recovery artifact")
	}
	// Before the retention a sweep (a restart) deletes nothing.
	s.Store.Sweep(time.Now())
	if len(backups(t, config, scope.ProjectKey)) != 1 || read(t, root, filepath.Base(artifact)) != mainTF {
		t.Fatal("swept before the retention")
	}
	// After it, the unchanged artifact, its record and its backup go together.
	age(t, filepath.Dir(files[0]))
	s.Store.Sweep(time.Now())
	if len(backups(t, config, scope.ProjectKey)) != 0 {
		t.Fatal("old backup kept")
	}
	if _, err := os.Lstat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unchanged artifact kept after the retention")
	}
	// Without its backup, Undo writes nothing.
	_, err := s.UndoChange("launch", id, 1, 0, scope, free)
	refused(t, err, RefuseChangeNoUndo)
	if read(t, root, "main.tf") != next {
		t.Fatal("undo wrote without a backup")
	}
}

func TestBackupRefusesLinkedFolders(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	config, outside := t.TempDir(), t.TempDir()
	s.Store = OpenStore(config, time.Now())
	if err := os.Symlink(outside, filepath.Join(config, "ai-conversations", scope.ProjectKey)); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	views, _ := s.Changes("launch", id, scope, files)
	_, err := s.ApplyChange("launch", id, ChangeRequest{Turn: 1, Index: 0, BaseSHA256: views[0].BaseSHA256, NewSHA256: views[0].NewSHA256}, scope, files, free)
	refused(t, err, RefuseChangeNoBackup)
	if read(t, root, "main.tf") != mainTF {
		t.Fatal("written without a contained backup")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("backup written through a link")
	}
}

// A backup folder swapped for a link with a planted backup after apply is never restored.
func TestUndoRefusesPlantedBackup(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	config, outside := t.TempDir(), t.TempDir()
	s.Store = OpenStore(config, time.Now())
	applyFirst(t, s, id, scope)
	files := backups(t, config, scope.ProjectKey)
	keyDir := filepath.Dir(files[0])
	if err := os.WriteFile(filepath.Join(outside, filepath.Base(files[0])), []byte("planted {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(keyDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, keyDir); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	_, err := s.UndoChange("launch", id, 1, 0, scope, free)
	refused(t, err, RefuseChangeNoUndo)
	if read(t, root, "main.tf") != next {
		t.Fatal("planted backup restored")
	}
}

// Undo belongs to the conversation's project and workspace, and never restores over later edits.
func TestUndoIsBoundToItsWorkspace(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	applyFirst(t, s, id, scope)
	other := scope
	other.Workspace = "prod"
	_, err := s.UndoChange("launch", id, 1, 0, other, free)
	refused(t, err, RefuseStale)
	_, err = s.UndoChange("hub-x", id, 1, 0, scope, free)
	refused(t, err, RefuseNoChat)
	elsewhere := scope
	elsewhere.Root = t.TempDir()
	_, err = s.UndoChange("launch", id, 1, 0, elsewhere, free)
	refused(t, err, RefuseStale)
	// A later edit, even one that only changes line endings, blocks Undo.
	write(t, root, "main.tf", strings.ReplaceAll(next, "\n", "\r\n"))
	_, err = s.UndoChange("launch", id, 1, 0, scope, free)
	refused(t, err, RefuseChangeNoUndo)
	if read(t, root, "main.tf") != strings.ReplaceAll(next, "\n", "\r\n") {
		t.Fatal("undo overwrote a later edit")
	}
}

// age makes every record and backup in dir older than the retention.
func age(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-40 * 24 * time.Hour)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(dir, e.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// An artifact something wrote into is never deleted, not even after the retention.
func TestChangedArtifactSurvivesCleanup(t *testing.T) {
	root := changeRoot(t)
	next := strings.Replace(mainTF, `"1"`, `"2"`, 1)
	s, id, scope, _ := proposing(t, root, block("main.tf", sum(mainTF), next))
	config := t.TempDir()
	s.Store = OpenStore(config, time.Now())
	v := applyFirst(t, s, id, scope)
	if v.Aside == "" {
		t.Fatal("the view does not name the artifact")
	}
	write(t, root, v.Aside, mainTF+"# written late through an open file\n")
	files := backups(t, config, scope.ProjectKey)
	age(t, filepath.Dir(files[0]))
	s.Store.Sweep(time.Now())
	if read(t, root, v.Aside) != mainTF+"# written late through an open file\n" {
		t.Fatal("a changed artifact was deleted")
	}
	if len(backups(t, config, scope.ProjectKey)) != 0 {
		t.Fatal("record kept after the retention")
	}
}

// Hclaro stopped after moving the file aside and before placing the new one: the next start
// puts the original back, never over a file someone created meanwhile.
func TestInterruptedChangeIsRolledBackAtStart(t *testing.T) {
	for _, taken := range []bool{false, true} {
		root := changeRoot(t)
		config := t.TempDir()
		store := OpenStore(config, time.Now())
		key := ProjectKey(root)
		id, err := store.Backup(key, []byte(mainTF))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Retain(key, id, root, "main.tf"); err != nil {
			t.Fatal(err)
		}
		// The state the move leaves: the name empty, the original as the artifact.
		aside := filepath.Join(root, ".hclaro-aside-"+id+".tmp")
		if err := os.Rename(filepath.Join(root, "main.tf"), aside); err != nil {
			t.Fatal(err)
		}
		if taken {
			write(t, root, "main.tf", "locals {} # saved after the stop\n")
		}
		OpenStore(config, time.Now()) // the next start
		switch {
		case !taken && (read(t, root, "main.tf") != mainTF || exists(aside)):
			t.Fatal("not rolled back")
		case taken && (read(t, root, "main.tf") != "locals {} # saved after the stop\n" || read(t, root, filepath.Base(aside)) != mainTF):
			t.Fatal("rolled back over a newer file, or the original was lost")
		}
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// New files stay supported: allowed in a folder whose configuration the package shares, never
// over an existing name, and applied and undone end to end.
func TestNewFileProposals(t *testing.T) {
	root := changeRoot(t)
	repo := GitFiles{Repo: true, Known: true, Listed: map[string]bool{"main.tf": true, "modules/net/main.tf": true}, Ignored: notIgnored}
	write(t, root, "docs/readme.tf.txt", "x")
	for p, code := range map[string]string{
		"extra.tf":             "",
		"modules/net/extra.tf": "",
		"modules/net/x.tofu":   "",
		"modules/new/extra.tf": "change_folder", // folders are never created
		"docs/extra.tf":        "change_scope",  // no shared configuration there
		"main.tf":              "change_exists",
	} {
		v, _ := checkChange(root, proposal{path: p, base: "new", text: "locals {}\n"}, repo)
		if code == "" && v.Status != ChangeReady || code != "" && v.Code != code {
			t.Errorf("%s: %s %s", p, v.Status, v.Code)
		}
	}
	s, id, scope, _ := proposing(t, root, block("extra.tf", "new", "locals {}\n"))
	v := applyFirst(t, s, id, scope)
	if !v.Created || v.Aside != "" || read(t, root, "extra.tf") != "locals {}\n" {
		t.Fatalf("created %+v", v)
	}
	if _, err := s.UndoChange("launch", id, 1, 0, scope, free); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "extra.tf")) {
		t.Fatal("undo kept the new file")
	}
}

func notIgnored(string) (bool, error) { return false, nil }

// A new file's name is in no Git listing, so Git's own ignore rules decide (gitctx.Ignored):
// a name Git ignores is refused before Apply, as is one Git cannot answer for.
func TestNewFileIgnoredByGitIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// No user or system Git configuration (core.excludesFile) changes the outcome.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1") // read by git only through the test's environment
	root := changeRoot(t)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	write(t, root, ".gitignore", "*_override.tf\n!keep_override.tf\ngen/\nmodules/**/secret*.tf\n")
	write(t, root, "modules/net/.gitignore", "local.tf\n")
	write(t, root, ".git/info/exclude", "scratch.tf\n")
	write(t, root, "gen/main.tf", "locals {}\n")
	git("add", "-f", "gen/main.tf") // tracked inside an ignored folder: listed, so its folder is in scope

	svc := gitctx.New()
	ctx := context.Background()
	listed, repo, ok := svc.ConfigFiles(ctx, root)
	files := GitFiles{Repo: repo, Listed: listed, Known: ok, Ignored: func(rel string) (bool, error) { return svc.Ignored(ctx, root, rel) }}
	if !repo || !ok || !listed["gen/main.tf"] || !listed["modules/net/main.tf"] {
		t.Fatalf("listing: %v %v %v", listed, repo, ok)
	}
	for p, code := range map[string]string{
		"main_override.tf":          "change_ignored", // file pattern in the root .gitignore
		"modules/net/x_override.tf": "change_ignored", // a parent folder's rule applies below it
		"modules/net/local.tf":      "change_ignored", // nested .gitignore
		"modules/net/secrets.tf":    "change_ignored", // ** pattern from the root
		"gen/extra.tf":              "change_ignored", // ignored directory pattern
		"scratch.tf":                "change_ignored", // .git/info/exclude
		"extra.tf":                  "",
		"local.tf":                  "", // the nested rule does not apply above its folder
		"keep_override.tf":          "", // re-included by a negated rule
		"modules/net/extra.tf":      "",
	} {
		v, _ := checkChange(root, proposal{path: p, base: "new", text: "locals {}\n"}, files)
		if code == "" && v.Status != ChangeReady || code != "" && v.Code != code {
			t.Errorf("%s: %s %s", p, v.Status, v.Code)
		}
	}
	// Replacing a listed file is unchanged by the new-file check.
	if v, _ := checkChange(root, proposal{path: "gen/main.tf", base: sum("locals {}\n"), text: "locals {\n  a = 1\n}\n"}, files); v.Status != ChangeReady {
		t.Fatalf("listed file in an ignored folder: %+v", v)
	}
	// Fail closed: Git could not answer, or no answer is wired.
	for name, ignored := range map[string]func(string) (bool, error){
		"error": func(string) (bool, error) { return false, errors.New("git failed") },
		"nil":   nil,
	} {
		f := files
		f.Ignored = ignored
		if v, _ := checkChange(root, proposal{path: "extra.tf", base: "new", text: "locals {}\n"}, f); v.Code != "change_ignored" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// A change whose "finished" record cannot be written reports it instead of dropping the error:
// the next start would treat the change as interrupted.
func TestSettleReportsUnwrittenRecord(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions as a non-root user")
	}
	root := changeRoot(t)
	config := t.TempDir()
	store := OpenStore(config, time.Now())
	key := ProjectKey(root)
	id, err := store.Backup(key, []byte(mainTF))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Retain(key, id, root, "main.tf"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(config, savedDir, key)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := store.Settle(key, id, root, "main.tf", true); err == nil {
		t.Fatal("unwritten record not reported")
	}
}

// When the record that a removal finished cannot be written, moved reports it and the removed
// file can be put back before the next start would do it unasked.
func TestUnsettledRemovalIsPutBack(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions as a non-root user")
	}
	root := changeRoot(t)
	write(t, root, "modules/net/extra.tf", "locals {}\n")
	s, _, scope, _ := proposing(t, root, "no change")
	var keyDir string
	id, unsettled, err := s.moved(scope, "modules/net/extra.tf", []byte("locals {}\n"), func(keep string) error {
		dir := filepath.Join(root, "modules/net")
		if err := os.Rename(filepath.Join(dir, "extra.tf"), filepath.Join(dir, safefile.AsideName(keep))); err != nil {
			return err
		}
		keyDir = filepath.Join(s.Store.config, savedDir, scope.ProjectKey)
		return os.Chmod(keyDir, 0o500)
	})
	t.Cleanup(func() { _ = os.Chmod(keyDir, 0o700) })
	if err != nil || !unsettled || id == "" {
		t.Fatalf("id %q unsettled %v err %v", id, unsettled, err)
	}
	if !putBackRemoved(root, "modules/net/extra.tf", id) || read(t, root, "modules/net/extra.tf") != "locals {}\n" {
		t.Fatal("removed file not put back")
	}
	if exists(filepath.Join(root, "modules/net", safefile.AsideName(id))) {
		t.Fatal("aside name left behind")
	}
}
