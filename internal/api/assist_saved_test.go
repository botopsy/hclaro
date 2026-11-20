package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
)

// AI-01.11 over HTTP: opt-in saved conversations, reopen after a restart (a new session on the
// same configuration folder), replay, read-only reopen, delete and project isolation.

type savedList struct {
	Conversations []assist.SavedListed
	Available     bool
	RetentionDays int
}

// restart replaces the AI session as a new Hclaro process would: nothing in memory, the same
// configuration folder.
func restart(s *planServer, config string, a assist.Assistant) {
	s.t.Helper()
	s.manager.Assist = assist.NewSession(false, a)
	s.manager.Assist.Store = assist.OpenStore(config, time.Now())
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
}

func savedFiles(t *testing.T, config string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(filepath.Join(config, "ai-conversations"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") && filepath.Base(path) != "settings.json" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func startChat(t *testing.T, s *planServer, message string) assist.Conversation {
	t.Helper()
	w := s.do("POST", "/api/v1/ai/conversations", chatBody(message))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return waitChat(t, s.do, "/api/v1", decode[assist.Conversation](t, w).ID)
}

func TestAISavedConversationSurvivesRestartAndReplays(t *testing.T) {
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	config := t.TempDir()
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "first reply"}
	restart(s, config, stub)

	// Unsaved conversations leave no file.
	unsaved := startChat(t, s, "Is this one kept?")
	if files := savedFiles(t, config); len(files) != 0 {
		t.Fatalf("an unsaved conversation was written: %v", files)
	}
	if unsaved.Saved {
		t.Fatal("saved by default")
	}

	c := startChat(t, s, "How do moved blocks work?")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{}`), 400, "invalid_request")
	if w := s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":true}`); w.Code != 200 || !decode[assist.Conversation](t, w).Saved {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	// Attach project context and continue, so the file holds a context turn and its fingerprint.
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`))
	if w := s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/context", `{"packageSha256":"`+p.SHA256+`"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/messages", `{"text":"Which buckets exist?"}`)
	before := waitChat(t, s.do, "/api/v1", c.ID)
	files := savedFiles(t, config)
	if len(files) != 1 || filepath.Base(files[0]) != c.ID+".json" || filepath.Base(filepath.Dir(files[0])) != assist.ProjectKey(root) {
		t.Fatalf("files: %v", files)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{files[0]: 0o600, filepath.Dir(files[0]): 0o700, filepath.Join(config, "ai-conversations"): 0o700} {
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
				t.Fatalf("%s: %v %v, want %v", path, info.Mode().Perm(), err, want)
			}
		}
	}
	raw, _ := os.ReadFile(files[0])
	text := string(raw)
	if !strings.Contains(text, "first reply") || !strings.Contains(text, "legacy_logs") {
		t.Fatalf("file lacks the transcript:\n%s", text)
	}
	// Never stored: the provider path (argv), environment, stderr, provider metadata or cost,
	// and the project's local path.
	for _, never := range []string{"/synthetic/bin", root, "session_id", "credential_expires_at", "total_cost", "stderr", "PATH=", "HOME=", "argv"} {
		if strings.Contains(text, never) {
			t.Fatalf("stored file contains %q", never)
		}
	}

	// Restart: a new session on the same folder lists, reopens and continues it.
	stub2 := &stubAssistant{mode: assist.ModeHeadless, reply: "second reply"}
	restart(s, config, stub2)
	list := decode[savedList](t, s.do("GET", "/api/v1/ai/saved", ""))
	if !list.Available || list.RetentionDays != 30 || len(list.Conversations) != 1 || list.Conversations[0].ID != c.ID || list.Conversations[0].Title != "How do moved blocks work?" || list.Conversations[0].Open {
		t.Fatalf("%+v", list)
	}
	w := s.do("POST", "/api/v1/ai/saved/"+c.ID+"/open", `{}`)
	reopened := decode[assist.Conversation](t, w)
	if w.Code != 200 || reopened.ReadOnly != "" || reopened.NeedsReview || !reopened.Saved || len(reopened.Turns) != len(before.Turns) {
		t.Fatalf("reopen: %d %s", w.Code, w.Body)
	}
	if !decode[savedList](t, s.do("GET", "/api/v1/ai/saved", "")).Conversations[0].Open {
		t.Fatal("reopened conversation not marked open")
	}
	if w := s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/messages", `{"text":"And after a restart?"}`); w.Code != 202 {
		t.Fatalf("continue: %d %s", w.Code, w.Body)
	}
	after := waitChat(t, s.do, "/api/v1", c.ID)
	// The fake's stdin is the full replayed transcript: exactly the last stdin before the restart
	// (context bytes included), then the saved reply and the new message.
	want := stub.payloads[2] + "\n## Turn 5: assistant\n\n```text\nfirst reply\n```\n\n## Turn 6: user\n\n```text\nAnd after a restart?\n```\n"
	if stub2.ran() != 1 || stub2.last() != want || !strings.Contains(stub2.last(), "\n"+p.Markdown+"\n") || len(after.Turns) != 7 {
		t.Fatalf("replayed stdin:\n%s\nwant:\n%s", stub2.last(), want)
	}
	raw, _ = os.ReadFile(files[0])
	if !strings.Contains(string(raw), "second reply") {
		t.Fatal("the continued turn was not saved")
	}

	// Changed project files: the reopened conversation is stale until current context is attached.
	if err := os.WriteFile(filepath.Join(root, "extra.tf"), []byte("locals {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restart(s, config, stub2)
	s.do("POST", "/api/v1/ai/saved/"+c.ID+"/open", `{}`)
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/messages", `{"text":"still there?"}`), 409, "context_changed")

	// Unsave deletes the file; delete and delete-all remove everything of this project.
	if w := s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":false}`); w.Code != 200 || decode[assist.Conversation](t, w).Saved {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if files := savedFiles(t, config); len(files) != 0 {
		t.Fatalf("unsave left %v", files)
	}
	s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":true}`)
	if w := s.do("POST", "/api/v1/ai/saved/"+c.ID+"/delete", `{}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if files := savedFiles(t, config); len(files) != 0 {
		t.Fatalf("delete left %v", files)
	}
	s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":true}`)
	other := startChat(t, s, "another one")
	s.do("POST", "/api/v1/ai/conversations/"+other.ID+"/save", `{"saved":true}`)
	if n := decode[map[string]int](t, s.do("POST", "/api/v1/ai/saved/delete-all", `{}`)); n["deleted"] != 2 {
		t.Fatalf("delete-all: %v", n)
	}
	entries, _ := os.ReadDir(filepath.Join(config, "ai-conversations"))
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("left in the store: %v", entries)
	}
}

// A planted canary in a stored file hides that turn on load and makes the conversation
// read-only "needs review"; nothing is sent and the canary never reaches the browser.
func TestAISavedConversationRescanOnLoad(t *testing.T) {
	s, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	config := t.TempDir()
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "a harmless reply"}
	restart(s, config, stub)
	c := startChat(t, s, "hello")
	s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":true}`)
	path := savedFiles(t, config)[0]
	raw, _ := os.ReadFile(path)
	planted := strings.Replace(string(raw), "a harmless reply", "key AKIAIOSFODNN7EXAMPLE", 1)
	if planted == string(raw) {
		t.Fatal("reply not found in the file")
	}
	if err := os.WriteFile(path, []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	restart(s, config, stub)
	w := s.do("POST", "/api/v1/ai/saved/"+c.ID+"/open", `{}`)
	got := decode[assist.Conversation](t, w)
	if w.Code != 200 || strings.Contains(w.Body.String(), "AKIA") || !got.NeedsReview || got.ReadOnly == "" || !got.Turns[1].Hidden || got.Turns[0].Hidden {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(s.do("GET", "/api/v1/ai/conversations/"+c.ID, "").Body.String(), "AKIA") {
		t.Fatal("canary returned by GET")
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/messages", `{"text":"go on"}`), 409, "conversation_read_only")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":true}`), 409, "conversation_read_only")
	if stub.ran() != 1 {
		t.Fatal("a needs-review conversation reached the assistant")
	}
	// It can still be deleted.
	if w := s.do("POST", "/api/v1/ai/saved/"+c.ID+"/delete", `{}`); w.Code != 200 || len(savedFiles(t, config)) != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// Reopen never claims a resume that will not work: a missing assistant, or a review of a plan
// of an earlier session, opens read-only with the reason.
func TestAISavedConversationReadOnlyReasons(t *testing.T) {
	s, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	config := t.TempDir()
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	restart(s, config, stub)
	chat := startChat(t, s, "hello")
	s.do("POST", "/api/v1/ai/conversations/"+chat.ID+"/save", `{"saved":true}`)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	w := s.do("POST", "/api/v1/ai/conversations", `{"packageSha256":"`+p.SHA256+`","disclosureVersion":"`+assist.DisclosureVersion+`","prompt":"review-plan@1"}`)
	review := waitChat(t, s.do, "/api/v1", decode[assist.Conversation](t, w).ID)
	s.do("POST", "/api/v1/ai/conversations/"+review.ID+"/save", `{"saved":true}`)

	restart(s, config, stub)
	got := decode[assist.Conversation](t, s.do("POST", "/api/v1/ai/saved/"+review.ID+"/open", `{}`))
	if !strings.Contains(got.ReadOnly, "earlier Hclaro session") || len(got.Turns) != 2 {
		t.Fatalf("%+v", got)
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+review.ID+"/messages", `{"text":"more"}`), 409, "conversation_read_only")

	restart(s, config, &stubAssistant{mode: assist.ModeUnsupported})
	got = decode[assist.Conversation](t, s.do("POST", "/api/v1/ai/saved/"+chat.ID+"/open", `{}`))
	if !strings.Contains(got.ReadOnly, "cannot run headless here now") || got.Turns[1].Text != "ok" {
		t.Fatalf("%+v", got)
	}
	if stub.ran() != 2 {
		t.Fatal("a read-only conversation reached an assistant")
	}
}

// A file of project A, copied into project B's folder or asked for from B, is refused.
func TestAISavedConversationIsolatedPerProject(t *testing.T) {
	a, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	b, rootB := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	config := t.TempDir()
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	restart(a, config, stub)
	restart(b, config, stub)
	c := startChat(t, a, "project A only")
	a.do("POST", "/api/v1/ai/conversations/"+c.ID+"/save", `{"saved":true}`)
	if l := decode[savedList](t, b.do("GET", "/api/v1/ai/saved", "")); len(l.Conversations) != 0 {
		t.Fatalf("B lists A's conversation: %+v", l)
	}
	wantError(t, b.do("POST", "/api/v1/ai/saved/"+c.ID+"/open", `{}`), 404, "conversation_not_found")
	// Planted: A's file copied into B's folder keeps A's project key and is refused.
	raw, _ := os.ReadFile(savedFiles(t, config)[0])
	dirB := filepath.Join(config, "ai-conversations", assist.ProjectKey(rootB))
	_ = os.MkdirAll(dirB, 0o700)
	if err := os.WriteFile(filepath.Join(dirB, c.ID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wantError(t, b.do("POST", "/api/v1/ai/saved/"+c.ID+"/open", `{}`), 404, "conversation_not_found")
	if l := decode[savedList](t, b.do("GET", "/api/v1/ai/saved", "")); len(l.Conversations) != 0 {
		t.Fatalf("B lists a planted file: %+v", l)
	}
	// Invalid IDs never reach the file system.
	wantError(t, b.do("POST", "/api/v1/ai/saved/..%2f..%2fx/open", `{}`), 404, "conversation_not_found")
}

// Hub: each read context lists only its project's saved conversations.
func TestAIHubSavedConversationsArePerProject(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.launch.Assist = assist.NewSession(false, stub)
	config := t.TempDir()
	s.launch.Assist.Store = assist.OpenStore(config, time.Now())
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	other := s.openLoaded("platform", "staging", "")
	do := func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) }
	w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations", chatBody("network question"))
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, do, "/api/v1/contexts/"+a, id)
	if w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations/"+id+"/save", `{"saved":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if l := decode[savedList](t, s.do("GET", "/api/v1/contexts/"+a+"/ai/saved", "")); len(l.Conversations) != 1 || l.Conversations[0].Workspace != "dev" {
		t.Fatalf("%+v", l)
	}
	if l := decode[savedList](t, s.do("GET", "/api/v1/contexts/"+other+"/ai/saved", "")); len(l.Conversations) != 0 {
		t.Fatalf("another project lists it: %+v", l)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+other+"/ai/saved/"+id+"/open", `{}`), 404, "conversation_not_found")
	// Another workspace of the same project lists it and opens it read-only.
	b := s.openLoaded("network", "prod", "")
	// The open copy belongs to context a; close it so b can reopen the file.
	s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations/"+id+"/close", `{}`)
	got := decode[assist.Conversation](t, s.do("POST", "/api/v1/contexts/"+b+"/ai/saved/"+id+"/open", `{}`))
	if !strings.Contains(got.ReadOnly, "workspace dev") {
		t.Fatalf("%+v", got)
	}
}
