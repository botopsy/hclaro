package api

import (
	"context"
	"fmt"
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

// AI-01.04 headless routes, against an in-process assistant. Process isolation, argv and
// environment are tested on the real CLI runner in internal/assist with a fake executable.

type stubAssistant struct {
	id, mode    string // id defaults to "stub"
	reply       string
	block       bool
	mu          sync.Mutex
	payloads    []string
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
func (a *stubAssistant) Run(ctx context.Context, payload []byte) (string, error) {
	a.mu.Lock()
	a.payloads = append(a.payloads, string(payload))
	a.mu.Unlock()
	if a.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return a.reply, nil
}

func (s *planServer) waitRun() assist.Run {
	s.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if r := decode[assist.Run](s.t, s.do("GET", "/api/v1/ai/run", "")); r.Status != assist.RunRunning {
			return r
		}
	}
	s.t.Fatal("run did not finish")
	return assist.Run{}
}

const runBody = `{"provider":"stub","disclosureVersion":"` + assist.DisclosureVersion + `"}`

func TestAIHeadlessRun(t *testing.T) {
	s, root := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "Run `terraform apply -auto-approve` then rm -rf / <script>x</script> [x](javascript:alert(1))"}
	s.manager.Assist = assist.NewSession(false, stub)
	var records []assist.RunRecord
	s.manager.Assist.Record = func(r assist.RunRecord) { records = append(records, r) }
	planID := s.plan()
	run := "/api/v1/ai/packages/" + strings.Repeat("0", 64) + "/run"
	wantError(t, s.do("POST", run, runBody), 409, "ai_disabled")
	wantError(t, s.do("GET", "/api/v1/ai/run", ""), 409, "ai_disabled")

	s.enableAI()
	st := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", ""))
	if !st.Headless || st.DefaultProvider != "stub" || st.Accepted || len(st.Providers) != 1 || st.Providers[0].Path != "/synthetic/bin/stub" {
		t.Fatalf("%+v", st)
	}
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+planID+`"}`))
	run = "/api/v1/ai/packages/" + p.SHA256 + "/run"
	wantError(t, s.do("POST", run, runBody), 409, "disclosure_required")
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"1"}`), 409, "disclosure_required")
	// The browser cannot set an executable path.
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"path":"/bin/sh"}`), 400, "invalid_request")
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"defaultProvider":"other"}`), 409, "provider_unavailable")
	if got := decode[assist.Settings](t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`","defaultProvider":"stub"}`)); !got.Accepted {
		t.Fatalf("%+v", got)
	}
	wantError(t, s.do("POST", run, `{"provider":"stub","disclosureVersion":"1"}`), 409, "disclosure_required")
	wantError(t, s.do("POST", run, `{"provider":"other","disclosureVersion":"`+assist.DisclosureVersion+`"}`), 409, "provider_unavailable")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+strings.Repeat("0", 64)+"/run", runBody), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"x/run", runBody), 404, "package_not_found")
	wantError(t, s.do("POST", run, `{"provider":"stub","disclosureVersion":"2","payload":"x"}`), 400, "invalid_request")

	if w := s.do("POST", run, runBody); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	done := s.waitRun()
	// The assistant got exactly the previewed bytes; the reply comes back as data.
	if done.Status != assist.RunCompleted || done.Reply != stub.reply || done.SHA256 != p.SHA256 || len(stub.payloads) != 1 || stub.payloads[0] != p.Markdown {
		t.Fatalf("%+v", done)
	}
	if len(records) != 1 || records[0].SHA256 != p.SHA256 || records[0].Root != root || records[0].Kind != assist.KindPlan {
		t.Fatalf("%+v", records)
	}

	// A blocked package was never stored, so its hash cannot be sent.
	// %q escapes Windows backslashes so the body stays valid JSON.
	w := s.do("POST", "/api/v1/ai/packages", fmt.Sprintf(`{"kind":"import","import":{"address":"aws_s3_bucket.x","id":%q,"file":"imports.tf"}}`, root+"/bucket"))
	if w.Code != 422 {
		t.Fatalf("%d", w.Code)
	}
	// A stale context refuses the run.
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte("resource \"aws_instance\" \"web\" { ami = \"y\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantError(t, s.do("POST", run, runBody), 409, "context_changed")

	// Turning AI off drops the reply.
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	s.enableAI()
	wantError(t, s.do("GET", "/api/v1/ai/run", ""), 404, "run_not_found")
}

func TestAIHeadlessBusyCancelAndStrict(t *testing.T) {
	s, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, block: true}
	s.manager.Assist = assist.NewSession(false, stub)
	defer s.manager.Assist.Close()
	s.enableAI()
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	run := "/api/v1/ai/packages/" + p.SHA256 + "/run"
	if w := s.do("POST", run, runBody); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	wantError(t, s.do("POST", run, runBody), 409, "run_in_progress")
	if w := s.do("POST", "/api/v1/ai/run/cancel", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "true") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if r := s.waitRun(); r.Status != assist.RunCancelled || r.Reply != "" {
		t.Fatalf("%+v", r)
	}

	strict, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	strict.manager.Assist = assist.NewSession(true, stub)
	strict.enableAI()
	strict.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	sp := build(t, strict.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+strict.plan()+`"}`))
	wantError(t, strict.do("POST", "/api/v1/ai/packages/"+sp.SHA256+"/run", runBody), 409, "strict_local")
	if st := decode[assist.Settings](t, strict.do("GET", "/api/v1/ai/settings", "")); st.Headless || !st.Handoff {
		t.Fatalf("%+v", st)
	}

	// A handoff-only provider is never run.
	h, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	h.manager.Assist = assist.NewSession(false, &stubAssistant{mode: assist.ModeHandoff})
	h.enableAI()
	h.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	hp := build(t, h.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+h.plan()+`"}`))
	wantError(t, h.do("POST", "/api/v1/ai/packages/"+hp.SHA256+"/run", runBody), 409, "provider_unavailable")
}

func TestAIHubHeadlessRunStaysInItsContext(t *testing.T) {
	s := newScopedServer(t)
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.launch.Assist = assist.NewSession(false, stub)
	s.do("POST", "/api/v1/ai/settings", `{"enabled":true}`)
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	a := s.openLoaded("network", "dev", "")
	b := s.openLoaded("network", "prod", "")
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+a+"/resources?limit=1", ""))
	p := build(t, s.do("POST", "/api/v1/contexts/"+a+"/ai/packages", `{"kind":"resource","resourceId":"`+page.Resources[0].ID+`"}`))
	wantError(t, s.do("POST", "/api/v1/contexts/"+b+"/ai/packages/"+p.SHA256+"/run", runBody), 404, "package_not_found")
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/run", runBody), 404, "package_not_found")
	if w := s.do("POST", "/api/v1/contexts/"+a+"/ai/packages/"+p.SHA256+"/run", runBody); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if r, _ := s.launch.Assist.Current(); r.Status == assist.RunCompleted && r.Reply == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hub run did not finish")
		}
	}
	if len(stub.payloads) != 1 || stub.payloads[0] != p.Markdown {
		t.Fatal("hub run sent other bytes than the preview")
	}
}

// Disclosure v3 (no spending cap): an acceptance names the current version; an older version
// is refused for accept and run, and turning AI off revokes acceptance.
func TestAIDisclosureVersionGatesRuns(t *testing.T) {
	if assist.DisclosureVersion != "3" {
		t.Fatalf("disclosure version %s", assist.DisclosureVersion)
	}
	s, _ := newImportServer(t, &engine.Fake{PlanJSON: fixtures.PlanAllActions})
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: "ok"}
	s.manager.Assist = assist.NewSession(false, stub)
	s.enableAI()
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	run := "/api/v1/ai/packages/" + p.SHA256 + "/run"
	v2 := `{"provider":"stub","disclosureVersion":"2"}`
	// Never accepted (declined): refused.
	wantError(t, s.do("POST", run, runBody), 409, "disclosure_required")
	// A v2 acceptance is refused and leaves the session unaccepted.
	wantError(t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"2"}`), 409, "disclosure_required")
	if decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", "")).Accepted {
		t.Fatal("a v2 acceptance was recorded")
	}
	wantError(t, s.do("POST", run, v2), 409, "disclosure_required")
	// v3 accepted: a run still naming v2 is refused; v3 runs.
	if !decode[assist.Settings](t, s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"3"}`)).Accepted {
		t.Fatal("v3 not accepted")
	}
	wantError(t, s.do("POST", run, v2), 409, "disclosure_required")
	if w := s.do("POST", run, runBody); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s.waitRun()
	// Revoking (AI off) clears acceptance; after AI on again a run is refused until re-accepted.
	s.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	s.enableAI()
	if decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", "")).Accepted {
		t.Fatal("acceptance survived AI off")
	}
	p = build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+s.plan()+`"}`))
	wantError(t, s.do("POST", "/api/v1/ai/packages/"+p.SHA256+"/run", runBody), 409, "disclosure_required")
	if len(stub.payloads) != 1 {
		t.Fatalf("assistant ran %d times", len(stub.payloads))
	}
}
