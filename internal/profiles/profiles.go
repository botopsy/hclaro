// Package profiles stores named execution profiles per registered project and resolves
// them into an immutable, sanitized execution context. A profile holds only non-secret
// choices: an engine preference, a workspace selector, approved variable file references,
// two bounded plan options, an AWS profile alias and region, and the names (never values)
// of environment variables an operation expects. Credentials, tokens, variable values and
// file contents have no field and cannot be stored.
package profiles

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"hclaro/internal/engine"
	"hclaro/internal/state"
)

// SchemaVersion is the profile file format written by this build.
const SchemaVersion = 1

const (
	fileName = "profiles.json"
	// MaxPerProject bounds the profiles of one project; MaxProfiles bounds the file.
	MaxPerProject = 50
	MaxProfiles   = 2000
	MaxVarFiles   = 16
	MaxEnvNames   = 16
	maxName       = 60
	maxFileBytes  = 1 << 20
)

// Options are the only engine options a profile can set. Zero keeps the engine default.
type Options struct {
	Parallelism        int `json:"parallelism,omitempty"`
	LockTimeoutSeconds int `json:"lock_timeout_seconds,omitempty"`
}

// Profile is one stored execution profile. Revision increases with every change, so an
// operation bound to a revision can tell that its profile changed.
type Profile struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"project_id"`
	Name       string   `json:"name"`
	Engine     string   `json:"engine,omitempty"`
	Workspace  string   `json:"workspace,omitempty"`
	VarFiles   []string `json:"var_files"`
	Options    Options  `json:"options"`
	AWSProfile string   `json:"aws_profile,omitempty"`
	AWSRegion  string   `json:"aws_region,omitempty"`
	EnvNames   []string `json:"env_names"`
	Revision   int      `json:"revision"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

// Input is the editable part of a profile.
type Input struct {
	Name       string
	Engine     string
	Workspace  string
	VarFiles   []string
	Options    Options
	AWSProfile string
	AWSRegion  string
	EnvNames   []string
}

type document struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
	// Defaults maps a project ID to its default profile ID.
	Defaults map[string]string `json:"defaults"`
}

// Store status values.
const (
	Ready       = "ready"
	Unavailable = "unavailable"
)

var (
	ErrUnavailable = errors.New("execution profiles are unavailable")
	ErrNotFound    = errors.New("execution profile not found")
	ErrRevision    = errors.New("the execution profile changed since it was loaded")
	ErrTooMany     = errors.New("too many execution profiles")
	ErrDuplicate   = errors.New("a profile with this name already exists in the project")
)

// FieldError rejects one input field with an actionable message.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

func invalid(field, message string) error { return &FieldError{field, message} }

// Store persists profiles in one JSON file. Like the project registry, every change re-reads
// the file first and replaces it atomically.
type Store struct {
	mu     sync.Mutex
	path   string
	status string
	notice string
	cached document
	Now    func() time.Time
}

// Open loads the store in dir. An undecodable file is moved aside and the store starts
// empty; a file from a newer schema or an unreadable file makes the store read-only.
func Open(dir string, now time.Time) *Store {
	s := &Store{path: filepath.Join(dir, fileName), status: Ready, cached: empty(), Now: time.Now}
	doc, err := s.read()
	var corrupt *corruptError
	switch {
	case err == nil:
		s.cached = doc
	case errors.As(err, &corrupt):
		backup := fmt.Sprintf("%s.corrupt-%s", s.path, now.UTC().Format("20060102T150405Z"))
		if os.Rename(s.path, backup) != nil {
			s.status, s.notice = Unavailable, "The execution profile file could not be decoded and could not be moved aside, so profiles are read-only. "+corrupt.reason
			return s
		}
		s.notice = "The execution profile file could not be decoded (" + corrupt.reason + "). It was moved to " + filepath.Base(backup) + " and profiles started empty."
	default:
		s.status, s.notice = Unavailable, err.Error()
	}
	return s
}

// Disabled is an empty, read-only store that never touches the disk.
func Disabled(notice string) *Store {
	return &Store{status: Unavailable, notice: notice, cached: empty(), Now: time.Now}
}

func empty() document {
	return document{Version: SchemaVersion, Profiles: []Profile{}, Defaults: map[string]string{}}
}

// Status reports the store status and a notice for the user.
func (s *Store) Status() (status, notice string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.notice
}

type corruptError struct{ reason string }

func (e *corruptError) Error() string { return e.reason }

func (s *Store) read() (document, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return empty(), nil
	}
	if err != nil {
		return document{}, errors.New("The execution profile file cannot be read. Check the permissions of " + s.path + ". The file was not changed.")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return document{}, errors.New("The execution profile file is not a regular file. The file was not changed.")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return document{}, errors.New("The execution profile file cannot be read. The file was not changed.")
	}
	if len(b) > maxFileBytes {
		return document{}, &corruptError{"the file is larger than 1 MiB"}
	}
	var version struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(b, &version) != nil {
		return document{}, &corruptError{"the file is not valid JSON"}
	}
	if version.Version > SchemaVersion {
		return document{}, fmt.Errorf("The execution profile file was written by a newer Hclaro (schema %d). Use that version, or move the file away. The file was not changed.", version.Version)
	}
	var doc document
	dec := json.NewDecoder(strings.NewReader(string(b)))
	// Unknown fields are refused, so a hand-edited file cannot smuggle values in.
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || doc.Version != SchemaVersion {
		return document{}, &corruptError{"the schema is not recognized"}
	}
	if doc.Profiles == nil {
		doc.Profiles = []Profile{}
	}
	if doc.Defaults == nil {
		doc.Defaults = map[string]string{}
	}
	if len(doc.Profiles) > MaxProfiles {
		return document{}, &corruptError{"it lists too many profiles"}
	}
	ids := map[string]bool{}
	for _, p := range doc.Profiles {
		if !validID(p.ID) || !validID(p.ProjectID) || ids[p.ID] || p.Revision < 1 || check(inputOf(p)) != nil {
			return document{}, &corruptError{"it contains an invalid profile"}
		}
		ids[p.ID] = true
	}
	for project, id := range doc.Defaults {
		i := slices.IndexFunc(doc.Profiles, func(p Profile) bool { return p.ID == id })
		if !validID(project) || i < 0 || doc.Profiles[i].ProjectID != project {
			return document{}, &corruptError{"it names an invalid default profile"}
		}
	}
	return doc, nil
}

func (s *Store) write(doc document) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("the configuration directory cannot be created")
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".profiles-*.tmp")
	if err != nil {
		return errors.New("the execution profile file cannot be written")
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return errors.New("the execution profile file cannot be written")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.New("the execution profile file cannot be written")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("the execution profile file cannot be written")
	}
	if err := os.Rename(name, s.path); err != nil {
		return errors.New("the execution profile file cannot be written")
	}
	return nil
}

func clone(doc document) document {
	out := document{Version: doc.Version, Profiles: make([]Profile, len(doc.Profiles)), Defaults: map[string]string{}}
	for i, p := range doc.Profiles {
		out.Profiles[i] = cloneProfile(p)
	}
	for k, v := range doc.Defaults {
		out.Defaults[k] = v
	}
	return out
}

func cloneProfile(p Profile) Profile {
	p.VarFiles = slices.Clone(p.VarFiles)
	p.EnvNames = slices.Clone(p.EnvNames)
	if p.VarFiles == nil {
		p.VarFiles = []string{}
	}
	if p.EnvNames == nil {
		p.EnvNames = []string{}
	}
	return p
}

// current returns the latest document; a file that became unreadable keeps the last view.
func (s *Store) current() (document, error) {
	if s.status != Ready {
		return clone(s.cached), ErrUnavailable
	}
	doc, err := s.read()
	if err != nil {
		return clone(s.cached), ErrUnavailable
	}
	s.cached = doc
	return clone(doc), nil
}

func (s *Store) mutate(change func(*document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != Ready {
		return ErrUnavailable
	}
	doc, err := s.read()
	if err != nil {
		return ErrUnavailable
	}
	next := clone(doc)
	if err := change(&next); err != nil {
		return err
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.cached = next
	return nil
}

// List returns one project's profiles in creation order and its default profile ID. A
// project only ever sees its own profiles.
func (s *Store) List(projectID string) ([]Profile, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.current()
	out := []Profile{}
	for _, p := range doc.Profiles {
		if p.ProjectID == projectID {
			out = append(out, p)
		}
	}
	return out, doc.Defaults[projectID], err
}

// Get returns a profile that belongs to projectID.
func (s *Store) Get(projectID, id string) (Profile, error) {
	profiles, _, err := s.List(projectID)
	for _, p := range profiles {
		if p.ID == id {
			return p, nil
		}
	}
	if err != nil {
		return Profile{}, err
	}
	return Profile{}, ErrNotFound
}

func (s *Store) now() string { return s.Now().UTC().Format(time.RFC3339) }

// Create adds a profile to a project.
func (s *Store) Create(projectID string, in Input) (Profile, error) {
	in = normalize(in)
	if err := check(in); err != nil {
		return Profile{}, err
	}
	var created Profile
	err := s.mutate(func(doc *document) error {
		count := 0
		for _, p := range doc.Profiles {
			if p.ProjectID == projectID {
				count++
				if strings.EqualFold(p.Name, in.Name) {
					return ErrDuplicate
				}
			}
		}
		if count >= MaxPerProject || len(doc.Profiles) >= MaxProfiles {
			return ErrTooMany
		}
		now := s.now()
		created = apply(Profile{ID: NewID(), ProjectID: projectID, Revision: 1, CreatedAt: now, UpdatedAt: now}, in)
		doc.Profiles = append(doc.Profiles, created)
		return nil
	})
	return created, err
}

// Update replaces a profile's fields when expectedRevision is still current.
func (s *Store) Update(projectID, id string, expectedRevision int, in Input) (Profile, error) {
	in = normalize(in)
	if err := check(in); err != nil {
		return Profile{}, err
	}
	var updated Profile
	err := s.mutate(func(doc *document) error {
		i := slices.IndexFunc(doc.Profiles, func(p Profile) bool { return p.ID == id && p.ProjectID == projectID })
		if i < 0 {
			return ErrNotFound
		}
		if doc.Profiles[i].Revision != expectedRevision {
			return ErrRevision
		}
		for _, p := range doc.Profiles {
			if p.ProjectID == projectID && p.ID != id && strings.EqualFold(p.Name, in.Name) {
				return ErrDuplicate
			}
		}
		next := apply(doc.Profiles[i], in)
		next.Revision++
		next.UpdatedAt = s.now()
		doc.Profiles[i] = next
		updated = next
		return nil
	})
	return updated, err
}

// Clone copies a profile within its project under a new name.
func (s *Store) Clone(projectID, id, name string) (Profile, error) {
	source, err := s.Get(projectID, id)
	if err != nil {
		return Profile{}, err
	}
	in := inputOf(source)
	in.Name = name
	return s.Create(projectID, in)
}

// Delete removes a profile when expectedRevision is still current; it stops being the
// default. Operations that already started keep their own immutable copy.
func (s *Store) Delete(projectID, id string, expectedRevision int) error {
	return s.mutate(func(doc *document) error {
		i := slices.IndexFunc(doc.Profiles, func(p Profile) bool { return p.ID == id && p.ProjectID == projectID })
		if i < 0 {
			return ErrNotFound
		}
		if doc.Profiles[i].Revision != expectedRevision {
			return ErrRevision
		}
		doc.Profiles = slices.Delete(doc.Profiles, i, i+1)
		if doc.Defaults[projectID] == id {
			delete(doc.Defaults, projectID)
		}
		return nil
	})
}

// SetDefault makes id the project's default profile; "" clears it.
func (s *Store) SetDefault(projectID, id string) error {
	return s.mutate(func(doc *document) error {
		if id == "" {
			delete(doc.Defaults, projectID)
			return nil
		}
		if !slices.ContainsFunc(doc.Profiles, func(p Profile) bool { return p.ID == id && p.ProjectID == projectID }) {
			return ErrNotFound
		}
		doc.Defaults[projectID] = id
		return nil
	})
}

// RemoveProject deletes every profile of an unregistered project.
func (s *Store) RemoveProject(projectID string) error {
	return s.mutate(func(doc *document) error {
		doc.Profiles = slices.DeleteFunc(doc.Profiles, func(p Profile) bool { return p.ProjectID == projectID })
		delete(doc.Defaults, projectID)
		return nil
	})
}

func inputOf(p Profile) Input {
	return Input{Name: p.Name, Engine: p.Engine, Workspace: p.Workspace, VarFiles: slices.Clone(p.VarFiles), Options: p.Options, AWSProfile: p.AWSProfile, AWSRegion: p.AWSRegion, EnvNames: slices.Clone(p.EnvNames)}
}

func apply(p Profile, in Input) Profile {
	p.Name, p.Engine, p.Workspace, p.Options, p.AWSProfile, p.AWSRegion = in.Name, in.Engine, in.Workspace, in.Options, in.AWSProfile, in.AWSRegion
	p.VarFiles, p.EnvNames = slices.Clone(in.VarFiles), slices.Clone(in.EnvNames)
	return cloneProfile(p)
}

func normalize(in Input) Input {
	if in.VarFiles == nil {
		in.VarFiles = []string{}
	}
	if in.EnvNames == nil {
		in.EnvNames = []string{}
	}
	return in
}

var (
	idPattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	awsProfileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+=@,-]{0,63}$`)
	awsRegion      = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
	envName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

func validID(id string) bool { return idPattern.MatchString(id) }

// ValidID reports whether id has the profile ID format.
func ValidID(id string) bool { return validID(id) }

// NewID returns a random profile ID.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// AutoLoaded reports variable files the engines load on their own from the root module.
func AutoLoaded(path string) bool {
	if strings.Contains(path, "/") {
		return false
	}
	return path == "terraform.tfvars" || path == "terraform.tfvars.json" || strings.HasSuffix(path, ".auto.tfvars") || strings.HasSuffix(path, ".auto.tfvars.json")
}

// controlled names are set or neutralized by Hclaro itself, so a profile cannot list them.
func controlled(name string) bool {
	upper := strings.ToUpper(name)
	switch upper {
	case "TF_WORKSPACE", "TF_IN_AUTOMATION", "CHECKPOINT_DISABLE", "TF_DATA_DIR", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION":
		return true
	}
	return strings.HasPrefix(upper, "TF_CLI_ARGS") || strings.HasPrefix(upper, "TF_LOG")
}

// check validates every field. Values that look like credentials are refused everywhere,
// because profile fields are displayed and stored in plain text.
func check(in Input) error {
	name := in.Name
	if name == "" || len([]rune(name)) > maxName || strings.TrimSpace(name) != name {
		return invalid("name", "Enter a profile name of 1–60 characters without leading or trailing spaces.")
	}
	for _, c := range name {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || c == unicode.ReplacementChar {
			return invalid("name", "The profile name contains control or formatting characters.")
		}
	}
	if state.LooksSecret(name) {
		return invalid("name", "The profile name looks like a credential. Use a descriptive label instead.")
	}
	if in.Engine != "" && in.Engine != "terraform" && in.Engine != "tofu" {
		return invalid("engine", "The engine must be Terraform, OpenTofu or the project's choice.")
	}
	if in.Workspace != "" && !engine.ValidWorkspace(in.Workspace) {
		return invalid("workspace", "Workspace names use letters, digits, - and _ (at most 90 characters).")
	}
	if len(in.VarFiles) > MaxVarFiles {
		return invalid("varFiles", fmt.Sprintf("A profile can approve at most %d variable files.", MaxVarFiles))
	}
	seen := map[string]bool{}
	for _, ref := range in.VarFiles {
		switch {
		case !engine.ValidVarFilePath(ref) || state.Suspicious(ref):
			return invalid("varFiles", "Variable files must be .tfvars or .tfvars.json paths inside the project root, without hidden folders.")
		case AutoLoaded(ref):
			return invalid("varFiles", ref+" is loaded automatically by the engine; it does not need to be approved.")
		case seen[ref]:
			return invalid("varFiles", ref+" is listed twice.")
		}
		seen[ref] = true
	}
	o := in.Options
	if o.Parallelism < 0 || o.Parallelism > engine.MaxParallelism {
		return invalid("parallelism", fmt.Sprintf("Parallelism must be between 1 and %d, or empty for the engine default.", engine.MaxParallelism))
	}
	if o.LockTimeoutSeconds < 0 || o.LockTimeoutSeconds > engine.MaxLockTimeoutSeconds {
		return invalid("lockTimeoutSeconds", "The state lock timeout must be between 1 and 3600 seconds, or empty for the engine default.")
	}
	if in.AWSProfile != "" && (!awsProfileName.MatchString(in.AWSProfile) || state.LooksSecret(in.AWSProfile)) {
		return invalid("awsProfile", "Enter the name of a profile from your AWS configuration (letters, digits and _.+=@,- only). Never enter an access key.")
	}
	if in.AWSRegion != "" && !awsRegion.MatchString(in.AWSRegion) {
		return invalid("awsRegion", "Enter an AWS region code such as eu-west-1.")
	}
	if len(in.EnvNames) > MaxEnvNames {
		return invalid("envNames", fmt.Sprintf("A profile can list at most %d environment variable names.", MaxEnvNames))
	}
	names := map[string]bool{}
	for _, n := range in.EnvNames {
		switch {
		case strings.Contains(n, "="):
			return invalid("envNames", "Enter environment variable names only, never values.")
		case !envName.MatchString(n) || state.Suspicious(n):
			return invalid("envNames", "Environment variable names use letters, digits and _ and cannot start with a digit.")
		case controlled(n):
			return invalid("envNames", n+" is set or neutralized by Hclaro and cannot be listed.")
		case names[strings.ToUpper(n)]:
			return invalid("envNames", n+" is listed twice.")
		}
		names[strings.ToUpper(n)] = true
	}
	return nil
}
