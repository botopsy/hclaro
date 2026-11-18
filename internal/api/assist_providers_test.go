package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/engine"
)

// AI-01.05: the context workflow does not depend on which assistant is installed. With only
// handoff-only assistants, or none, every kind builds and exports, and Send never starts one.

type providerSet struct {
	name     string
	claude   *stubAssistant // nil: not registered
	handoffs []*stubAssistant
}

func providerSets() []providerSet {
	codex := func() *stubAssistant { return &stubAssistant{id: "codex", mode: assist.ModeHandoff} }
	copilot := func() *stubAssistant { return &stubAssistant{id: "copilot", mode: assist.ModeHandoff} }
	return []providerSet{
		{name: "codex only", claude: &stubAssistant{id: "claude", mode: assist.ModeUnsupported}, handoffs: []*stubAssistant{codex()}},
		{name: "copilot only", claude: &stubAssistant{id: "claude", mode: assist.ModeUnsupported}, handoffs: []*stubAssistant{copilot()}},
		{name: "claude absent, codex and copilot", claude: &stubAssistant{id: "claude", mode: assist.ModeUnsupported}, handoffs: []*stubAssistant{codex(), copilot()}},
		{name: "none installed"},
		{name: "claude installed", claude: &stubAssistant{id: "claude", mode: assist.ModeHeadless, reply: "ok"}, handoffs: []*stubAssistant{codex()}},
	}
}

func (ps providerSet) session() *assist.Session {
	var all []assist.Assistant
	if ps.claude != nil {
		all = append(all, ps.claude)
	}
	for _, h := range ps.handoffs {
		all = append(all, h)
	}
	return assist.NewSession(false, all...)
}

type doFunc func(method, path, body string) *httptest.ResponseRecorder

// checkProviderSet builds and exports each kind under prefix, checks Settings and that Send
// (a conversation) reaches only a headless-eligible Claude.
func checkProviderSet(t *testing.T, ps providerSet, do doFunc, prefix string, requests map[string]string) {
	t.Helper()
	do("POST", "/api/v1/ai/settings", `{"enabled":true}`)
	if w := do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	headless := ps.claude != nil && ps.claude.mode == assist.ModeHeadless
	st := decode[assist.Settings](t, do("GET", "/api/v1/ai/settings", ""))
	if !st.Handoff || st.Headless != headless || (st.DefaultProvider == "claude") != headless || (!headless && st.DefaultProvider != "") {
		t.Fatalf("settings %+v", st)
	}
	// The Anthropic billing line is shown only when Claude Code can be sent requests.
	anthropic := func(d assist.Disclosure) bool { return strings.Contains(strings.Join(d.Lines, "\n"), "Anthropic") }
	if anthropic(st.Disclosure) != headless || st.Disclosure.Version != assist.DisclosureVersion {
		t.Fatalf("settings disclosure %q", st.Disclosure.Lines)
	}
	if w := do("POST", "/api/v1/ai/settings", `{"defaultProvider":"codex"}`); w.Code != 409 {
		t.Fatalf("handoff-only assistant became the default: %d %s", w.Code, w.Body)
	}
	var last builtPackage
	for kind, body := range requests {
		p := build(t, do("POST", prefix+"/ai/packages", body))
		if p.Kind != kind || anthropic(p.Disclosure) != headless {
			t.Fatalf("%s built %s", kind, p.Kind)
		}
		exportSame(t, do("POST", prefix+"/ai/packages/"+p.SHA256+"/export", `{}`), p)
		last = p
	}
	for _, h := range ps.handoffs {
		wantError(t, do("POST", prefix+"/ai/conversations", startBody(last.SHA256, h.id, "")), 409, "provider_unavailable")
	}
	claudeRun := do("POST", prefix+"/ai/conversations", startBody(last.SHA256, "claude", ""))
	if headless {
		if claudeRun.Code != 202 {
			t.Fatalf("claude run: %d %s", claudeRun.Code, claudeRun.Body)
		}
	} else {
		wantError(t, claudeRun, 409, "provider_unavailable")
	}
	for _, h := range ps.handoffs {
		if h.ran() != 0 {
			t.Fatalf("handoff-only %s was run", h.id)
		}
	}
	if ps.claude != nil && !headless && ps.claude.ran() != 0 {
		t.Fatal("missing claude was run")
	}
}

func TestAIHandoffWithoutClaude(t *testing.T) {
	for _, ps := range providerSets() {
		t.Run(ps.name, func(t *testing.T) {
			s, _ := newDriftServer(t, &engine.Fake{Lines: lines(fixtures.DriftLog), PlanJSON: fixtures.DriftMixed})
			s.manager.Assist = ps.session()
			s.enableAI()
			_, scan := startScan(t, s)
			page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/resources?limit=1", ""))
			checkProviderSet(t, ps, s.do, "/api/v1", map[string]string{
				assist.KindPlan:     `{"kind":"plan","planId":"` + s.plan() + `"}`,
				assist.KindDrift:    `{"kind":"drift","scanId":"` + scan.ID + `"}`,
				assist.KindResource: `{"kind":"resource","resourceId":"` + page.Resources[0].ID + `"}`,
				assist.KindImport:   `{"kind":"import","import":{"address":"aws_s3_bucket.missing","id":"bucket-x","file":"imports.tf"}}`,
			})
		})
	}
}

func TestAIHubHandoffWithoutClaude(t *testing.T) {
	for _, ps := range providerSets() {
		t.Run(ps.name, func(t *testing.T) {
			s := newScopedServer(t)
			s.launch.Assist = ps.session()
			do := func(method, path, body string) *httptest.ResponseRecorder { return s.do(method, path, body) }
			do("POST", "/api/v1/ai/settings", `{"enabled":true}`)
			a := s.openLoaded("network", "dev", "")
			done := s.waitScoped(a, s.plan(a).ID)
			scan := s.scan(a)
			page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/contexts/"+a+"/resources?limit=1", ""))
			checkProviderSet(t, ps, do, "/api/v1/contexts/"+a, map[string]string{
				assist.KindPlan:     `{"kind":"plan","planId":"` + done.PlanID + `"}`,
				assist.KindDrift:    `{"kind":"drift","scanId":"` + scan.ID + `"}`,
				assist.KindResource: `{"kind":"resource","resourceId":"` + page.Resources[0].ID + `"}`,
				assist.KindImport:   `{"kind":"import","import":{"address":"aws_s3_bucket.missing","id":"bucket-x","file":"imports.tf"}}`,
			})
		})
	}
}
