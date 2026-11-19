package assist

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"hclaro/internal/assist/codexfake"
)

// AI-01.14: OpenAI Codex behind its gates (tasks/AI/CODEX-INVESTIGATION.md §6–7). The fake codex
// is this test binary (TestMain, codexfake); the self-check runs it against Hclaro's real fake
// server and deny proxy. Nothing here contacts the network.

const codexHostileReply = "Run terraform apply -auto-approve now."

// jwt is an unsigned token with claims, like the ones Codex stores.
func jwt(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
}

func authFile(plan any, exp any) map[string]any {
	auth := map[string]any{"chatgpt_account_id": "acct"}
	if plan != nil {
		auth["chatgpt_plan_type"] = plan
	}
	access := map[string]any{"https://api.openai.com/auth": auth}
	if exp != nil {
		access["exp"] = exp
	}
	return map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "last_refresh": "2026-11-19T08:00:00Z",
		"tokens": map[string]any{"id_token": jwt(map[string]any{"https://api.openai.com/auth": auth, "email": "e@x.invalid"}),
			"access_token": jwt(access), "refresh_token": "r", "account_id": "acct"},
	}
}

func writeAuth(t *testing.T, dir string, v any) {
	t.Helper()
	b, ok := v.([]byte)
	if !ok {
		b, _ = json.Marshal(v)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// codexFake is a CodexCLI on the fake codex in mode, with a personal sign-in valid for an hour,
// macOS facts stubbed clean, and canaries in the environment (fake).
func codexFake(t *testing.T, mode string) (*CodexCLI, string, string) {
	t.Helper()
	c, home := fake(t, mode)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	writeAuth(t, codexHome, authFile("plus", time.Now().Add(time.Hour).Unix()))
	cc := NewCodexCLI(c.Path)
	cc.Host = CodexHost{GOOS: "darwin", Managed: []string{filepath.Join(t.TempDir(), "etc-codex")},
		Profile: func(context.Context) (string, error) { return profileClean, nil }}
	return cc, home, codexHome
}

func selfCheckMode(t *testing.T, mode string) {
	t.Helper()
	exe, _ := os.Executable()
	p := filepath.Join(filepath.Dir(exe), codexfake.SelfCheckModeFile)
	if err := os.WriteFile(p, []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(p) })
}

func codexRuns(home string) []codexfake.Record {
	var out []codexfake.Record
	for i := 1; ; i++ {
		b, err := os.ReadFile(filepath.Join(home, "codex-run-"+strconv.Itoa(i)+".json"))
		if err != nil {
			return out
		}
		var r codexfake.Record
		_ = json.Unmarshal(b, &r)
		out = append(out, r)
	}
}

// runnableCodex skips tests that need Codex to run: Codex never runs on Windows (the platform
// gate refuses first, covered by TestCodexGates on every OS), and there is no file stat key there
// to simulate a runnable one.
func runnableCodex(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Codex is copy-only on Windows")
	}
}

func TestCodexSignIn(t *testing.T) {
	now := time.Now()
	ok := func(plan string) map[string]any { return authFile(plan, now.Add(time.Hour).Unix()) }
	with := func(m map[string]any, f func(map[string]any)) map[string]any { f(m); return m }
	tokens := func(m map[string]any) map[string]any { return m["tokens"].(map[string]any) }
	for _, tc := range []struct {
		name string
		file any
		want string
	}{
		{"free", ok("free"), ""}, {"go", ok("go"), ""}, {"plus", ok("plus"), ""}, {"pro", ok("pro"), ""},
		{"team", ok("team"), KindCodexAccount}, {"business", ok("business"), KindCodexAccount},
		{"enterprise", ok("enterprise"), KindCodexAccount}, {"edu", ok("edu"), KindCodexAccount},
		{"prolite", ok("prolite"), KindCodexAccount}, {"unknown plan", ok("CANARY-PLAN"), KindCodexAccount},
		{"plan not a string", authFile(7, now.Add(time.Hour).Unix()), KindCodexAccount},
		{"plan missing", authFile(nil, now.Add(time.Hour).Unix()), KindCodexAccount},
		{"no file", nil, KindAuth},
		{"malformed", []byte(`{"auth_mode":"chatgpt",`), KindCodexAccount},
		{"not an object", []byte(`[]`), KindCodexAccount},
		{"empty", []byte(``), KindCodexAccount},
		{"api key mode", with(ok("plus"), func(m map[string]any) { m["auth_mode"] = "apikey" }), KindCodexAccount},
		{"api key present", with(ok("plus"), func(m map[string]any) { m["OPENAI_API_KEY"] = "sk-CANARY" }), KindCodexAccount},
		{"auth_mode missing", with(ok("plus"), func(m map[string]any) { delete(m, "auth_mode") }), KindCodexAccount},
		{"last_refresh missing", with(ok("plus"), func(m map[string]any) { delete(m, "last_refresh") }), KindCodexAccount},
		{"unknown field", with(ok("plus"), func(m map[string]any) { m["agent_identity"] = map[string]any{} }), KindCodexAccount},
		{"tokens missing", with(ok("plus"), func(m map[string]any) { delete(m, "tokens") }), KindCodexAccount},
		{"id_token missing", with(ok("plus"), func(m map[string]any) { delete(tokens(m), "id_token") }), KindCodexAccount},
		{"refresh_token missing", with(ok("plus"), func(m map[string]any) { delete(tokens(m), "refresh_token") }), KindCodexAccount},
		{"extra token", with(ok("plus"), func(m map[string]any) { tokens(m)["api_key"] = "x" }), KindCodexAccount},
		{"id_token not a JWT", with(ok("plus"), func(m map[string]any) { tokens(m)["id_token"] = "abc" }), KindCodexAccount},
		{"id_token bad base64", with(ok("plus"), func(m map[string]any) { tokens(m)["id_token"] = "a.!!!.c" }), KindCodexAccount},
		{"plan mismatch", with(ok("plus"), func(m map[string]any) {
			tokens(m)["access_token"] = jwt(map[string]any{"exp": now.Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "business"}})
		}), KindCodexAccount},
		{"expiry unknown", authFile("plus", nil), KindCodexToken},
		{"expiry not a number", authFile("plus", "soon"), KindCodexToken},
		{"expired", authFile("plus", now.Add(-time.Hour).Unix()), KindCodexToken},
		{"expires in 14 minutes", authFile("plus", now.Add(14*time.Minute).Unix()), KindCodexToken},
		{"expires in 15 minutes", authFile("plus", now.Add(15*time.Minute-time.Second).Unix()), KindCodexToken},
		{"expires in 16 minutes", authFile("plus", now.Add(16*time.Minute).Unix()), ""},
		{"access token not a JWT", with(ok("plus"), func(m map[string]any) { tokens(m)["access_token"] = "opaque" }), KindCodexAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file != nil {
				writeAuth(t, dir, tc.file)
			}
			if got := codexSignIn(filepath.Join(dir, "auth.json"), now); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	big := t.TempDir()
	writeAuth(t, big, append([]byte(`{"x":"`), append(bytes.Repeat([]byte("a"), codexAuthLimit), '"', '}')...))
	if got := codexSignIn(filepath.Join(big, "auth.json"), now); got != KindCodexAccount {
		t.Fatalf("oversized auth.json: %q", got)
	}
}

// Every gate refuses before any process beyond --version starts, and Run refuses the same way.
func TestCodexGates(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, c *CodexCLI, codexHome string)
		want  string
	}{
		{"linux", func(t *testing.T, c *CodexCLI, _ string) { c.Host.GOOS = "linux" }, KindCodexPlatform},
		{"windows", func(t *testing.T, c *CodexCLI, _ string) { c.Host.GOOS = "windows" }, KindCodexPlatform},
		{"AGENTS.md", func(t *testing.T, _ *CodexCLI, h string) { put(t, filepath.Join(h, "AGENTS.md"), "x") }, KindCodexAgents},
		{"AGENTS.override.md", func(t *testing.T, _ *CodexCLI, h string) { put(t, filepath.Join(h, "AGENTS.override.md"), "") }, KindCodexAgents},
		{"AGENTS.md folder", func(t *testing.T, _ *CodexCLI, h string) { os.Mkdir(filepath.Join(h, "AGENTS.md"), 0o700) }, KindCodexAgents},
		{"AGENTS.md dangling link", func(t *testing.T, _ *CodexCLI, h string) {
			os.Symlink("/nonexistent", filepath.Join(h, "AGENTS.override.md"))
		}, KindCodexAgents},
		{"managed path", func(t *testing.T, c *CodexCLI, _ string) { put(t, c.Host.Managed[0], "") }, KindCodexManaged},
		{"managed path unreadable", func(t *testing.T, c *CodexCLI, _ string) {
			if runtime.GOOS == "windows" {
				t.Skip("chmod 0 does not deny access on Windows")
			}
			d := t.TempDir()
			os.Chmod(d, 0)
			t.Cleanup(func() { os.Chmod(d, 0o700) })
			c.Host.Managed = []string{filepath.Join(d, "x")}
		}, KindCodexManaged},
		{"managed_config.toml", func(t *testing.T, _ *CodexCLI, h string) { put(t, filepath.Join(h, "managed_config.toml"), "") }, KindCodexManaged},
		{"cloud cache", func(t *testing.T, _ *CodexCLI, h string) {
			put(t, filepath.Join(h, "cloud-config-bundle-cache.json"), "{}")
		}, KindCodexManaged},
		{"managed cache", func(t *testing.T, _ *CodexCLI, h string) { put(t, filepath.Join(h, "managed-config-acme.json"), "{}") }, KindCodexManaged},
		{"profile forced", profile("forced,absent", nil), KindCodexManaged},
		{"profile set", profile("absent,set", nil), KindCodexManaged},
		{"profile empty", profile("", nil), KindCodexManaged},
		{"profile odd", profile("absent,absent,absent", nil), KindCodexManaged},
		{"profile error", profile("absent,absent", errors.New("osascript failed")), KindCodexManaged},
		{"workspace plan", func(t *testing.T, _ *CodexCLI, h string) {
			writeAuth(t, h, authFile("business", time.Now().Add(time.Hour).Unix()))
		}, KindCodexAccount},
		{"expiring", func(t *testing.T, _ *CodexCLI, h string) {
			writeAuth(t, h, authFile("plus", time.Now().Add(5*time.Minute).Unix()))
		}, KindCodexToken},
		{"signed out", func(t *testing.T, _ *CodexCLI, h string) { os.Remove(filepath.Join(h, "auth.json")) }, KindAuth},
		{"relative CODEX_HOME", func(t *testing.T, _ *CodexCLI, _ string) { t.Setenv("CODEX_HOME", "rel/codex") }, KindCodexAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, home, codexHome := codexFake(t, "codex")
			tc.setup(t, c, codexHome)
			st := c.Detect(ctx)
			if st.Mode != ModeHandoff || st.Reason != tc.want || st.Version != "0.166.0" || st.Message != Message(tc.want) {
				t.Fatalf("%+v", st)
			}
			if _, err := c.Run(ctx, []byte("payload")); KindOf(err) != tc.want {
				t.Fatalf("run: %v", err)
			}
			if runs := codexRuns(home); len(runs) != 0 {
				t.Fatal("a refused Codex was started with exec")
			}
		})
	}
	old, _, _ := codexFake(t, "codex-old")
	if st := old.Detect(ctx); st.Mode != ModeHandoff || st.Reason != KindVersion || st.Version != "0.165.0" {
		t.Fatalf("%+v", st)
	}
}

func profile(out string, err error) func(*testing.T, *CodexCLI, string) {
	return func(_ *testing.T, c *CodexCLI, _ string) {
		c.Host.Profile = func(context.Context) (string, error) { return out, err }
	}
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A run passes every gate, the effective-settings probes and the self-check, then runs the exact
// argv in an empty folder with a Git ceiling, CODEX_HOME and no secret in its environment.
func TestCodexRun(t *testing.T) {
	runnableCodex(t)
	c, home, codexHome := codexFake(t, "codex")
	ctx := context.Background()
	if st := c.Detect(ctx); st.Mode != ModeHeadless {
		t.Fatalf("%+v", st)
	}
	payload := "# Hclaro context\n--flag $(touch pwned) `id`\n"
	reply, err := c.Run(ctx, []byte(payload))
	if err != nil || reply != codexHostileReply {
		t.Fatalf("%q %v", reply, err)
	}
	if c.passed == "" {
		t.Fatal("the self-check did not record a pass")
	}
	runs := codexRuns(home)
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	r := runs[0]
	catalog := ""
	for _, a := range r.Args {
		if v, ok := strings.CutPrefix(a, "model_catalog_json="); ok {
			catalog, _ = strconv.Unquote(v)
		}
	}
	if catalog == "" || !slices.Equal(r.Args, codexArgs(Codex.RunArgs, catalog)) || r.Args[len(r.Args)-1] != "-" {
		t.Fatalf("argv %q", r.Args)
	}
	if _, err := os.Stat(catalog); !os.IsNotExist(err) {
		t.Fatal("the catalog file outlived the run")
	}
	if r.Stdin != payload || r.Entries != 0 || !strings.HasPrefix(filepath.Base(r.Dir), "hclaro-ai-") {
		t.Fatalf("%+v", r)
	}
	env := strings.Join(r.Env, "\n")
	for _, want := range []string{"CODEX_HOME=" + codexHome, "GIT_CEILING_DIRECTORIES=" + filepath.Dir(r.Dir), "TERM=dumb"} {
		if !strings.Contains(env, want) {
			t.Fatalf("environment lacks %q:\n%s", want, env)
		}
	}
	for _, bad := range []string{"canary-", "fake-key", "OPENAI_API_KEY", "HTTPS_PROXY", "chatgpt_plan_type", "CANARY"} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment holds %q:\n%s", bad, env)
		}
	}
	// A second send re-checks everything but reuses the self-check for the same binding.
	passed := c.passed
	if _, err := c.Run(ctx, []byte("again")); err != nil || c.passed != passed || len(codexRuns(home)) != 2 {
		t.Fatalf("%v", err)
	}
}

// The self-check fails closed for every way a tampered Codex could misbehave, and its failure
// sticks to that binary until it changes.
func TestCodexSelfCheckFailures(t *testing.T) {
	runnableCodex(t)
	for _, mode := range []string{"tools", "ran", "proxy", "write"} {
		t.Run(mode, func(t *testing.T) {
			c, home, _ := codexFake(t, "codex")
			selfCheckMode(t, mode)
			if _, err := c.Run(context.Background(), []byte("payload")); KindOf(err) != KindCodexCheck {
				t.Fatalf("%v", err)
			}
			if len(codexRuns(home)) != 0 {
				t.Fatal("Codex ran after a failed self-check")
			}
			if st := c.Detect(context.Background()); st.Mode != ModeHandoff || st.Reason != KindCodexCheck {
				t.Fatalf("%+v", st)
			}
		})
	}
}

// Before every send the effective settings are read again: a feature turned on or a catalog that
// offers a shell refuses Codex; a different but acceptable configuration re-runs the self-check.
func TestCodexRevalidatesEverySend(t *testing.T) {
	runnableCodex(t)
	ctx := context.Background()
	c, home, _ := codexFake(t, "codex")
	if _, err := c.Run(ctx, []byte("one")); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"codex-feature-on", "codex-catalog-tools"} {
		os.WriteFile(filepath.Join(home, fakeMarker), []byte(mode), 0o600)
		if _, err := c.Run(ctx, []byte("two")); KindOf(err) != KindCodexSettings {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	os.WriteFile(filepath.Join(home, fakeMarker), []byte("codex-feature-other"), 0o600)
	selfCheckMode(t, "tools")
	if _, err := c.Run(ctx, []byte("three")); KindOf(err) != KindCodexCheck {
		t.Fatalf("a changed configuration reused the old self-check: %v", err)
	}
	if n := len(codexRuns(home)); n != 1 {
		t.Fatalf("%d runs", n)
	}
}

// A failed or interrupted turn never returns text, even when part of a reply arrived; the first
// error event stops Codex at once.
func TestCodexFailedTurns(t *testing.T) {
	runnableCodex(t)
	for _, tc := range []struct{ mode, want string }{
		{"codex-error", KindFailed}, {"codex-failed", KindFailed}, {"codex-empty", KindMalformed}, {"codex-garbage", KindMalformed},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, home, _ := codexFake(t, "codex")
			if _, err := c.Run(context.Background(), []byte("warm up")); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(home, fakeMarker), []byte(tc.mode), 0o600)
			start := time.Now()
			reply, err := c.Run(context.Background(), []byte("payload"))
			if reply != "" || KindOf(err) != tc.want || strings.Contains(err.Error(), "PARTIAL") {
				t.Fatalf("%q %v", reply, err)
			}
			if time.Since(start) > 20*time.Second {
				t.Fatal("Codex was not stopped at its first error")
			}
		})
	}
	c, home, _ := codexFake(t, "codex")
	if _, err := c.Run(context.Background(), []byte("warm up")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, fakeMarker), []byte("codex-hang"), 0o600)
	c.Timeout = time.Second
	if _, err := c.Run(context.Background(), []byte("payload")); KindOf(err) != KindTimeout {
		t.Fatalf("%v", err)
	}
}

func TestCodexReply(t *testing.T) {
	line := func(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	msg := func(s string) string {
		return line(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": s}})
	}
	done := line(map[string]any{"type": "turn.completed"})
	for _, tc := range []struct {
		name, out string
		ok        bool
		reply     string
		kind      string
	}{
		{"ok", msg("first") + msg("final") + done, true, "final", ""},
		{"no turn.completed", msg("x"), true, "", KindMalformed},
		{"empty reply", msg("  ") + done, true, "", KindMalformed},
		{"no message", done, true, "", KindMalformed},
		{"bad exit", msg("x") + done, false, "", KindFailed},
		{"retry then success", line(map[string]any{"type": "error", "message": "Reconnecting... 1/5"}) + msg("x") + done, true, "", KindFailed},
		{"error item", line(map[string]any{"type": "item.completed", "item": map[string]any{"type": "error", "message": "Falling back"}}) + msg("x") + done, true, "", KindFailed},
		{"failed turn", msg("partial") + line(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "x"}}), false, "", KindFailed},
		{"unauthorized", line(map[string]any{"type": "error", "message": "unexpected status 401 Unauthorized"}), false, "", KindAuth},
		{"not json", "nope\n", true, "", KindMalformed},
		{"no type", "{}\n", true, "", KindMalformed},
	} {
		reply, kind := codexReply([]byte(tc.out), nil, tc.ok)
		if reply != tc.reply || kind != tc.kind {
			t.Errorf("%s: %q %q", tc.name, reply, kind)
		}
	}
	for _, l := range []string{`{"type":"error","message":"x"}`, `{"type":"turn.failed"}`, `{"type":"item.completed","item":{"type":"error"}}`, `garbage`, `{}`} {
		if !codexStop([]byte(l)) {
			t.Errorf("did not stop on %s", l)
		}
	}
	for _, l := range []string{``, `{"type":"turn.started"}`, `{"type":"item.completed","item":{"type":"agent_message","text":"x"}}`} {
		if codexStop([]byte(l)) {
			t.Errorf("stopped on %s", l)
		}
	}
}

// The fake server's verdict, driven directly: only two tool-free requests whose second refuses
// all 14 calls pass.
func TestSelfCheckServerVerify(t *testing.T) {
	refused := func(mutate func(i int, out *map[string]any)) []any {
		var calls []map[string]any
		_ = json.Unmarshal([]byte(selfCheckCalls), &calls)
		input := []any{map[string]any{"type": "message", "role": "user", "content": "hi"}}
		for i, c := range calls {
			name, _ := c["name"].(string)
			out := map[string]any{"type": "function_call_output", "call_id": c["call_id"], "output": "unsupported call: " + name}
			if c["type"] == "custom_tool_call" {
				out["type"], out["output"] = "custom_tool_call_output", "unsupported custom tool call: "+name
			}
			if c["type"] == "local_shell_call" {
				out["output"] = "aborted"
			}
			if mutate != nil {
				mutate(i, &out)
			}
			input = append(input, c, out)
		}
		return input
	}
	first := []any{map[string]any{"type": "message", "role": "user", "content": "hi"}}
	body := func(input []any, extra map[string]any) map[string]any {
		b := map[string]any{"model": codexModel, "input": input}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for _, tc := range []struct {
		name string
		reqs []map[string]any
		want bool
	}{
		{"honest", []map[string]any{body(first, nil), body(refused(nil), nil)}, true},
		{"empty tools field", []map[string]any{body(first, map[string]any{"tools": []any{}}), body(refused(nil), nil)}, true},
		{"tool offered", []map[string]any{body(first, map[string]any{"tools": []any{map[string]any{"name": "shell"}}}), body(refused(nil), nil)}, false},
		{"additional_tools", []map[string]any{body(append(first, map[string]any{"type": "additional_tools", "tools": []any{}}), nil), body(refused(nil), nil)}, false},
		{"agent section", []map[string]any{body(append([]any{map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"text": "<multi_agent_mode>"}}}}, first...), nil), body(refused(nil), nil)}, false},
		{"wrong model", []map[string]any{{"model": "gpt-other", "input": first}, body(refused(nil), nil)}, false},
		{"a call ran", []map[string]any{body(first, nil), body(refused(func(i int, o *map[string]any) {
			if i == 0 {
				(*o)["output"] = "Process exited with code 0"
			}
		}), nil)}, false},
		{"a call missing", []map[string]any{body(first, nil), body(refused(nil)[:len(refused(nil))-1], nil)}, false},
		{"one request", []map[string]any{body(first, nil)}, false},
		{"three requests", []map[string]any{body(first, nil), body(refused(nil), nil), body(first, nil)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := startSelfCheckServer()
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			for _, r := range tc.reqs {
				b, _ := json.Marshal(r)
				resp, err := http.Post(s.url+"/v1/responses", "application/json", bytes.NewReader(b))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
			}
			if got := s.verify(); got != tc.want {
				t.Fatalf("verify %v", got)
			}
		})
	}
	s, _ := startSelfCheckServer()
	defer s.close()
	if resp, err := http.Get(s.url + "/v1/models"); err == nil {
		resp.Body.Close()
	}
	if s.verify() {
		t.Fatal("an unexpected request passed")
	}
}

// The real Codex 0.166.0, offline: set HCLARO_REAL_CODEX to its path to run the self-check and
// the effective-settings probes against it (fake server, deny proxy, empty CODEX_HOME for the
// self-check; the probes read the real CODEX_HOME and make no connection).
func TestCodexRealBinaryOffline(t *testing.T) {
	path := os.Getenv("HCLARO_REAL_CODEX")
	if path == "" {
		t.Skip("set HCLARO_REAL_CODEX=/path/to/codex to run against the real binary, offline")
	}
	c := NewCodexCLI(path)
	bin, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, cleanup, err := writeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	if st := c.CLI.Detect(ctx); st.Mode != ModeHeadless || st.Version != codexVerified {
		t.Fatalf("%+v", st)
	}
	start := time.Now()
	if kind := c.selfCheck(ctx, bin, catalog); kind != "" {
		t.Fatalf("self-check: %s", kind)
	}
	t.Logf("self-check passed in %v", time.Since(start).Round(time.Millisecond))
	// The same check must fail when the real Codex is given a catalog field that adds a tool
	// despite every flag (CODEX-INVESTIGATION.md §2.1, §6 D): apply_patch, or sub-agents.
	for _, swap := range [][2]string{
		{`"apply_patch_tool_type": null`, `"apply_patch_tool_type": "freeform"`},
		{`"multi_agent_version": null`, `"multi_agent_version": "v2"`},
	} {
		if !strings.Contains(string(codexCatalog), swap[0]) {
			t.Fatalf("catalog format changed: %s", swap[0])
		}
		bad := filepath.Join(t.TempDir(), "catalog.json")
		put(t, bad, strings.Replace(string(codexCatalog), swap[0], swap[1], 1))
		if kind := c.selfCheck(ctx, bin, bad); kind != KindCodexCheck {
			t.Fatalf("%s passed the self-check", swap[1])
		}
	}
	if os.Getenv("CODEX_HOME") != "" || os.Getenv("HOME") != "" {
		home, err := codexHome()
		if err != nil {
			t.Fatal(err)
		}
		features, models, kind := c.effective(ctx, bin, home, catalog)
		if kind != "" {
			t.Fatalf("effective settings: %s", kind)
		}
		t.Logf("catalog tool fields: %s; features list: %d bytes", models, len(features))
	}
	key, sum, err := c.binaryHash(bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("binary %s sha256 %s (stat %s)", bin, sum, key)
}
