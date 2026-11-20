package assist

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/bounded"
	"hclaro/internal/proc"
)

// Headless providers (AI-01.04). An Assistant is detected and run through one
// provider-independent path; an Adapter (the provider profile) holds the only provider-specific
// parts: the argv recorded in tasks/AI/FEASIBILITY.md, gates, version parsing, reply/failure
// classification, its failure kinds and its disclosure line. Each provider lives in its own
// file (claude.go, codex.go) and is registered once in adapters.

// adapters are the registered provider profiles, in preference order: with no saved choice,
// the first headless-eligible one is used. Their argv shapes form the process allowlist.
var adapters = []*Adapter{&Claude, &codexAdapter}

// adapterOf is the registered profile with id, or nil (e.g. a test fake).
func adapterOf(id string) *Adapter {
	for _, a := range adapters {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// rank is id's place in the preference order; unknown providers come last.
func rank(id string) int {
	for i, a := range adapters {
		if a.ID == id {
			return i
		}
	}
	return len(adapters)
}

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
}

// Message returns the user-facing explanation of a failure kind.
func Message(kind string) string {
	if m, ok := kindMessages[kind]; ok {
		return m
	}
	for _, a := range adapters {
		if m, ok := a.Kinds[kind]; ok {
			return m
		}
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
	// Set by Settings from Mode and Reason, so the UI copies no mode or reason string: Send is
	// headless-eligible, Usable is anything but unsupported, Installed is found on PATH.
	Send      bool `json:"send"`
	Usable    bool `json:"usable"`
	Installed bool `json:"installed"`
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

	// Optional profile parts; nil or empty means none.
	// Gate refines a headless-eligible status with checks that start no run (Detect and Run).
	Gate func(ctx context.Context, st ProviderStatus) ProviderStatus
	// Prepare readies one run of the eligible st: binary, argv, extra environment and cleanup.
	// Without it a run is RunArgs on st.Path.
	Prepare func(ctx context.Context, st ProviderStatus) (Prepared, error)
	// Stop sees every stdout line of a run; true stops it at once as failed.
	Stop func(line []byte) bool
	// Argvs, when set, replaces RunArgs in the allowlist: it reports every run-time argv shape
	// this provider uses (RunArgs is then a template).
	Argvs func(args []string) bool
	// Helpers are fixed helper programs (absolute path) and their one allowed argv.
	Helpers map[string][]string
	// SameVersion reports whether a conversation saved with one version may continue on another
	// (default: same major version).
	SameVersion func(saved, now string) bool
	// Disclosure is shown when this provider can run headless; Kinds are its own failure kinds.
	Disclosure string
	Kinds      map[string]string
}

// Prepared is one run's binary, argv, extra environment for its private folder and cleanup.
type Prepared struct {
	Bin     string
	Args    []string
	Env     func(dir string) []string
	Cleanup func()
}

// sameVersion applies id's version policy; unknown providers compare major versions.
func sameVersion(id, saved, now string) bool {
	if a := adapterOf(id); a != nil && a.SameVersion != nil {
		return a.SameVersion(saved, now)
	}
	return major(now) != "" && major(now) == major(saved)
}

func major(version string) string {
	m, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), ".")
	if _, err := strconv.Atoi(m); err != nil {
		return ""
	}
	return m
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
	if kind == "" && c.Gate != nil {
		return c.Gate(ctx, st)
	}
	if kind != "" {
		st.Reason, st.Message = kind, Message(kind)
	}
	if kind == KindNotEligible && c.Handoff != "" {
		st.Message += " " + c.Handoff
	}
	return st
}

// Run is the one run pipeline for every provider. It re-detects first (gates included), so a
// provider removed, updated or no longer passing a gate since Settings showed it is refused;
// then it prepares the run and starts it once in a fresh private folder.
func (c *CLI) Run(ctx context.Context, payload []byte) (string, error) {
	st := c.Detect(ctx)
	if st.Mode != ModeHeadless {
		return "", &Failure{Kind: st.Reason}
	}
	p := Prepared{Bin: st.Path, Args: c.RunArgs}
	if c.Prepare != nil {
		var err error
		if p, err = c.Prepare(ctx, st); err != nil {
			return "", err
		}
	}
	if p.Cleanup != nil {
		defer p.Cleanup()
	}
	// The global allowlist accepts every registered provider's shapes; a run must use its own.
	if own := c.Argvs; (own == nil && !slices.Equal(p.Args, c.RunArgs)) || (own != nil && !own(p.Args)) {
		return "", &Failure{Kind: KindRefusedArgs}
	}
	dir, err := privateDir()
	if err != nil {
		return "", &Failure{Kind: KindFailed}
	}
	defer os.RemoveAll(dir)
	env := c.Environ(os.Environ())
	if p.Env != nil {
		env = append(env, p.Env(dir)...)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = runTimeout
	}
	r, err := runProc(ctx, command{path: p.Bin, args: p.Args, dir: dir, env: env, stdin: payload, limit: maxReply, timeout: timeout, stop: c.Stop})
	switch {
	case err != nil:
		return "", err
	case r.overflow:
		return "", &Failure{Kind: KindOutputTooLarge}
	}
	reply, kind := c.Reply(r.stdout, r.stderr, r.exitOK && !r.stopped)
	if kind == "" && r.stopped {
		kind = KindFailed
	}
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
	dir, err := privateDir()
	if err != nil {
		return nil, nil, false, false, &Failure{Kind: KindFailed}
	}
	defer os.RemoveAll(dir)
	r, err := runProc(ctx, command{path: path, args: args, dir: dir, env: c.Environ(os.Environ()), stdin: stdin, limit: limit, timeout: timeout})
	return r.stdout, r.stderr, r.exitOK, r.overflow, err
}

// command is one process start. stop, when set, sees every complete stdout line; returning true
// kills the process group at once and the result is marked stopped.
type command struct {
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

// allowedArgv is the allowlist every process start passes: the exact argv shapes the registered
// providers run. A helper path allows only its one argv. Substituted values (e.g. Codex's
// catalog path and self-check URL) are validated by the provider's Argvs.
func allowedArgv(path string, args []string) bool {
	for _, a := range adapters {
		if want, ok := a.Helpers[path]; ok {
			return slices.Equal(args, want)
		}
	}
	for _, a := range adapters {
		// A provider with Argvs has a RunArgs template (e.g. a placeholder to fill), so only Argvs
		// may accept its run shapes.
		if slices.Equal(args, a.VersionArgs) || (a.Argvs == nil && slices.Equal(args, a.RunArgs)) || (a.Argvs != nil && a.Argvs(args)) {
			return true
		}
	}
	return false
}

// privateDir is a fresh empty folder with symbolic links resolved (so a Git ceiling at its
// parent matches what the child sees). Every provider process runs in one.
func privateDir() (string, error) {
	dir, err := os.MkdirTemp("", "hclaro-ai-")
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return real, nil
}

// runProc starts one allowlisted process; anything else is refused before exec.
func runProc(ctx context.Context, p command) (procResult, error) {
	if !allowedArgv(p.path, p.args) {
		return procResult{}, &Failure{Kind: KindRefusedArgs}
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	cmd := exec.CommandContext(runCtx, p.path, p.args...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	cmd.Stdin = bytes.NewReader(p.stdin)
	// Own process group: cancel, timeout and stop kill the assistant and anything it started.
	proc.PrepareGroup(cmd)
	cmd.Cancel = func() error { return proc.KillGroup(cmd.Process) }
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

func classify(text string) string {
	s := strings.ToLower(text)
	if strings.Contains(s, "not logged in") || strings.Contains(s, "/login") || strings.Contains(s, "api key") || strings.Contains(s, "unauthorized") {
		return KindAuth
	}
	return KindFailed
}
