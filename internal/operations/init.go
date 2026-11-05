package operations

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

// Statuses used by initialization in addition to Running, Succeeded, Failed and Cancelled.
const (
	Queued     = "queued"
	Cancelling = "cancelling"
	Unknown    = "unknown"
)

// InitRequest is a reviewed, pinned initialization of one project root. The caller has
// already claimed the root exclusively; Release gives the claim up once the operation ends.
type InitRequest struct {
	ProjectID string
	Root      string
	Engine    string
	Initer    engine.Initer
	// Environ is the pinned child environment; it is used here only to recognize secret
	// values the engine might echo.
	Environ []string
	// Notices disclose inherited settings that were ignored or that affect init.
	Notices []string
	// Revalidate runs just before the engine starts. A non-empty code refuses to run.
	Revalidate func() string
	// Initialized reports whether initialization metadata exists after a zero exit.
	Initialized func() bool
	// Lockfile reads .terraform.lock.hcl, or returns nil when it does not exist.
	Lockfile func() []byte
	// Finished runs after the outcome is recorded, for example to refresh cached metadata.
	Finished func()
	Release  func()
}

// Inits runs Hub project operations: initialization and workspace commands. Different roots
// may run in parallel; the root claim keeps one operation per root.
type Inits struct {
	mu      sync.Mutex
	entries []*initEntry
	base    context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
	NewID   func() string
}

type initEntry struct {
	entry
	projectID string
}

const (
	maxInits    = 50
	maxInitLogs = 10
)

func NewInits() *Inits {
	base, stop := context.WithCancel(context.Background())
	return &Inits{base: base, stop: stop, NewID: newID}
}

var initMessages = map[string]string{
	"succeeded":         "Initialization finished. Workspaces and state can now be read; nothing else was run.",
	"unsafe_cli_args":   "Inherited TF_CLI_ARGS or TF_CLI_ARGS_init adds flags other than -no-color. Hidden flags such as -upgrade or -reconfigure would change what init does, so Hclaro refused to run it. Restart Hclaro without them. Values were not logged.",
	"engine_version":    "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13. Nothing was initialized.",
	"path_changed":      "The project folder, its engine or its environment changed after you reviewed initialization. Nothing was run. Review the project again.",
	"backend_migration": "The backend configuration changed, so the engine asks to migrate or reconfigure state. Hclaro never runs init with -migrate-state, -force-copy or -reconfigure. Review the backend change and run the command you intend yourself in a terminal. Nothing was migrated.",
	"input_required":    "Init needs interactive input, such as a backend setting or a variable value. Hclaro runs init with -input=false and never asks for secrets. Provide the value through your usual configuration and run init in a terminal, or retry here once it no longer needs input.",
	"state_locked":      "The state is locked by another operation. Hclaro never force-unlocks state. Wait for the other operation to finish, then retry.",
	"lockfile_upgrade":  "The dependency lock file does not allow the provider versions the configuration requires. Changing it needs init -upgrade, which Hclaro does not run. Review the version constraints and run init -upgrade yourself if that is intended.",
	"version_mismatch":  "The configuration requires a different engine version. Install a matching Terraform/OpenTofu version or change this project's engine, then retry.",
	"registry":          "A provider or module could not be downloaded from its registry. Check network and VPN access, registry mirrors in your CLI configuration and the source addresses, then retry.",
	"credentials":       "The engine could not authenticate with the backend or a registry. Refresh your credentials (for example, an SSO login) in your terminal, then retry. Hclaro inherits credentials and never stores them.",
	"network":           "A backend or registry endpoint was unreachable. Check network and VPN access, then retry.",
	"backend_config":    "The backend configuration is incomplete or was rejected. Hclaro does not edit backend settings or accept -backend-config values. Fix the backend block, or run init with your backend configuration in a terminal.",
	"init_failed":       "Initialization failed. Review the diagnostics; lines that look like secrets are withheld.",
	"timeout":           "Initialization exceeded its time limit and was stopped. Downloads under the data directory may be incomplete; running init again completes or replaces them.",
	"cancelled":         "Initialization was cancelled. Partly downloaded providers or modules may remain under the data directory, and the lock file may already have been written; running init again completes or replaces them.",
	"shutdown":          "Hclaro stopped while initialization was running. Downloads under the data directory may be incomplete.",
	"unverified":        "The engine exited successfully, but Hclaro could not find the initialized data directory afterwards. Check the project with your CLI before relying on it.",
}

// InitCommand is the argv shown to the user, without the executable path.
func InitCommand(engineName string, json bool) []string {
	return append([]string{engineName}, engine.InitArgs(json)...)
}

// Start records the operation and runs it in the background. The engine starts only after
// its version is supported and Revalidate passes.
func (r *Inits) Start(req InitRequest) (Operation, error) {
	op := Operation{
		Kind: "init", Command: InitCommand(req.Engine, false), ProjectID: req.ProjectID, Notices: slices.Clone(req.Notices),
	}
	return r.begin(op, req.Release, func(ctx context.Context, e *initEntry) { r.run(ctx, e, req) })
}

// begin records a queued operation for a project and runs it in the background. The caller
// has claimed the project root; release gives it up once the operation ends.
func (r *Inits) begin(op Operation, release func(), run func(context.Context, *initEntry)) (Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		release()
		return Operation{}, ErrClosed
	}
	ctx, cancel := context.WithCancel(r.base)
	op.ID, op.Status, op.Stage, op.StartedAt = r.NewID(), Queued, "Queued", time.Now().UTC().Format(time.RFC3339)
	op.VarFiles, op.Replace, op.Diagnostics, op.LogRetained = []string{}, []string{}, []EngineDiagnostic{}, true
	e := &initEntry{projectID: op.ProjectID, entry: entry{op: op, log: NewLog(logEvents, logBytes), cancel: cancel, start: time.Now(), release: release}}
	r.entries = append(r.entries, e)
	if len(r.entries) > maxInits {
		r.entries = r.entries[len(r.entries)-maxInits:]
	}
	for i := 0; i < len(r.entries)-maxInitLogs; i++ {
		if s := r.entries[i].op.Status; s != Queued && s != Running && s != Cancelling {
			r.entries[i].log = nil
			r.entries[i].op.LogRetained = false
		}
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer e.cancel()
		run(ctx, e)
	}()
	return r.snapshot(e), nil
}

func (r *Inits) update(e *initEntry, change func(*Operation)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(&e.op)
}

func (r *Inits) stage(e *initEntry, stage string) {
	r.update(e, func(o *Operation) {
		o.Stage = stage
		if o.Status == Queued {
			o.Status = Running
		}
	})
	e.log.Append("info", stage+"…")
}

func (r *Inits) run(ctx context.Context, e *initEntry, req InitRequest) {
	before := req.Lockfile()
	code, exitCode, ran := r.execute(ctx, e, req)
	status := Failed
	switch code {
	case "succeeded":
		status = Succeeded
	case "unverified":
		status = Unknown
	case "cancelled", "shutdown":
		status = Cancelled
	}
	var changed *bool
	if ran {
		c := !bytes.Equal(before, req.Lockfile())
		changed = &c
	}
	if req.Finished != nil {
		req.Finished()
	}
	r.mu.Lock()
	o := &e.op
	o.Status, o.Stage, o.ExitCode, o.LockfileChanged = status, "Finished", exitCode, changed
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	if code != "succeeded" {
		o.Diagnostic = &Diagnostic{Code: code, Message: initMessages[code]}
	}
	o.LogDropped = e.log.Dropped()
	r.mu.Unlock()
	e.release()
	if changed != nil && *changed {
		e.log.Append("warn", ".terraform.lock.hcl was created or changed. Review it before committing.")
	}
	level := "error"
	if status == Succeeded {
		level = "info"
	}
	e.log.Append(level, initMessages[code])
	e.log.Finish()
}

// execute returns a code from initMessages, the engine exit code when it ran to completion,
// and whether the engine was started.
func (r *Inits) execute(ctx context.Context, e *initEntry, req InitRequest) (code string, exitCode *int, ran bool) {
	stopped := func() string {
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case e.op.CancelRequested:
			return "cancelled"
		case r.closed:
			return "shutdown"
		}
		return "timeout"
	}
	for _, n := range req.Notices {
		e.log.Append("warn", n)
	}
	r.stage(e, "Checking engine version")
	version, err := req.Initer.Version(ctx)
	switch {
	case ctx.Err() != nil:
		return stopped(), nil, false
	case errors.Is(err, engine.ErrUnsafeArgs):
		return "unsafe_cli_args", nil, false
	case err != nil:
		return "engine_version", nil, false
	}
	json := engine.InitJSON(req.Engine, version)
	r.update(e, func(o *Operation) { o.Command = InitCommand(req.Engine, json) })
	r.stage(e, "Checking project")
	if code := req.Revalidate(); code != "" {
		return code, nil, false
	}
	if ctx.Err() != nil {
		return stopped(), nil, false
	}
	r.stage(e, "Initializing")
	k := initKnown(req.Root, req.Environ)
	label := engineLabel(req.Engine)
	undecoded := false
	// shown keeps a bounded tail of the sanitized lines for classification; raw output is
	// never kept.
	var shown strings.Builder
	diagnose := func(d *EngineDiagnostic) {
		r.update(e, func(o *Operation) {
			if len(o.Diagnostics) < maxDiags {
				o.Diagnostics = append(o.Diagnostics, *d)
			}
		})
	}
	err = req.Initer.Init(ctx, engine.InitRequest{JSON: json}, func(stream string, line []byte) {
		if json && stream == "stdout" {
			level, text, diag, ok := translate(line, "init", label, k)
			if !ok {
				if !undecoded {
					undecoded = true
					e.log.Append("warn", "Some engine output was not in the expected machine-readable format and was withheld.")
				}
				return
			}
			if text != "" {
				e.log.Append(level, text)
			}
			if diag != nil {
				diagnose(diag)
			}
			return
		}
		level, text, diag := humanLine(stream, string(line), k)
		if text != "" {
			e.log.Append(level, text)
			if shown.Len() < 64<<10 {
				shown.WriteString(text + "\n")
			}
		}
		if diag != nil {
			diagnose(diag)
		}
	})
	if err != nil {
		var exit *engine.ExitError
		switch {
		case ctx.Err() != nil || errors.Is(err, engine.ErrCancelled):
			return stopped(), nil, true
		case errors.Is(err, engine.ErrUnsafeArgs):
			return "unsafe_cli_args", nil, false
		case errors.As(err, &exit):
			code := exit.Code
			return r.classify(e, shown.String(), exit.Hint), &code, true
		}
		return "init_failed", nil, true
	}
	zero := 0
	if !req.Initialized() {
		return "unverified", &zero, true
	}
	return "succeeded", &zero, true
}

// classify prefers the sanitized diagnostics and log lines over the hint from the output tail.
func (r *Inits) classify(e *initEntry, shown, hint string) string {
	r.mu.Lock()
	var text strings.Builder
	text.WriteString(shown)
	for _, d := range e.op.Diagnostics {
		text.WriteString(d.Summary + "\n" + d.Detail + "\n")
	}
	r.mu.Unlock()
	if code := engine.ClassifyInit(text.String()); code != "" {
		return code
	}
	if hint != "" {
		return hint
	}
	return "init_failed"
}

// humanLine sanitizes one line of human-readable init output. Box-drawing borders around
// diagnostics are removed; "Error:" and "Warning:" lines also become diagnostics.
func humanLine(stream, line string, k *known) (level, text string, diag *EngineDiagnostic) {
	line = strings.TrimSpace(strings.TrimLeft(stripControls(line), "│╷╵ "))
	if line == "" {
		return "", "", nil
	}
	level = "info"
	if stream == "stderr" {
		level = "warn"
	}
	// The label is split off first so its colon does not count as an assignment.
	for prefix, severity := range map[string]string{"Error: ": "error", "Warning: ": "warning"} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			summary := sanitize(rest, k, 1)
			level = map[string]string{"error": "error", "warning": "warn"}[severity]
			return level, prefix + summary, &EngineDiagnostic{Severity: severity, Summary: summary}
		}
	}
	return level, sanitize(line, k, 1), nil
}

// initKnown adds values of credential-shaped environment variables (for example
// AWS_SECRET_ACCESS_KEY, ARM_CLIENT_SECRET or TF_TOKEN_*) to the usual known inputs: a
// backend or registry error may echo them.
func initKnown(root string, environ []string) *known {
	k := collectKnown(root, nil, environ)
	seen := map[string]bool{}
	for _, v := range k.values {
		seen[v] = true
	}
	for _, item := range environ {
		key, value, ok := strings.Cut(item, "=")
		if ok && len(value) >= minKnownValue && !seen[value] && len(k.values) < maxKnown && (state.RiskyKey(key) || strings.HasPrefix(key, "TF_TOKEN_")) {
			seen[value] = true
			k.values = append(k.values, value)
		}
	}
	sort.Slice(k.values, func(i, j int) bool { return len(k.values[i]) > len(k.values[j]) })
	return k
}

// Cancel requests cancellation of a queued or running initialization.
func (r *Inits) Cancel(id string) (Operation, error) {
	r.mu.Lock()
	e := r.find(id)
	if e == nil {
		r.mu.Unlock()
		return Operation{}, ErrNotFound
	}
	if s := e.op.Status; s != Queued && s != Running {
		op := r.snapshot(e)
		r.mu.Unlock()
		return op, ErrNotRunning
	}
	e.op.CancelRequested = true
	e.op.Status = Cancelling
	op := r.snapshot(e)
	r.mu.Unlock()
	e.log.Append("warn", "Cancellation requested. Interrupting the engine and anything it started…")
	e.cancel()
	return op, nil
}

func (r *Inits) find(id string) *initEntry {
	for _, e := range r.entries {
		if e.op.ID == id {
			return e
		}
	}
	return nil
}

func (r *Inits) snapshot(e *initEntry) Operation {
	op := e.op
	op.Command = slices.Clone(op.Command)
	op.Notices = slices.Clone(op.Notices)
	op.Diagnostics = slices.Clone(op.Diagnostics)
	if op.Workspace != nil {
		w := *op.Workspace
		w.Workspaces = slices.Clone(w.Workspaces)
		op.Workspace = &w
	}
	if op.LockfileChanged != nil {
		v := *op.LockfileChanged
		op.LockfileChanged = &v
	}
	if op.CompletedAt == "" {
		op.DurationMs = time.Since(e.start).Milliseconds()
	}
	if e.log != nil {
		op.LogDropped = e.log.Dropped()
	}
	return op
}

// Operation returns one initialization and its log, if it is still retained.
func (r *Inits) Operation(id string) (Operation, *Log, bool) {
	if r == nil {
		return Operation{}, nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.find(id)
	if e == nil {
		return Operation{}, nil, false
	}
	return r.snapshot(e), e.log, true
}

// Latest returns the most recent operation of one kind ("init" or "workspace") in a project.
func (r *Inits) Latest(projectID, kind string) (Operation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.entries) - 1; i >= 0; i-- {
		if r.entries[i].projectID == projectID && r.entries[i].op.Kind == kind {
			return r.snapshot(r.entries[i]), true
		}
	}
	return Operation{}, false
}

// Close cancels running initializations and waits, bounded, for them to stop.
func (r *Inits) Close(wait time.Duration) {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	}
}
