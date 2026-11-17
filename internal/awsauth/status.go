package awsauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"strings"
	"time"

	"hclaro/internal/discovery"
	"hclaro/internal/preflight"
	"hclaro/internal/profiles"
)

// Target is one project's execution profile: its resolved, value-free context and the child
// environment composed from it. Only the Hub builds targets, from registered projects and
// stored profiles; no path, command or environment value comes from a request.
type Target struct {
	ProjectID string
	Context   profiles.Resolved
	Environ   []string
}

func (t Target) key() string { return key(t.ProjectID, t.Context.ProfileID) }

// Command is a copyable, nonsecret command for the user's own terminal.
type Command struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

// Login is a sign-in command Hclaro started. Its output is never kept or shown: a sign-in
// URL or device code belongs to the browser window the AWS CLI opens.
type Login struct {
	ID         string `json:"id"`
	Mode       string `json:"mode"`
	State      string `json:"state"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	cancel     context.CancelFunc
}

// Login modes and states.
const (
	ModeSSO       = "sso"
	ModeLogin     = "login"
	LoginRunning  = "running"
	LoginDone     = "completed"
	LoginFailed   = "failed"
	LoginCanceled = "cancelled"
	LoginTimedOut = "timed_out"
)

// Status is the authentication state of one execution profile. It holds names, the account
// and ARN STS reported, timestamps and codes; never credentials or CLI output.
type Status struct {
	ProjectID       string    `json:"projectId"`
	ProfileID       string    `json:"profileId"`
	ProfileName     string    `json:"profileName"`
	Revision        int       `json:"profileRevision"`
	Method          string    `json:"method,omitempty"`
	AWSProfile      string    `json:"awsProfile,omitempty"`
	Region          string    `json:"region,omitempty"`
	ExpectedAccount string    `json:"expectedAccount,omitempty"`
	ExpectedRole    string    `json:"expectedRole,omitempty"`
	ProfileKind     string    `json:"profileKind,omitempty"`
	State           string    `json:"state"`
	Code            string    `json:"code,omitempty"`
	Message         string    `json:"message"`
	Account         string    `json:"account,omitempty"`
	ARN             string    `json:"arn,omitempty"`
	CheckedAt       string    `json:"checkedAt,omitempty"`
	Login           *Login    `json:"login,omitempty"`
	CanSSOLogin     bool      `json:"canSsoLogin"`
	CanLogin        bool      `json:"canLogin"`
	Commands        []Command `json:"commands"`
	Disclosure      string    `json:"disclosure"`
	Tools           Tools     `json:"tools"`
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// commands are the nonsecret terminal commands for this binding.
func commands(t Target, tools Tools, kind string) []Command {
	p := t.Context.AWSProfile
	if p == "" {
		return []Command{}
	}
	if t.Context.AWSAuth == profiles.AWSVault {
		return []Command{{"Verify in a terminal (aws-vault may ask for a keychain password or MFA code there)", "aws-vault exec " + p + " -- aws sts get-caller-identity"}}
	}
	out := []Command{}
	if kind != KindOther {
		out = append(out, Command{"Sign in with IAM Identity Center", "aws sso login --profile " + p}, Command{"Sign in with a device code (no browser on this machine)", "aws sso login --profile " + p + " --use-device-code"})
	}
	if tools.Login {
		out = append(out, Command{"Sign in with console credentials", "aws login --profile " + p})
	}
	return append(out, Command{"Verify in a terminal", "aws sts get-caller-identity --profile " + p})
}

// Status reports the state of t without contacting AWS. The tools and the profile kind are
// detected locally the first time.
func (m *Manager) Status(ctx context.Context, t Target) Status {
	tools := m.DetectTools(ctx, false)
	r := t.Context
	s := Status{
		ProjectID: t.ProjectID, ProfileID: r.ProfileID, ProfileName: r.ProfileName, Revision: r.Revision, Method: r.AWSAuth,
		AWSProfile: r.AWSProfile, Region: r.AWSRegion, ExpectedAccount: r.AWSExpectedAccount, ExpectedRole: r.AWSExpectedRole,
		Disclosure: providerIdentityDisclose, Tools: tools, Commands: []Command{},
	}
	if r.AWSProfile == "" {
		s.State, s.Message = NotConfigured, "This execution profile names no AWS profile, so operations use the environment Hclaro was started with. Choose an AWS profile to verify and guard it."
		return s
	}
	switch {
	case !tools.AWS.Available:
		s.State, s.Code, s.Message = ToolMissing, CodeToolMissing, tools.AWS.Message
	case r.AWSAuth == profiles.AWSVault && !tools.Vault.Available:
		s.State, s.Code, s.Message = ToolMissing, CodeToolMissing, discovery.Message(discovery.KindVaultMissing)
	}
	if r.AWSAuth != profiles.AWSVault && tools.AWS.Supported {
		s.ProfileKind = m.ProfileKind(ctx, r.AWSProfile)
		s.CanSSOLogin = tools.SSOLogin && s.ProfileKind == KindSSO
		s.CanLogin = tools.Login
	}
	s.Commands = commands(t, tools, s.ProfileKind)
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.logins[t.key()]; l != nil {
		copy := *l
		s.Login = &copy
	}
	if s.State != "" {
		return s
	}
	c := m.checks[t.key()]
	switch {
	case c == nil || c.revision != r.Revision || c.fingerprint != r.Fingerprint:
		s.State, s.Message = NotChecked, "Not checked. A valid profile name does not mean a valid session: choose Verify identity to ask STS (read-only, contacts AWS)."
	case c.running:
		s.State, s.Message = Checking, "Asking STS for the caller identity…"
	default:
		s.State, s.Code, s.Message = judge(r, c.identity)
		s.Account, s.ARN = c.identity.Account, c.identity.ARN
		s.CheckedAt = c.at.UTC().Format(time.RFC3339)
	}
	return s
}

// judge turns an identity probe into a status, comparing it with the expected account and
// role. A withheld ARN never matches a role guard.
func judge(r profiles.Resolved, id preflight.Identity) (state, code, message string) {
	if id.State != "verified" {
		state, code = stateOf(id)
		return state, code, id.Message
	}
	if r.AWSExpectedAccount != "" && id.Account != r.AWSExpectedAccount {
		return Mismatch, CodeAccountMismatch, "STS reports account " + id.Account + ", but this profile expects " + r.AWSExpectedAccount + ". Operations that check the expected account are blocked. Sign in to the right account or change the profile."
	}
	if r.AWSExpectedRole != "" && !profiles.RoleMatches(r.AWSExpectedRole, id.ARN) {
		return Mismatch, CodeAccountMismatch, "STS reports " + id.ARN + ", which does not match the expected role " + r.AWSExpectedRole + "."
	}
	return Verified, "", "Verified when last checked. STS does not report when the session expires; check again before important work."
}

// Verify asks STS for the caller identity of t, through the same AWS CLI or aws-vault path the
// engine uses. It contacts AWS and may run a configured credential_process, so it runs only
// on an explicit request.
func (m *Manager) Verify(ctx context.Context, t Target) (Status, error) {
	if t.Context.AWSProfile == "" {
		return Status{}, &Error{CodeError, "Choose an AWS profile for this execution profile first."}
	}
	k := t.key()
	m.mu.Lock()
	// The running flag lives apart from the result, so a sign-in that clears the result
	// cannot let a second check start alongside.
	if m.verifying[k] {
		m.mu.Unlock()
		return Status{}, &Error{CodeLoginRunning, "An identity check for this profile is already running."}
	}
	m.verifying[k] = true
	running := &check{revision: t.Context.Revision, fingerprint: t.Context.Fingerprint, running: true}
	m.checks[k] = running
	epoch := m.epochs[k]
	m.mu.Unlock()
	id := m.Probe(ctx, t.Environ)
	m.mu.Lock()
	delete(m.verifying, k)
	// A sign-in or a removed profile during the probe makes its answer stale: it may describe
	// the credentials from before the sign-in, so it never becomes the profile's result.
	if m.epochs[k] != epoch {
		if m.checks[k] == running {
			delete(m.checks, k)
		}
		m.mu.Unlock()
		return Status{}, &Error{CodeContextStale, "A sign-in started, or the profile changed, while the identity was being checked. That answer was discarded; verify again."}
	}
	m.checks[k] = &check{revision: t.Context.Revision, fingerprint: t.Context.Fingerprint, identity: id, at: m.Now()}
	// A different principal than the last verified one, even with failed checks in between,
	// is a new identity for review. Session names are ignored (preflight.Principal).
	if id.State == "verified" {
		if last, ok := m.verified[k]; ok && !preflight.SameIdentity(last, id) {
			m.epochs[k]++
		}
		m.verified[k] = id
	}
	m.mu.Unlock()
	return m.Status(ctx, t), nil
}

// StartLogin starts the AWS CLI's own sign-in for t: `aws sso login --profile <p>` or
// `aws login --profile <p>`. The CLI opens the browser itself; Hclaro passes no URL, token
// or code and keeps no output. A finished sign-in does not verify anything: the identity is
// unknown until the user verifies again, and nothing that was waiting resumes.
func (m *Manager) StartLogin(ctx context.Context, t Target, mode string) (Status, error) {
	r := t.Context
	tools := m.DetectTools(ctx, false)
	switch {
	case r.AWSProfile == "":
		return Status{}, &Error{CodeError, "Choose an AWS profile for this execution profile first."}
	case r.AWSAuth == profiles.AWSVault:
		return Status{}, &Error{CodeInteractive, "aws-vault may ask for a keychain password or MFA code, which Hclaro cannot show. Run the aws-vault command shown in a terminal, then verify again."}
	case !tools.AWS.Supported:
		return Status{}, &Error{CodeToolMissing, discovery.Message(discovery.KindUnsupportedCLI)}
	case mode == ModeLogin && !tools.Login:
		return Status{}, &Error{CodeLoginUnsupported, tools.LoginMsg}
	case mode == ModeSSO && m.ProfileKind(ctx, r.AWSProfile) != KindSSO:
		return Status{}, &Error{CodeLoginUnsupported, "AWS profile " + r.AWSProfile + " is not configured for IAM Identity Center (no sso_session or sso_start_url), so aws sso login does not apply. Verify its credentials instead."}
	case mode != ModeSSO && mode != ModeLogin:
		return Status{}, &Error{CodeError, "Unknown sign-in method."}
	}
	args := []string{"sso", "login", "--profile", r.AWSProfile}
	if mode == ModeLogin {
		args = []string{"login", "--profile", r.AWSProfile}
	}
	k := t.key()
	m.mu.Lock()
	running := 0
	for _, l := range m.logins {
		if l.State == LoginRunning {
			running++
		}
	}
	if l := m.logins[k]; l != nil && l.State == LoginRunning {
		m.mu.Unlock()
		return Status{}, &Error{CodeLoginRunning, "A sign-in for this profile is already running. Finish it in the browser, or cancel it."}
	}
	if running >= maxRunningLogins {
		m.mu.Unlock()
		return Status{}, &Error{CodeLoginRunning, "Too many sign-ins are running. Finish or cancel one first."}
	}
	// The sign-in outlives the request; Close (at Hclaro's shutdown) cancels it.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginTimeout)
	l := &Login{ID: newID(), Mode: mode, State: LoginRunning, Message: "Waiting for the AWS CLI sign-in. Complete it in the browser window the AWS CLI opened.", StartedAt: m.Now().UTC().Format(time.RFC3339), cancel: cancel}
	m.logins[k] = l
	// Signing in may change the identity: an earlier preflight must be reviewed again.
	m.epochs[k]++
	delete(m.checks, k)
	m.mu.Unlock()
	go func() {
		defer cancel()
		_, errOut, err := m.Run(runCtx, t.Environ, "aws", args...)
		m.mu.Lock()
		defer m.mu.Unlock()
		l.FinishedAt = m.Now().UTC().Format(time.RFC3339)
		switch {
		case err == nil:
			l.State, l.Message = LoginDone, "Login process completed — verify identity. Hclaro does not treat a sign-in as verified until STS answers."
		case errors.Is(runCtx.Err(), context.Canceled):
			l.State, l.Message = LoginCanceled, "Sign-in cancelled. Nothing else was started."
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			l.State, l.Code, l.Message = LoginTimedOut, CodeInteractive, "The sign-in did not finish within 10 minutes. Use the terminal command shown, then verify again."
		case errors.Is(err, exec.ErrNotFound):
			l.State, l.Code, l.Message = LoginFailed, CodeToolMissing, discovery.Message(discovery.KindMissingCLI)
		default:
			l.State, l.Code, l.Message = LoginFailed, CodeInteractive, loginFailure(string(errOut))
		}
		m.epochs[k]++
		delete(m.checks, k)
	}()
	return m.Status(ctx, t), nil
}

// loginFailure explains a failed sign-in without echoing CLI output.
func loginFailure(stderr string) string {
	switch discovery.Classify(stderr) {
	case discovery.KindProfileNotFound:
		return discovery.Message(discovery.KindProfileNotFound)
	case discovery.KindNetwork:
		return discovery.Message(discovery.KindNetwork)
	}
	if strings.Contains(strings.ToLower(stderr), "browser") {
		return "The AWS CLI could not open a browser. Use the device-code command shown in a terminal, then verify again."
	}
	return "The sign-in did not complete. It may need a terminal prompt Hclaro cannot show; use the command shown in a terminal, then verify again."
}

// CancelLogin stops a running sign-in of t. It reports false when none is running.
func (m *Manager) CancelLogin(t Target) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.logins[t.key()]
	if l == nil || l.State != LoginRunning {
		return false
	}
	l.cancel()
	return true
}

// Close cancels every running sign-in, so none outlives Hclaro or keeps writing the SSO
// cache after it exits. It waits up to wait for them to end.
func (m *Manager) Close(wait time.Duration) {
	m.mu.Lock()
	for _, l := range m.logins {
		if l.State == LoginRunning {
			l.cancel()
		}
	}
	m.mu.Unlock()
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		m.mu.Lock()
		running := false
		for _, l := range m.logins {
			running = running || l.State == LoginRunning
		}
		m.mu.Unlock()
		if !running {
			return
		}
	}
}

// Forget drops the checks and sign-ins of a removed project or profile.
func (m *Manager) Forget(projectID, profileID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(projectID, profileID)
	if l := m.logins[k]; l != nil && l.State == LoginRunning {
		l.cancel()
	}
	delete(m.checks, k)
	delete(m.verified, k)
	m.epochs[k]++
}
