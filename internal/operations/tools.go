package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

// ToolRequest is one project utility pinned to a Hub context: project, root, engine
// executable, environment and workspace. The caller holds a shared claim on the root;
// Release gives it up once the command ends.
type ToolRequest struct {
	ProjectID string
	ContextID string
	Root      string
	Engine    string
	// Workspace is the context's UI workspace. Only providers and outputs pass it to the
	// engine, as the child's TF_WORKSPACE.
	Workspace string
	Tool      engine.Tool
	Recursive bool
	Tools     engine.Tools
	// Environ is the pinned child environment; it is used here only to recognize secret
	// values the engine might echo.
	Environ []string
	Notices []string
	// Files are the configuration files fmt may report, as slash-separated paths relative to
	// Root. Paths outside this set are counted, never shown.
	Files   map[string]bool
	Folders int
	Skipped []string
	// Revalidate runs before the command starts. A non-empty code refuses to run.
	Revalidate func() string
	Release    func()
}

// ToolResult is the sanitized result of a project utility. Outcome is "passed", "findings"
// (the check ran and found problems) or "not_checked" (the check did not complete, so
// nothing can be concluded).
type ToolResult struct {
	Tool      engine.Tool `json:"tool"`
	ContextID string      `json:"contextId"`
	Workspace string      `json:"workspace"`
	// UsesWorkspace is true when the engine received Workspace as TF_WORKSPACE.
	UsesWorkspace bool   `json:"usesWorkspace"`
	EngineVersion string `json:"engineVersion,omitempty"`
	Outcome       string `json:"outcome"`
	// Output is "json" or "human" for validate.
	Output string `json:"output,omitempty"`
	// Partial is set when results were read from a command that failed.
	Partial   bool             `json:"partial,omitempty"`
	Valid     *bool            `json:"valid,omitempty"`
	Errors    int              `json:"errors"`
	Warnings  int              `json:"warnings"`
	Findings  []Finding        `json:"findings"`
	Format    *FormatResult    `json:"format,omitempty"`
	Providers *ProvidersResult `json:"providers,omitempty"`
	Outputs   *OutputsResult   `json:"outputs,omitempty"`
}

func (t *ToolResult) clone() *ToolResult {
	c := *t
	c.Findings = slices.Clone(t.Findings)
	if t.Valid != nil {
		v := *t.Valid
		c.Valid = &v
	}
	if t.Format != nil {
		f := *t.Format
		f.Unformatted, f.Skipped = slices.Clone(f.Unformatted), slices.Clone(f.Skipped)
		c.Format = &f
	}
	if t.Providers != nil {
		p := *t.Providers
		p.Configuration, p.State = slices.Clone(p.Configuration), slices.Clone(p.State)
		c.Providers = &p
	}
	if t.Outputs != nil {
		o := *t.Outputs
		o.Items = slices.Clone(o.Items)
		c.Outputs = &o
	}
	return &c
}

// Finding is one sanitized diagnostic. File is relative to the project root and is shown
// as text only; Hclaro never opens it.
type Finding struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

// FormatResult lists the files fmt -check reported. Checked counts the configuration files
// Hclaro enumerated before running; Skipped lists symbolic links that were not traversed.
type FormatResult struct {
	Recursive   bool     `json:"recursive"`
	Checked     int      `json:"checked"`
	Folders     int      `json:"folders"`
	Unformatted []string `json:"unformatted"`
	Hidden      int      `json:"hidden"`
	Skipped     []string `json:"skipped"`
}

// ProviderRequirement is one provider line of `providers`. Module is empty for the root.
type ProviderRequirement struct {
	Source     string `json:"source"`
	Constraint string `json:"constraint,omitempty"`
	Module     string `json:"module,omitempty"`
}

// ProvidersResult is the parsed `providers` tree. Hidden counts lines Hclaro did not show.
type ProvidersResult struct {
	Configuration []ProviderRequirement `json:"configuration"`
	State         []ProviderRequirement `json:"state"`
	Hidden        int                   `json:"hidden"`
}

// OutputsResult is output metadata. Values are never part of it.
type OutputsResult struct {
	Items  []engine.OutputMeta `json:"items"`
	Hidden int                 `json:"hidden"`
}

// ErrToolRunning refuses a second run of the same tool in the same context while one runs;
// ErrToolsBusy bounds how many project utilities run at once.
var (
	ErrToolRunning = errors.New("this tool is already running for this workspace")
	ErrToolsBusy   = errors.New("too many project tools are running")
)

const (
	maxRunningTools = 8
	maxFindings     = 200
	maxListed       = 1000
)

func active(status string) bool {
	return status == Queued || status == Running || status == Cancelling
}

// ToolCommand is the argv shown to the user, without the executable path.
func ToolCommand(engineName string, tool engine.Tool, json, recursive bool) []string {
	return append([]string{engineName}, engine.ToolArgs(tool, json, recursive)...)
}

var toolMessages = map[string]string{
	"unsafe_cli_args":   "Inherited TF_CLI_ARGS or a command-specific TF_CLI_ARGS_* setting adds flags other than -no-color, which would change this read-only command. Nothing was run. Restart Hclaro without them.",
	"engine_version":    "Unable to read a supported engine version. Use stable Terraform 1.5–1.17 or OpenTofu 1.6–1.13. Nothing was run.",
	"path_changed":      "The project folder or its engine changed after this workspace was opened. Nothing was run. Open the workspace again.",
	"workspace_missing": "The engine reported that this workspace does not exist. List workspaces again on the project page.",
	"init_required":     "The engine needs this project to be initialized first. Nothing was checked. Review and run Init from the project page, or run init in a terminal.",
	"plugins_missing":   "Required providers or modules are not installed, or do not match the lock file. Nothing was checked. Run Init from the project page; Hclaro never upgrades providers.",
	"version_mismatch":  "The configuration requires a different engine version. Install a matching Terraform/OpenTofu version or change this project's engine.",
	"state_locked":      "The state is locked by another operation. Hclaro never force-unlocks state. Try again when it finishes.",
	"credentials":       "The engine could not authenticate with the backend. Refresh your credentials (for example, an SSO login) in your terminal, then try again.",
	"network":           "The backend was unreachable. Check network and VPN access, then try again.",
	"unsupported_flag":  "This engine version does not accept the flags Hclaro uses for this command. Nothing was checked.",
	"output_unreadable": "The engine's output was not in the expected format, so Hclaro shows no result. Nothing was concluded.",
	"output_too_large":  "The engine printed more output than Hclaro keeps, so the result is incomplete and is not shown.",
	"format_error":      "The engine could not read some configuration files, so the format check did not complete. Fix the errors shown, then check again.",
	"tool_failed":       "The command failed. Review the diagnostics; lines that look like secrets are withheld.",
	"cancelled":         "Cancelled. The engine was interrupted; partial output was discarded and no result is shown. Nothing was changed.",
	"timeout":           "The command exceeded its time limit and was stopped. Partial output was discarded. Nothing was changed.",
	"shutdown":          "Hclaro stopped while the command was running. No result is shown.",
	"validation_errors": "The configuration is not valid. Validate checks syntax and internal consistency only; it does not contact providers or the backend.",
	"unformatted":       "Some files are not in canonical format. Hclaro checks only; it never rewrites files. Run fmt yourself to format them.",
}

func toolSuccess(t engine.Tool, r *ToolResult) string {
	switch t {
	case engine.ToolValidate:
		return "The configuration is valid. This checks syntax and internal consistency only: it is not a plan, does not detect drift and does not prove an apply would succeed."
	case engine.ToolFormat:
		return fmt.Sprintf("All %d checked files are in canonical format. Nothing was written.", r.Format.Checked)
	case engine.ToolProviders:
		return "Listed provider requirements. Requirements say which providers the configuration and state need; they do not prove which cloud account or identity is in use."
	}
	if n := len(r.Outputs.Items); n > 0 {
		return fmt.Sprintf("Read the names, types and sensitivity of %d outputs. Values were discarded inside Hclaro and never displayed.", n)
	}
	return "This workspace's state records no root module outputs."
}

// StartTool records a project utility and runs it in the background. A second run of the
// same tool in the same context is refused while one is active.
func (r *Inits) StartTool(req ToolRequest) (Operation, error) {
	op := Operation{
		Kind: "tool", Command: ToolCommand(req.Engine, req.Tool, true, req.Recursive), ProjectID: req.ProjectID, Notices: slices.Clone(req.Notices),
		Tool: &ToolResult{Tool: req.Tool, ContextID: req.ContextID, Workspace: req.Workspace, UsesWorkspace: engine.WorkspaceDependent(req.Tool), Outcome: "not_checked", Findings: []Finding{}},
	}
	conflict := func(entries []*initEntry) error {
		running := 0
		for _, e := range entries {
			if e.op.Kind != "tool" || !active(e.op.Status) {
				continue
			}
			running++
			if e.op.Tool.ContextID == req.ContextID && e.op.Tool.Tool == req.Tool {
				return ErrToolRunning
			}
		}
		if running >= maxRunningTools {
			return ErrToolsBusy
		}
		return nil
	}
	return r.begin(op, req.Release, conflict, func(ctx context.Context, e *initEntry) { r.runTool(ctx, e, req) })
}

func (r *Inits) runTool(ctx context.Context, e *initEntry, req ToolRequest) {
	code, exitCode := r.executeTool(ctx, e, req)
	status := Failed
	switch code {
	case "succeeded":
		status = Succeeded
	case "cancelled", "shutdown":
		status = Cancelled
	}
	r.mu.Lock()
	o := &e.op
	o.Status, o.Stage, o.ExitCode = status, "Finished", exitCode
	o.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	o.DurationMs = time.Since(e.start).Milliseconds()
	message := toolMessages[code]
	if code == "succeeded" {
		message = toolSuccess(req.Tool, o.Tool)
	} else {
		o.Diagnostic = &Diagnostic{Code: code, Message: message}
	}
	if status != Succeeded && code != "validation_errors" && code != "unformatted" {
		o.Tool.Outcome = "not_checked"
		o.Tool.Valid = nil
	}
	o.LogDropped = e.log.Dropped()
	r.mu.Unlock()
	e.release()
	level := "error"
	if status == Succeeded {
		level = "info"
	}
	e.log.Append(level, message)
	e.log.Finish()
}

// executeTool returns a code from toolMessages (or "succeeded") and the engine exit code
// when the command ran to completion.
func (r *Inits) executeTool(ctx context.Context, e *initEntry, req ToolRequest) (code string, exitCode *int) {
	for _, n := range req.Notices {
		e.log.Append("warn", n)
	}
	r.stage(e, "Checking engine version")
	version, err := req.Tools.Version(ctx)
	switch {
	case ctx.Err() != nil:
		return r.stopped(e), nil
	case errors.Is(err, engine.ErrUnsafeArgs):
		return "unsafe_cli_args", nil
	case err != nil:
		return "engine_version", nil
	}
	r.update(e, func(o *Operation) { o.Tool.EngineVersion = version })
	r.stage(e, "Checking project")
	if code := req.Revalidate(); code != "" {
		return code, nil
	}
	if ctx.Err() != nil {
		return r.stopped(e), nil
	}
	k := initKnown(req.Root, req.Environ)
	run := toolRun{r: r, e: e, k: k}
	switch req.Tool {
	case engine.ToolValidate:
		return run.validate(ctx, req)
	case engine.ToolFormat:
		return run.format(ctx, req)
	case engine.ToolProviders:
		return run.providers(ctx, req)
	case engine.ToolOutputs:
		return run.outputs(ctx, req)
	}
	return "tool_failed", nil
}

func (r *Inits) stopped(e *initEntry) string {
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

// toolRun holds what one utility run needs to log sanitized output.
type toolRun struct {
	r *Inits
	e *initEntry
	k *known
	// shown keeps a bounded copy of sanitized lines for classification; raw output is never kept.
	shown strings.Builder
}

func (t *toolRun) update(change func(*ToolResult)) {
	t.r.update(t.e, func(o *Operation) { change(o.Tool) })
}

// failed maps an error from a capture to a code.
func (t *toolRun) failed(ctx context.Context, err error) string {
	switch {
	case ctx.Err() != nil || errors.Is(err, engine.ErrCancelled):
		return t.r.stopped(t.e)
	case errors.Is(err, engine.ErrUnsafeArgs):
		return "unsafe_cli_args"
	}
	return "tool_failed"
}

// human logs sanitized human-readable lines and returns the diagnostics among them. "on FILE
// line N" lines attach a location to the preceding diagnostic.
func (t *toolRun) human(stream string, b []byte) []Finding {
	findings := []Finding{}
	for _, raw := range strings.Split(string(b), "\n") {
		level, text, diag := humanLine(stream, raw, t.k)
		if text == "" {
			continue
		}
		t.e.log.Append(level, text)
		if t.shown.Len() < 64<<10 {
			t.shown.WriteString(text + "\n")
		}
		if diag != nil {
			findings = append(findings, Finding{Severity: diag.Severity, Summary: diag.Summary})
			continue
		}
		if m := humanLocation.FindStringSubmatch(text); m != nil && len(findings) > 0 && findings[len(findings)-1].File == "" {
			if file := localFile(m[1]); file != "" {
				findings[len(findings)-1].File = file
				findings[len(findings)-1].Line, _ = strconv.Atoi(m[2])
			}
		} else if len(findings) > 0 && findings[len(findings)-1].Detail == "" && !strings.HasPrefix(text, "on ") {
			findings[len(findings)-1].Detail = text
		}
	}
	return findings
}

var humanLocation = regexp.MustCompile(`^on (\S+) line (\d+)`)

// localFile returns a diagnostic's file name when it is a plain path inside the project
// root; anything else is dropped.
func localFile(name string) string {
	name = filepath.ToSlash(name)
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\n\r") || !filepath.IsLocal(filepath.FromSlash(name)) || state.Suspicious(name) {
		return ""
	}
	return path.Clean(name)
}

func (t *toolRun) addFindings(findings []Finding) {
	t.update(func(r *ToolResult) {
		for _, f := range findings {
			switch f.Severity {
			case "error":
				r.Errors++
			case "warning":
				r.Warnings++
			}
			if len(r.Findings) < maxFindings {
				r.Findings = append(r.Findings, f)
			}
		}
	})
}

func exitOf(out engine.Capture) *int {
	c := out.ExitCode
	return &c
}

// initClass reports failures that mean the check could not run at all.
func initClass(class string) bool {
	switch class {
	case "init_required", "plugins_missing", "version_mismatch", "unsupported_flag":
		return true
	}
	return false
}

type validateDoc struct {
	Valid       *bool `json:"valid"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
		Range    *struct {
			Filename string `json:"filename"`
			Start    struct {
				Line   int `json:"line"`
				Column int `json:"column"`
			} `json:"start"`
		} `json:"range"`
	} `json:"diagnostics"`
}

// parseValidate reads `validate -json`. Plain text before the document (for example a
// failure printed before JSON output started) is skipped. The snippet field, which quotes
// configuration source, is never decoded.
func parseValidate(b []byte) (validateDoc, bool) {
	for offset := 0; offset < len(b); {
		i := bytes.IndexByte(b[offset:], '{')
		if i < 0 {
			break
		}
		start := offset + i
		if start == 0 || b[start-1] == '\n' {
			var doc validateDoc
			if json.NewDecoder(bytes.NewReader(b[start:])).Decode(&doc) == nil && doc.Valid != nil {
				return doc, true
			}
		}
		offset = start + 1
	}
	return validateDoc{}, false
}

func (t *toolRun) validate(ctx context.Context, req ToolRequest) (string, *int) {
	t.r.stage(t.e, "Running validate")
	out, err := req.Tools.Validate(ctx, true)
	if err != nil {
		return t.failed(ctx, err), nil
	}
	t.update(func(r *ToolResult) { r.Output = "json" })
	if out.Truncated {
		return "output_too_large", exitOf(out)
	}
	stderr := t.human("stderr", out.Stderr)
	doc, ok := parseValidate(out.Stdout)
	if !ok {
		class := engine.ClassifyTool(t.shown.String())
		if class != "" && class != "unsupported_flag" {
			t.addFindings(stderr)
			return class, exitOf(out)
		}
		return t.validateHuman(ctx, req)
	}
	findings := []Finding{}
	var text strings.Builder
	for _, d := range doc.Diagnostics {
		f := Finding{Severity: d.Severity, Summary: sanitize(d.Summary, t.k, 1), Detail: sanitize(d.Detail, t.k, 6)}
		if f.Severity != "error" && f.Severity != "warning" {
			f.Severity = "warning"
		}
		if d.Range != nil {
			if f.File = localFile(d.Range.Filename); f.File != "" {
				f.Line, f.Column = max(d.Range.Start.Line, 0), max(d.Range.Start.Column, 0)
			}
		}
		text.WriteString(f.Summary + "\n" + f.Detail + "\n")
		label := map[string]string{"error": "Error: ", "warning": "Warning: "}[f.Severity]
		where := ""
		if f.File != "" {
			where = fmt.Sprintf(" (%s:%d)", f.File, f.Line)
		}
		t.e.log.Append(map[string]string{"error": "error", "warning": "warn"}[f.Severity], label+f.Summary+where)
		findings = append(findings, f)
	}
	t.addFindings(findings)
	if *doc.Valid {
		if out.ExitCode != 0 {
			return "tool_failed", exitOf(out)
		}
		valid := true
		t.update(func(r *ToolResult) { r.Valid, r.Outcome = &valid, "passed" })
		return "succeeded", exitOf(out)
	}
	if class := engine.ClassifyTool(text.String()); initClass(class) {
		return class, exitOf(out)
	}
	invalid := false
	t.update(func(r *ToolResult) { r.Valid, r.Outcome = &invalid, "findings" })
	return "validation_errors", exitOf(out)
}

// validateHuman is the fallback when validate printed no readable JSON document.
func (t *toolRun) validateHuman(ctx context.Context, req ToolRequest) (string, *int) {
	t.e.log.Append("warn", "The engine printed no machine-readable validate result. Running validate again with human-readable output.")
	t.r.update(t.e, func(o *Operation) {
		o.Command = ToolCommand(req.Engine, engine.ToolValidate, false, false)
		o.Tool.Output = "human"
	})
	t.shown.Reset()
	out, err := req.Tools.Validate(ctx, false)
	if err != nil {
		return t.failed(ctx, err), nil
	}
	if out.Truncated {
		return "output_too_large", exitOf(out)
	}
	findings := append(t.human("stdout", out.Stdout), t.human("stderr", out.Stderr)...)
	t.addFindings(findings)
	errorsFound := slices.ContainsFunc(findings, func(f Finding) bool { return f.Severity == "error" })
	switch class := engine.ClassifyTool(t.shown.String()); {
	case out.ExitCode == 0 && !errorsFound:
		valid := true
		t.update(func(r *ToolResult) { r.Valid, r.Outcome = &valid, "passed" })
		return "succeeded", exitOf(out)
	case initClass(class):
		return class, exitOf(out)
	case out.ExitCode != 0 && errorsFound:
		invalid := false
		t.update(func(r *ToolResult) { r.Valid, r.Outcome = &invalid, "findings" })
		return "validation_errors", exitOf(out)
	case class != "":
		return class, exitOf(out)
	}
	return "output_unreadable", exitOf(out)
}

func (t *toolRun) format(ctx context.Context, req ToolRequest) (string, *int) {
	t.update(func(r *ToolResult) {
		r.Format = &FormatResult{Recursive: req.Recursive, Checked: len(req.Files), Folders: req.Folders, Unformatted: []string{}, Skipped: slices.Clone(req.Skipped)}
	})
	t.r.stage(t.e, "Running fmt -check")
	out, err := req.Tools.FormatCheck(ctx, req.Recursive)
	if err != nil {
		return t.failed(ctx, err), nil
	}
	if out.Truncated {
		return "output_too_large", exitOf(out)
	}
	listed, hidden := []string{}, 0
	for _, line := range strings.Split(string(out.Stdout), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		name := localFile(line)
		switch {
		case name == "" || !req.Files[name]:
			hidden++
		case len(listed) < maxListed:
			listed = append(listed, name)
		default:
			hidden++
		}
	}
	t.update(func(r *ToolResult) { r.Format.Unformatted, r.Format.Hidden = listed, hidden })
	for _, name := range listed {
		t.e.log.Append("warn", "Not in canonical format: "+name)
	}
	findings := t.human("stderr", out.Stderr)
	t.addFindings(findings)
	errorsFound := slices.ContainsFunc(findings, func(f Finding) bool { return f.Severity == "error" })
	switch {
	case errorsFound:
		if class := engine.ClassifyTool(t.shown.String()); class != "" {
			return class, exitOf(out)
		}
		t.update(func(r *ToolResult) { r.Partial = len(listed)+hidden > 0 })
		return "format_error", exitOf(out)
	case len(listed)+hidden > 0:
		t.update(func(r *ToolResult) { r.Outcome = "findings" })
		return "unformatted", exitOf(out)
	case out.ExitCode == 0:
		t.update(func(r *ToolResult) { r.Outcome = "passed" })
		return "succeeded", exitOf(out)
	}
	if class := engine.ClassifyTool(t.shown.String()); class != "" {
		return class, exitOf(out)
	}
	return "tool_failed", exitOf(out)
}

var (
	providerLine  = regexp.MustCompile(`^provider\[([^\]\s]{1,200})\](?:\s+(.{1,200}))?$`)
	providerSrc   = regexp.MustCompile(`^(?:[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?(?::[0-9]{1,5})?/)?[A-Za-z0-9][A-Za-z0-9_-]{0,63}/[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	constraintRe  = regexp.MustCompile(`^[0-9A-Za-z.,<>=~! +-]{1,120}$`)
	moduleLine    = regexp.MustCompile(`^module\.([A-Za-z_][A-Za-z0-9_-]{0,63})$`)
	treeDecorator = "│├└─  "
)

// ParseProviders reads the tree printed by `providers`. Module nesting comes from the tree
// indentation (four columns per level). Lines that are not recognized are returned for
// sanitized logging.
func ParseProviders(b []byte) (ProvidersResult, []string) {
	res := ProvidersResult{Configuration: []ProviderRequirement{}, State: []ProviderRequirement{}}
	other := []string{}
	section := ""
	modules := []string{}
	for _, raw := range strings.Split(string(b), "\n") {
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		lower := strings.ToLower(trimmed)
		switch {
		case trimmed == "" || trimmed == ".":
			continue
		case strings.HasPrefix(lower, "providers required by configuration"):
			section, modules = "configuration", modules[:0]
			continue
		case strings.HasPrefix(lower, "providers required by state"):
			section = "state"
			continue
		}
		body := strings.TrimLeft(raw, treeDecorator)
		depth := utf8.RuneCountInString(raw[:len(raw)-len(body)]) / 4
		if m := moduleLine.FindStringSubmatch(body); m != nil && section == "configuration" {
			if depth < 1 || depth > 32 {
				res.Hidden++
				continue
			}
			modules = append(modules[:min(depth-1, len(modules))], m[1])
			continue
		}
		m := providerLine.FindStringSubmatch(body)
		if m == nil || section == "" {
			other = append(other, raw)
			continue
		}
		constraint := strings.TrimSpace(m[2])
		if !providerSrc.MatchString(m[1]) || constraint != "" && !constraintRe.MatchString(constraint) {
			res.Hidden++
			continue
		}
		req := ProviderRequirement{Source: m[1], Constraint: constraint}
		if section == "configuration" {
			parents := modules[:min(max(depth-1, 0), len(modules))]
			if len(parents) > 0 {
				req.Module = "module." + strings.Join(parents, ".module.")
			}
			if len(res.Configuration) < maxListed {
				res.Configuration = append(res.Configuration, req)
			} else {
				res.Hidden++
			}
		} else if len(res.State) < maxListed {
			res.State = append(res.State, req)
		} else {
			res.Hidden++
		}
	}
	return res, other
}

func (t *toolRun) providers(ctx context.Context, req ToolRequest) (string, *int) {
	t.r.stage(t.e, "Running providers")
	out, err := req.Tools.Providers(ctx)
	if err != nil {
		return t.failed(ctx, err), nil
	}
	if out.Truncated {
		return "output_too_large", exitOf(out)
	}
	parsed, other := ParseProviders(out.Stdout)
	findings := append(t.human("stdout", []byte(strings.Join(other, "\n"))), t.human("stderr", out.Stderr)...)
	t.addFindings(findings)
	found := len(parsed.Configuration)+len(parsed.State) > 0
	t.update(func(r *ToolResult) { r.Providers = &parsed })
	if out.ExitCode == 0 {
		t.update(func(r *ToolResult) { r.Outcome = "passed" })
		return "succeeded", exitOf(out)
	}
	t.update(func(r *ToolResult) { r.Partial = found })
	if class := engine.ClassifyTool(t.shown.String()); class != "" {
		return class, exitOf(out)
	}
	return "tool_failed", exitOf(out)
}

func (t *toolRun) outputs(ctx context.Context, req ToolRequest) (string, *int) {
	t.r.stage(t.e, "Reading output metadata")
	outs, err := req.Tools.OutputsMetadata(ctx)
	var exit *engine.ExitError
	switch {
	case errors.As(err, &exit):
		code := exit.Code
		if exit.Hint != "" {
			return exit.Hint, &code
		}
		return "tool_failed", &code
	case errors.Is(err, engine.ErrUnreadableOutputs):
		zero := 0
		return "output_unreadable", &zero
	case err != nil:
		return t.failed(ctx, err), nil
	}
	t.update(func(r *ToolResult) {
		r.Outputs = &OutputsResult{Items: outs.Items, Hidden: outs.Hidden}
		r.Outcome = "passed"
	})
	if outs.Hidden > 0 {
		noun := "output name that looks like a secret or uses unusual characters is"
		if outs.Hidden > 1 {
			noun = "output names that look like secrets or use unusual characters are"
		}
		t.e.log.Append("warn", fmt.Sprintf("%d %s not shown.", outs.Hidden, noun))
	}
	zero := 0
	return "succeeded", &zero
}
