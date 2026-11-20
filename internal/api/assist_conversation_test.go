package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
)

// AI-01.07 conversation routes, against an in-process assistant. Process isolation, argv and
// environment of each turn are tested on the real CLI runner with a fake executable
// (internal/assist and TestAICanaryNeverLeaves).

type stubAssistant struct {
	id, mode  string // id defaults to "stub"
	reply     string
	block     bool
	mu        sync.Mutex
	payloads  []string
	cancelled int
}

func (a *stubAssistant) ID() string {
	if a.id == "" {
		return "stub"
	}
	return a.id
}
func (a *stubAssistant) Detect(context.Context) assist.ProviderStatus {
	if a.mode == assist.ModeUnsupported {
		return assist.ProviderStatus{ID: a.ID(), Name: a.ID(), Mode: a.mode, Reason: assist.KindMissing, Message: assist.Message(assist.KindMissing)}
	}
	return assist.ProviderStatus{ID: a.ID(), Name: a.ID(), Mode: a.mode, Version: "1.0.0", Path: "/synthetic/bin/" + a.ID()}
}
func (a *stubAssistant) ran() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.payloads)
}
func (a *stubAssistant) last() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.payloads[len(a.payloads)-1]
}
func (a *stubAssistant) Run(ctx context.Context, payload []byte) (string, error) {
	a.mu.Lock()
	a.payloads = append(a.payloads, string(payload))
	block := a.block
	a.mu.Unlock()
	if block {
		<-ctx.Done()
		a.mu.Lock()
		a.cancelled++
		a.mu.Unlock()
		return "", ctx.Err()
	}
	return a.reply, nil
}

// startBody names a previewed package and the current disclosure version; the server picks the
// assistant.
func startBody(hash, message string) string {
	return fmt.Sprintf(`{"packageSha256":%q,"disclosureVersion":%q,"message":%q}`, hash, assist.DisclosureVersion, message)
}

// waitChat polls a conversation under base until its turn has finished.
func waitChat(t *testing.T, do doFunc, base, id string) assist.Conversation {
	t.Helper()
	// 30 s: a first Codex turn also runs the self-check, slow under -race on a loaded machine.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		c := decode[assist.Conversation](t, do("GET", base+"/ai/conversations/"+id, ""))
		if c.State != assist.StateRunning {
			return c
		}
	}
	t.Fatal("turn did not finish")
	return assist.Conversation{}
}

func TestAIConversationOverHTTP(t *testing.T) {
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "Run `terraform apply -auto-approve` then rm -rf / <script>x</script> [x](javascript:alert(1))"}
	s.svc.Assist = assist.NewSession(false, stub)
	var mu sync.Mutex
	var records []assist.RunRecord
	s.svc.Assist.Record = func(r assist.RunRecord) { mu.Lock(); records = append(records, r); mu.Unlock() }
	planID := s.plan()
	zero := strings.Repeat("0", 64)
	s.svc.Assist.SetEnabled(false) // on by default (AI-01.14); these checks start from off
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(zero, "")), 409, "ai_disabled")
	wantError(t, s.do("GET", "/api/v1/ai/conversations/"+strings.Repeat("a", 32), ""), 409, "ai_disabled")

	s.enableAI()
	st := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", ""))
	if !st.Headless || st.DefaultProvider != "stub" || st.Accepted || st.Disclosure.Version != "10" {
		t.Fatalf("%+v", st)
	}
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`))
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "")), 409, "disclosure_required")
	// The browser cannot set an executable path.
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"path":"/bin/sh"}`), 400, "invalid_request")
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"10"}`)
	wantError(t, s.do("POST", "/api/v1/ai/conversations", `{"packageSha256":"`+p.SHA256+`","disclosureVersion":"7"}`), 409, "disclosure_required")
	// The server picks the assistant (B07): a request naming another one than the chosen one
	// (the UI names the one it showed) is refused and runs nothing.
	for _, provider := range []string{"other", "claude"} {
		wantError(t, s.do("POST", "/api/v1/ai/conversations", `{"packageSha256":"`+p.SHA256+`","provider":"`+provider+`","disclosureVersion":"`+assist.DisclosureVersion+`"}`), 409, "provider_unavailable")
		wantError(t, s.do("POST", "/api/v1/ai/conversations", `{"provider":"`+provider+`","disclosureVersion":"`+assist.DisclosureVersion+`","message":"hi"}`), 409, "provider_unavailable")
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(zero, "")), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256+"x", "")), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/conversations", `{"packageSha256":"`+p.SHA256+`","disclosureVersion":"5","payload":"x"}`), 400, "invalid_request")
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "key AKIAIOSFODNN7EXAMPLE")), 422, "message_blocked")
	if stub.ran() != 0 {
		t.Fatal("a refused request reached the assistant")
	}

	// Start, follow up twice; each turn replays the conversation.
	w := s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, ""))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	c := waitChat(t, s.do, "/api/v1", id)
	if c.State != assist.StateReady || len(c.Turns) != 2 || c.Turns[1].Text != stub.reply || stub.last() != assist.Transcript(p.Markdown, c.Turns[:1]) {
		t.Fatalf("%+v", c)
	}
	for _, m := range []string{"Which change is riskiest?", "Explain the replacement."} {
		if w := s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", fmt.Sprintf(`{"text":%q}`, m)); w.Code != 202 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		c = waitChat(t, s.do, "/api/v1", id)
		if stub.last() != assist.Transcript(p.Markdown, c.Turns[:len(c.Turns)-1]) {
			t.Fatal("a turn's stdin is not the canonical transcript")
		}
	}
	if len(c.Turns) != 6 || stub.ran() != 3 || c.UsedBytes == 0 || c.LimitBytes != assist.MaxTranscript {
		t.Fatalf("%+v", c)
	}
	mu.Lock()
	if len(records) != 3 || records[2].SHA256 != sum(stub.last()) || records[2].Root != root || records[2].Kind != assist.KindPlan {
		t.Fatalf("%+v", records)
	}
	mu.Unlock()
	// Follow-ups are refused for secrets, size, emptiness and a changed context; none is sent.
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"`+fixtures.Canary+`"}`), 422, "message_blocked")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", fmt.Sprintf(`{"text":%q}`, "see "+root+"/main.tf")), 422, "message_blocked")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"`+strings.Repeat("a", assist.MaxMessage+1)+`"}`), 413, "message_too_long")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":" "}`), 400, "message_required")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+strings.Repeat("b", 32)+"/messages", `{"text":"hi"}`), 404, "conversation_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/conversations/x/messages", `{"text":"hi"}`), 404, "conversation_not_found")
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_instance\" \"web\" { ami = \"y\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !decode[assist.Conversation](t, s.do("GET", "/api/v1/ai/conversations/"+id, "")).Stale {
		t.Fatal("a changed context is not reported as stale")
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"still?"}`), 409, "context_changed")
	if stub.ran() != 3 {
		t.Fatal("a refused follow-up reached the assistant")
	}
	// Close forgets it; turning AI off drops every conversation.
	if w := s.do("POST", "/api/v1/ai/conversations/"+id+"/close", `{}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	wantError(t, s.do("GET", "/api/v1/ai/conversations/"+id, ""), 404, "conversation_not_found")
}

func TestAIConversationBusyCancelAndStrict(t *testing.T) {
	s, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, block: true}
	s.svc.Assist = assist.NewSession(false, stub)
	defer s.svc.Assist.Close()
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	id := decode[assist.Conversation](t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "first"))).ID
	wantError(t, s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"again"}`), 409, "turn_in_progress")
	if w := s.do("POST", "/api/v1/ai/conversations/"+id+"/cancel", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "true") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	c := waitChat(t, s.do, "/api/v1", id)
	if c.State != assist.StateCancelled || len(c.Turns) != 0 || c.Draft == nil || *c.Draft != "first" {
		t.Fatalf("%+v", c)
	}
	// AI off drops the conversation while a turn runs.
	s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"again"}`)
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	s.enableAI()
	wantError(t, s.do("GET", "/api/v1/ai/conversations/"+id, ""), 404, "conversation_not_found")

	strict, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	strict.svc.Assist = assist.NewSession(true, stub)
	strict.enableAI()
	strict.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	sp := build(t, strict.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+strict.plan()+`"}`))
	wantError(t, strict.do("POST", "/api/v1/ai/conversations", startBody(sp.SHA256, "")), 409, "strict_local")
	if st := decode[assist.Settings](t, strict.do("GET", "/api/v1/ai/settings", "")); st.Headless || !st.Handoff {
		t.Fatalf("%+v", st)
	}

	// A handoff-only provider never starts a conversation.
	h, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	handoff := &stubAssistant{mode: assist.ModeHandoff}
	h.svc.Assist = assist.NewSession(false, handoff)
	h.enableAI()
	h.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	hp := build(t, h.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+h.plan()+`"}`))
	wantError(t, h.do("POST", "/api/v1/ai/conversations", startBody(hp.SHA256, "")), 409, "provider_unavailable")
	if handoff.ran() != 0 {
		t.Fatal("a handoff-only assistant was run")
	}
}

// Hub: a conversation belongs to the read context it started in. Another workspace of the same
// project, another project and the launch project cannot see or continue it.
func TestAIHubConversationStaysInItsContext(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.svc.Assist = assist.NewSession(false, stub)
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("network", "prod", "")
	other := s.openLoaded("platform", "staging", "")
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+a+"/resources?limit=1", ""))
	p := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"resource","resourceId":"`+page.Resources[0].ID+`"}`))
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/conversations", startBody(p.SHA256, "")), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "")), 404, "package_not_found")
	w := s.do("POST", "/api/v1/contexts/"+a+"/ai/conversations", startBody(p.SHA256, ""))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	base := "/api/v1/contexts/" + a
	do := func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) }
	c := waitChat(t, do, base, id)
	if c.State != assist.StateReady || stub.last() != assist.Transcript(p.Markdown, c.Turns[:1]) {
		t.Fatalf("%+v", c)
	}
	for _, elsewhere := range []string{"/api/v1/contexts/" + b, "/api/v1/contexts/" + other, "/api/v1"} {
		wantError(t, s.do("GET", elsewhere+"/ai/conversations/"+id, ""), 404, "conversation_not_found")
		wantError(t, s.do("POST", elsewhere+"/ai/conversations/"+id+"/messages", `{"text":"hi"}`), 404, "conversation_not_found")
		wantError(t, s.do("POST", elsewhere+"/ai/conversations/"+id+"/close", `{}`), 404, "conversation_not_found")
	}
	if w := s.do("POST", base+"/ai/conversations/"+id+"/messages", `{"text":"and then?"}`); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if c = waitChat(t, do, base, id); len(c.Turns) != 4 || stub.ran() != 2 {
		t.Fatalf("%+v", c)
	}
	// Reopening replaces the read context (a new revision): the old one and its conversation
	// are gone, and the new context cannot reach the conversation either.
	fresh := s.openLoaded("network", "dev", "")
	wantError(t, s.do("POST", base+"/ai/conversations/"+id+"/messages", `{"text":"after reload"}`), 404, "context_closed")
	wantError(t, s.do("POST", "/api/v1/contexts/"+fresh+"/ai/conversations/"+id+"/messages", `{"text":"after reload"}`), 404, "conversation_not_found")
	if stub.ran() != 2 {
		t.Fatal("a turn after the reload reached the assistant")
	}
}

// Disclosure v10: a v7 or v9 acceptance does not count; turning AI off revokes acceptance.
func TestAIDisclosureVersionGatesConversations(t *testing.T) {
	if assist.DisclosureVersion != "10" {
		t.Fatalf("disclosure version %s", assist.DisclosureVersion)
	}
	s, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.svc.Assist = assist.NewSession(false, stub)
	s.enableAI()
	st := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", ""))
	if !strings.Contains(strings.Join(st.Disclosure.Lines, "\n"), "resends the whole conversation") {
		t.Fatalf("v9 lacks the replay cost line: %q", st.Disclosure.Lines)
	}
	if !strings.Contains(strings.Join(st.Disclosure.Lines, "\n"), "unless you tick Save conversation") {
		t.Fatalf("v9 lacks the saved conversations line: %q", st.Disclosure.Lines)
	}
	if !strings.Contains(strings.Join(st.Disclosure.Lines, "\n"), "writes that file only when you apply that one proposal") {
		t.Fatalf("v9 lacks the proposed changes line: %q", st.Disclosure.Lines)
	}
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	old := `{"packageSha256":"` + p.SHA256 + `","disclosureVersion":"7"}`
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "")), 409, "disclosure_required")
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"7"}`), 409, "disclosure_required")
	if decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", "")).Accepted {
		t.Fatal("a v7 acceptance was recorded")
	}
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"9"}`), 409, "disclosure_required")
	if !decode[assist.Settings](t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"10"}`)).Accepted {
		t.Fatal("v10 not accepted")
	}
	wantError(t, s.do("POST", "/api/v1/ai/conversations", old), 409, "disclosure_required")
	if w := s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "")); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	s.enableAI()
	if decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", "")).Accepted {
		t.Fatal("acceptance survived AI off")
	}
	p = build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	wantError(t, s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "")), 409, "disclosure_required")
}

// Reopening (or closing) a Hub context drops its conversations: a running turn is cancelled,
// and neither the old nor the new context can reach it.
func TestAIHubContextCloseDropsConversations(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, block: true}
	s.svc.Assist = assist.NewSession(false, stub)
	defer s.svc.Assist.Close()
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	for _, how := range []string{"reopen", "close"} {
		cid := s.openLoaded("network", "dev", "")
		page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+cid+"/resources?limit=1", ""))
		p := build(t, s.do("POST", "/api/v1/contexts/"+cid+"/ai/packages", `{"kind":"resource","resourceId":"`+page.Resources[0].ID+`"}`))
		id := decode[assist.Conversation](t, s.do("POST", "/api/v1/contexts/"+cid+"/ai/conversations", startBody(p.SHA256, ""))).ID
		before := stub.cancelledCount()
		var next string
		if how == "reopen" {
			next = s.openLoaded("network", "dev", "")
		} else {
			if w := s.do("DELETE", "/api/v1/contexts/"+cid, `{}`); w.Code >= 300 {
				t.Fatalf("close context: %d %s", w.Code, w.Body)
			}
			next = s.openLoaded("network", "dev", "")
		}
		for deadline := time.Now().Add(5 * time.Second); stub.cancelledCount() == before; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the running turn was not cancelled", how)
			}
		}
		wantError(t, s.do("GET", "/api/v1/contexts/"+cid+"/ai/conversations/"+id, ""), 404, "context_closed")
		wantError(t, s.do("GET", "/api/v1/contexts/"+next+"/ai/conversations/"+id, ""), 404, "conversation_not_found")
		if _, err := s.svc.Assist.Conversation(cid, id); err == nil {
			t.Fatalf("%s: the conversation is still in memory", how)
		}
	}
}

func (a *stubAssistant) cancelledCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelled
}
