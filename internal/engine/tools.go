package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"hclaro/internal/state"
)

// Tool is one of the read-only project utilities. None of them writes configuration, state
// or the data directory: fmt runs only with -check, and output values are never returned.
type Tool string

const (
	ToolValidate  Tool = "validate"
	ToolFormat    Tool = "fmt-check"
	ToolProviders Tool = "providers"
	ToolOutputs   Tool = "outputs-metadata"
)

// ValidTool reports whether t is a project utility Hclaro runs.
func ValidTool(t Tool) bool {
	return t == ToolValidate || t == ToolFormat || t == ToolProviders || t == ToolOutputs
}

// WorkspaceDependent reports whether a tool reads a workspace's state, so it runs with the
// context's TF_WORKSPACE. validate and fmt read configuration only and get no TF_WORKSPACE.
func WorkspaceDependent(t Tool) bool { return t == ToolProviders || t == ToolOutputs }

// Tools runs the project utilities. It is a separate interface so the read-only state adapter
// stays minimal and no utility can be turned into init, plan, apply or plain fmt.
type Tools interface {
	Version(context.Context) (string, error)
	Validate(ctx context.Context, json bool) (Capture, error)
	FormatCheck(ctx context.Context, recursive bool) (Capture, error)
	Providers(ctx context.Context) (Capture, error)
	// OutputsMetadata returns output names, types and sensitive flags. Values are dropped
	// inside the adapter and never returned.
	OutputsMetadata(ctx context.Context) (Outputs, error)
}

// Capture is the bounded output of one completed utility command. A non-zero exit is not an
// error here: validate and fmt -check report findings through their exit codes.
type Capture struct {
	Stdout, Stderr []byte
	ExitCode       int
	// Truncated is set when output exceeded the capture bound; the rest was discarded.
	Truncated bool
}

// ToolArgs is the complete argv after the executable for a tool. Nothing from a request is
// appended except the fixed choice between JSON and human validate output and -recursive.
func ToolArgs(t Tool, json, recursive bool) []string {
	switch t {
	case ToolValidate:
		if json {
			return []string{"validate", "-json"}
		}
		return []string{"validate", "-no-color"}
	case ToolFormat:
		if recursive {
			return []string{"fmt", "-check", "-no-color", "-recursive"}
		}
		return []string{"fmt", "-check", "-no-color"}
	case ToolProviders:
		return []string{"providers"}
	case ToolOutputs:
		return []string{"output", "-json"}
	}
	return nil
}

const (
	toolLimit     = 5 * time.Minute
	maxToolStdout = 8 << 20
	maxToolStderr = 256 << 10
)

// ErrToolWorkspace refuses a workspace-dependent tool without a bound workspace.
var ErrToolWorkspace = errors.New("a workspace is required")

// capture runs one tool command to completion with the shared process-group, cancellation
// and environment policy. Workspace-dependent tools get the CLI's SelectedWorkspace as
// TF_WORKSPACE; the others get none.
func (c *CLI) capture(ctx context.Context, t Tool, args []string) (Capture, error) {
	selected := ""
	if WorkspaceDependent(t) {
		if c.SelectedWorkspace == "" {
			return Capture{}, ErrToolWorkspace
		}
		selected = c.SelectedWorkspace
	}
	out := Capture{}
	err := c.streamLines(ctx, toolLimit, true, selected, args, func(string) string { return "" }, func(stream string, line []byte) {
		buf, limit := &out.Stdout, maxToolStdout
		if stream == "stderr" {
			buf, limit = &out.Stderr, maxToolStderr
		}
		if len(*buf)+len(line)+1 > limit {
			out.Truncated = true
			return
		}
		*buf = append(append(*buf, line...), '\n')
	})
	var exit *ExitError
	if errors.As(err, &exit) {
		out.ExitCode = exit.Code
		return out, nil
	}
	return out, err
}

func (c *CLI) Validate(ctx context.Context, json bool) (Capture, error) {
	return c.capture(ctx, ToolValidate, ToolArgs(ToolValidate, json, false))
}

func (c *CLI) FormatCheck(ctx context.Context, recursive bool) (Capture, error) {
	return c.capture(ctx, ToolFormat, ToolArgs(ToolFormat, false, recursive))
}

func (c *CLI) Providers(ctx context.Context) (Capture, error) {
	return c.capture(ctx, ToolProviders, ToolArgs(ToolProviders, false, false))
}

// OutputsMetadata runs `output -json` and keeps only metadata. Raw output, which contains
// every value in plaintext, never leaves this function; stderr is used only to classify a
// failure.
func (c *CLI) OutputsMetadata(ctx context.Context) (Outputs, error) {
	out, err := c.capture(ctx, ToolOutputs, ToolArgs(ToolOutputs, false, false))
	if err != nil {
		return Outputs{}, err
	}
	return outputsFrom(out)
}

func outputsFrom(out Capture) (Outputs, error) {
	if out.ExitCode != 0 {
		return Outputs{}, &ExitError{Code: out.ExitCode, Hint: ClassifyTool(string(out.Stderr))}
	}
	if out.Truncated {
		return Outputs{}, ErrUnreadableOutputs
	}
	return ParseOutputs(out.Stdout)
}

// OutputMeta describes one root module output without its value.
type OutputMeta struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Sensitive bool   `json:"sensitive"`
}

// Outputs is the metadata of a workspace's root module outputs. Hidden counts outputs whose
// names Hclaro does not display.
type Outputs struct {
	Items  []OutputMeta
	Hidden int
}

// ErrUnreadableOutputs means `output -json` did not print the expected document.
var ErrUnreadableOutputs = errors.New("engine output metadata is unreadable")

const maxOutputs = 2000

var outputName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)

// ParseOutputs reads `output -json`. The value field is not part of the decoded struct, so
// values are skipped by the decoder and never stored.
func ParseOutputs(b []byte) (Outputs, error) {
	var doc map[string]struct {
		Sensitive bool            `json:"sensitive"`
		Type      json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
		return Outputs{}, ErrUnreadableOutputs
	}
	o := Outputs{Items: []OutputMeta{}}
	for name, v := range doc {
		if !outputName.MatchString(name) || state.Suspicious(name) || len(o.Items) == maxOutputs {
			o.Hidden++
			continue
		}
		o.Items = append(o.Items, OutputMeta{Name: name, Type: TypeLabel(v.Type), Sensitive: v.Sensitive})
	}
	sort.Slice(o.Items, func(i, j int) bool { return o.Items[i].Name < o.Items[j].Name })
	return o, nil
}

// TypeLabel renders a JSON type constraint compactly: string, list(string), map(number).
// Object attribute and tuple element names are not shown, only counted: a dynamically built
// object can carry data in its attribute names.
func TypeLabel(raw json.RawMessage) string {
	var t any
	if len(raw) == 0 || json.Unmarshal(raw, &t) != nil {
		return "unknown"
	}
	return typeLabel(t, 0)
}

func typeLabel(t any, depth int) string {
	if depth > 6 {
		return "…"
	}
	switch v := t.(type) {
	case string:
		switch v {
		case "string", "number", "bool", "dynamic":
			return v
		}
	case []any:
		if len(v) == 2 {
			kind, _ := v[0].(string)
			switch kind {
			case "list", "set", "map":
				return kind + "(" + typeLabel(v[1], depth+1) + ")"
			case "object":
				attrs, _ := v[1].(map[string]any)
				return fmt.Sprintf("object (%d attributes)", len(attrs))
			case "tuple":
				elems, _ := v[1].([]any)
				return fmt.Sprintf("tuple (%d elements)", len(elems))
			}
		}
	}
	return "unknown"
}

// ClassifyTool maps a utility's diagnostics text to a failure category without retaining
// it. The most specific remediation wins.
func ClassifyTool(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "flag provided but not defined", "unknown flag", "unsupported flag"):
		return "unsupported_flag"
	case containsAny(lower, "unsupported terraform core version", "unsupported opentofu version", "unsupported tofu version", "required_version"):
		return "version_mismatch"
	case containsAny(lower, "required plugins are not installed", "missing required provider", "could not load plugin", "failed to load plugin schemas", "plugin reinitialization required", "inconsistent dependency lock file", "provider requirements cannot be satisfied by locked dependencies", "does not match configured version constraint"):
		return "plugins_missing"
	case containsAny(lower, "backend initialization required", "module not installed", "please run \"terraform init\"", "please run \"tofu init\"", "run \"terraform init\"", "run \"tofu init\"", "initialization required"):
		return "init_required"
	case containsAny(lower, "currently selected workspace", "workspace doesn't exist", "workspace does not exist"):
		return "workspace_missing"
	case containsAny(lower, "error acquiring the state lock", "state lock", "lock info"):
		return "state_locked"
	}
	switch code := Classify(text); code {
	case "credentials", "network":
		return code
	}
	return ""
}

// Validate returns ValidateOut (JSON) or ValidateHumanOut, or ToolErr.
func (m *Fake) Validate(ctx context.Context, json bool) (Capture, error) {
	args := ToolArgs(ToolValidate, json, false)
	m.record(strings.Join(args, " "))
	if err := m.toolWait(ctx); err != nil {
		return Capture{}, err
	}
	if json {
		return m.ValidateOut, nil
	}
	return m.ValidateHumanOut, nil
}

// FormatCheck returns FormatOut, or ToolErr.
func (m *Fake) FormatCheck(ctx context.Context, recursive bool) (Capture, error) {
	m.record(strings.Join(ToolArgs(ToolFormat, false, recursive), " "))
	if err := m.toolWait(ctx); err != nil {
		return Capture{}, err
	}
	return m.FormatOut, nil
}

// Providers returns ProvidersOut, or ToolErr.
func (m *Fake) Providers(ctx context.Context) (Capture, error) {
	m.record("providers")
	if err := m.toolWait(ctx); err != nil {
		return Capture{}, err
	}
	return m.ProvidersOut, nil
}

// OutputsMetadata parses OutputsOut like the CLI adapter does.
func (m *Fake) OutputsMetadata(ctx context.Context) (Outputs, error) {
	m.record("output -json")
	if err := m.toolWait(ctx); err != nil {
		return Outputs{}, err
	}
	return outputsFrom(m.OutputsOut)
}

func (m *Fake) toolWait(ctx context.Context) error {
	if m.BlockTool {
		<-ctx.Done()
		return ErrCancelled
	}
	return m.ToolErr
}
