package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/bounded"
)

// Headless providers (AI-01.04). An Assistant is detected and run through one
// provider-independent path; an Adapter holds the only provider-specific parts: the argv
// recorded in tasks/AI/FEASIBILITY.md, version parsing and reply/failure classification.

// Provider modes (tasks/AI/README.md, "Provider eligibility").
const (
	ModeHeadless    = "headless-eligible"
	ModeHandoff     = "handoff-only"
	ModeUnsupported = "unsupported"
)

// Failure kinds. Messages come from Message; provider output is never returned in one.
const (
	KindMissing        = "cli_missing"
	KindPathInvalid    = "path_invalid"
	KindVersion        = "version_unsupported"
	KindMalformed      = "malformed_output"
	KindNotEligible    = "not_headless"
	KindAuth           = "auth_required"
	KindFailed         = "failed"
	KindTimeout        = "timeout"
	KindCancelled      = "cancelled"
	KindOutputTooLarge = "output_too_large"
	KindRefusedArgs    = "refused"
	// OpenAI Codex refusals (AI-01.14, tasks/AI/CODEX-INVESTIGATION.md §6–7). None names a plan,
	// account or path read from the sign-in.
	KindCodexPlatform = "codex_platform"
	KindCodexAgents   = "codex_agents_file"
	KindCodexManaged  = "codex_managed"
	KindCodexAccount  = "codex_account"
	KindCodexToken    = "codex_sign_in_expiring"
	KindCodexSettings = "codex_settings_changed"
	KindCodexCheck    = "codex_self_check"
)

var kindMessages = map[string]string{
	KindMissing:        "The assistant's executable was not found on PATH. Install it yourself, or start Hclaro with its path flag.",
	KindPathInvalid:    "The configured executable path is not an executable file. Fix the path flag and restart Hclaro.",
	KindVersion:        "This version of the assistant has not been verified for headless use. Copy or Download still work.",
	KindMalformed:      "The assistant returned output Hclaro could not read safely. Nothing from it is shown.",
	KindNotEligible:    "This assistant is handoff-only: Hclaro has not verified a way to run it with its tools off. Copy or Download still work.",
	KindAuth:           "The assistant is not signed in. Sign in from your terminal; Hclaro never asks for or stores credentials.",
	KindFailed:         "The assistant failed. Details are withheld because its messages can echo the context.",
	KindTimeout:        "The assistant did not answer in time.",
	KindCancelled:      "The request was cancelled.",
	KindOutputTooLarge: "The assistant returned more output than Hclaro reads.",
	KindRefusedArgs:    "Hclaro refused to run this command because it is not on the allowlist.",
	KindCodexPlatform:  "Hclaro runs OpenAI Codex only on macOS for now; here it is copy only. Copy or Download still work.",
	KindCodexAgents:    "Hclaro does not run OpenAI Codex while AGENTS.md or AGENTS.override.md exists in your Codex folder (~/.codex): Codex would send that file to OpenAI with every message. Move or rename it to send from Hclaro; Copy or Download still work.",
	KindCodexManaged:   "OpenAI Codex has organization-managed settings on this Mac, or Hclaro could not check for them. They can turn Codex tools back on, so Hclaro does not run it. Copy or Download still work.",
	KindCodexAccount:   "Hclaro runs OpenAI Codex only when it is signed in with a personal ChatGPT plan (Free, Go, Plus or Pro). Team, Business, Enterprise and Education workspaces, API keys and sign-ins Hclaro cannot read are copy only.",
	KindCodexToken:     "OpenAI Codex's sign-in expires within 15 minutes, or Hclaro could not tell when it expires. Use Codex once in your terminal to refresh it, then try again.",
	KindCodexSettings:  "OpenAI Codex's effective settings differ from the ones Hclaro verified (a tool feature or the model list changed). Hclaro does not run it; Copy or Download still work.",
	KindCodexCheck:     "OpenAI Codex did not pass Hclaro's local security check (no tools offered, every tool call refused). Hclaro does not run it; Copy or Download still work.",
}

// Message returns the user-facing explanation of a failure kind.
func Message(kind string) string {
	if m, ok := kindMessages[kind]; ok {
		return m
	}
	return kindMessages[KindFailed]
}

// Failure is a classified, sanitized provider failure.
type Failure struct{ Kind string }

func (f *Failure) Error() string { return Message(f.Kind) }

// KindOf returns the failure kind of err.
func KindOf(err error) string {
	var f *Failure
	switch {
	case errors.As(err, &f):
		return f.Kind
	case errors.Is(err, context.Canceled):
		return KindCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	}
	return KindFailed
}

// ProviderStatus is what detection found. Path is read-only in the UI (OD-AI-3).
type ProviderStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Assistant is one provider: Detect reports its version and mode, Run sends payload on stdin
// and returns the reply text. Workflow code uses only this interface.
type Assistant interface {
	ID() string
	Detect(ctx context.Context) ProviderStatus
	Run(ctx context.Context, payload []byte) (string, error)
}

// Adapter is the provider-specific part of a CLI assistant.
type Adapter struct {
	ID, Name, Command string
	VersionArgs       []string
	// RunArgs is the verified headless argv; nil makes the provider handoff-only.
	RunArgs []string
	// Handoff is the feasibility reason a handoff-only provider (nil RunArgs) cannot run.
	Handoff string
	// AuthEnv names the auth variables recorded in feasibility; they are passed through.
	AuthEnv []string
	// ParseVersion returns the version in `--version` output ("" when unreadable) and whether
	// it is in the verified range.
	ParseVersion func(out []byte) (version string, supported bool)
	// Reply returns the reply text, or a failure kind. Provider text never goes into a kind.
	Reply func(stdout, stderr []byte, exitOK bool) (reply, kind string)
}

// CLI runs an Adapter's executable from PATH or from Path, set only by a CLI flag (OD-AI-3).
type CLI struct {
	Adapter
	Path    string
	Timeout time.Duration
}

const (
	versionTimeout = 10 * time.Second
	runTimeout     = 5 * time.Minute
	maxReply       = 1 << 20
	maxStderr      = 16 << 10
	maxVersion     = 4 << 10
)

func (c *CLI) ID() string { return c.Adapter.ID }

func (c *CLI) resolve() (string, string) {
	if c.Path == "" {
		p, err := exec.LookPath(c.Command)
		if err != nil {
			return "", KindMissing
		}
		return p, ""
	}
	info, err := os.Stat(c.Path)
	if err != nil || !filepath.IsAbs(c.Path) || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return "", KindPathInvalid
	}
	return c.Path, ""
}

func (c *CLI) Detect(ctx context.Context) ProviderStatus {
	st := ProviderStatus{ID: c.Adapter.ID, Name: c.Name, Mode: ModeUnsupported}
	path, kind := c.resolve()
	st.Path = path
	if kind == "" {
		out, errOut, ok, overflow, err := c.exec(ctx, path, c.VersionArgs, nil, maxVersion, versionTimeout)
		version, supported := "", false
		if err == nil && ok && !overflow {
			version, supported = c.ParseVersion(append(out, errOut...))
		}
		switch {
		case err != nil:
			kind = KindOf(err)
		case version == "":
			kind = KindMalformed
		case !supported:
			st.Version, st.Mode, kind = version, ModeHandoff, KindVersion
		case c.RunArgs == nil:
			st.Version, st.Mode, kind = version, ModeHandoff, KindNotEligible
		default:
			st.Version, st.Mode = version, ModeHeadless
		}
	}
	if kind != "" {
		st.Reason, st.Message = kind, Message(kind)
	}
	if kind == KindNotEligible && c.Handoff != "" {
		st.Message += " " + c.Handoff
	}
	return st
}

// Run re-detects first, so a provider removed or updated since Settings showed it is refused.
func (c *CLI) Run(ctx context.Context, payload []byte) (string, error) {
	st := c.Detect(ctx)
	if st.Mode != ModeHeadless {
		return "", &Failure{Kind: st.Reason}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = runTimeout
	}
	out, errOut, ok, overflow, err := c.exec(ctx, st.Path, c.RunArgs, payload, maxReply, timeout)
	switch {
	case err != nil:
		return "", err
	case overflow:
		return "", &Failure{Kind: KindOutputTooLarge}
	}
	reply, kind := c.Reply(out, errOut, ok)
	if kind != "" {
		return "", &Failure{Kind: kind}
	}
	return reply, nil
}

// exec runs one allowlisted argv in a fresh empty private directory with an allowlisted
// environment, payload on stdin and bounded output. stderr is returned only for
// classification.
func (c *CLI) exec(ctx context.Context, path string, args []string, stdin []byte, limit int, timeout time.Duration) (stdout, stderr []byte, exitOK, overflow bool, err error) {
	if !slices.Equal(args, c.VersionArgs) && !slices.Equal(args, c.RunArgs) {
		return nil, nil, false, false, &Failure{Kind: KindRefusedArgs}
	}
	dir, err := os.MkdirTemp("", "hclaro-ai-")
	if err != nil {
		return nil, nil, false, false, &Failure{Kind: KindFailed}
	}
	defer os.RemoveAll(dir)
	r, err := runProc(ctx, proc{path: path, args: args, dir: dir, env: c.Environ(os.Environ()), stdin: stdin, limit: limit, timeout: timeout})
	return r.stdout, r.stderr, r.exitOK, r.overflow, err
}

// proc is one process start. stop, when set, sees every complete stdout line; returning true
// kills the process group at once and the result is marked stopped.
type proc struct {
	path    string
	args    []string
	dir     string
	env     []string
	stdin   []byte
	limit   int
	timeout time.Duration
	stop    func(line []byte) bool
}

type procResult struct {
	stdout, stderr            []byte
	exitOK, overflow, stopped bool
}

func runProc(ctx context.Context, p proc) (procResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	cmd := exec.CommandContext(runCtx, p.path, p.args...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	cmd.Stdin = bytes.NewReader(p.stdin)
	// Own process group: cancel, timeout and stop kill the assistant and anything it started.
	prepareGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd.Process) }
	cmd.WaitDelay = 5 * time.Second
	out, errOut := &bounded.Buffer{Limit: p.limit}, &bounded.Buffer{Limit: maxStderr}
	watch := &lineWatch{out: out, stop: p.stop, hit: stopRun, max: p.limit}
	cmd.Stdout, cmd.Stderr = watch, errOut
	runErr := cmd.Run()
	if watch.stopped && ctx.Err() == nil {
		return procResult{stdout: out.Bytes(), stderr: errOut.Bytes(), stopped: true, overflow: out.Overflow}, nil
	}
	if ctx.Err() != nil {
		return procResult{}, ctx.Err()
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return procResult{}, &Failure{Kind: KindMissing} // could not start: removed or not executable
	}
	return procResult{stdout: out.Bytes(), stderr: errOut.Bytes(), exitOK: runErr == nil, overflow: out.Overflow}, nil
}

// lineWatch passes stdout to out and, when stop is set, hands it every complete line.
type lineWatch struct {
	out     *bounded.Buffer
	stop    func([]byte) bool
	hit     func()
	max     int
	pending []byte
	stopped bool
}

func (w *lineWatch) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if w.stop == nil || w.stopped {
		return n, err
	}
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := w.pending[:i]
		w.pending = w.pending[i+1:]
		if w.stop(line) {
			w.stopped = true
			w.hit()
			return n, err
		}
	}
	if len(w.pending) > w.max { // a line longer than the reply limit: stop, fail closed
		w.stopped = true
		w.hit()
	}
	return n, err
}

// baseEnv are the only inherited variables besides an adapter's AuthEnv: process basics and
// the locale. Cloud (AWS_*, ARM_*, AZURE_*, GOOGLE_*), Terraform (TF_*), backend and Hclaro
// variables are never passed.
var baseEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "TMP", "TEMP"}

// Environ filters environ to the allowlist and adds TERM=dumb, as in the feasibility probe.
func (c *CLI) Environ(environ []string) []string {
	out := []string{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if key != "TERM" && (slices.Contains(baseEnv, key) || slices.Contains(c.AuthEnv, key)) {
			out = append(out, item)
		}
	}
	return append(out, "TERM=dumb")
}

// Claude Code, headless-eligible at 2.1.326 (FEASIBILITY.md, 2026-11-17). The argv is the
// probe argv without --max-budget-usd and with --restricted (security review 2026-11-17). The
// CLI checks the budget only after a turn, so it billed requests and withheld their replies;
// Anthropic bills usage directly and Hclaro imposes no cost limit. --safe-mode alone still loads the
// user's installed plugins and user settings files (permission rules, env, apiKeyHelper);
// --restricted ignores those files and refuses bypassPermissions. --tools "" leaves no
// built-in tool and --strict-mcp-config without --mcp-config no MCP server (verified from the
// CLI's own init record: tools [] and mcp_servers []).
var Claude = Adapter{
	ID: "claude", Name: "Claude Code", Command: "claude",
	VersionArgs: []string{"--version"},
	RunArgs: []string{"-p", "--restricted", "--safe-mode", "--tools", "", "--strict-mcp-config", "--permission-mode", "dontAsk",
		"--permission-prompts", "none", "--no-session-persistence", "--output-format", "json"},
	AuthEnv:      []string{"ANTHROPIC_API_KEY"},
	ParseVersion: claudeVersion,
	Reply:        claudeReply,
}

var claudeVersionRe = regexp.MustCompile(`^([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,6}) \(Claude Code\)$`)

// claudeVersion accepts 2.1.x from the probed 2.1.326 on. shortcut: patch releases are
// trusted to keep the probed flags; re-run the probe and widen or pin this on a minor bump.
func claudeVersion(out []byte) (string, bool) {
	m := claudeVersionRe.FindSubmatch(bytes.TrimSpace(out))
	if m == nil {
		return "", false
	}
	patch, _ := strconv.Atoi(string(m[3]))
	return string(m[1]) + "." + string(m[2]) + "." + string(m[3]), string(m[1]) == "2" && string(m[2]) == "1" && patch >= 326
}

// claudeReply reads the single JSON result object. Failure is is_error or a non-zero exit;
// subtype stays "success" even when not logged in, so it is never used.
func claudeReply(stdout, stderr []byte, exitOK bool) (string, string) {
	var r struct {
		IsError *bool   `json:"is_error"`
		Result  *string `json:"result"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &r) != nil || r.IsError == nil {
		if !exitOK {
			return "", classify(string(stderr))
		}
		return "", KindMalformed
	}
	if !exitOK || *r.IsError {
		text := string(stderr)
		if r.Result != nil {
			text += *r.Result
		}
		return "", classify(text)
	}
	if r.Result == nil {
		return "", KindMalformed
	}
	return *r.Result, ""
}

// OpenAI Codex 0.166.0, runnable only through CodexCLI (codex.go), which adds the gates and
// the self-check of tasks/AI/CODEX-INVESTIGATION.md §6–7. The argv is the verified hardened
// argv: --ignore-user-config and --ignore-rules drop user config and rules; the --disable list
// and -c settings turn off every tool-capable feature found in 0.166.0; the model catalog
// (codex_catalog.json, gpt-5.6-luna with shell, apply_patch, code mode, multi-agent and tool
// search removed) leaves no tool; --strict-config fails on a misspelt key; project docs, skill
// instructions, environment context and analytics are off; credentials come only from
// CODEX_HOME/auth.json, the file the gates read. codexCatalogArg is replaced by the catalog path.
var Codex = Adapter{
	ID: "codex", Name: "OpenAI Codex", Command: "codex",
	VersionArgs: []string{"--version"},
	RunArgs: []string{"exec", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--disable", "shell_tool", "--disable", "unified_exec", "--disable", "apps", "--disable", "plugins", "--disable", "browser_use",
		"--disable", "computer_use", "--disable", "view_image", "--disable", "hooks", "-c", "web_search=disabled", "--json",
		"-m", codexModel, "-c", "model_catalog_json=" + codexCatalogArg, "-c", "tools.experimental_request_user_input.enabled=false",
		"--disable", "multi_agent", "--disable", "multi_agent_v2", "--disable", "sleep_tool", "--disable", "goals",
		"--disable", "code_mode_host", "--disable", "image_generation", "--disable", "tool_suggest", "--disable", "skill_search",
		"--strict-config", "-c", "project_doc_max_bytes=0", "-c", "skills.include_instructions=false",
		"-c", "include_environment_context=false", "-c", "analytics.enabled=false", "--disable", "shell_snapshot",
		"-c", `cli_auth_credentials_store="file"`, "-"},
	ParseVersion: codexVersion,
	Reply:        codexReply,
}

var codexVersionRe = regexp.MustCompile(`^codex-cli ([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6})$`)

// codexVersion accepts exactly the verified 0.166.0 (OD-CODEX-2): any other version needs a
// new assessment.
func codexVersion(out []byte) (string, bool) {
	first, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
	m := codexVersionRe.FindSubmatch(bytes.TrimSpace(first))
	if m == nil {
		return "", false
	}
	return string(m[1]), string(m[1]) == codexVerified
}

func classify(text string) string {
	s := strings.ToLower(text)
	if strings.Contains(s, "not logged in") || strings.Contains(s, "/login") || strings.Contains(s, "api key") || strings.Contains(s, "unauthorized") {
		return KindAuth
	}
	return KindFailed
}
