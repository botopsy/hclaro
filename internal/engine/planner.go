package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Planner is the engine surface that can run a plan. It has no apply method.
type Planner interface {
	Workspace(context.Context) (string, error)
	Plan(ctx context.Context, request PlanRequest, line func([]byte)) error
	ShowPlan(ctx context.Context, path string) ([]byte, error)
}

// Applier applies a saved plan file and nothing else: there is no way to pass targets,
// variables or an unsaved configuration. It is a separate interface so read-only and
// plan-only adapters cannot apply.
type Applier interface {
	Apply(ctx context.Context, request ApplyRequest, line func([]byte)) error
}

// ApplyRequest is a saved plan's absolute path and the execution profile's engine options,
// the same ones its plan ran with; zero keeps the engine default. Neither option changes
// what the saved plan applies.
type ApplyRequest struct {
	Path               string
	Parallelism        int
	LockTimeoutSeconds int
}

// PlanRequest holds validated inputs. Out is an absolute path in a private temp directory;
// VarFiles are bare file names inside the project root. Replace holds exact managed
// resource instance addresses taken from parsed state, one -replace argument each.
// GenerateConfigOut, for import plans, is a bare new file name in the project root where the
// engine writes configuration for import targets that have no resource block.
// Parallelism and LockTimeoutSeconds are the only engine options an execution profile can
// set; zero keeps the engine default.
// RefreshOnly makes a drift scan: `plan -refresh-only`, which compares recorded state with
// remote objects and proposes no configuration changes. It cannot be combined with Replace or
// GenerateConfigOut. Its saved artifact must never be applied (applying it writes state).
type PlanRequest struct {
	Out                string
	VarFiles           []string
	Replace            []string
	GenerateConfigOut  string
	Parallelism        int
	LockTimeoutSeconds int
	RefreshOnly        bool
}

// Bounds for the profile-selectable plan options.
const (
	MaxParallelism        = 256
	MaxLockTimeoutSeconds = 3600
)

var varFileSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidVarFilePath accepts a variable file reference relative to the project root:
// slash-separated, at most five segments, no hidden or dot segments, no leading dash, and a
// .tfvars or .tfvars.json name. It never accepts an absolute path or a flag.
func ValidVarFilePath(path string) bool {
	if path == "" || len(path) > 512 || strings.Contains(path, `\`) {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 5 {
		return false
	}
	for _, part := range parts {
		if !varFileSegment.MatchString(part) {
			return false
		}
	}
	return strings.HasSuffix(path, ".tfvars") || strings.HasSuffix(path, ".tfvars.json")
}

// ExitError reports a failed engine command without its output. Hint is a classification
// derived from stderr keywords; the stderr text itself is never returned.
type ExitError struct {
	Code int
	Hint string
}

func (e *ExitError) Error() string { return "engine command failed" }

var ErrCancelled = errors.New("engine command was cancelled or timed out")

// MaxReplace bounds how many instances one plan may replace.
const MaxReplace = 50

const (
	maxLine       = 1 << 20
	showPlanLimit = 5 * time.Minute
)

// gracePeriod is how long an interrupted engine may take to stop before it is killed.
// ApplyGracePeriod is longer: providers finish in-flight calls and the engine writes state,
// and killing it then can lose state or leave a lock. Variables only so tests can shorten them.
var (
	gracePeriod      = 15 * time.Second
	ApplyGracePeriod = 10 * time.Minute
)

func planArgs(request PlanRequest) ([]string, error) {
	if !filepath.IsAbs(request.Out) {
		return nil, errors.New("plan output must be an absolute private path")
	}
	args := []string{"plan", "-input=false", "-json", "-out=" + request.Out}
	if request.RefreshOnly {
		if len(request.Replace) > 0 || request.GenerateConfigOut != "" {
			return nil, errors.New("a refresh-only plan takes no replace or generated configuration options")
		}
		args = append(args, "-refresh-only")
	}
	for _, name := range request.VarFiles {
		if !ValidVarFilePath(name) {
			return nil, errors.New("invalid variable file name")
		}
		args = append(args, "-var-file="+name)
	}
	args, err := optionArgs(args, request.Parallelism, request.LockTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	if len(request.Replace) > MaxReplace {
		return nil, errors.New("too many replace addresses")
	}
	seen := map[string]bool{}
	for _, address := range request.Replace {
		// Each address is one argv element, so quotes and brackets reach the engine verbatim.
		if !validAddress(address) || seen[address] {
			return nil, errors.New("invalid replace address")
		}
		seen[address] = true
		args = append(args, "-replace="+address)
	}
	if name := request.GenerateConfigOut; name != "" {
		if !generatedName.MatchString(name) || len(request.Replace) > 0 {
			return nil, errors.New("invalid generated configuration file name")
		}
		args = append(args, "-generate-config-out="+name)
	}
	return args, nil
}

// generatedName is a bare .tf file name; the engine runs in the project root.
var generatedName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,80}\.tf$`)

func validAddress(address string) bool {
	if address == "" || len(address) > 1024 || strings.HasPrefix(address, "-") || strings.HasPrefix(address, "data.") {
		return false
	}
	for _, r := range address {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// optionArgs appends the profile-selectable engine options shared by plan and apply.
func optionArgs(args []string, parallelism, lockTimeout int) ([]string, error) {
	if n := parallelism; n != 0 {
		if n < 1 || n > MaxParallelism {
			return nil, errors.New("invalid parallelism")
		}
		args = append(args, "-parallelism="+strconv.Itoa(n))
	}
	if n := lockTimeout; n != 0 {
		if n < 1 || n > MaxLockTimeoutSeconds {
			return nil, errors.New("invalid lock timeout")
		}
		args = append(args, "-lock-timeout="+strconv.Itoa(n)+"s")
	}
	return args, nil
}

func applyArgs(request ApplyRequest) ([]string, error) {
	if !filepath.IsAbs(request.Path) {
		return nil, errors.New("saved plan path must be absolute")
	}
	// A saved plan never prompts, so -auto-approve is neither needed nor passed. Options come
	// before the path: the engine stops reading flags at the first positional argument.
	args, err := optionArgs([]string{"apply", "-input=false", "-json"}, request.Parallelism, request.LockTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return append(args, request.Path), nil
}

// Plan streams machine-readable UI lines to line. Lines longer than 1 MiB are truncated,
// which makes them invalid JSON for the consumer rather than buffering without bound.
func (c *CLI) Plan(ctx context.Context, request PlanRequest, line func([]byte)) error {
	args, err := planArgs(request)
	if err != nil {
		return err
	}
	if err := c.stream(ctx, args, gracePeriod, line); err != nil || ctx.Err() == nil {
		return err
	}
	return ErrCancelled
}

// Apply applies the saved plan at request.Path and streams machine-readable UI lines.
func (c *CLI) Apply(ctx context.Context, request ApplyRequest, line func([]byte)) error {
	args, err := applyArgs(request)
	if err != nil {
		return err
	}
	// A clean exit after an interrupt means the engine finished applying; it is not reported
	// as cancelled.
	return c.stream(ctx, args, ApplyGracePeriod, line)
}

// stream runs the engine in its own process group (see startGroup) and returns nil whenever
// it exits 0, even after cancellation; a non-zero exit after cancellation is ErrCancelled.
func (c *CLI) stream(ctx context.Context, args []string, grace time.Duration, line func([]byte)) error {
	cmd, err := c.command(ctx, args...)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &boundedTail{max: 64 << 10}
	cmd.Stderr = stderr
	exited, err := startGroup(ctx, cmd, grace)
	if err != nil {
		return errors.New("engine could not be started")
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	for {
		chunk, readErr := readLine(reader)
		if len(chunk) > 0 {
			line(chunk)
		}
		if readErr != nil {
			break
		}
	}
	err = cmd.Wait()
	exited()
	// After cancellation Wait reports the context error even for exit status 0.
	if cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return &ExitError{Code: code, Hint: Classify(stderr.String())}
	}
	return nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		part, isPrefix, err := r.ReadLine()
		if len(out) < maxLine {
			out = append(out, part[:min(len(part), maxLine-len(out))]...)
		}
		if err != nil || !isPrefix {
			return out, err
		}
	}
}

func (c *CLI) ShowPlan(ctx context.Context, path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("plan path must be absolute")
	}
	return c.run(ctx, showPlanLimit, "show", "-json", path)
}

// classes is the one engine failure table, matched in order against lowercased text. A
// release error is printed after the engine ran (with its own Lock Info), so it is checked
// before acquisition and is never a "did not run" lock failure.
var classes = []struct {
	code string
	re   *regexp.Regexp
}{
	{"stale_plan", regexp.MustCompile(`saved plan is stale|plan file is stale|saved plan no longer valid`)},
	{"state_unlock_failed", regexp.MustCompile(`error releasing the state lock|failed to unlock state|error unlocking state`)},
	{"state_locked", regexp.MustCompile(`error acquiring the state lock|error locking state|lock info:|conditionalcheckfailed`)},
	{"init_required", regexp.MustCompile(`backend initialization required|module not installed|required plugins are not installed|inconsistent dependency lock file|please run "(terraform|tofu) init"`)},
	{"credentials", regexp.MustCompile(`credential|expiredtoken|token has expired|\bsso\b|unauthorized|authenticat|access ?denied|forbidden`)},
	{"network", regexp.MustCompile(`no such host|i/o timeout|connection refused|network is unreachable|tls handshake timeout`)},
}

// Classify maps engine diagnostics text to a coarse failure category without retaining it.
func Classify(text string) string {
	lower := strings.ToLower(text)
	for _, c := range classes {
		if c.re.MatchString(lower) {
			return c.code
		}
	}
	return ""
}

// CredentialKind narrows a "credentials" failure: expired (an expired token or SSO session),
// missing (no credentials found) or denied (authenticated but refused), or "".
func CredentialKind(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "expiredtoken", "token has expired", "token is expired", "expired token", "sso session", "session has expired", "failed to refresh cached credentials", "sso token"):
		return "expired"
	case containsAny(lower, "no valid credential sources", "unable to locate credentials", "could not load credentials", "no credentials", "credentials not found", "could not find default credentials"):
		return "missing"
	case containsAny(lower, "access denied", "accessdenied", "forbidden", "unauthorized", "not authorized"):
		return "denied"
	}
	return ""
}

// boundedTail keeps the last max bytes written; it never fails the writer.
type boundedTail struct {
	b   []byte
	max int
}

func (t *boundedTail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = bytes.Clone(t.b[len(t.b)-t.max:])
	}
	return len(p), nil
}
func (t *boundedTail) String() string { return string(t.b) }

// Fake implements Adapter and Planner for tests and synthetic helpers. It is never
// selectable through the API or CLI flags.
type Fake struct {
	mu             sync.Mutex
	VersionValue   string
	WorkspaceValue string
	StateJSON      []byte
	Lines          [][]byte
	PlanJSON       []byte
	PlanErr        error
	// WriteOnError writes PlanJSON even when PlanErr is returned, like an engine that saves an
	// errored plan.
	WriteOnError bool
	ShowErr      error
	ApplyLines   [][]byte
	ApplyErr     error
	// Root and GeneratedConfig emulate -generate-config-out: the content is written to the
	// requested file name inside Root.
	Root            string
	GeneratedConfig []byte
	// Block makes Plan wait for cancellation, to exercise cancel paths.
	Block bool
	// BlockApply makes Apply wait for cancellation, then take ApplyStopDelay to stop and
	// return ErrCancelled, or nil with ApplyFinishesAfterCancel.
	BlockApply               bool
	ApplyStopDelay           time.Duration
	ApplyFinishesAfterCancel bool
	Delay                    time.Duration
	// InitLines and InitStderr are emitted by Init on stdout and stderr; InitErr is its
	// result, BlockInit makes it wait for cancellation and InitDataDir is created on success.
	InitLines   [][]byte
	InitStderr  [][]byte
	InitErr     error
	BlockInit   bool
	InitDataDir string
	// Workspaces, CLIDefault and WorkspaceStates back the WorkspaceManager methods.
	// WorkspaceErr fails ChangeWorkspace, BlockWorkspace makes it wait for cancellation,
	// ListErr fails listings and ListErrAfter sets it after a successful change.
	Workspaces      []string
	CLIDefault      string
	WorkspaceStates map[string][]byte
	WorkspaceLines  [][]byte
	WorkspaceErr    error
	BlockWorkspace  bool
	ListErr         error
	ListErrAfter    bool
	StateErr        error
	// ValidateOut, ValidateHumanOut, FormatOut, ProvidersOut and OutputsOut are the captured
	// output of the project utilities; ToolErr fails them and BlockTool makes them wait for
	// cancellation.
	ValidateOut      Capture
	ValidateHumanOut Capture
	FormatOut        Capture
	ProvidersOut     Capture
	OutputsOut       Capture
	ToolErr          error
	BlockTool        bool
	// GraphDOT is the `graph` output; GraphErr fails it and BlockGraph makes it wait for
	// cancellation.
	GraphDOT      []byte
	GraphErr      error
	BlockGraph    bool
	calls         []string
	requests      []PlanRequest
	applied       [][]byte
	applyRequests []ApplyRequest
	inits         [][]string
}

func (m *Fake) record(call string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call)
}

// Calls returns the engine methods invoked so far.
func (m *Fake) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// Requests returns the validated plan requests received so far.
func (m *Fake) Requests() []PlanRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PlanRequest(nil), m.requests...)
}

// ApplyRequests returns the validated apply requests received so far.
func (m *Fake) ApplyRequests() []ApplyRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ApplyRequest(nil), m.applyRequests...)
}

// Applied returns the saved plan contents passed to each Apply call.
func (m *Fake) Applied() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.applied...)
}

// SetWorkspace changes the workspace reported from now on.
func (m *Fake) SetWorkspace(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WorkspaceValue = name
}

func (m *Fake) Version(context.Context) (string, error) {
	m.record("version")
	return m.VersionValue, nil
}
func (m *Fake) State(context.Context) ([]byte, error) {
	m.record("show")
	return m.StateJSON, nil
}
func (m *Fake) Workspace(context.Context) (string, error) {
	m.record("workspace")
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.WorkspaceValue, nil
}
func (m *Fake) Plan(ctx context.Context, request PlanRequest, line func([]byte)) error {
	m.record("plan")
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if _, err := planArgs(request); err != nil {
		return err
	}
	for _, l := range m.Lines {
		line(l)
		if m.Delay > 0 {
			select {
			case <-time.After(m.Delay):
			case <-ctx.Done():
				return ErrCancelled
			}
		}
	}
	if m.Block {
		<-ctx.Done()
		return ErrCancelled
	}
	if request.GenerateConfigOut != "" && m.Root != "" {
		path := filepath.Join(m.Root, request.GenerateConfigOut)
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			_, _ = f.Write(m.GeneratedConfig)
			_ = f.Close()
		}
	}
	if m.PlanErr != nil {
		if m.WriteOnError {
			_ = os.WriteFile(request.Out, m.PlanJSON, 0o600)
		}
		return m.PlanErr
	}
	// A real engine writes a binary artifact; the mock writes the JSON it will show.
	return os.WriteFile(request.Out, m.PlanJSON, 0o600)
}
func (m *Fake) ShowPlan(_ context.Context, path string) ([]byte, error) {
	m.record("show-plan")
	if m.ShowErr != nil {
		return nil, m.ShowErr
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}

func (m *Fake) Apply(ctx context.Context, request ApplyRequest, line func([]byte)) error {
	m.record("apply")
	if _, err := applyArgs(request); err != nil {
		return err
	}
	b, err := os.ReadFile(request.Path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.applyRequests = append(m.applyRequests, request)
	m.applied = append(m.applied, b)
	m.mu.Unlock()
	for _, l := range m.ApplyLines {
		line(l)
	}
	if m.BlockApply {
		<-ctx.Done()
		time.Sleep(m.ApplyStopDelay)
		if m.ApplyFinishesAfterCancel {
			return nil
		}
		return ErrCancelled
	}
	return m.ApplyErr
}

func mkdirAll(path string) error { return os.MkdirAll(path, 0o755) }
