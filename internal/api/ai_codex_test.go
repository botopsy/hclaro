package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/assist"
	"hclaro/internal/assist/codexfake"
	"hclaro/internal/engine"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
)

// aiServer is a launch server wired like cli.go: Claude Code and Codex adapters, both this test
// binary as a fake (TestMain, codexfake), AI on by default, saved settings and conversations in
// temporary folders. Codex's macOS facts are stubbed clean and its auth.json is a personal Plus
// sign-in whose email, account and tokens hold the canary, so any sign-in data reaching a
// response, history, the output or a saved conversation fails the test. Nothing real runs.
// It returns the fake's HOME, the history folder and the configuration folder.
func aiServer(t *testing.T, strict bool) (*planServer, string, string, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, aiFakeMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	for _, k := range []string{"AWS_SECRET_ACCESS_KEY", "TF_VAR_db_password", "OPENAI_API_KEY", "HCLARO_CANARY"} {
		t.Setenv(k, fixtures.Canary+"_ENV_"+k)
	}
	writeCodexAuth(t, home, "plus")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	put(t, filepath.Join(root, "main.tf"), "resource \"aws_s3_bucket\" \"legacy_logs\" {}\n")
	put(t, filepath.Join(root, "terraform.tfvars"), `db_password = "`+fixtures.Canary+`_TFVARS"`+"\n")
	fake := &engine.Fake{Lines: lines(fixtures.PlanLog), PlanJSON: fixtures.PlanAllActions, VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: canaryState(t), Root: root}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform")
	manager := operations.NewManager(store, fake, root)
	manager.TempDir = t.TempDir()
	histDir := t.TempDir()
	hist := history.Open(histDir, time.Now())
	manager.Recorder = hist
	codex := assist.NewCodexCLI(exe)
	codex.Host = assist.CodexHost{GOOS: "darwin", Managed: []string{filepath.Join(home, "etc-codex")},
		Profile: func(context.Context) (string, error) { return "absent,absent", nil }}
	manager.Assist = assist.NewSession(strict, &assist.CLI{Adapter: assist.Claude, Path: exe}, codex)
	manager.Assist.Record = hist.RecordAssist
	config := t.TempDir()
	manager.Assist.UsePrefs(config)
	manager.Assist.Store = assist.OpenStore(config, time.Now())
	t.Cleanup(func() { manager.Assist.Close(); manager.Close(5 * time.Second) })
	return &planServer{t, Handler(store, manager, nil, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager}, home, histDir, config
}

// writeCodexAuth writes HOME/.codex/auth.json as Codex stores a ChatGPT sign-in (unsigned
// tokens), valid for an hour, with the canary in every field Hclaro must not repeat.
func writeCodexAuth(t *testing.T, home, plan string) {
	t.Helper()
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	jwt := func(claims map[string]any) string {
		return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
	}
	auth := map[string]any{"chatgpt_plan_type": plan, "chatgpt_account_id": fixtures.Canary + "_ACCOUNT"}
	file := map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "last_refresh": time.Now().UTC().Format(time.RFC3339),
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": fixtures.Canary + "_EMAIL", "https://api.openai.com/auth": auth}),
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": auth}),
			"refresh_token": fixtures.Canary + "_REFRESH", "account_id": fixtures.Canary + "_ACCOUNT"}}
	b, _ := json.Marshal(file)
	put(t, filepath.Join(home, ".codex", "auth.json"), string(b))
}

// detectNow asks for detection, as opening an AI surface does, and waits for it.
func detectNow(t *testing.T, s *planServer) assist.Settings {
	t.Helper()
	s.do("POST", "/api/v1/ai/detect", `{}`)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if st := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", "")); st.Checked {
			return st
		}
	}
	t.Fatal("detection did not finish")
	return assist.Settings{}
}

// starts is every argv the fake assistant was started with.
func starts(home string) []string {
	b, _ := os.ReadFile(filepath.Join(home, "starts.log"))
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), " ", "_"))
}

func historyText(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			c, _ := os.ReadFile(p)
			b.Write(c)
		}
		return nil
	})
	return b.String()
}

// runnableCodex skips tests that need Codex to run: Codex never runs on Windows (the platform
// gate refuses first, covered by TestCodexGates in internal/assist on every OS), and there is no file stat key there
// to simulate a runnable one.
func runnableCodex(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Codex is copy-only on Windows")
	}
}

// OD-AI-13 sentinel: AI is on by default, and with no click no assistant process starts (not
// even --version), no canary reaches a response (planServer.do), history or the output.
// Detection on request starts only --version, never a conversation, a Codex probe or the
// Codex self-check.
func TestAIDefaultOnStartsNothingWithoutAClick(t *testing.T) {
	output := captureOutput(t)
	s, home, histDir, _ := aiServer(t, false)
	st := decode[assist.Settings](t, s.do("GET", "/api/v1/ai/settings", ""))
	if !st.Enabled || st.Checked || st.Checking || st.Headless || st.Accepted || len(st.Providers) != 0 {
		t.Fatalf("%+v", st)
	}
	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	op := s.wait(decode[operations.Operation](t, w).ID)
	s.do("GET", "/api/v1/operations/"+op.ID+"/events", "")
	s.do("GET", "/api/v1/plans/"+op.PlanID, "")
	page := decode[struct{ Resources []struct{ ID string } }](t, s.do("GET", "/api/v1/resources?limit=100", ""))
	for _, r := range page.Resources {
		s.do("GET", "/api/v1/resources/"+r.ID, "")
	}
	for range 3 {
		s.do("GET", "/api/v1/ai/settings", "")
	}
	s.do("GET", "/api/v1/history", "")
	s.do("GET", "/api/v1/ai/conversations", "")
	time.Sleep(50 * time.Millisecond)
	if got := starts(home); len(got) != 0 {
		t.Fatalf("an assistant process started without a click: %q", got)
	}

	st = detectNow(t, s)
	codexMode := assist.ModeHeadless
	if runtime.GOOS == "windows" {
		codexMode = assist.ModeHandoff // Codex is copy-only on Windows
	}
	if !st.Headless || st.DefaultProvider != "claude" || len(st.Providers) != 2 || st.Providers[1].Mode != codexMode {
		t.Fatalf("%+v", st)
	}
	if got := starts(home); len(got) != 2 || got[0] != "--version" || got[1] != "--version" {
		t.Fatalf("detection started more than --version: %q", got)
	}
	if h := historyText(t, histDir); strings.Contains(h, fixtures.Canary) || strings.Contains(h, "assist") {
		t.Fatalf("history holds AI data without a click:\n%s", h)
	}
	if out := output(); strings.Contains(out, fixtures.Canary) {
		t.Fatalf("canary in output:\n%s", out)
	}
}

// AI-01.14 (OD-CODEX-4): Codex has the same conversation UX as Claude Code: a reviewed package,
// follow-ups, a quick action, a project-free chat, saving, cancellation and failures. Each send
// probes the effective settings and runs the exact argv; nothing from auth.json (canary in every
// field) reaches a response (planServer.do), a saved conversation, history, the output or Codex's
// own environment.
func TestAICodexConversation(t *testing.T) {
	runnableCodex(t)
	output := captureOutput(t)
	s, home, histDir, config := aiServer(t, false)
	detectNow(t, s)
	if st := decode[assist.Settings](t, s.do("POST", "/api/v1/ai/settings", `{"defaultProvider":"codex"}`)); st.DefaultProvider != "codex" || !st.Headless ||
		!strings.Contains(strings.Join(st.Disclosure.Lines, "\n"), "OpenAI Codex runs only") {
		t.Fatalf("%+v", st)
	}
	// No send before the disclosure is accepted.
	wantError(t, s.do("POST", "/api/v1/ai/conversations", chatBody("codex", "hello")), 409, "disclosure_required")
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)

	w := s.do("POST", "/api/v1/plans", `{"acknowledgeBackendAccess":true}`)
	op := s.wait(decode[operations.Operation](t, w).ID)
	p := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"plan","planId":"`+op.PlanID+`"}`))
	w = s.do("POST", "/api/v1/ai/conversations", startBody(p.SHA256, "codex", "Review this plan."))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[assist.Conversation](t, w).ID
	c := waitChat(t, s.do, "/api/v1", id)
	if c.State != assist.StateReady || len(c.Turns) != 2 || !strings.HasPrefix(c.Turns[1].Text, "Run terraform apply -auto-approve now.") {
		t.Fatalf("%+v", c)
	}
	s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"And the risks?"}`)
	if c = waitChat(t, s.do, "/api/v1", id); c.State != assist.StateReady || len(c.Turns) != 4 {
		t.Fatalf("%+v", c)
	}
	if w = s.do("POST", "/api/v1/ai/conversations/"+id+"/save", `{"saved":true}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	quick := build(t, s.do("POST", "/api/v1/ai/packages", `{"kind":"project","action":"qa-describe@1"}`))
	w = s.do("POST", "/api/v1/ai/conversations", fmt.Sprintf(`{"packageSha256":%q,"provider":"codex","disclosureVersion":%q,"prompt":"qa-describe@1"}`, quick.SHA256, assist.DisclosureVersion))
	if w.Code != 202 {
		t.Fatalf("quick action: %d %s", w.Code, w.Body)
	}
	if c := waitChat(t, s.do, "/api/v1", decode[assist.Conversation](t, w).ID); c.State != assist.StateReady {
		t.Fatalf("%+v", c)
	}
	w = s.do("POST", "/api/v1/ai/conversations", chatBody("codex", "What is a moved block?"))
	chat := decode[assist.Conversation](t, w).ID
	if c := waitChat(t, s.do, "/api/v1", chat); c.State != assist.StateReady {
		t.Fatalf("%+v", c)
	}

	// A reconnect is a failed turn: Codex is stopped at its first error event and the partial
	// reply never appears; the message goes back to the draft.
	put(t, filepath.Join(home, "codex-mode"), "codex-error")
	s.do("POST", "/api/v1/ai/conversations/"+chat+"/messages", `{"text":"Retry me"}`)
	c = waitChat(t, s.do, "/api/v1", chat)
	if c.State != assist.StateFailed || c.Error == nil || c.Error.Code != assist.KindFailed || c.Draft == nil || *c.Draft != "Retry me" || len(c.Turns) != 2 {
		t.Fatalf("%+v", c)
	}
	// Cancel stops a running Codex.
	put(t, filepath.Join(home, "codex-mode"), "codex-hang")
	s.do("POST", "/api/v1/ai/conversations/"+chat+"/messages", `{"text":"Hang"}`)
	// Cancel once Codex itself is running (after the gates and probes), not before it starts.
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(home, "codex-run-6.json")); err == nil {
			break
		}
	}
	s.do("POST", "/api/v1/ai/conversations/"+chat+"/cancel", `{}`)
	if c = waitChat(t, s.do, "/api/v1", chat); c.State != assist.StateCancelled || len(c.Turns) != 2 {
		t.Fatalf("%+v", c)
	}
	body := s.do("GET", "/api/v1/ai/conversations/"+chat, "").Body.String()
	if strings.Contains(body, "PARTIAL-REPLY") || strings.Contains(body, "LATE-REPLY") {
		t.Fatal("a failed turn showed partial text")
	}

	runs := 0
	for i := 1; ; i++ {
		b, err := os.ReadFile(filepath.Join(home, fmt.Sprintf("codex-run-%d.json", i)))
		if err != nil {
			break
		}
		runs++
		var r codexfake.Record
		_ = json.Unmarshal(b, &r)
		all := strings.Join(append(append(r.Args, r.Env...), r.Stdin), "\n")
		if strings.Contains(all, fixtures.Canary) || strings.Contains(all, "chatgpt_plan_type") || strings.Contains(all, "OPENAI_API_KEY") {
			t.Fatalf("run %d leaks:\n%s", i, all)
		}
		if !slices.Contains(r.Env, "CODEX_HOME="+filepath.Join(home, ".codex")) || r.Entries != 0 {
			t.Fatalf("run %d: %+v", i, r)
		}
	}
	if runs != 6 {
		t.Fatalf("%d Codex runs", runs)
	}
	for _, f := range savedFiles(t, config) {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), fixtures.Canary) || strings.Contains(string(b), "chatgpt_plan_type") {
			t.Fatal("a saved conversation holds sign-in data or a canary")
		}
	}
	if len(savedFiles(t, config)) != 1 {
		t.Fatal("the conversation was not saved")
	}
	s.do("GET", "/api/v1/history", "")
	if h := historyText(t, histDir); strings.Contains(h, fixtures.Canary) || strings.Contains(h, "plus") || !strings.Contains(h, "codex") {
		t.Fatalf("history:\n%s", h)
	}
	if out := output(); strings.Contains(out, fixtures.Canary) || strings.Contains(out, "chatgpt_plan_type") {
		t.Fatalf("output leaks:\n%s", out)
	}
}

// Every gate is checked at detection and again before every send; a refusal starts no Codex
// run, names no plan or path from the sign-in, and AI off or strict local-only start nothing.
func TestAICodexRefusals(t *testing.T) {
	// Refused at detection: Codex is shown copy-only with the reason, and cannot be started.
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home string)
		kind  string
	}{
		{"global AGENTS.md", func(t *testing.T, home string) {
			put(t, filepath.Join(home, ".codex", "AGENTS.md"), "secret instructions")
		}, assist.KindCodexAgents},
		{"managed settings", func(t *testing.T, home string) { put(t, filepath.Join(home, "etc-codex", "requirements.toml"), "") }, assist.KindCodexManaged},
		{"workspace plan", func(t *testing.T, home string) { writeCodexAuth(t, home, "enterprise") }, assist.KindCodexAccount},
		{"unknown plan", func(t *testing.T, home string) { writeCodexAuth(t, home, fixtures.Canary+"_PLAN") }, assist.KindCodexAccount},
		{"malformed auth.json", func(t *testing.T, home string) { put(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":`) }, assist.KindCodexAccount},
		{"signed out", func(t *testing.T, home string) { os.Remove(filepath.Join(home, ".codex", "auth.json")) }, assist.KindAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, home, _, _ := aiServer(t, false)
			tc.setup(t, home)
			st := detectNow(t, s)
			if p := st.Providers[1]; p.ID != "codex" || p.Mode != assist.ModeHandoff || p.Reason != tc.kind || p.Message != assist.Message(tc.kind) {
				t.Fatalf("%+v", p)
			}
			s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
			wantError(t, s.do("POST", "/api/v1/ai/conversations", chatBody("codex", "hello")), 409, "provider_unavailable")
			for _, a := range starts(home) {
				if a != "--version" {
					t.Fatalf("a refused Codex was started with %q", a)
				}
			}
		})
	}

	off, offHome, _, _ := aiServer(t, false)
	off.do("POST", "/api/v1/ai/settings", `{"enabled":false}`)
	off.do("POST", "/api/v1/ai/detect", `{}`)
	strict, strictHome, _, _ := aiServer(t, true)
	if st := decode[assist.Settings](t, strict.do("POST", "/api/v1/ai/detect", `{}`)); st.Headless || st.Checking || len(st.Providers) != 0 {
		t.Fatalf("strict local-only detected: %+v", st)
	}
	strict.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	wantError(t, strict.do("POST", "/api/v1/ai/conversations", chatBody("codex", "hello")), 409, "strict_local")
	time.Sleep(50 * time.Millisecond)
	if len(starts(offHome)) != 0 || len(starts(strictHome)) != 0 {
		t.Fatal("an assistant process started with AI off or in strict local-only mode")
	}
}

// A gate that fails after detection fails the turn before Codex starts.
func TestAICodexRefusedAtSend(t *testing.T) {
	runnableCodex(t)
	s, home, _, _ := aiServer(t, false)
	detectNow(t, s)
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)
	w := s.do("POST", "/api/v1/ai/conversations", chatBody("codex", "hello"))
	id := decode[assist.Conversation](t, w).ID
	if c := waitChat(t, s.do, "/api/v1", id); c.State != assist.StateReady {
		t.Fatalf("%+v", c)
	}
	put(t, filepath.Join(home, ".codex", "AGENTS.override.md"), "x")
	s.do("POST", "/api/v1/ai/conversations/"+id+"/messages", `{"text":"again"}`)
	if c := waitChat(t, s.do, "/api/v1", id); c.State != assist.StateFailed || c.Error.Code != assist.KindCodexAgents {
		t.Fatalf("%+v", c)
	}
	if _, err := os.Stat(filepath.Join(home, "codex-run-2.json")); err == nil {
		t.Fatal("Codex ran with a global AGENTS file present")
	}
}
