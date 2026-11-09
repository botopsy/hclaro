package cost

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Runner runs one allowlisted Infracost CLI call with environ and returns its standard
// output. Errors are *ProviderError. Tests inject fakes.
type Runner interface {
	// Find reports whether the executable is installed. It only searches PATH; it never
	// starts a process.
	Find() bool
	Run(ctx context.Context, environ []string, args []string) ([]byte, error)
}

// Error kinds. User-facing messages come from Message; CLI output is never returned.
const (
	KindStrictLocal    = "strict_local"
	KindOptIn          = "opt_in_required"
	KindMissingCLI     = "cli_missing"
	KindUnsupported    = "cli_unsupported"
	KindRefused        = "refused"
	KindAuth           = "auth_required"
	KindPricing        = "pricing_unavailable"
	KindTimeout        = "timeout"
	KindCancelled      = "cancelled"
	KindMalformed      = "malformed_output"
	KindOutputTooLarge = "output_too_large"
	KindFailed         = "failed"
	KindInputTooLarge  = "input_too_large"
	KindNoInput        = "input_unavailable"
	KindContextChanged = "context_changed"
	KindBusy           = "busy"
	KindShutdown       = "shutting_down"
)

var kindMessages = map[string]string{
	KindStrictLocal:    "Cost estimation is not available in strict local-only mode: Infracost prices resources with a remote pricing API, and Hclaro has no offline price source.",
	KindOptIn:          "Cost estimation is off. Review the data disclosure and turn it on for this session first.",
	KindMissingCLI:     "The Infracost CLI (infracost) was not found on PATH. Hclaro never installs it; install it yourself and restart Hclaro to use cost estimates.",
	KindUnsupported:    "This Infracost CLI version is not supported. Hclaro supports Infracost v0.10.",
	KindRefused:        "Hclaro refused to run this Infracost command because it is not on the allowlist.",
	KindAuth:           "Infracost has no valid API key. Run `infracost auth login` in your terminal (or configure INFRACOST_API_KEY yourself); Hclaro never asks for or stores the key.",
	KindPricing:        "The Infracost pricing API could not be reached. Check network or proxy access and try again. Plans and applies are not affected.",
	KindTimeout:        "Infracost did not finish in time. Plans and applies are not affected.",
	KindCancelled:      "The estimate was cancelled.",
	KindMalformed:      "Infracost returned output Hclaro could not read safely. Nothing from it is shown.",
	KindOutputTooLarge: "Infracost returned more output than Hclaro reads.",
	KindFailed:         "The Infracost CLI failed. Details are withheld because CLI messages can echo configuration.",
	KindInputTooLarge:  "This plan is too large to estimate.",
	KindNoInput:        "This plan has no cost input. Run a new plan to estimate it.",
	KindContextChanged: "The project context changed since this plan was made. Run a new plan, then estimate it.",
	KindBusy:           "An estimate is already running. Wait for it or cancel it.",
	KindShutdown:       "Hclaro is stopping.",
}

// Message returns the user-facing explanation of an error kind.
func Message(kind string) string {
	if m, ok := kindMessages[kind]; ok {
		return m
	}
	return kindMessages[KindFailed]
}

// ProviderError is a classified, sanitized failure.
type ProviderError struct{ Kind string }

func (e *ProviderError) Error() string { return Message(e.Kind) }

func fail(kind string) error { return &ProviderError{Kind: kind} }

// KindOf returns the kind of err.
func KindOf(err error) string {
	var p *ProviderError
	switch {
	case errors.As(err, &p):
		return p.Kind
	case errors.Is(err, context.Canceled):
		return KindCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	}
	return KindFailed
}

// inputName is the only file name the adapter is given; it must sit in a private directory
// created by the service (hclaro-cost-*).
const inputName = "plan.json"

var inputDir = regexp.MustCompile(`^hclaro-cost-[0-9]+$`)

// Allowed reports whether args is `--version` or the one estimate command Hclaro runs:
// `breakdown --path <private input> --format json --no-color --log-level=warn`. The flags are documented
// Infracost v0.10 options. Anything else (upload, comment, auth, configure, --usage-file,
// --config-file, other paths or formats) is refused.
func Allowed(args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	want := estimateArgs("")
	if len(args) != len(want) {
		return false
	}
	for i, a := range args {
		if i != 2 && a != want[i] {
			return false
		}
	}
	return validInputPath(args[2])
}

// validInputPath accepts an absolute path to plan.json directly inside a private
// hclaro-cost-* directory.
func validInputPath(p string) bool {
	if !filepath.IsAbs(p) || filepath.Base(p) != inputName || filepath.Clean(p) != p {
		return false
	}
	return inputDir.MatchString(filepath.Base(filepath.Dir(p)))
}

func estimateArgs(path string) []string {
	return []string{"breakdown", "--path", path, "--format", "json", "--no-color", "--log-level=warn"}
}

// passEnv are the only inherited variables the adapter receives: process basics, proxy and
// TLS settings, and Infracost's own documented configuration (API key, pricing endpoint,
// currency). Cloud credentials (AWS_*, ARM_*, GOOGLE_*), TF_VAR_* values, Terraform Cloud
// tokens and everything else are not passed: Infracost prices a plan file and needs no cloud
// access.
var passEnv = map[string]bool{
	"PATH": true, "HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true, "XDG_CONFIG_HOME": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true, "LANG": true, "LC_ALL": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"INFRACOST_API_KEY": true, "INFRACOST_PRICING_API_ENDPOINT": true, "INFRACOST_CURRENCY": true,
}

// Environ builds the adapter environment from environ (nil uses Hclaro's). Update checks,
// colours, Infracost Cloud uploads and the dashboard are switched off.
func Environ(environ []string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	out := []string{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if passEnv[key] {
			out = append(out, item)
		}
	}
	return append(out,
		"INFRACOST_SKIP_UPDATE_CHECK=true",
		"INFRACOST_NO_COLOR=true",
		"INFRACOST_ENABLE_CLOUD=false",
		"INFRACOST_ENABLE_DASHBOARD=false",
		"INFRACOST_SELF_HOSTED_TELEMETRY=false",
		"INFRACOST_LOG_LEVEL=warn",
	)
}

// DefaultEndpoint is Infracost's hosted Cloud Pricing API.
const DefaultEndpoint = "pricing.api.infracost.io"

// Endpoint returns the host of the pricing API the adapter will call, from
// INFRACOST_PRICING_API_ENDPOINT in environ, without scheme, credentials or path.
func Endpoint(environ []string) string {
	if environ == nil {
		environ = os.Environ()
	}
	for _, item := range environ {
		if v, ok := strings.CutPrefix(item, "INFRACOST_PRICING_API_ENDPOINT="); ok && v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Hostname() == "" || len(u.Host) > 255 {
				return "custom endpoint (unreadable)"
			}
			return u.Host
		}
	}
	return DefaultEndpoint
}

var versionPattern = regexp.MustCompile(`(?m)^Infracost v(0\.10\.[0-9]{1,3})\s*$`)
var anyVersion = regexp.MustCompile(`(?m)^Infracost v[0-9]+\.[0-9]+\.[0-9]+`)

// ParseVersion returns the supported Infracost version in `infracost --version` output.
func ParseVersion(out []byte) (string, error) {
	if m := versionPattern.FindSubmatch(bytes.TrimSpace(out)); m != nil {
		return string(m[1]), nil
	}
	if anyVersion.Match(out) {
		return "", fail(KindUnsupported)
	}
	return "", fail(KindMalformed)
}

const (
	maxOutput   = 16 << 20
	maxStderr   = 16 << 10
	callTimeout = 3 * time.Minute
)

// Exec is the real runner: `infracost` from PATH, argv only, empty stdin, bounded output.
type Exec struct {
	Timeout time.Duration
}

func (Exec) Find() bool {
	_, err := exec.LookPath("infracost")
	return err == nil
}

type bounded struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
			b.overflow = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.overflow = true
	}
	return len(p), nil
}

func (e Exec) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !Allowed(args) {
		return nil, fail(KindRefused)
	}
	path, err := exec.LookPath("infracost")
	if err != nil {
		return nil, fail(KindMissingCLI)
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = callTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = Environ(environ)
	cmd.Stdin = nil
	if len(args) > 1 {
		// Infracost resolves relative paths and writes nothing else; run it in the private
		// directory so no project file is read by accident.
		cmd.Dir = filepath.Dir(args[2])
	}
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := &bounded{limit: maxOutput}, &bounded{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.overflow {
		return nil, fail(KindOutputTooLarge)
	}
	if runErr != nil {
		return nil, fail(Classify(stderr.buf.String()))
	}
	if len(args) == 1 {
		// Some versions print the version on stderr.
		return append(stdout.buf.Bytes(), stderr.buf.Bytes()...), nil
	}
	return stdout.buf.Bytes(), nil
}

// Classify maps Infracost stderr to an error kind. The text itself is discarded.
func Classify(stderr string) string {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "api key") || strings.Contains(s, "api_key") || strings.Contains(s, "auth login") || strings.Contains(s, "unauthorized") || strings.Contains(s, "403"):
		return KindAuth
	case strings.Contains(s, "pricing api") || strings.Contains(s, "no such host") || strings.Contains(s, "connection refused") ||
		strings.Contains(s, "dial tcp") || strings.Contains(s, "i/o timeout") || strings.Contains(s, "tls") || strings.Contains(s, "proxy"):
		return KindPricing
	}
	return KindFailed
}
