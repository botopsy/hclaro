package assist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"hclaro/internal/assist/codexfake"
)

// The fake assistant CLI is this test binary: when HOME holds fakeMarker, TestMain acts as
// `claude` in the mode written there and records what it was given. No real assistant runs.
const fakeMarker = "hclaro-fake-assistant-mode"

type fakeRecord struct {
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`
	Entries int      `json:"entries"`
	Env     []string `json:"env"`
	Stdin   string   `json:"stdin"`
}

func TestMain(m *testing.M) {
	// Hclaro's Codex self-check runs with a fresh HOME, so it is recognized by its argv.
	if codexfake.IsSelfCheck(os.Args[1:]) {
		os.Exit(codexfake.Main("", ""))
	}
	if mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), fakeMarker)); err == nil {
		if strings.HasPrefix(string(mode), "codex") {
			os.Exit(codexfake.Main(string(mode), codexHostileReply))
		}
		os.Exit(fakeCLI(string(mode)))
	}
	os.Exit(m.Run())
}

func fakeCLI(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	entries, _ := os.ReadDir(dir)
	sum := sha256.Sum256(in)
	rec, _ := json.Marshal(fakeRecord{Args: os.Args[1:], Dir: dir, Entries: len(entries), Env: os.Environ(), Stdin: hex.EncodeToString(sum[:])})
	name := "run.json"
	if slices.Equal(os.Args[1:], []string{"--version"}) {
		name = "version.json"
		switch mode {
		case "old":
			os.Stdout.WriteString("2.0.9 (Claude Code)\n")
		case "garbage":
			os.Stdout.WriteString("something else\n")
		case "codex":
			os.Stdout.WriteString("codex-cli 0.166.0\n")
		default:
			os.Stdout.WriteString("2.1.326 (Claude Code)\n")
		}
		_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), name), rec, 0o600)
		return 0
	}
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), name), rec, 0o600)
	switch mode {
	case "auth":
		os.Stdout.WriteString(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`)
		return 1
	case "fail":
		os.Stderr.WriteString("boom: " + string(in))
		return 3
	case "big":
		os.Stdout.WriteString(`{"is_error":false,"result":"` + strings.Repeat("x", maxReply) + `"}`)
		return 0
	case "malformed":
		os.Stdout.WriteString("not json")
		return 0
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "spawn":
		// Start a grandchild that hangs; cancellation must kill it with the assistant.
		child := filepath.Join(os.Getenv("HOME"), "child")
		_ = os.MkdirAll(child, 0o700)
		_ = os.WriteFile(filepath.Join(child, fakeMarker), []byte("hang"), 0o600)
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "--version-not")
		cmd.Env = append(os.Environ(), "HOME="+child)
		if cmd.Start() == nil {
			_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), "grandchild.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		}
		time.Sleep(time.Minute)
		return 0
	}
	b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "Reply: terraform apply -auto-approve; rm -rf /", "session_id": "s", "total_cost_usd": 0.01})
	os.Stdout.Write(b)
	return 0
}

// fake returns a CLI whose configured path is this test binary, with HOME set to a fresh
// directory in mode. Canaries are planted in the parent environment.
func fake(t *testing.T, mode string) (*CLI, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, fakeMarker), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, k := range []string{"AWS_SECRET_ACCESS_KEY", "AWS_PROFILE", "TF_VAR_password", "TF_TOKEN_app_terraform_io", "GOOGLE_APPLICATION_CREDENTIALS", "ARM_CLIENT_SECRET", "AZURE_CLIENT_SECRET", "HCLARO_CANARY", "GITHUB_TOKEN", "ANTHROPIC_BASE_URL",
		"GH_TOKEN", "COPILOT_GITHUB_TOKEN", "COPILOT_ALLOW_ALL", "OPENAI_API_KEY", "CODEX_HOME"} {
		t.Setenv(k, "canary-"+k)
	}
	t.Setenv("ANTHROPIC_API_KEY", "fake-key")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &CLI{Adapter: Claude, Path: exe}, home
}

func record(t *testing.T, home, name string) fakeRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, name))
	if err != nil {
		t.Fatal(err)
	}
	var r fakeRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDetection(t *testing.T) {
	ctx := context.Background()
	c, _ := fake(t, "ok")
	if st := c.Detect(ctx); st.Mode != ModeHeadless || st.Version != "2.1.326" || st.Path != c.Path || st.Reason != "" {
		t.Fatalf("%+v", st)
	}
	missing := &CLI{Adapter: Claude}
	missing.Command = "hclaro-no-such-assistant"
	if st := missing.Detect(ctx); st.Mode != ModeUnsupported || st.Reason != KindMissing {
		t.Fatalf("%+v", st)
	}
	for _, p := range []string{"relative/claude", filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
		bad := &CLI{Adapter: Claude, Path: p}
		if st := bad.Detect(ctx); st.Mode != ModeUnsupported || st.Reason != KindPathInvalid {
			t.Fatalf("%s: %+v", p, st)
		}
	}
	if runtime.GOOS != "windows" {
		notExec := filepath.Join(t.TempDir(), "claude")
		_ = os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o600)
		if st := (&CLI{Adapter: Claude, Path: notExec}).Detect(ctx); st.Reason != KindPathInvalid {
			t.Fatalf("%+v", st)
		}
	}
	old, _ := fake(t, "old")
	if st := old.Detect(ctx); st.Mode != ModeHandoff || st.Reason != KindVersion || st.Version != "2.0.9" {
		t.Fatalf("%+v", st)
	}
	if _, err := old.Run(ctx, []byte("x")); KindOf(err) != KindVersion {
		t.Fatalf("unsupported version ran: %v", err)
	}
	garbage, _ := fake(t, "garbage")
	if st := garbage.Detect(ctx); st.Mode != ModeUnsupported || st.Reason != KindMalformed || st.Version != "" {
		t.Fatalf("%+v", st)
	}
	handoff, _ := fake(t, "ok")
	handoff.RunArgs = nil
	if st := handoff.Detect(ctx); st.Mode != ModeHandoff || st.Reason != KindNotEligible {
		t.Fatalf("%+v", st)
	}
}

// handoffOnly is a synthetic second handoff-only adapter: Copilot was removed (OD-AI-14), but
// the path stays covered by more than one adapter.
var handoffOnly = Adapter{ID: "handoff", Name: "Handoff Fake", Command: "handoff-fake", VersionArgs: []string{"--version"},
	ParseVersion: anyVersion(regexp.MustCompile(`^([0-9.]+) \(Claude Code\)$`)), Handoff: "It has no verified tools-off mode."}

// anyVersion reads a handoff-only test adapter's version from the first output line; every
// version is "supported" so Detect reports the handoff reason, never a version warning.
func anyVersion(re *regexp.Regexp) func([]byte) (string, bool) {
	return func(out []byte) (string, bool) {
		first, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
		m := re.FindSubmatch(bytes.TrimSpace(first))
		if m == nil {
			return "", false
		}
		return string(m[1]), true
	}
}

// Handoff-only adapters (AI-01.05) go through the same fake-CLI checks: detection runs only
// --version, isolated like a run, and Run refuses before starting the assistant.
func TestHandoffOnlyAdapters(t *testing.T) {
	ctx := context.Background()
	for _, a := range []Adapter{handoffOnly} {
		t.Run(a.ID, func(t *testing.T) {
			c, home := fake(t, "ok")
			c.Adapter = a
			st := c.Detect(ctx)
			if st.Mode != ModeHandoff || st.Reason != KindNotEligible || st.Version == "" || st.Path != c.Path || !strings.HasSuffix(st.Message, a.Handoff) {
				t.Fatalf("%+v", st)
			}
			if _, err := c.Run(ctx, []byte("payload")); KindOf(err) != KindNotEligible {
				t.Fatalf("handoff-only provider ran: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, "run.json")); !os.IsNotExist(err) {
				t.Fatal("handoff-only provider was started with a run argv")
			}
			v := record(t, home, "version.json")
			if !slices.Equal(v.Args, []string{"--version"}) || v.Entries != 0 || !strings.Contains(filepath.Base(v.Dir), "hclaro-ai-") {
				t.Fatalf("%+v", v)
			}
			if env := strings.Join(v.Env, "\n"); strings.Contains(env, "canary-") || strings.Contains(env, "fake-key") {
				t.Fatalf("environment leaks:\n%s", env)
			}
			garbage, _ := fake(t, "garbage")
			garbage.Adapter = a
			if st := garbage.Detect(ctx); st.Mode != ModeUnsupported || st.Reason != KindMalformed {
				t.Fatalf("%+v", st)
			}
			missing := &CLI{Adapter: a}
			missing.Command = "hclaro-no-such-assistant"
			if st := missing.Detect(ctx); st.Mode != ModeUnsupported || st.Reason != KindMissing {
				t.Fatalf("%+v", st)
			}
		})
	}
}

// OD-CODEX-2: exactly 0.166.0 is runnable; every other version is reported but refused.
func TestCodexVersionPinned(t *testing.T) {
	for _, tc := range []struct {
		out, ver string
		ok       bool
	}{
		{"codex-cli 0.166.0\n", "0.166.0", true},
		{"codex 0.166.0", "", false},
		{"codex-cli 0.167.0\nupdate available\n", "0.167.0", false},
		{"codex-cli 0.166.1", "0.166.1", false},
		{"codex-cli 0.165.0", "0.165.0", false},
	} {
		if v, ok := Codex.ParseVersion([]byte(tc.out)); v != tc.ver || ok != tc.ok {
			t.Errorf("%q: %q %v", tc.out, v, ok)
		}
	}
}

func TestClaudeVersionRange(t *testing.T) {
	for out, want := range map[string]bool{"2.1.326 (Claude Code)": true, "2.1.400 (Claude Code)\n": true, "2.1.325 (Claude Code)": false, "2.2.0 (Claude Code)": false, "3.1.326 (Claude Code)": false, "2.1.326": false} {
		if _, ok := claudeVersion([]byte(out)); ok != want {
			t.Errorf("%q: %v", out, ok)
		}
	}
}

func TestRunArgvStdinAndIsolation(t *testing.T) {
	c, home := fake(t, "ok")
	payload := "# Hclaro context\n--flag $(touch pwned) `id` \"quoted\" 'single'\n--max-budget-usd 999\n"
	reply, err := c.Run(context.Background(), []byte(payload))
	if err != nil || reply != "Reply: terraform apply -auto-approve; rm -rf /" {
		t.Fatalf("%q %v", reply, err)
	}
	r := record(t, home, "run.json")
	if !slices.Equal(r.Args, Claude.RunArgs) {
		t.Fatalf("argv %q", r.Args)
	}
	if sum := sha256.Sum256([]byte(payload)); r.Stdin != hex.EncodeToString(sum[:]) {
		t.Fatal("stdin differs from the payload")
	}
	wd, _ := os.Getwd()
	if r.Entries != 0 || strings.HasPrefix(r.Dir, wd) || !strings.Contains(filepath.Base(r.Dir), "hclaro-ai-") {
		t.Fatalf("working directory %s with %d entries", r.Dir, r.Entries)
	}
	if _, err := os.Stat(r.Dir); !os.IsNotExist(err) {
		t.Fatal("private directory was not removed")
	}
	env := strings.Join(r.Env, "\n")
	for _, bad := range []string{"canary-", "AWS_", "TF_", "GOOGLE_", "ARM_", "AZURE_", "HCLARO_", "GITHUB_TOKEN", "ANTHROPIC_BASE_URL"} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment leaks %s:\n%s", bad, env)
		}
	}
	if !slices.Contains(r.Env, "ANTHROPIC_API_KEY=fake-key") || !slices.Contains(r.Env, "TERM=dumb") || !slices.Contains(r.Env, "HOME="+home) {
		t.Fatalf("environment %q", r.Env)
	}
	if v := record(t, home, "version.json"); !slices.Equal(v.Args, []string{"--version"}) || v.Entries != 0 {
		t.Fatalf("%+v", v)
	}
}

// The owner removed the spending threshold (2026-11-17): the argv names no budget flag.
func TestClaudeArgvHasNoSpendingLimit(t *testing.T) {
	for _, a := range Claude.RunArgs {
		if strings.Contains(a, "budget") || strings.Contains(a, "usd") {
			t.Fatalf("spending-limit flag in argv: %q", Claude.RunArgs)
		}
	}
	want := []string{"-p", "--restricted", "--safe-mode", "--tools", "", "--strict-mcp-config", "--permission-mode", "dontAsk",
		"--permission-prompts", "none", "--no-session-persistence", "--output-format", "json"}
	if !slices.Equal(Claude.RunArgs, want) {
		t.Fatalf("argv %q", Claude.RunArgs)
	}
}

func TestArgvAllowlist(t *testing.T) {
	c, _ := fake(t, "ok")
	if _, _, _, _, err := c.exec(context.Background(), c.Path, []string{"-p", "--tools", "Bash"}, nil, 10, time.Second); KindOf(err) != KindRefusedArgs {
		t.Fatal(err)
	}
}

func TestRunFailures(t *testing.T) {
	ctx := context.Background()
	for mode, want := range map[string]string{"auth": KindAuth, "fail": KindFailed, "big": KindOutputTooLarge, "malformed": KindMalformed} {
		c, _ := fake(t, mode)
		_, err := c.Run(ctx, []byte("secret-looking payload"))
		if KindOf(err) != want || strings.Contains(err.Error(), "payload") || strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: %v", mode, err)
		}
	}
	c, _ := fake(t, "hang")
	c.Timeout = 300 * time.Millisecond
	if _, err := c.Run(ctx, nil); KindOf(err) != KindTimeout {
		t.Fatalf("timeout: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(2*time.Second, cancel)
	c.Timeout = 0
	if _, err := c.Run(cctx, nil); KindOf(err) != KindCancelled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestProviderRemovedBeforeRun(t *testing.T) {
	c, _ := fake(t, "ok")
	exe := c.Path
	copied := filepath.Join(t.TempDir(), filepath.Base(exe))
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, b, 0o700); err != nil {
		t.Fatal(err)
	}
	c.Path = copied
	if st := c.Detect(context.Background()); st.Mode != ModeHeadless {
		t.Fatalf("%+v", st)
	}
	_ = os.Remove(copied)
	if _, err := c.Run(context.Background(), []byte("x")); KindOf(err) != KindPathInvalid {
		t.Fatalf("%v", err)
	}
}

// Outputs recorded from the real Claude Code 2.1.326 on 2026-11-17 (security review),
// reduced to the fields the adapter reads.
func TestClaudeReplyRecordedOutputs(t *testing.T) {
	for name, tc := range map[string]struct {
		out    string
		exitOK bool
		reply  string
		kind   string
	}{
		"success":       {`{"type":"result","subtype":"success","is_error":false,"result":"OK","terminal_reason":"completed","permission_denials":[]}`, true, "OK", ""},
		"not logged in": {`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","terminal_reason":"api_error"}`, false, "", KindAuth},
		"no result":     {`{"type":"result","is_error":false}`, true, "", KindMalformed},
		"not json":      {"Error: something", false, "", KindFailed},
	} {
		if reply, kind := claudeReply([]byte(tc.out), nil, tc.exitOK); reply != tc.reply || kind != tc.kind {
			t.Errorf("%s: %q %q", name, reply, kind)
		}
	}
}

func TestCancelKillsProcessesTheAssistantStarted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shortcut: Windows kills the assistant process only (proc_windows.go)")
	}
	c, home := fake(t, "spawn")
	ctx, cancel := context.WithCancel(context.Background())
	pidFile := filepath.Join(home, "grandchild.pid")
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
		}
		cancel()
	}()
	if _, err := c.Run(ctx, nil); KindOf(err) != KindCancelled {
		t.Fatalf("%v", err)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal("the fake did not start a grandchild")
	}
	pid, _ := strconv.Atoi(string(b))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		p, _ := os.FindProcess(pid)
		if p.Signal(syscall.Signal(0)) != nil {
			return
		}
		if time.Now().After(deadline) {
			_ = p.Kill()
			t.Fatal("the assistant's child survived cancellation")
		}
	}
}
