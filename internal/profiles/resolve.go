package profiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"hclaro/internal/awsvault"
	"hclaro/internal/tfpolicy"
)

// VarFile and VarFileError are the shared variable file rules (see tfpolicy).
type (
	VarFile      = tfpolicy.VarFile
	VarFileError = tfpolicy.VarFileError
)

// EnvStatus says whether an expected environment variable is set. The value is never read
// into the result.
type EnvStatus struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
}

// Issue is a problem that blocks operations with a resolved context.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Resolved is the immutable, sanitized execution context of one profile at one revision.
// It is safe to return through the API: it holds names, aliases and paths, never values.
type Resolved struct {
	ProjectID   string    `json:"projectId"`
	ProfileID   string    `json:"profileId,omitempty"`
	ProfileName string    `json:"profileName,omitempty"`
	Revision    int       `json:"profileRevision,omitempty"`
	Engine      string    `json:"enginePreference,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	VarFiles    []VarFile `json:"varFiles"`
	Options     Options   `json:"options"`
	AWSProfile  string    `json:"awsProfile,omitempty"`
	AWSRegion   string    `json:"awsRegion,omitempty"`
	// AWSAuth is aws-cli or aws-vault when an AWS profile is chosen, "" otherwise.
	AWSAuth            string         `json:"awsAuth,omitempty"`
	AWSExpectedAccount string         `json:"awsExpectedAccount,omitempty"`
	AWSExpectedRole    string         `json:"awsExpectedRole,omitempty"`
	Env                []EnvStatus    `json:"env"`
	Identity           string         `json:"identity"`
	Notices            []string       `json:"notices"`
	Issues             []Issue        `json:"issues"`
	VarIssues          []VarFileError `json:"varFileIssues"`
	Fingerprint        string         `json:"fingerprint"`
}

// IdentityNotVerified is the identity status of a resolved context: resolving never calls a
// cloud API. The AWS caller identity is verified only when the user asks (internal/awsauth
// and the apply preflight), and it describes the CLI credentials, not every provider.
const IdentityNotVerified = "not_verified"

// None is the context of an operation without a profile: the inherited environment.
func None(projectID string, environ []string) Resolved {
	r := Resolved{ProjectID: projectID, VarFiles: []VarFile{}, Env: []EnvStatus{}, Identity: IdentityNotVerified, Notices: []string{}, Issues: []Issue{}, VarIssues: []VarFileError{}}
	if tfpolicy.UnsafeArgs(environ) {
		r.Issues = append(r.Issues, unsafeIssue)
	}
	r.Fingerprint = r.fingerprint()
	return r
}

var unsafeIssue = Issue{"unsafe_cli_args", "Inherited TF_CLI_ARGS or a TF_CLI_ARGS_<command> setting adds flags other than -no-color. Such flags could disable locking, auto-approve or change what a reviewed plan does, so Hclaro refuses to run. Restart Hclaro without them. Values were not read into Hclaro."}

// Resolve builds the execution context of p for a project root and environment. Nothing is
// executed and no file content is read.
func Resolve(p Profile, root string, environ []string) Resolved {
	r := None(p.ProjectID, environ)
	r.ProfileID, r.ProfileName, r.Revision = p.ID, p.Name, p.Revision
	r.Engine, r.Workspace, r.Options, r.AWSProfile, r.AWSRegion = p.Engine, p.Workspace, p.Options, p.AWSProfile, p.AWSRegion
	r.AWSExpectedAccount, r.AWSExpectedRole = p.AWSExpectedAccount, p.AWSExpectedRole
	if p.AWSProfile != "" {
		r.AWSAuth = AWSCLI
		if p.AWSAuth == AWSVault {
			r.AWSAuth = AWSVault
		}
	}
	for _, ref := range p.VarFiles {
		f, err := tfpolicy.ResolveVarFile(root, ref)
		var vf *VarFileError
		if errors.As(err, &vf) {
			r.VarIssues = append(r.VarIssues, *vf)
			r.Issues = append(r.Issues, Issue{vf.Code, vf.Message})
			continue
		}
		r.VarFiles = append(r.VarFiles, f)
	}
	set := map[string]bool{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		set[strings.ToUpper(key)] = true
	}
	for _, name := range p.EnvNames {
		present := set[strings.ToUpper(name)]
		r.Env = append(r.Env, EnvStatus{name, present})
		if !present {
			r.Issues = append(r.Issues, Issue{"env_missing", name + " is expected by this profile but is not set in the environment Hclaro was started with. Set it in your shell and restart Hclaro; Hclaro never stores its value."})
		}
	}
	r.Notices = compositionNotices(p, environ)
	r.Fingerprint = r.fingerprint()
	return r
}

// fingerprint hashes the non-secret context. Revision is included, so any profile edit
// changes it even when the edited fields hash the same.
func (r Resolved) fingerprint() string {
	h := sha256.New()
	field := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	field("hclaro-execution-context-v1")
	field(r.ProjectID)
	field(r.ProfileID)
	field(strconv.Itoa(r.Revision))
	field(r.Engine)
	field(r.Workspace)
	for _, f := range r.VarFiles {
		field("var:" + f.Ref + "=" + f.Path)
	}
	for _, f := range r.VarIssues {
		field("var-issue:" + f.Ref + "=" + f.Code)
	}
	field(fmt.Sprintf("options:%d:%d", r.Options.Parallelism, r.Options.LockTimeoutSeconds))
	field("aws:" + r.AWSProfile + ":" + r.AWSRegion)
	// Only a binding beyond schema 1 is hashed, so profiles without one keep the fingerprint
	// they had before M20 (the aws-cli method is what schema 1 did).
	if r.AWSAuth == AWSVault || r.AWSExpectedAccount != "" || r.AWSExpectedRole != "" {
		field("aws-auth:" + r.AWSAuth + ":" + r.AWSExpectedAccount + ":" + r.AWSExpectedRole)
	}
	for _, e := range r.Env {
		field(fmt.Sprintf("env:%s:%t", e.Name, e.Present))
	}
	for _, i := range r.Issues {
		field("issue:" + i.Code)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Blocking returns the first issue that prevents running an operation, or nil.
func (r Resolved) Blocking() *Issue {
	if len(r.Issues) == 0 {
		return nil
	}
	issue := r.Issues[0]
	return &issue
}

// awsCredentialNames are inherited values that would take precedence over, or compete with,
// the chosen AWS profile in the AWS credential chain: static keys, another profile, web
// identity and role variables, container credential endpoints and an enclosing aws-vault
// session. They are not passed when a profile names an AWS profile, so the alias the user
// chose is the credential source. Values are never read.
var awsCredentialNames = map[string]bool{
	"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_SECURITY_TOKEN": true,
	"AWS_CREDENTIAL_EXPIRATION": true, "AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true,
	"AWS_ROLE_ARN": true, "AWS_ROLE_SESSION_NAME": true, "AWS_WEB_IDENTITY_TOKEN_FILE": true,
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": true, "AWS_CONTAINER_CREDENTIALS_FULL_URI": true,
	"AWS_CONTAINER_AUTHORIZATION_TOKEN": true, "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": true,
	"AWS_VAULT": true,
}

func compositionNotices(p Profile, environ []string) []string {
	notices := []string{}
	dropped := map[string]bool{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		switch {
		case upper == "TF_WORKSPACE":
			dropped["TF_WORKSPACE"] = true
		case p.AWSProfile != "" && awsCredentialNames[upper] && upper != "AWS_PROFILE":
			dropped[upper] = true
		}
	}
	if dropped["TF_WORKSPACE"] {
		notices = append(notices, "Inherited TF_WORKSPACE is not passed; each command gets the workspace of this execution context.")
	}
	creds := []string{}
	for name := range dropped {
		if name != "TF_WORKSPACE" {
			creds = append(creds, name)
		}
	}
	sort.Strings(creds)
	if len(creds) > 0 {
		notices = append(notices, "Inherited "+strings.Join(creds, ", ")+" "+map[bool]string{true: "is", false: "are"}[len(creds) == 1]+" not passed, so AWS profile "+p.AWSProfile+" is the credential source. Values were not read into Hclaro.")
	}
	if p.AWSAuth == AWSVault {
		notices = append(notices, "Engine and AWS CLI commands run through aws-vault exec "+p.AWSProfile+" --. aws-vault passes temporary credentials to that command only; Hclaro never reads them.")
	}
	if p.AWSProfile != "" || p.AWSRegion != "" {
		notices = append(notices, "The AWS profile and region are names passed to the provider. Verify identity checks the AWS CLI credentials on request; provider aliases, assume_role or a profile set in the configuration can still use another account.")
	}
	return notices
}

// Compose returns the child environment for r: the inherited environment with TF_WORKSPACE,
// engine-owned variables and TF_CLI_ARGS* removed (unsafe TF_CLI_ARGS* refuse instead), inherited AWS
// credential overrides removed when an AWS profile is chosen, and only AWS_PROFILE (aws-cli)
// or the aws-vault marker (aws-vault, see internal/awsvault), AWS_REGION and
// AWS_DEFAULT_REGION added. The workspace is set by the engine adapter. Hclaro's own
// environment is never changed and nothing is persisted.
func Compose(base []string, r Resolved) ([]string, error) {
	if tfpolicy.UnsafeArgs(base) {
		return nil, tfpolicy.ErrUnsafeArgs
	}
	out := make([]string, 0, len(base)+3)
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		upper, rule := strings.ToUpper(key), tfpolicy.Controlled(key)
		switch {
		case rule == tfpolicy.Workspace, rule == tfpolicy.CLIArgs, rule == tfpolicy.EngineOwned, upper == awsvault.Marker:
			continue
		case r.AWSProfile != "" && awsCredentialNames[upper]:
			continue
		case r.AWSRegion != "" && rule == tfpolicy.AWSRegion:
			continue
		}
		out = append(out, item)
	}
	switch {
	case r.AWSAuth == AWSVault:
		out = append(out, awsvault.Marker+"="+r.AWSProfile)
	case r.AWSProfile != "":
		out = append(out, "AWS_PROFILE="+r.AWSProfile)
	}
	if r.AWSRegion != "" {
		out = append(out, "AWS_REGION="+r.AWSRegion, "AWS_DEFAULT_REGION="+r.AWSRegion)
	}
	return out, nil
}
