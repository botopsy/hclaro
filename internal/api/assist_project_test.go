package api

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
)

// AI-01.09 over HTTP: project packages, conversation-only start, attaching a previewed project
// package as a turn, and the session conversation list.

func chatBody(message string) string {
	return fmt.Sprintf(`{"disclosureVersion":%q,"message":%q}`, assist.DisclosureVersion, message)
}

func TestAIProjectContextOverHTTP(t *testing.T) {
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.manager.Assist = assist.NewSession(false, stub)
	for name, text := range map[string]string{
		"terraform.tfvars":  `db_password = "CANARY_TFVARS"`,
		"terraform.tfstate": `{"resources":[{"instances":[{"attributes":{"p":"CANARY_STATE"}}]}]}`,
		"secrets.tf":        "locals {\n  api_token = \"CANARY_LOCAL\"\n}\n",
		"backend.tf":        "terraform {\n  backend \"s3\" {\n    bucket = \"CANARY_BACKEND\"\n  }\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.manager.Assist.SetEnabled(false) // on by default (AI-01.14); these checks start from off
	wantError(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`), 409, "ai_disabled")
	wantError(t, s.do("GET", "/api/v1/ai/conversations", ""), 409, "ai_disabled")
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)

	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`))
	if p.Kind != assist.KindProject || strings.Contains(p.Markdown, "CANARY") || !strings.Contains(p.Markdown, `resource "aws_s3_bucket" "legacy_logs"`) ||
		!strings.Contains(p.Markdown, "secrets.tf: may contain a secret or a local path") {
		t.Fatalf("project package:\n%s", p.Markdown)
	}
	exportSame(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), p)

	// Conversation only: a message is required and stdin carries no project data.
	wantError(t, s.do("POST", "/api/v1/ai/conversations", chatBody("")), 400, "message_required")
	w := s.do("POST", "/api/v1/ai/conversations", chatBody("How do moved blocks work?"))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	c := waitChat(t, s.do, "/api/v1", id)
	if c.Kind != assist.KindChat || strings.Contains(stub.last(), "legacy_logs") || strings.Contains(stub.last(), "## Context package") ||
		stub.last() != assist.Transcript("", c.Turns[:1]) {
		t.Fatalf("conversation-only stdin:\n%s", stub.last())
	}

	// Attach the previewed project package; the next turn sends exactly those bytes.
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/context", `{"packageSha256":"nope"}`), 404, "package_not_found")
	w = s.do("POST", "/api/v1/ai/conversations/"+id+"/context", `{"packageSha256":"`+p.SHA256+`"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "legacy_logs") {
		t.Fatalf("attach: %d %s", w.Code, w.Body)
	}
	if stub.ran() != 1 {
		t.Fatal("attaching ran the assistant")
	}
	s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"Which buckets exist?"}`)
	c = waitChat(t, s.do, "/api/v1", id)
	if len(c.Turns) != 5 || c.Turns[2].Role != "context" || c.Turns[2].SHA256 != p.SHA256 || !strings.Contains(stub.last(), "\n"+p.Markdown+"\n") {
		t.Fatalf("%+v\n%s", c.Turns, stub.last())
	}

	// The list names it, as a conversation of this workspace.
	list := decode[struct{ Conversations []assist.Listed }](t, s.do("GET", "/api/v1/ai/conversations", ""))
	if len(list.Conversations) != 1 || !list.Conversations[0].Here || list.Conversations[0].Kind != assist.KindProject || list.Conversations[0].Title != "How do moved blocks work?" {
		t.Fatalf("%+v", list)
	}
}

func TestAIHubProjectContextStaysInItsContext(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.launch.Assist = assist.NewSession(false, stub)
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("platform", "staging", "")
	do := func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) }

	p := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"project"}`))
	w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations", chatBody("hello"))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, do, "/api/v1/contexts/"+a, id)
	// Another workspace lists it read-only and cannot attach to or continue it.
	list := decode[struct{ Conversations []assist.Listed }](t, s.do("GET", "/api/v1/contexts/"+b+"/ai/conversations", ""))
	if len(list.Conversations) != 1 || list.Conversations[0].Here || list.Conversations[0].Workspace != "dev" {
		t.Fatalf("%+v", list)
	}
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/conversations/"+id+"/context", `{"packageSha256":"`+p.SHA256+`"}`), 404, "conversation_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/context", `{"packageSha256":"`+p.SHA256+`"}`), 404, "conversation_not_found")
	if w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations/"+id+"/context", `{"packageSha256":"`+p.SHA256+`"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if here := decode[struct{ Conversations []assist.Listed }](t, s.do("GET", "/api/v1/contexts/"+a+"/ai/conversations", "")); !here.Conversations[0].Here {
		t.Fatalf("%+v", here)
	}
}

// A project conversation is checked against the files on disk before every turn: an edited .tf
// file refuses the turn until current project context is attached; unrelated files do not.
func TestAIProjectConversationGoesStaleOnFileChange(t *testing.T) {
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.manager.Assist = assist.NewSession(false, stub)
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`))
	w := s.do("POST", "/api/v1/ai/conversations", fmt.Sprintf(`{"packageSha256":%q,"disclosureVersion":%q,"message":"Summarize"}`, p.SHA256, assist.DisclosureVersion))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, s.do, "/api/v1", id)

	for name, text := range map[string]string{"README.md": "docs", "terraform.tfvars": `x = "y"`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if c := waitChat(t, s.do, "/api/v1", id); c.Stale {
		t.Fatal("an unrelated file made the conversation stale")
	}
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_s3_bucket\" \"renamed\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := waitChat(t, s.do, "/api/v1", id); !c.Stale {
		t.Fatal("an edited .tf file did not make the conversation stale")
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"and now?"}`), 409, "context_changed")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/context", `{"packageSha256":"`+p.SHA256+`"}`), 409, "context_changed")
	ran := stub.ran()

	// Attaching current project context continues the conversation with the new bytes.
	fresh := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`))
	if w := s.do("POST", "/api/v1/ai/conversations/"+id+"/context", `{"packageSha256":"`+fresh.SHA256+`"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"and now?"}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitChat(t, s.do, "/api/v1", id)
	if stub.ran() != ran+1 || !strings.Contains(stub.last(), "aws_s3_bucket\" \"renamed\"") {
		t.Fatal("the turn after re-attaching did not carry the current configuration")
	}
}

// Git ignore rules that change after a package was built (a file in the preview is now ignored)
// make every hand-over refuse: start, attach and export re-check the fingerprint with a fresh
// Git listing. Nothing reaches the assistant.
func TestAIProjectPackageRefusedWhenIgnoreRulesChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	git := exec.Command("git", "init", "-q")
	git.Dir = root
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.manager.Assist = assist.NewSession(false, stub)
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`))
	if !strings.Contains(p.Markdown, "legacy_logs") {
		t.Fatalf("untracked, not ignored main.tf missing:\n%s", p.Markdown)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("main.tf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations", fmt.Sprintf(`{"packageSha256":%q,"disclosureVersion":%q,"message":"x"}`, p.SHA256, assist.DisclosureVersion)), 409, "context_changed")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/export", `{}`), 409, "context_changed")
	w := s.do("POST", "/api/v1/ai/conversations", chatBody("hello"))
	id := decode[assist.Conversation](t, w).ID
	waitChat(t, s.do, "/api/v1", id)
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/context", `{"packageSha256":"`+p.SHA256+`"}`), 409, "context_changed")
	if strings.Contains(stub.last(), "legacy_logs") {
		t.Fatal("the now-ignored file reached the assistant")
	}
	// A rebuilt package leaves it out.
	if again := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project"}`)); strings.Contains(again.Markdown, "legacy_logs") {
		t.Fatal("rebuilt package still holds the ignored file")
	}
}
