// Package awsauth is Hclaro's AWS authentication orchestration (M20). It is not a credential
// wallet: it detects the AWS CLI and aws-vault, lists profile names, checks the caller
// identity with STS on request and starts the AWS CLI's own sign-in commands. Every command
// is a fixed argv built here from validated names; there is no shell and no generic runner.
// Credentials, tokens, sign-in URLs and codes, and CLI output never leave this package:
// stdout and stderr are read only to parse a version, a profile list or the STS account and
// ARN, or to classify a failure.
package awsauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"hclaro/internal/awsvault"
	"hclaro/internal/bounded"
	"hclaro/internal/discovery"
	"hclaro/internal/preflight"
)

// Status values of a project's execution profile.
const (
	NotConfigured = "not_configured"
	ToolMissing   = "tool_missing"
	NotChecked    = "not_checked"
	Checking      = "checking"
	Verified      = "verified"
	AuthRequired  = "auth_required"
	AccessError   = "access_denied_or_error"
	Mismatch      = "identity_mismatch"
	Unknown       = "unknown"
)

// Diagnostic codes returned with refusals and failed checks.
const (
	CodeToolMissing          = "AUTH_TOOL_MISSING"
	CodeAuthRequired         = "AUTH_REQUIRED"
	CodeNetwork              = "AUTH_NETWORK"
	CodeInteractive          = "AUTH_INTERACTIVE_UNSUPPORTED"
	CodeAccountMismatch      = "AUTH_ACCOUNT_MISMATCH"
	CodeContextStale         = "AUTH_CONTEXT_STALE"
	CodeProviderUnverified   = "AUTH_PROVIDER_IDENTITY_UNVERIFIED"
	CodeLoginUnsupported     = "AUTH_LOGIN_UNSUPPORTED"
	CodeLoginRunning         = "AUTH_LOGIN_RUNNING"
	CodeError                = "AUTH_ERROR"
	loginTimeout             = 10 * time.Minute
	localTimeout             = 15 * time.Second
	maxProfiles              = 500
	maxLocalOutput           = 256 << 10
	maxRunningLogins         = 4
	providerIdentityDisclose = "STS reports the identity of the AWS CLI credentials in this context (CLI execution identity). Provider aliases, assume_role, web identity or a profile set in the Terraform configuration can make the provider use another principal or account."
)

// Error is a refusal with a diagnostic code and a message for the user.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

// Exec runs one fixed command and returns bounded stdout and stderr. Tests may replace it;
// the real one is run.
type Exec func(ctx context.Context, environ []string, name string, args ...string) (stdout, stderr []byte, err error)

// Manager holds the tool detection, the last identity check of each execution profile and
// running sign-in commands. All state is in memory: nothing here is persisted.
type Manager struct {
	// Probe checks the caller identity with a composed environment (preflight.AWSIdentity).
	Probe preflight.IdentityProbe
	Run   Exec
	Now   func() time.Time

	mu     sync.Mutex
	tools  *Tools
	kinds  map[string]string
	checks map[string]*check
	// verified is the last verified identity of each profile, kept across failed checks so
	// a change A → failure → B still counts as a new identity.
	verified  map[string]preflight.Identity
	verifying map[string]bool
	logins    map[string]*Login
	epochs    map[string]uint64
}

// check is the last identity check of one execution profile at one revision.
type check struct {
	revision    int
	fingerprint string
	running     bool
	identity    preflight.Identity
	at          time.Time
}

// New returns a manager using the real AWS CLI and aws-vault.
func New() *Manager {
	return &Manager{Probe: preflight.AWSIdentity, Run: run, Now: time.Now, checks: map[string]*check{}, verified: map[string]preflight.Identity{}, verifying: map[string]bool{}, logins: map[string]*Login{}, epochs: map[string]uint64{}}
}

func key(projectID, profileID string) string { return projectID + "/" + profileID }

// Epoch counts sign-ins and identity changes of an execution profile. An apply preflight
// records it, and a different epoch at approval or apply refuses the preflight binding, so a
// sign-in after review always means reviewing again.
func (m *Manager) Epoch(projectID, profileID string) uint64 {
	if m == nil || profileID == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epochs[key(projectID, profileID)]
}

// run is the real Exec: the executable from PATH, argv only, empty stdin, bounded output,
// the pager and auto-prompt disabled.
func run(ctx context.Context, environ []string, name string, args ...string) ([]byte, []byte, error) {
	exe, err := exec.LookPath(name)
	if err != nil {
		return nil, nil, exec.ErrNotFound
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	// Cancelling (a cancelled sign-in, shutdown) interrupts first so the AWS CLI can finish
	// writing its SSO cache; it is killed if it has not exited 3 seconds later. Windows has
	// no interrupt signal for another process, so there it is killed at once.
	cmd.Cancel = func() error {
		if runtime.GOOS != "windows" && cmd.Process.Signal(os.Interrupt) == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = cliEnviron(environ)
	out := &bounded.Buffer{Limit: maxLocalOutput}
	errOut := &bounded.Buffer{Limit: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// cliEnviron is environ (Hclaro's own when nil) without the aws-vault marker, with the
// pager and auto-prompt disabled.
func cliEnviron(environ []string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	out := []string{}
	for _, item := range awsvault.Strip(environ) {
		k, _, _ := strings.Cut(item, "=")
		if strings.EqualFold(k, "AWS_PAGER") || strings.EqualFold(k, "AWS_CLI_AUTO_PROMPT") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
}

// Tool is one detected executable. Its path is not reported.
type Tool struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Supported bool   `json:"supported"`
	Message   string `json:"message,omitempty"`
}

// Tools are the detected authentication tools and the sign-in methods the AWS CLI offers.
type Tools struct {
	AWS      Tool   `json:"aws"`
	Vault    Tool   `json:"awsVault"`
	SSOLogin bool   `json:"ssoLogin"`
	Login    bool   `json:"login"`
	LoginMsg string `json:"loginMessage,omitempty"`
	At       string `json:"detectedAt"`
}

var (
	vaultVersion = regexp.MustCompile(`v?([0-9]+\.[0-9]+\.[0-9]+)`)
	profileLine  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+=@,-]{0,63}$`)
)

// DetectTools finds aws and aws-vault on PATH and asks for their versions. `aws login` is
// offered only when the installed CLI documents it (`aws login help` succeeds); nothing is
// installed or downloaded. The result is cached until refresh.
func (m *Manager) DetectTools(ctx context.Context, refresh bool) Tools {
	m.mu.Lock()
	if m.tools != nil && !refresh {
		t := *m.tools
		m.mu.Unlock()
		return t
	}
	m.mu.Unlock()
	t := Tools{At: m.Now().UTC().Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	out, errOut, err := m.Run(ctx, nil, "aws", "--version")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		t.AWS.Message = discovery.Message(discovery.KindMissingCLI)
	case err != nil:
		t.AWS = Tool{Available: true, Message: "aws --version failed, so the AWS CLI version is unknown."}
	default:
		// AWS CLI v1 prints its version to stderr.
		text := out
		if len(text) == 0 {
			text = errOut
		}
		t.AWS.Available = true
		t.AWS.Version, t.AWS.Supported = discovery.ParseVersion(text)
		if !t.AWS.Supported {
			t.AWS.Message = discovery.Message(discovery.KindUnsupportedCLI)
		}
	}
	if t.AWS.Supported {
		// IAM Identity Center sign-in exists in every AWS CLI v2.
		t.SSOLogin = true
		_, _, err := m.Run(ctx, nil, "aws", "login", "help")
		t.Login = err == nil
		if !t.Login {
			t.LoginMsg = "Hclaro could not confirm that this AWS CLI offers aws login (console-credential sign-in): aws login help did not succeed. Update AWS CLI v2 to use it; aws sso login is unaffected."
		}
	}
	out, errOut, err = m.Run(ctx, nil, "aws-vault", "--version")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		t.Vault.Message = "aws-vault was not found on PATH. It is optional; install it only if you use it."
	case err != nil:
		t.Vault = Tool{Available: true, Message: "aws-vault --version failed, so its version is unknown."}
	default:
		// aws-vault prints its version to stderr.
		if v := vaultVersion.FindSubmatch(append(out, errOut...)); v != nil {
			t.Vault.Version = string(v[1])
		}
		t.Vault.Available, t.Vault.Supported = true, true
	}
	// A cancelled request (a page reload) would make a present tool look missing: keep it
	// out of the cache so the next request detects again.
	if ctx.Err() == nil {
		m.mu.Lock()
		m.tools, m.kinds = &t, map[string]string{}
		m.mu.Unlock()
	}
	return t
}

// Profiles runs `aws configure list-profiles` with Hclaro's environment (so AWS_CONFIG_FILE
// and AWS_SHARED_CREDENTIALS_FILE apply as in the user's shell) and returns valid profile
// names only. No configuration value is read.
func (m *Manager) Profiles(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	out, errOut, err := m.Run(ctx, nil, "aws", "configure", "list-profiles")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, &Error{CodeToolMissing, discovery.Message(discovery.KindMissingCLI)}
	case ctx.Err() != nil:
		return nil, &Error{CodeError, "The AWS CLI did not list profiles in time."}
	case err != nil:
		return nil, &Error{CodeError, failureMessage(discovery.Classify(string(errOut)))}
	}
	names := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if profileLine.MatchString(line) && !slices.Contains(names, line) && len(names) < maxProfiles {
			names = append(names, line)
		}
	}
	slices.Sort(names)
	return names, nil
}

// SSO kinds of an AWS profile.
const (
	KindSSO     = "sso"
	KindOther   = "other"
	KindUnknown = "unknown"
)

// ProfileKind reports whether an AWS profile is configured for IAM Identity Center, by asking
// the AWS CLI whether sso_session or sso_start_url is set. Only presence is used; the
// values are discarded. Answers are kept until the tools are detected again.
func (m *Manager) ProfileKind(ctx context.Context, profile string) string {
	if !awsvault.ValidProfile(profile) {
		return KindUnknown
	}
	m.mu.Lock()
	kind, ok := m.kinds[profile]
	m.mu.Unlock()
	if ok {
		return kind
	}
	kind = m.profileKind(ctx, profile)
	if kind != KindUnknown {
		m.mu.Lock()
		if m.kinds != nil {
			m.kinds[profile] = kind
		}
		m.mu.Unlock()
	}
	return kind
}

func (m *Manager) profileKind(ctx context.Context, profile string) string {
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	failed := 0
	for _, setting := range []string{"sso_session", "sso_start_url"} {
		out, _, err := m.Run(ctx, nil, "aws", "configure", "get", setting, "--profile", profile)
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return KindSSO
		}
		var exit *exec.ExitError
		// `aws configure get` exits 1 for an unset setting; anything else is unknown.
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
			failed++
		}
	}
	if failed > 0 {
		return KindUnknown
	}
	return KindOther
}

// failureMessage is the user-facing text of a classified failure.
func failureMessage(kind string) string { return discovery.Message(kind) }

// stateOf maps a failed identity probe to a status and diagnostic code.
func stateOf(id preflight.Identity) (state, code string) {
	switch id.Kind {
	case discovery.KindMissingCLI, discovery.KindVaultMissing:
		return ToolMissing, CodeToolMissing
	case discovery.KindExpired, discovery.KindNoCredentials, discovery.KindInvalid:
		return AuthRequired, CodeAuthRequired
	case discovery.KindVault:
		return AuthRequired, CodeInteractive
	case discovery.KindNetwork, discovery.KindTimeout:
		return Unknown, CodeNetwork
	case discovery.KindCancelled:
		return Unknown, CodeError
	case discovery.KindProfileNotFound, discovery.KindAccessDenied:
		return AccessError, CodeError
	}
	return AccessError, CodeError
}
