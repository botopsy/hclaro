// Package history keeps a local, sanitized record of the operations Hclaro ran: what was
// run, when, against which project and workspace, how it ended, and a redacted summary of
// what a plan proposed. It never stores plan files, state, environment values, credentials
// or raw logs, and a historical plan can never be applied.
//
// The history is one JSON file in the per-user configuration directory, written atomically
// with owner-only permissions and bounded by a retention policy.
package history

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

	"hclaro/internal/operations"
)

// SchemaVersion is the history file format written by this build.
const SchemaVersion = 1

const fileName = "history.json"

// Store statuses.
const (
	Ready = "ready"
	// ReadOnly: the file was written by a newer Hclaro; it is never changed or decoded.
	ReadOnly = "read_only"
	// Unavailable: there is no usable configuration directory or the file cannot be read.
	Unavailable = "unavailable"
)

// Entry statuses. Running is only ever written for an operation in progress; an entry still
// running when Hclaro starts again was interrupted and becomes Unknown.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusUnknown   = "unknown"
)

// Kinds of recorded operations.
var Kinds = []string{"plan", "replace", "import", "apply", "drift", "graph", "init", "workspace", "tool"}

var statuses = []string{StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled, StatusUnknown}

// HistoryEntry is one recorded operation. Every string in it has passed the redaction
// checks in clean; nothing here can re-run or apply anything.
type HistoryEntry struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Action     string `json:"action,omitempty"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	DurationMs int64  `json:"durationMs"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	// Engine is terraform or tofu; EngineVersion is empty when it was not known.
	Engine        string `json:"engine"`
	EngineVersion string `json:"engineVersion,omitempty"`
	// ProjectID is the Hub project ID or, for a project that is not registered, an ID derived
	// from its root. ProjectName is the Hub alias or the folder name; the path is not kept.
	ProjectID   string          `json:"projectId"`
	ProjectName string          `json:"projectName"`
	Registered  bool            `json:"registered"`
	Workspace   string          `json:"workspace,omitempty"`
	Context     *ContextSummary `json:"context,omitempty"`
	Command     []string        `json:"command"`
	Diagnostic  *Diagnostic     `json:"diagnostic,omitempty"`
	Plan        *PlanSummary    `json:"plan,omitempty"`
	Outcome     Outcome         `json:"outcome"`
	Git         *GitIdentity    `json:"git,omitempty"`
	Excerpt     []Line          `json:"excerpt"`
	// ExcerptOmitted counts earlier log lines that were not kept.
	ExcerptOmitted int64 `json:"excerptOmitted,omitempty"`
	// Session identifies the Hclaro process that wrote a running entry.
	Session string `json:"session,omitempty"`
}

// ContextSummary identifies the execution context without holding environment values.
type ContextSummary struct {
	// Fingerprint is a shortened fingerprint of the context: equal values mean the same
	// engine, workspace, variable files and profile environment names.
	Fingerprint     string `json:"fingerprint,omitempty"`
	ProfileName     string `json:"profileName,omitempty"`
	ProfileRevision int    `json:"profileRevision,omitempty"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Line is one kept, sanitized log line.
type Line struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// GitIdentity is the commit checked out in the project root when the operation started, read
// from the repository files. Dirty working trees are not detected.
type GitIdentity struct {
	Commit string `json:"commit"`
	Branch string `json:"branch,omitempty"`
}

// PlanSummary is what a reviewed plan proposed: counts and the addresses with their actions.
// Attribute values, outputs and the plan file are never kept.
type PlanSummary struct {
	PlanID      string           `json:"planId"`
	Kind        string           `json:"kind"`
	Counts      map[string]int   `json:"counts"`
	Changes     []PlannedChange  `json:"changes"`
	Omitted     int              `json:"omitted,omitempty"`
	Outputs     int              `json:"outputs"`
	Drift       int              `json:"drift"`
	Errored     bool             `json:"errored"`
	Destructive bool             `json:"destructive"`
	VarFiles    []string         `json:"varFiles"`
	Replace     []string         `json:"replace"`
	Import      string           `json:"importAddress,omitempty"`
	Fingerprint string           `json:"fingerprint"`
	Execution   *ExecutionRecord `json:"execution,omitempty"`
}

// PlannedChange is one resource instance a plan proposed to change.
type PlannedChange struct {
	Address string `json:"address"`
	Action  string `json:"action"`
}

// ExecutionRecord links a plan to the apply that used it.
type ExecutionRecord struct {
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
	Result      string `json:"result"`
	FinishedAt  string `json:"finishedAt,omitempty"`
}

// Outcome is the confirmed result of an operation, kept apart from what was planned.
// Verified is never true for applies: an engine exit is not proof that a resource is healthy.
type Outcome struct {
	Result        string `json:"result"`
	Summary       string `json:"summary"`
	StateReloaded bool   `json:"stateReloaded,omitempty"`
	Verified      bool   `json:"verified"`
	// PlanID is the plan an apply used.
	PlanID string `json:"planId,omitempty"`
	// Drift counts for a drift scan.
	Drift *DriftCounts `json:"drift,omitempty"`
}

type DriftCounts struct {
	Changed      int `json:"changed"`
	Missing      int `json:"missing"`
	CannotVerify int `json:"cannotVerify"`
}

// HistoryRetentionPolicy bounds the history. Entries older than MaxAgeDays are removed, then
// the oldest entries until at most MaxEntries remain and the file fits in MaxBytes.
type HistoryRetentionPolicy struct {
	MaxEntries int `json:"maxEntries"`
	MaxAgeDays int `json:"maxAgeDays"`
	MaxBytes   int `json:"maxBytes"`
}

// DefaultPolicy is the retention for a new history file.
var DefaultPolicy = HistoryRetentionPolicy{MaxEntries: 500, MaxAgeDays: 90, MaxBytes: 4 << 20}

// Policy bounds.
const (
	MinEntries, MaxEntriesLimit = 10, 5000
	MinAgeDays, MaxAgeDaysLimit = 1, 3650
	MinBytes, MaxBytesLimit     = 64 << 10, 16 << 20
)

// Validate checks a policy against the bounds above.
func (p HistoryRetentionPolicy) Validate() error {
	switch {
	case p.MaxEntries < MinEntries || p.MaxEntries > MaxEntriesLimit:
		return fmt.Errorf("Keep between %d and %d operations.", MinEntries, MaxEntriesLimit)
	case p.MaxAgeDays < MinAgeDays || p.MaxAgeDays > MaxAgeDaysLimit:
		return fmt.Errorf("Keep operations for between %d and %d days.", MinAgeDays, MaxAgeDaysLimit)
	case p.MaxBytes < MinBytes || p.MaxBytes > MaxBytesLimit:
		return fmt.Errorf("The history size limit must be between %d KiB and %d MiB.", MinBytes>>10, MaxBytesLimit>>20)
	}
	return nil
}

type document struct {
	Version   int                    `json:"version"`
	Retention HistoryRetentionPolicy `json:"retention"`
	Entries   []HistoryEntry         `json:"entries"`
}

// Migration upgrades a document of one schema version to the next.
type Migration func(json.RawMessage) (json.RawMessage, error)

// Store is the history file. Every change re-reads the file first, so two Hclaro processes
// sharing a configuration directory merge their entries; the last writer wins for a single
// entry, and every write replaces the file atomically.
type Store struct {
	mu      sync.Mutex
	path    string
	status  string
	notice  string
	session string
	doc     document
	stamp   fileStamp
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Project resolves a project root to its ID, display name and whether it is registered.
	Project func(root string) (id, name string, registered bool)
	// Git reads the commit checked out in a root; nil disables it.
	Git func(root string) *GitIdentity
	// Migrations maps a schema version to the migration that upgrades it to the next one.
	Migrations map[int]Migration
	// Sync forces data to disk before the rename. Tests may turn it off for speed.
	Sync bool
}

type fileStamp struct {
	size int64
	mod  time.Time
}

// maxFileBytes bounds what Open reads; anything larger is treated as corrupt.
const maxFileBytes = MaxBytesLimit + 1<<20

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidID reports whether id has the shape of an operation ID.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Open loads the history in dir. Leftover temporary files from an interrupted write are
// removed. A file that cannot be decoded is moved aside to a timestamped backup and history
// starts empty; a file from a newer schema is left untouched and nothing is recorded.
// Entries still marked running by an earlier process are marked interrupted.
func Open(dir string, now time.Time) *Store {
	s := newStore(filepath.Join(dir, fileName))
	s.Migrations = DefaultMigrations()
	s.load(now)
	return s
}

// OpenWith is Open with explicit migrations (tests).
func OpenWith(dir string, now time.Time, migrations map[int]Migration) *Store {
	s := newStore(filepath.Join(dir, fileName))
	s.Migrations = migrations
	s.load(now)
	return s
}

func newStore(path string) *Store {
	return &Store{path: path, status: Ready, session: newSession(), doc: document{Version: SchemaVersion, Retention: DefaultPolicy, Entries: []HistoryEntry{}}, Now: time.Now, Sync: true}
}

// DefaultMigrations is empty: schema 1 is the first history format. A later schema adds
// its upgrade here, keyed by the version it upgrades from.
func DefaultMigrations() map[int]Migration { return map[int]Migration{} }

// Disabled is an empty history that never touches the disk.
func Disabled(notice string) *Store {
	s := newStore("")
	s.status, s.notice = Unavailable, notice
	return s
}

func newSession() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Store) load(now time.Time) {
	dir := filepath.Dir(s.path)
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".history-*.tmp")); len(leftovers) > 0 {
		for _, name := range leftovers {
			_ = os.Remove(name)
		}
	}
	doc, loaded, err := s.read()
	var corrupt *corruptError
	switch {
	case err == nil:
		if loaded.from != SchemaVersion {
			// A schema upgrade rewrites the file. The original is copied aside first so a
			// rollback to the older Hclaro can restore it; without a copy nothing is changed.
			backup := fmt.Sprintf("%s.schema-%d-%s", s.path, loaded.from, now.UTC().Format("20060102T150405Z"))
			if err := writeBackup(backup, loaded.original); err != nil {
				s.status, s.notice = Unavailable, fmt.Sprintf("Operation history uses schema %d and must be upgraded to schema %d, but a backup copy could not be written, so the file was left unchanged and nothing new is recorded.", loaded.from, SchemaVersion)
				return
			}
			s.notice = fmt.Sprintf("Operation history was upgraded from schema %d to schema %d. The previous file was kept as %s next to the history file.", loaded.from, SchemaVersion, filepath.Base(backup))
		}
		s.doc = doc
	case errors.As(err, &corrupt):
		backup := fmt.Sprintf("%s.corrupt-%s", s.path, now.UTC().Format("20060102T150405Z"))
		if os.Rename(s.path, backup) != nil {
			s.status, s.notice = Unavailable, "Operation history could not be decoded and could not be moved aside, so it was left unchanged and nothing new is recorded. "+corrupt.reason
			return
		}
		s.notice = "Operation history could not be decoded (" + corrupt.reason + "). It was moved to " + filepath.Base(backup) + " next to the history file, and history started empty."
		return
	case errors.As(err, new(*newerError)):
		s.status, s.notice = ReadOnly, err.Error()
		return
	default:
		s.status, s.notice = Unavailable, err.Error()
		return
	}
	interrupted := false
	for i := range s.doc.Entries {
		if e := &s.doc.Entries[i]; e.Status == StatusRunning && e.Session != s.session {
			interrupt(e)
			interrupted = true
		}
	}
	if loaded.rewrite || interrupted {
		if err := s.write(s.doc); err != nil {
			s.notice = "Operation history could not be updated: " + err.Error()
		}
	}
}

// interrupt marks an entry whose process ended before recording an outcome.
func interrupt(e *HistoryEntry) {
	e.Status, e.Session = StatusUnknown, ""
	e.Outcome = Outcome{Result: "interrupted", Summary: "Hclaro stopped before this operation recorded an outcome. Its result is unknown." + interruptedRisk(e.Kind)}
	if e.Plan != nil && e.Kind == "apply" {
		e.Outcome.PlanID = e.Plan.PlanID
	}
}

func interruptedRisk(kind string) string {
	switch kind {
	case "apply":
		return " Infrastructure and state may be partly changed. Check state with your CLI and run a new plan."
	case "init":
		return " Downloads under the data directory may be incomplete."
	case "workspace":
		return " Check the workspace list with your CLI."
	}
	return " It did not change infrastructure."
}

type newerError struct{ version int }

func (e *newerError) Error() string {
	return fmt.Sprintf("Operation history was written by a newer Hclaro (schema %d). The file was not changed, nothing is shown from it and nothing new is recorded. Use that version, or move the file away.", e.version)
}

type corruptError struct{ reason string }

func (e *corruptError) Error() string { return e.reason }

// read returns the document on disk, migrated to SchemaVersion. A missing file is empty.
// loadedFile describes how read obtained a document: the schema version on disk, its
// original bytes when that version was upgraded, and whether the file needs rewriting.
type loadedFile struct {
	from     int
	original []byte
	rewrite  bool
}

func (s *Store) read() (doc document, loaded loadedFile, err error) {
	doc = document{Version: SchemaVersion, Retention: DefaultPolicy, Entries: []HistoryEntry{}}
	loaded.from = SchemaVersion
	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		s.stamp = fileStamp{}
		return doc, loaded, nil
	}
	if err != nil {
		return doc, loaded, errors.New("Operation history cannot be read. Check the permissions of " + s.path + ". The file was not changed.")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return doc, loaded, errors.New("Operation history is not a regular file. The file was not changed.")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return doc, loaded, errors.New("Operation history cannot be read. The file was not changed.")
	}
	if len(b) > maxFileBytes {
		return doc, loaded, &corruptError{"the file is larger than the history size limit"}
	}
	var version struct {
		Version *int `json:"version"`
	}
	if json.Unmarshal(b, &version) != nil {
		return doc, loaded, &corruptError{"the file is not valid JSON"}
	}
	if version.Version == nil || *version.Version < 0 {
		return doc, loaded, &corruptError{"the schema is not recognized"}
	}
	v := *version.Version
	if v > SchemaVersion {
		return doc, loaded, &newerError{v}
	}
	raw := json.RawMessage(b)
	if v < SchemaVersion {
		loaded.from, loaded.original, loaded.rewrite = v, b, true
	}
	for ; v < SchemaVersion; v++ {
		migrate := s.Migrations[v]
		if migrate == nil {
			return doc, loaded, &corruptError{fmt.Sprintf("there is no upgrade from schema %d", v)}
		}
		if raw, err = migrate(raw); err != nil {
			return doc, loaded, &corruptError{fmt.Sprintf("the upgrade from schema %d failed", v)}
		}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var decoded document
	if dec.Decode(&decoded) != nil || decoded.Version != SchemaVersion {
		return doc, loaded, &corruptError{"the schema is not recognized"}
	}
	if decoded.Retention.Validate() != nil {
		decoded.Retention = DefaultPolicy
	}
	// Entries that fail validation are dropped one by one; the rest are kept.
	kept := []HistoryEntry{}
	seen := map[string]bool{}
	for _, e := range decoded.Entries {
		if valid(e) && !seen[e.ID] {
			seen[e.ID] = true
			kept = append(kept, scrubEntry(e))
		}
	}
	if len(kept) != len(decoded.Entries) {
		loaded.rewrite = true
	}
	decoded.Entries = kept
	s.stamp = fileStamp{fi.Size(), fi.ModTime()}
	return decoded, loaded, nil
}

func valid(e HistoryEntry) bool {
	_, err := time.Parse(time.RFC3339, e.StartedAt)
	return ValidID(e.ID) && slices.Contains(Kinds, e.Kind) && slices.Contains(statuses, e.Status) && err == nil && len(e.Command) <= maxCommand && len(e.Excerpt) <= maxExcerpt && (e.ProjectID == "" || ValidID(e.ProjectID))
}

// write replaces the file atomically: a private temporary file in the same directory is
// written, synced and renamed over the old one. An interrupted write leaves the previous
// file intact and a temporary file that the next Open removes.
func (s *Store) write(doc document) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("the configuration directory cannot be created")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".history-*.tmp")
	if err != nil {
		return errors.New("the history file cannot be written")
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return errors.New("the history file cannot be written")
	}
	if s.Sync {
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return errors.New("the history file cannot be written")
		}
	}
	if err := tmp.Close(); err != nil {
		return errors.New("the history file cannot be written")
	}
	if err := os.Rename(name, s.path); err != nil {
		return errors.New("the history file cannot be written")
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.stamp = fileStamp{fi.Size(), fi.ModTime()}
	}
	return nil
}

// writeBackup writes a private copy of a file that is about to be rewritten. It never
// replaces an existing file.
func writeBackup(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// current returns the document, re-read from disk when another process changed it.
// Caller holds mu.
func (s *Store) current() document {
	if s.status != Ready {
		return s.doc
	}
	fi, err := os.Stat(s.path)
	if err == nil && fi.Size() == s.stamp.size && fi.ModTime().Equal(s.stamp.mod) {
		return s.doc
	}
	if errors.Is(err, fs.ErrNotExist) && s.stamp == (fileStamp{}) {
		return s.doc
	}
	doc, loaded, err := s.read()
	var newer *newerError
	switch {
	case err == nil && loaded.from != SchemaVersion:
		// An older Hclaro sharing the folder rewrote the file. Upgrading it again here would
		// happen without a backup and race that process, so nothing more is recorded.
		s.status, s.notice = ReadOnly, fmt.Sprintf("Operation history was rewritten by an older Hclaro (schema %d) while this one was running. The file was not changed and nothing new is recorded. Restart Hclaro to upgrade it with a backup.", loaded.from)
	case err == nil:
		s.doc = doc
	case errors.As(err, &newer):
		// A newer Hclaro sharing the folder replaced the file. Writing now would downgrade it,
		// so history becomes read-only for the rest of this session.
		s.status, s.notice = ReadOnly, err.Error()
	}
	return s.doc
}

// ErrUnavailable refuses changes while history is read-only or unavailable.
var ErrUnavailable = errors.New("operation history cannot be changed")

// ErrNotFound reports an unknown entry.
var ErrNotFound = errors.New("history entry not found")

// ErrRunning refuses to delete an entry whose operation is still running.
var ErrRunning = errors.New("the operation is still running")

// mutate applies change to the current document, enforces retention and writes it.
// Caller holds mu.
func (s *Store) mutate(change func(*document) error) error {
	if s.status != Ready {
		return ErrUnavailable
	}
	doc := cloneDoc(s.current())
	if s.status != Ready {
		return ErrUnavailable
	}
	if err := change(&doc); err != nil {
		return err
	}
	retain(&doc, s.Now())
	if err := s.write(doc); err != nil {
		s.notice = "Operation history could not be written: " + err.Error()
		return err
	}
	s.doc = doc
	return nil
}

func cloneDoc(d document) document {
	d.Entries = slices.Clone(d.Entries)
	return d
}

// retain applies the retention policy. Running entries are never removed.
func retain(doc *document, now time.Time) {
	p := doc.Retention
	cutoff := now.Add(-time.Duration(p.MaxAgeDays) * 24 * time.Hour)
	kept := doc.Entries[:0:0]
	for _, e := range doc.Entries {
		started, _ := time.Parse(time.RFC3339, e.StartedAt)
		if e.Status == StatusRunning || !started.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	slices.SortStableFunc(kept, func(a, b HistoryEntry) int { return strings.Compare(a.StartedAt, b.StartedAt) })
	sizes := make([]int, len(kept))
	total := len(`{"version":1,"retention":{"maxEntries":5000,"maxAgeDays":3650,"maxBytes":16777216},"entries":[]}`) + 1
	for i, e := range kept {
		b, _ := json.Marshal(e)
		sizes[i] = len(b) + 1
		total += sizes[i]
	}
	count := len(kept)
	drop := make([]bool, len(kept))
	for i := 0; i < len(kept) && (count > p.MaxEntries || total > p.MaxBytes); i++ {
		if kept[i].Status == StatusRunning {
			continue
		}
		drop[i] = true
		count--
		total -= sizes[i]
	}
	out := make([]HistoryEntry, 0, count)
	for i, e := range kept {
		if !drop[i] {
			out = append(out, e)
		}
	}
	doc.Entries = out
}

// Status reports the store status, a notice for the user and the file path.
func (s *Store) Status() (status, notice, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.notice, s.path
}

// Retention returns the retention policy.
func (s *Store) Retention() HistoryRetentionPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current().Retention
}

// SetRetention validates and saves a retention policy and applies it at once.
func (s *Store) SetRetention(p HistoryRetentionPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func(d *document) error {
		d.Retention = p
		return nil
	})
}

// Filter selects entries. Empty fields match everything.
type Filter struct {
	ProjectID string
	Workspace string
	Kind      string
	Status    string
	Query     string
}

func (f Filter) match(e HistoryEntry) bool {
	if f.ProjectID != "" && e.ProjectID != f.ProjectID || f.Workspace != "" && e.Workspace != f.Workspace || f.Kind != "" && e.Kind != f.Kind || f.Status != "" && e.Status != f.Status {
		return false
	}
	if f.Query == "" {
		return true
	}
	q := strings.ToLower(f.Query)
	text := []string{e.ID, e.Kind, e.Action, e.Status, e.Engine, e.EngineVersion, e.ProjectName, e.Workspace, strings.Join(e.Command, " "), e.Outcome.Result}
	if e.Diagnostic != nil {
		text = append(text, e.Diagnostic.Code)
	}
	if e.Context != nil {
		text = append(text, e.Context.ProfileName)
	}
	if e.Git != nil {
		text = append(text, e.Git.Commit, e.Git.Branch)
	}
	if e.Plan != nil {
		for _, c := range e.Plan.Changes {
			text = append(text, c.Address)
		}
	}
	return strings.Contains(strings.ToLower(strings.Join(text, "\n")), q)
}

// List returns matching entries, newest first.
func (s *Store) List(f Filter) []HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.current().Entries
	out := []HistoryEntry{}
	for i := len(entries) - 1; i >= 0; i-- {
		if f.match(entries[i]) {
			out = append(out, entries[i])
		}
	}
	slices.SortStableFunc(out, func(a, b HistoryEntry) int { return strings.Compare(b.StartedAt, a.StartedAt) })
	return out
}

// Get returns one entry.
func (s *Store) Get(id string) (HistoryEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.current().Entries {
		if e.ID == id {
			return e, true
		}
	}
	return HistoryEntry{}, false
}

// Delete removes one finished entry. It only edits the history file.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func(d *document) error {
		i := slices.IndexFunc(d.Entries, func(e HistoryEntry) bool { return e.ID == id })
		switch {
		case i < 0:
			return ErrNotFound
		case d.Entries[i].Status == StatusRunning:
			return ErrRunning
		}
		d.Entries = slices.Delete(d.Entries, i, i+1)
		return nil
	})
}

// Clear removes every finished entry matching the filter and returns how many were removed.
// Entries of running operations are kept. It only edits the history file.
func (s *Store) Clear(f Filter) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	err := s.mutate(func(d *document) error {
		kept := []HistoryEntry{}
		for _, e := range d.Entries {
			if e.Status != StatusRunning && f.match(e) {
				removed++
				continue
			}
			kept = append(kept, e)
		}
		d.Entries = kept
		return nil
	})
	return removed, err
}

// Record keeps the start or end of an operation. Failures to write are reported through
// Status and never affect the operation.
func (s *Store) Record(r operations.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != Ready {
		return
	}
	entry := s.convert(r)
	_ = s.mutate(func(d *document) error {
		i := slices.IndexFunc(d.Entries, func(e HistoryEntry) bool { return e.ID == entry.ID })
		if i >= 0 {
			// The commit is read when the operation starts; keep it.
			if entry.Git == nil {
				entry.Git = d.Entries[i].Git
			}
			if entry.Plan != nil && entry.Plan.Execution == nil {
				entry.Plan.Execution = d.Entries[i].Plan.executionOrNil()
			}
			d.Entries[i] = entry
		} else {
			d.Entries = append(d.Entries, entry)
		}
		// An apply also records its result on the plan it used.
		if entry.Kind == "apply" && entry.Outcome.PlanID != "" {
			for j := range d.Entries {
				if p := d.Entries[j].Plan; p != nil && d.Entries[j].Kind != "apply" && p.PlanID == entry.Outcome.PlanID {
					// Entries are shared with readers; replace the summary rather than edit it.
					linked := *p
					linked.Execution = &ExecutionRecord{OperationID: entry.ID, Status: entry.Status, Result: entry.Outcome.Result, FinishedAt: entry.FinishedAt}
					d.Entries[j].Plan = &linked
				}
			}
		}
		return nil
	})
}

func (p *PlanSummary) executionOrNil() *ExecutionRecord {
	if p == nil {
		return nil
	}
	return p.Execution
}
