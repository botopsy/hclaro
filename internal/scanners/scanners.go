// Package scanners runs the optional static scanners tflint, Trivy and Checkov (M36-06).
// Each runs only from an explicit click, with a fixed argv, a minimal environment and
// bounded output. Hclaro never installs a scanner, never downloads a plugin, database or
// rule set for one, and decodes only the few result fields it shows.
package scanners

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/bounded"
	"hclaro/internal/proc"
)

// Names of the supported scanners. They are also the tool names of the HTTP API.
const (
	TFLint  = "tflint"
	Trivy   = "trivy"
	Checkov = "checkov"
)

// Names lists the scanners in display order.
var Names = []string{TFLint, Trivy, Checkov}

// Valid reports whether name is a scanner Hclaro runs.
func Valid(name string) bool { return name == TFLint || name == Trivy || name == Checkov }

// Title is the card title.
func Title(name string) string {
	return map[string]string{TFLint: "tflint", Trivy: "Trivy", Checkov: "Checkov"}[name]
}

// SupportText names the versions Hclaro runs. Ranges follow the flags the fixed argv needs;
// see docs/support-matrix.md.
const SupportText = "tflint 0.47–0.99, Trivy 0.57–0.99 or Checkov 3.x"

var versionRe = map[string]*regexp.Regexp{
	TFLint:  regexp.MustCompile(`(?m)^TFLint version (\d+)\.(\d+)\.(\d+)`),
	Trivy:   regexp.MustCompile(`(?m)^Version: (\d+)\.(\d+)\.(\d+)`),
	Checkov: regexp.MustCompile(`(?m)^(\d+)\.(\d+)\.(\d+)\s*$`),
}

// ParseVersion returns the version in `--version` output and whether Hclaro supports it.
func ParseVersion(name string, out []byte) (version string, supported bool) {
	re := versionRe[name]
	if re == nil {
		return "", false
	}
	m := re.FindSubmatch(bytes.ReplaceAll(out, []byte("\r"), nil))
	if m == nil {
		return "", false
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	version = fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3])
	switch name {
	case TFLint:
		supported = major == 0 && minor >= 47
	case Trivy:
		supported = major == 0 && minor >= 57
	case Checkov:
		supported = major == 3
	}
	return version, supported
}

// Argv is the complete argument list after the executable. Nothing from a request reaches
// it except the fixed choice of tflint's --recursive.
func Argv(name string, recursive bool) []string {
	switch name {
	case TFLint:
		if recursive {
			return []string{"--format=json", "--no-color", "--recursive"}
		}
		return []string{"--format=json", "--no-color"}
	case Trivy:
		return []string{"config", "--format", "json", "--quiet", "--skip-check-update", "."}
	case Checkov:
		return []string{"-d", ".", "-o", "json", "--framework", "terraform", "--quiet", "--compact", "--skip-download"}
	}
	return nil
}

// ConfigFiles are the project files each scanner reads by itself; Hclaro only reports which
// exist (regular files, not links).
var configFiles = map[string][]string{
	TFLint:  {".tflint.hcl"},
	Trivy:   {".trivyignore", "trivy.yaml"},
	Checkov: {".checkov.yaml", ".checkov.yml"},
}

// FoundConfig lists the scanner's own configuration files present in root.
func FoundConfig(name, root string) []string {
	found := []string{}
	for _, f := range configFiles[name] {
		if info, err := os.Lstat(filepath.Join(root, f)); err == nil && info.Mode().IsRegular() {
			found = append(found, f)
		}
	}
	return found
}

// passEnv is all a scanner inherits: process basics, proxy and CA settings. The engine and
// profile environment (AWS_*, ARM_*, GOOGLE_*, TF_VAR_*, tokens) is never passed.
var passEnv = map[string]bool{
	"PATH": true, "HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true, "LANG": true, "LC_ALL": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

// Environ builds a scanner's environment from environ (nil uses Hclaro's).
func Environ(environ []string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	out := []string{}
	for _, item := range environ {
		if key, _, _ := strings.Cut(item, "="); passEnv[key] {
			out = append(out, item)
		}
	}
	// BC_SKIP_MAPPING stops Checkov fetching its check mapping; NO_COLOR keeps output plain.
	return append(out, "NO_COLOR=1", "BC_SKIP_MAPPING=TRUE", "CHECKOV_EXPERIMENTAL_SKIP_UPDATE=TRUE")
}

// Info is what the card shows before anything runs.
type Info struct {
	Name      string   `json:"name"`
	Title     string   `json:"title"`
	Installed bool     `json:"installed"`
	Version   string   `json:"version,omitempty"`
	Supported bool     `json:"supported"`
	Reason    string   `json:"reason,omitempty"`
	Config    []string `json:"config"`
}

// Runner finds and runs scanners. Paths holds the --tflint-path style overrides; "" looks the
// executable up on PATH.
type Runner struct {
	Paths   map[string]string
	Timeout time.Duration
	// Environ is the environment filtered by Environ; nil uses Hclaro's.
	Environ []string
}

const (
	runLimit = 5 * time.Minute
	// Checkov is a large Python program: its --version takes 1 to 12 seconds, so 10 was too short.
	versionLimit  = 30 * time.Second
	maxStdout     = 16 << 20
	maxStderr     = 64 << 10
	gracePeriod   = 5 * time.Second
	notInstalled  = "Not installed. Hclaro never installs scanners."
	unsupportedFm = "This version is not supported. Hclaro runs %s."
)

func (r *Runner) path(name string) (string, error) {
	if p := r.Paths[name]; p != "" {
		return p, nil
	}
	return proc.LookPath(name)
}

// Detect looks the scanner up and reads only its version.
func (r *Runner) Detect(ctx context.Context, name, root string) Info {
	info := Info{Name: name, Title: Title(name), Config: FoundConfig(name, root)}
	exe, err := r.path(name)
	if err != nil {
		info.Reason = notInstalled
		if errors.Is(err, proc.ErrBatchFile) {
			info.Reason = "Windows batch files are not run safely. Install the .exe."
		}
		return info
	}
	info.Installed = true
	ctx, cancel := context.WithTimeout(ctx, versionLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Env = Environ(r.Environ)
	cmd.Dir = os.TempDir()
	out := &bounded.Buffer{Limit: 16 << 10}
	cmd.Stdout = out
	if cmd.Run() != nil {
		info.Reason = "The version could not be read."
		return info
	}
	if info.Version, info.Supported = ParseVersion(name, out.Bytes()); !info.Supported {
		info.Reason = fmt.Sprintf(unsupportedFm, SupportText)
		if info.Version == "" {
			info.Reason = "The version could not be read. Hclaro runs " + SupportText + "."
		}
	}
	return info
}

// Capture is the bounded output of one finished scan.
type Capture struct {
	Stdout, Stderr []byte
	ExitCode       int
	Truncated      bool
}

// ErrNotRunnable means the scanner is missing or its version is not supported; nothing ran.
var ErrNotRunnable = errors.New("scanner is not installed or not supported")

// Run starts the scanner in root with the fixed argv and returns its bounded output. A
// non-zero exit is not an error: scanners report findings through it. The caller classifies.
func (r *Runner) Run(ctx context.Context, name, root string, recursive bool) (Capture, string, error) {
	info := r.Detect(ctx, name, root)
	if !info.Installed || !info.Supported {
		return Capture{}, info.Version, ErrNotRunnable
	}
	exe, _ := r.path(name)
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = runLimit
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, Argv(name, recursive)...)
	cmd.Dir, cmd.Env = root, Environ(r.Environ)
	proc.PrepareGroup(cmd)
	cmd.Cancel = func() error {
		if proc.InterruptGroup(cmd.Process) != nil {
			return proc.KillGroup(cmd.Process)
		}
		return nil
	}
	cmd.WaitDelay = gracePeriod
	stdout, stderr := &bounded.Buffer{Limit: maxStdout}, &bounded.Buffer{Limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// shortcut: after the grace period Go kills only the scanner itself, not its group; upgrade if a scanner leaves children.
	if err := cmd.Start(); err != nil {
		return Capture{}, info.Version, errors.New("scanner could not be started")
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return Capture{}, info.Version, ctx.Err()
	}
	out := Capture{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Truncated: stdout.Overflow}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		out.ExitCode = exit.ExitCode()
	case err != nil:
		return out, info.Version, err
	}
	return out, info.Version, nil
}

// Classify maps stderr to a code for output that did not parse; the text is discarded.
func Classify(name, stderr string) string {
	s := strings.ToLower(stderr)
	switch {
	case name == TFLint && (strings.Contains(s, "plugin") && (strings.Contains(s, "not installed") || strings.Contains(s, "--init") || strings.Contains(s, "not found"))):
		return "scanner_plugins"
	case name == Trivy && (strings.Contains(s, "download") || strings.Contains(s, "no such host") || strings.Contains(s, "failed to initialize") && strings.Contains(s, "check")):
		return "scanner_download"
	}
	return "scanner_failed"
}

// InitCommand is the command shown to the user when tflint needs its plugins.
const InitCommand = "tflint --init"
