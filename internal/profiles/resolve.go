package profiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hclaro/internal/engine"
)

// VarFile is a resolved variable file reference: the reference stored in the profile, the
// canonical root-relative path passed to the engine, and display metadata. File contents are
// never read here.
type VarFile struct {
	Ref        string `json:"ref"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
	Linked     bool   `json:"linked"`
}

// VarFileError explains why a reference cannot be used.
type VarFileError struct {
	Code    string `json:"code"`
	Ref     string `json:"ref"`
	Message string `json:"message"`
}

func (e *VarFileError) Error() string { return e.Message }

// ResolveVarFile resolves ref inside root. The reference and its canonical target must both
// be valid variable file paths inside the canonical root, so a symbolic link cannot point
// outside the root, into a hidden folder or at a non-variable file.
func ResolveVarFile(root, ref string) (VarFile, error) {
	fail := func(code, message string) (VarFile, error) {
		return VarFile{}, &VarFileError{code, ref, message}
	}
	if !engine.ValidVarFilePath(ref) || AutoLoaded(ref) {
		return fail("var_file_invalid", ref+" is not a selectable variable file path.")
	}
	// The root is compared in canonical form, so a root reached through a link (for example
	// /var on macOS) does not make every file look outside it.
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	} else {
		return fail("var_file_unreadable", "The project root cannot be resolved.")
	}
	full := filepath.Join(root, filepath.FromSlash(ref))
	link, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("var_file_missing", ref+" was not found in the project root. Restore it, or remove it from the profile.")
	}
	if err != nil {
		return fail("var_file_unreadable", ref+" cannot be read. Check its permissions.")
	}
	resolved, err := filepath.EvalSymlinks(full)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("var_file_missing", ref+" is a link to a file that no longer exists. Restore it, or remove it from the profile.")
	}
	if err != nil {
		return fail("var_file_unreadable", ref+" cannot be resolved. Check its permissions.")
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return fail("var_file_outside_root", ref+" resolves outside the project root through a symbolic link, so it was refused.")
	}
	rel = filepath.ToSlash(rel)
	if !engine.ValidVarFilePath(rel) || AutoLoaded(rel) {
		return fail("var_file_outside_root", ref+" resolves to "+rel+", which is not a selectable variable file (hidden folder, unusual name or automatically loaded file).")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return fail("var_file_not_regular", ref+" is not a regular file.")
	}
	return VarFile{Ref: ref, Path: rel, Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), Linked: link.Mode()&fs.ModeSymlink != 0}, nil
}

// Candidates lists variable files below root that a profile can approve: .tfvars and
// .tfvars.json files at most five levels deep, skipping hidden folders, node_modules and
// files the engine loads automatically. Linked files are listed only when they resolve to
// a valid file inside the root. The walk is bounded; truncated reports whether it stopped.
func Candidates(root string) (files []VarFile, truncated bool) {
	files = []VarFile{}
	visited := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		visited++
		if visited > 20_000 || len(files) >= 500 {
			truncated = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || strings.Count(rel, "/") >= 4) {
				return filepath.SkipDir
			}
			return nil
		}
		if !engine.ValidVarFilePath(rel) || AutoLoaded(rel) {
			return nil
		}
		if f, err := ResolveVarFile(root, rel); err == nil {
			files = append(files, f)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Ref < files[j].Ref })
	return files, truncated
}

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
	ProjectID   string         `json:"projectId"`
	ProfileID   string         `json:"profileId,omitempty"`
	ProfileName string         `json:"profileName,omitempty"`
	Revision    int            `json:"profileRevision,omitempty"`
	Engine      string         `json:"enginePreference,omitempty"`
	Workspace   string         `json:"workspace,omitempty"`
	VarFiles    []VarFile      `json:"varFiles"`
	Options     Options        `json:"options"`
	AWSProfile  string         `json:"awsProfile,omitempty"`
	AWSRegion   string         `json:"awsRegion,omitempty"`
	Env         []EnvStatus    `json:"env"`
	Identity    string         `json:"identity"`
	Notices     []string       `json:"notices"`
	Issues      []Issue        `json:"issues"`
	VarIssues   []VarFileError `json:"varFileIssues"`
	Fingerprint string         `json:"fingerprint"`
}

// IdentityNotVerified is the only identity status in this release: Hclaro never calls a
// cloud API to learn which account credentials belong to.
const IdentityNotVerified = "not_verified"

// None is the context of an operation without a profile: the inherited environment.
func None(projectID string, environ []string) Resolved {
	r := Resolved{ProjectID: projectID, VarFiles: []VarFile{}, Env: []EnvStatus{}, Identity: IdentityNotVerified, Notices: []string{}, Issues: []Issue{}, VarIssues: []VarFileError{}}
	if UnsafeCLIArgs(environ) {
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
	for _, ref := range p.VarFiles {
		f, err := ResolveVarFile(root, ref)
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

// VarFilePaths returns the canonical root-relative paths passed to the engine.
func (r Resolved) VarFilePaths() []string {
	out := make([]string, 0, len(r.VarFiles))
	for _, f := range r.VarFiles {
		out = append(out, f.Path)
	}
	return out
}

// UnsafeCLIArgs reports inherited TF_CLI_ARGS or TF_CLI_ARGS_<command> values with any
// argument other than -no-color. Names are compared without case, as Windows does.
func UnsafeCLIArgs(environ []string) bool {
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "TF_CLI_ARGS") {
			continue
		}
		for _, arg := range strings.Fields(value) {
			if arg != "-no-color" {
				return true
			}
		}
	}
	return false
}

// awsCredentialNames are inherited values that would take precedence over AWS_PROFILE in the
// AWS credential chain. They are not passed when a profile names an AWS profile, so the
// alias the user chose is the one the provider uses.
var awsCredentialNames = map[string]bool{"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_SECURITY_TOKEN": true, "AWS_PROFILE": true, "AWS_DEFAULT_PROFILE": true}

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
	if p.AWSProfile != "" || p.AWSRegion != "" {
		notices = append(notices, "The AWS profile and region are names passed to the provider. Hclaro does not verify which account they belong to.")
	}
	return notices
}

// Compose returns the child environment for r: the inherited environment with TF_WORKSPACE,
// TF_LOG* and TF_CLI_ARGS* removed (unsafe TF_CLI_ARGS* refuse instead), inherited AWS
// credential overrides removed when an AWS profile is chosen, and only AWS_PROFILE,
// AWS_REGION and AWS_DEFAULT_REGION added. The workspace is set by the engine adapter.
// Hclaro's own environment is never changed and nothing is persisted.
func Compose(base []string, r Resolved) ([]string, error) {
	if UnsafeCLIArgs(base) {
		return nil, engine.ErrUnsafeArgs
	}
	out := make([]string, 0, len(base)+3)
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		switch {
		case upper == "TF_WORKSPACE", strings.HasPrefix(upper, "TF_CLI_ARGS"), strings.HasPrefix(upper, "TF_LOG"):
			continue
		case r.AWSProfile != "" && awsCredentialNames[upper]:
			continue
		case r.AWSRegion != "" && (upper == "AWS_REGION" || upper == "AWS_DEFAULT_REGION"):
			continue
		}
		out = append(out, item)
	}
	if r.AWSProfile != "" {
		out = append(out, "AWS_PROFILE="+r.AWSProfile)
	}
	if r.AWSRegion != "" {
		out = append(out, "AWS_REGION="+r.AWSRegion, "AWS_DEFAULT_REGION="+r.AWSRegion)
	}
	return out, nil
}
