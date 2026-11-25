package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/tfpolicy"
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

func PlanArgs(request PlanRequest) ([]string, error) {
	if !filepath.IsAbs(request.Out) {
		return nil, errors.New("plan output must be an absolute private path")
	}
	return planArgs(request, request.Out)
}

// Display is the command PlanArgs builds, as shown to the user: the engine name first and out
// (a placeholder, request.Out is ignored) in place of the private plan file. If the request is
// invalid it shows only the subcommand; running it reports the real error.
func (r PlanRequest) Display(engineName, out string) []string {
	args, err := planArgs(r, out)
	if err != nil {
		args = []string{"plan"}
	}
	return append([]string{engineName}, args...)
}

func planArgs(request PlanRequest, out string) ([]string, error) {
	args := []string{"plan", "-input=false", "-json", "-out=" + out}
	if request.RefreshOnly {
		if len(request.Replace) > 0 || request.GenerateConfigOut != "" {
			return nil, errors.New("a refresh-only plan takes no replace or generated configuration options")
		}
		args = append(args, "-refresh-only")
	}
	for _, name := range request.VarFiles {
		if !tfpolicy.ValidVarFilePath(name) {
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
		if n < 1 || n > tfpolicy.MaxParallelism {
			return nil, errors.New("invalid parallelism")
		}
		args = append(args, "-parallelism="+strconv.Itoa(n))
	}
	if n := lockTimeout; n != 0 {
		if n < 1 || n > tfpolicy.MaxLockTimeoutSeconds {
			return nil, errors.New("invalid lock timeout")
		}
		args = append(args, "-lock-timeout="+strconv.Itoa(n)+"s")
	}
	return args, nil
}

func ApplyArgs(request ApplyRequest) ([]string, error) {
	if !filepath.IsAbs(request.Path) {
		return nil, errors.New("saved plan path must be absolute")
	}
	return applyArgs(request, request.Path)
}

// Display is the command ApplyArgs builds, as shown to the user, with plan (a label, request.Path
// is ignored) in place of the saved plan path; an invalid request shows only the subcommand.
func (r ApplyRequest) Display(engineName, plan string) []string {
	args, err := applyArgs(r, plan)
	if err != nil {
		args = []string{"apply"}
	}
	return append([]string{engineName}, args...)
}

func applyArgs(request ApplyRequest, path string) ([]string, error) {
	// A saved plan never prompts, so -auto-approve is neither needed nor passed. Options come
	// before the path: the engine stops reading flags at the first positional argument.
	args, err := optionArgs([]string{"apply", "-input=false", "-json"}, request.Parallelism, request.LockTimeoutSeconds)
	if err != nil {
		return nil, err
	}
	return append(args, path), nil
}

// Plan streams machine-readable UI lines to line. Lines longer than 1 MiB are truncated,
// which makes them invalid JSON for the consumer rather than buffering without bound.
func (c *CLI) Plan(ctx context.Context, request PlanRequest, line func([]byte)) error {
	args, err := PlanArgs(request)
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
	args, err := ApplyArgs(request)
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

// lockedStateRead is a local-backend state read on Windows while another process holds the
// lock: the OS lock-violation text, only together with a state-file read (OpenTofu 1.13.9, run
// 43834356151; Terraform 1.17.2, run 43871517546, wraps it across "│ " lines). It is matched
// on lockedText, so colour codes, box characters and line breaks do not matter. The OS text is
// localised, so other languages stay unclassified. It is the only text a read (runEnv) turns
// into ErrReadStateLocked.
var lockedStateRead = regexp.MustCompile(`(failed to read state file|state file could not be read|read [^ :]*\.tfstate\b).*another process has locked a portion of the file`)

var lockedNoise = regexp.MustCompile(`\x1b\[[0-9;]*m|[│╷╵]`)

// isLockedRead reports whether engine text is a state read blocked by another process's lock.
func isLockedRead(text string) bool {
	return lockedStateRead.MatchString(strings.Join(strings.Fields(lockedNoise.ReplaceAllString(strings.ToLower(text), " ")), " "))
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
	{"credentials", regexp.MustCompile(`credential|aws-vault: error|expiredtoken|token has expired|\bsso\b|unauthorized|authenticat|access ?denied|forbidden`)},
	{"network", regexp.MustCompile(`no such host|i/o timeout|connection refused|network is unreachable|tls handshake timeout`)},
}

// Classify maps engine diagnostics text to a coarse failure category without retaining it.
func Classify(text string) string {
	lower := strings.ToLower(text)
	for _, c := range classes {
		if c.re.MatchString(lower) || c.code == "state_locked" && isLockedRead(text) {
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
