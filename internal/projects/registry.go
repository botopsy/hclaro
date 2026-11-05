// Package projects implements the Workspace Hub: a persistent registry of project roots,
// read-only CLI inspection of each root, and immutable per-project, per-workspace read
// contexts. Nothing here initializes, selects workspaces or changes state.
package projects

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
)

// SchemaVersion is the registry file format written by this build.
const SchemaVersion = 1

const (
	// MaxProjects bounds the registry.
	MaxProjects  = 200
	maxAlias     = 80
	maxFileBytes = 1 << 20
	fileName     = "projects.json"
)

// Record is one registered project. Only non-sensitive metadata is stored: no credentials,
// environment values, backend configuration or state.
type Record struct {
	ID               string `json:"id"`
	Alias            string `json:"alias"`
	RootPath         string `json:"root_path"`
	EnginePreference string `json:"engine_preference,omitempty"`
	Pinned           bool   `json:"pinned"`
	AddedAt          string `json:"added_at"`
	LastOpenedAt     string `json:"last_opened_at,omitempty"`
	LastWorkspace    string `json:"last_workspace,omitempty"`
}

type document struct {
	Version  int      `json:"version"`
	Projects []Record `json:"projects"`
}

// Registry status values.
const (
	RegistryReady = "ready"
	// RegistryUnavailable means the file cannot be read or was written by a newer Hclaro.
	// Changes are refused so the file is never overwritten.
	RegistryUnavailable = "unavailable"
)

var (
	ErrRegistryUnavailable = errors.New("the project registry is unavailable")
	ErrNotFound            = errors.New("project not found")
	ErrDuplicate           = errors.New("project already registered")
	ErrTooMany             = errors.New("too many projects")
	ErrInvalidAlias        = errors.New("invalid alias")
	ErrInvalidEngine       = errors.New("invalid engine preference")
	ErrInvalidOrder        = errors.New("invalid project order")
)

// Registry persists records in one JSON file. Every change re-reads the file first, so
// edits made by another Hclaro process are kept unless both change it at the same moment
// (last writer wins; the write itself is atomic).
type Registry struct {
	mu     sync.Mutex
	path   string
	status string
	// notice explains the status or a recovery performed at startup.
	notice string
	cached []Record
}

// ConfigDir is the per-user Hclaro configuration directory. HCLARO_CONFIG_DIR overrides it,
// which keeps tests and demos away from the real registry.
func ConfigDir() (string, error) {
	if dir := os.Getenv("HCLARO_CONFIG_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("HCLARO_CONFIG_DIR must be an absolute path")
		}
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "hclaro"), nil
}

// Open loads the registry in dir. A file that cannot be decoded is moved aside to a
// timestamped backup and the registry starts empty; a file from a newer schema or one
// that cannot be read is left untouched and the registry becomes read-only.
func Open(dir string, now time.Time) *Registry {
	r := &Registry{path: filepath.Join(dir, fileName), status: RegistryReady, cached: []Record{}}
	records, err := r.read()
	var corrupt *corruptError
	switch {
	case err == nil:
		r.cached = records
	case errors.As(err, &corrupt):
		backup := fmt.Sprintf("%s.corrupt-%s", r.path, now.UTC().Format("20060102T150405Z"))
		if os.Rename(r.path, backup) != nil {
			r.status, r.notice = RegistryUnavailable, "The project registry could not be decoded and could not be moved aside, so it was left unchanged and the Hub is read-only. "+corrupt.reason
			return r
		}
		r.notice = "The project registry could not be decoded (" + corrupt.reason + "). It was moved to " + filepath.Base(backup) + " next to the registry file, and the Hub started empty."
	default:
		r.status, r.notice = RegistryUnavailable, err.Error()
	}
	return r
}

// Unavailable is an empty, read-only registry that never touches the disk.
func Unavailable(notice string) *Registry {
	return &Registry{status: RegistryUnavailable, notice: notice, cached: []Record{}}
}

// Status reports the registry status, a notice for the user and the file path.
func (r *Registry) Status() (status, notice, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, r.notice, r.path
}

type corruptError struct{ reason string }

func (e *corruptError) Error() string { return e.reason }

// read returns the records on disk. A missing file is an empty registry.
func (r *Registry) read() ([]Record, error) {
	f, err := os.Open(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, errors.New("The project registry cannot be read. Check the permissions of " + r.path + ". The file was not changed.")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("The project registry is not a regular file. The file was not changed.")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, errors.New("The project registry cannot be read. The file was not changed.")
	}
	if len(b) > maxFileBytes {
		return nil, &corruptError{"the file is larger than 1 MiB"}
	}
	var version struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(b, &version) != nil {
		return nil, &corruptError{"the file is not valid JSON"}
	}
	if version.Version > SchemaVersion {
		return nil, errors.New("The project registry was written by a newer Hclaro (schema " + fmt.Sprint(version.Version) + "). Use that version, or move the file away. The file was not changed.")
	}
	var doc document
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || doc.Version != SchemaVersion {
		return nil, &corruptError{"the schema is not recognized"}
	}
	if len(doc.Projects) > MaxProjects {
		return nil, &corruptError{"it lists too many projects"}
	}
	ids := map[string]bool{}
	roots := map[string]bool{}
	for _, p := range doc.Projects {
		if !ValidID(p.ID) || ids[p.ID] || roots[p.RootPath] || !filepath.IsAbs(p.RootPath) || filepath.Clean(p.RootPath) != p.RootPath || validAlias(p.Alias) != nil || validEngine(p.EnginePreference) != nil || p.LastWorkspace != "" && !workspaceName.MatchString(p.LastWorkspace) {
			return nil, &corruptError{"it contains an invalid project entry"}
		}
		ids[p.ID], roots[p.RootPath] = true, true
	}
	if doc.Projects == nil {
		doc.Projects = []Record{}
	}
	return doc.Projects, nil
}

// write replaces the file atomically: a private temporary file in the same directory is
// written, synced and renamed over the old one.
func (r *Registry) write(records []Record) error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("the configuration directory cannot be created")
	}
	b, err := json.MarshalIndent(document{SchemaVersion, records}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".projects-*.tmp")
	if err != nil {
		return errors.New("the project registry cannot be written")
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return errors.New("the project registry cannot be written")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.New("the project registry cannot be written")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("the project registry cannot be written")
	}
	if err := os.Rename(name, r.path); err != nil {
		return errors.New("the project registry cannot be written")
	}
	return nil
}

// List returns the current records, re-read from disk.
func (r *Registry) List() ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != RegistryReady {
		return slices.Clone(r.cached), ErrRegistryUnavailable
	}
	records, err := r.read()
	if err != nil {
		// Something changed the file under us; keep the last good view and refuse changes.
		return slices.Clone(r.cached), ErrRegistryUnavailable
	}
	r.cached = records
	return slices.Clone(records), nil
}

// Get returns one record.
func (r *Registry) Get(id string) (Record, error) {
	records, err := r.List()
	if err != nil && !errors.Is(err, ErrRegistryUnavailable) {
		return Record{}, err
	}
	for _, p := range records {
		if p.ID == id {
			return p, nil
		}
	}
	return Record{}, ErrNotFound
}

// mutate re-reads the file, applies change and writes the result.
func (r *Registry) mutate(change func([]Record) ([]Record, error)) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != RegistryReady {
		return nil, ErrRegistryUnavailable
	}
	records, err := r.read()
	if err != nil {
		return nil, ErrRegistryUnavailable
	}
	next, err := change(slices.Clone(records))
	if err != nil {
		return nil, err
	}
	if err := r.write(next); err != nil {
		return nil, err
	}
	r.cached = next
	return slices.Clone(next), nil
}

// Add registers a canonical root. same reports whether an existing root is the same
// directory (for case-insensitive file systems and hard-to-spot aliases).
func (r *Registry) Add(record Record, same func(existing string) bool) (Record, error) {
	if err := validAlias(record.Alias); err != nil {
		return Record{}, err
	}
	if err := validEngine(record.EnginePreference); err != nil {
		return Record{}, err
	}
	var existing string
	_, err := r.mutate(func(records []Record) ([]Record, error) {
		for _, p := range records {
			if p.RootPath == record.RootPath || same(p.RootPath) {
				existing = p.ID
				return nil, ErrDuplicate
			}
		}
		if len(records) >= MaxProjects {
			return nil, ErrTooMany
		}
		return append(records, record), nil
	})
	if errors.Is(err, ErrDuplicate) {
		return Record{ID: existing}, err
	}
	return record, err
}

// Change holds optional edits; nil fields are kept.
type Change struct {
	Alias            *string
	Pinned           *bool
	EnginePreference *string
	RootPath         *string
}

// Update edits one record. A new root must already be canonical and validated.
func (r *Registry) Update(id string, c Change, same func(existing string) bool) (Record, error) {
	if c.Alias != nil {
		if err := validAlias(*c.Alias); err != nil {
			return Record{}, err
		}
	}
	if c.EnginePreference != nil {
		if err := validEngine(*c.EnginePreference); err != nil {
			return Record{}, err
		}
	}
	var updated Record
	_, err := r.mutate(func(records []Record) ([]Record, error) {
		i := slices.IndexFunc(records, func(p Record) bool { return p.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		if c.RootPath != nil {
			for _, p := range records {
				if p.ID != id && (p.RootPath == *c.RootPath || same(p.RootPath)) {
					return nil, ErrDuplicate
				}
			}
			records[i].RootPath = *c.RootPath
			records[i].LastWorkspace = ""
		}
		if c.Alias != nil {
			records[i].Alias = *c.Alias
		}
		if c.Pinned != nil {
			records[i].Pinned = *c.Pinned
		}
		if c.EnginePreference != nil {
			records[i].EnginePreference = *c.EnginePreference
		}
		updated = records[i]
		return records, nil
	})
	return updated, err
}

// Touch records when a project workspace was last opened.
func (r *Registry) Touch(id, workspace string, now time.Time) error {
	_, err := r.mutate(func(records []Record) ([]Record, error) {
		i := slices.IndexFunc(records, func(p Record) bool { return p.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		records[i].LastOpenedAt = now.UTC().Format(time.RFC3339)
		records[i].LastWorkspace = workspace
		return records, nil
	})
	return err
}

// Remove unregisters a project. Only the registry entry is deleted; project files, data
// directories, state and backends are never touched.
func (r *Registry) Remove(id string) (Record, error) {
	var removed Record
	_, err := r.mutate(func(records []Record) ([]Record, error) {
		i := slices.IndexFunc(records, func(p Record) bool { return p.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		removed = records[i]
		return slices.Delete(records, i, i+1), nil
	})
	return removed, err
}

// Reorder sets the stored order. ids must be a permutation of the registered IDs.
func (r *Registry) Reorder(ids []string) error {
	_, err := r.mutate(func(records []Record) ([]Record, error) {
		if len(ids) != len(records) {
			return nil, ErrInvalidOrder
		}
		byID := map[string]Record{}
		for _, p := range records {
			byID[p.ID] = p
		}
		next := make([]Record, 0, len(records))
		for _, id := range ids {
			p, ok := byID[id]
			if !ok {
				return nil, ErrInvalidOrder
			}
			delete(byID, id)
			next = append(next, p)
		}
		return next, nil
	})
	return err
}

var (
	idPattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	workspaceName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,90}$`)
)

// ValidID reports whether id has the registry ID format.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// NewID returns a random registry ID.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// validAlias accepts display text only: 1–80 characters, no control or format characters
// and no surrounding spaces. Aliases are data; the UI renders them as text.
func validAlias(alias string) error {
	if alias == "" || len([]rune(alias)) > maxAlias || strings.TrimSpace(alias) != alias {
		return ErrInvalidAlias
	}
	for _, c := range alias {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || c == unicode.ReplacementChar {
			return ErrInvalidAlias
		}
	}
	return nil
}

func validEngine(name string) error {
	if name != "" && name != "terraform" && name != "tofu" {
		return ErrInvalidEngine
	}
	return nil
}
