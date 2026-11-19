package assist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"hclaro/internal/imports"
	"hclaro/internal/state"
)

// Saved conversations (AI-01.11, OD-AI-11). Opt-in per conversation: only a conversation the
// user ticks "Save conversation" for is written, as one JSON file under
// <config>/ai-conversations/<project key>/<id>.json (0600 files, 0700 folders, atomic
// replace). A file holds the metadata and the transcript Hclaro replays: the starting package
// and the turns, all of which passed the package scan when they were shared (replies are model
// output). Never stored: credentials, environment, argv, stderr, provider metadata or cost
// figures of a run. Every turn is scanned again when a file is reopened.

const (
	savedDir      = "ai-conversations"
	savedSettings = "settings.json"
	savedVersion  = 1
	// DefaultRetentionDays applies unless settings.json in the store folder sets retentionDays.
	DefaultRetentionDays = 30
	// A file holds at most the transcript limit plus the replies' JSON escaping.
	maxSavedFile = 4 << 20
)

var (
	savedID    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	projectKey = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// redactedPair is a name with a value Hclaro already redacted: `db_password: "[redacted]"`.
	redactedPair = regexp.MustCompile(`[A-Za-z0-9_.-]+"?\s*[:=]\s*"?\[redacted\]"?`)
	// hashMarker is a file hash Hclaro prints in a package file header, or that a proposal
	// header repeats (AI-01.12): a SHA-256, not a secret.
	hashMarker = regexp.MustCompile(`\(sha256 [0-9a-f]{64}\)|\bbase=[0-9a-f]{64}\b`)
)

// ProjectKey derives the store key of a project root without keeping the path.
func ProjectKey(root string) string {
	h := sha256.Sum256([]byte("hclaro-ai\x00" + root))
	return hex.EncodeToString(h[:8])
}

// Resume modes: how a reopened conversation re-checks its context.
const (
	resumeWorkspace = "workspace" // no project data: the same project and workspace
	resumeProject   = "project"   // project files: also the fingerprint of the latest project package
	resumeNone      = "none"      // a plan, scan, import, resource or cost estimate of one session
)

type savedTurn struct {
	Role    string `json:"role"`
	Text    string `json:"text,omitempty"`
	At      string `json:"at"`
	Prompt  string `json:"prompt,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Context string `json:"context,omitempty"`
}

type savedDoc struct {
	Version         int         `json:"version"`
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	ProjectKey      string      `json:"projectKey"`
	Project         string      `json:"project"`
	Workspace       string      `json:"workspace"`
	Provider        string      `json:"provider"`
	ProviderVersion string      `json:"providerVersion"`
	Kind            string      `json:"kind"`
	Prompts         []string    `json:"prompts"`
	CreatedAt       string      `json:"createdAt"`
	LastActivity    string      `json:"lastActivity"`
	State           string      `json:"state"`
	Resume          string      `json:"resume"`
	Fingerprint     string      `json:"fingerprint,omitempty"`
	PackageSHA256   string      `json:"packageSha256,omitempty"`
	Package         string      `json:"package,omitempty"`
	Turns           []savedTurn `json:"turns"`
}

// SavedListed is one saved conversation in the list. Open is true while it is also open in
// this session.
type SavedListed struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Provider        string `json:"provider"`
	ProviderVersion string `json:"providerVersion"`
	Kind            string `json:"kind"`
	Workspace       string `json:"workspace"`
	CreatedAt       string `json:"createdAt"`
	LastActivity    string `json:"lastActivity"`
	Open            bool   `json:"open"`
}

// Store is the folder of saved conversations. Every file operation goes through os.Root:
// the store folder is opened inside the configuration folder and everything below it is
// resolved inside the store folder, so no path, and no symbolic link swapped in between a
// check and an operation, can reach a file outside it.
type Store struct {
	mu        sync.Mutex
	config    string
	retention time.Duration
	Now       func() time.Time
	// hook, set by tests only, runs after a folder check (named by the folder) and before the
	// operation it guards: tests swap in a symbolic link there.
	hook func(name string)
}

// OpenStore opens the store under configDir, writes the default settings file when there is
// none (so the retention can be changed there), and deletes expired conversations.
func OpenStore(configDir string, now time.Time) *Store {
	s := &Store{config: configDir, retention: DefaultRetentionDays * 24 * time.Hour, Now: time.Now}
	var settings struct {
		Version       int `json:"version"`
		RetentionDays int `json:"retentionDays"`
	}
	if r, err := s.root(true); err == nil {
		if b, err := readFile(r, savedSettings, 4<<10); err == nil {
			if json.Unmarshal(b, &settings) == nil && settings.RetentionDays >= 1 && settings.RetentionDays <= 3650 {
				s.retention = time.Duration(settings.RetentionDays) * 24 * time.Hour
			}
		} else if errors.Is(err, os.ErrNotExist) {
			b, _ := json.MarshalIndent(map[string]int{"version": savedVersion, "retentionDays": DefaultRetentionDays}, "", "  ")
			_ = writeAtomic(r, ".", savedSettings, b)
		}
		r.Close()
	}
	s.Sweep(now)
	return s
}

// RetentionDays is the configured retention.
func (s *Store) RetentionDays() int { return int(s.retention / (24 * time.Hour)) }

var errNotPlain = errors.New("the saved conversations folder is not a plain folder")

// root opens the store folder through the configuration folder (os.Root refuses to leave it)
// and sets it owner-only; create makes the folders when missing. Callers close it.
func (s *Store) root(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.config, 0o700); err != nil {
			return nil, err
		}
	}
	cfg, err := os.OpenRoot(s.config)
	if err != nil {
		return nil, err
	}
	defer cfg.Close()
	if create {
		if err := cfg.Mkdir(savedDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	// A link here is refused; one swapped in after this check can only point inside the
	// configuration folder, because cfg.OpenRoot cannot leave it.
	if info, err := cfg.Lstat(savedDir); err != nil || !info.IsDir() {
		return nil, errNotPlain
	}
	if s.hook != nil {
		s.hook(savedDir)
	}
	r, err := cfg.OpenRoot(savedDir)
	if err != nil {
		return nil, err
	}
	if create {
		_ = r.Chmod(".", 0o700)
	}
	return r, nil
}

// plainDir reports whether name in r is a folder and not a symbolic link. A link swapped in
// after the check is still resolved inside r by the operation that follows.
func (s *Store) plainDir(r *os.Root, name string) bool {
	info, err := r.Lstat(name)
	ok := err == nil && info.IsDir()
	if s.hook != nil {
		s.hook(name)
	}
	return ok
}

// ours reports whether name is a file the store writes: a conversation or a temporary file.
// Sweep and delete-all never touch anything else.
func ours(name string) bool {
	id, ok := strings.CutSuffix(name, ".json")
	if !ok {
		id, ok = strings.CutSuffix(name, ".backup") // AI-01.12 backups, swept like conversations
	}
	return ok && savedID.MatchString(id) || strings.HasPrefix(name, ".conversation-") && strings.HasSuffix(name, ".tmp")
}

// names lists the entries of folder name in r.
func names(r *os.Root, name string) []string {
	f, err := r.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	out, _ := f.Readdirnames(-1)
	return out
}

// Sweep deletes conversations unused for longer than the retention, and leftover temporary
// files. A file is rewritten on every activity, so its modification time is its last activity.
func (s *Store) Sweep(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(false)
	if err != nil {
		return 0
	}
	defer r.Close()
	n := 0
	for _, f := range names(r, ".") {
		if strings.HasPrefix(f, ".conversation-") && strings.HasSuffix(f, ".tmp") {
			_ = r.Remove(f) // a settings write that did not finish
		}
	}
	for _, key := range names(r, ".") {
		if !projectKey.MatchString(key) || !s.plainDir(r, key) {
			continue
		}
		// Recovery artifacts first: their records decide when their backups may go.
		for _, f := range names(r, key) {
			if id, ok := strings.CutSuffix(f, ".aside"); ok && savedID.MatchString(id) {
				s.sweepAside(r, key, id, now)
			}
		}
		for _, f := range names(r, key) {
			name := filepath.Join(key, f)
			info, err := r.Lstat(name)
			if err != nil || !info.Mode().IsRegular() || !ours(f) {
				continue
			}
			if id, ok := strings.CutSuffix(f, ".backup"); ok {
				if _, err := r.Lstat(filepath.Join(key, id+".aside")); err == nil {
					continue // still needed to recognize its recovery artifact
				}
			}
			if strings.HasSuffix(f, ".tmp") || now.Sub(info.ModTime()) > s.retention {
				if r.Remove(name) == nil && strings.HasSuffix(f, ".json") {
					n++
				}
			}
		}
		_ = r.Remove(key) // only when empty
	}
	return n
}

// path is the file of conversation id of project key, relative to the store folder.
func (s *Store) path(key, id string) (string, bool) {
	if !projectKey.MatchString(key) || !savedID.MatchString(id) {
		return "", false
	}
	return filepath.Join(key, id+".json"), true
}

// errTooLarge refuses a file that read would refuse: it would be saved but never listed.
var errTooLarge = errors.New("saved conversation too large")

func (s *Store) write(doc savedDoc) error {
	path, ok := s.path(doc.ProjectKey, doc.ID)
	if !ok {
		return errors.New("invalid saved conversation")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(b) > maxSavedFile {
		return errTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(true)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Mkdir(doc.ProjectKey, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if !s.plainDir(r, doc.ProjectKey) {
		return errNotPlain
	}
	_ = r.Chmod(doc.ProjectKey, 0o700)
	return writeAtomic(r, doc.ProjectKey, path, b)
}

// Backup keeps a copy of a project file before an AI-proposed change replaces it (AI-01.12),
// as <project key>/<id>.backup (owner-only). It is deleted with the retention of saved
// conversations.
func (s *Store) Backup(key string, data []byte) (string, error) {
	id := randomID()
	if !projectKey.MatchString(key) {
		return "", errors.New("invalid project key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(true)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if err := r.Mkdir(key, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if !s.plainDir(r, key) {
		return "", errNotPlain
	}
	return id, writeAtomic(r, key, filepath.Join(key, id+".backup"), data)
}

// ReadBackup returns backup id of project key.
func (s *Store) ReadBackup(key, id string) ([]byte, error) {
	if !projectKey.MatchString(key) || !savedID.MatchString(id) {
		return nil, errNotPlain
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(false)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if !s.plainDir(r, key) {
		return nil, errNotPlain
	}
	return readFile(r, filepath.Join(key, id+".backup"), maxProjectFile)
}

// RemoveBackup deletes backup id of project key.
func (s *Store) RemoveBackup(key, id string) {
	if !projectKey.MatchString(key) || !savedID.MatchString(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, err := s.root(false); err == nil {
		_ = r.Remove(filepath.Join(key, id+".backup"))
		r.Close()
	}
}

// asideRecord owns one recovery artifact of a proposed change (AI-01.12): the file a replacement
// or removal moved aside, imports.AsideName(id) in the folder of Path inside Root. The backup with
// the same ID holds the bytes the artifact was verified to hold. Done is false while the change
// runs: a record still not done at startup means Hclaro stopped in the middle of it.
type asideRecord struct {
	Version int    `json:"version"`
	Root    string `json:"root"`
	Path    string `json:"path"`
	Done    bool   `json:"done"`
}

// Retain records, before anything moves, that backup id of project key may leave a recovery
// artifact next to rel in root.
func (s *Store) Retain(key, id, root, rel string) error {
	return s.writeAside(key, id, asideRecord{Version: 1, Root: root, Path: rel})
}

// Settle ends a change: with kept, its artifact and backup stay until the retention; without,
// nothing was moved, so the record and backup go now.
func (s *Store) Settle(key, id, root, rel string, kept bool) {
	if kept {
		_ = s.writeAside(key, id, asideRecord{Version: 1, Root: root, Path: rel, Done: true})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, err := s.root(false); err == nil {
		_ = r.Remove(filepath.Join(key, id+".aside"))
		_ = r.Remove(filepath.Join(key, id+".backup"))
		r.Close()
	}
}

func (s *Store) writeAside(key, id string, rec asideRecord) error {
	if !projectKey.MatchString(key) || !savedID.MatchString(id) {
		return errNotPlain
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(true)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Mkdir(key, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if !s.plainDir(r, key) {
		return errNotPlain
	}
	return writeAtomic(r, key, filepath.Join(key, id+".aside"), b)
}

// sweepAside handles one recovery artifact at startup. Callers hold mu.
//   - A change interrupted between moving the file aside and placing the new one left the name
//     empty: the artifact, if it still holds exactly the backed-up bytes, is linked back (never
//     overwriting). The change is rolled back, not completed.
//   - After the retention, the artifact is deleted only if it still holds exactly the backed-up
//     bytes (nothing anyone wrote into it is lost); otherwise it stays in the project for the
//     user. Then the record and backup go.
func (s *Store) sweepAside(r *os.Root, key, id string, now time.Time) {
	name := filepath.Join(key, id+".aside")
	info, err := r.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	var rec asideRecord
	b, err := readFile(r, name, 8<<10)
	expired := now.Sub(info.ModTime()) > s.retention
	aside, target := "", ""
	if err == nil && json.Unmarshal(b, &rec) == nil && rec.Version == 1 && filepath.IsAbs(rec.Root) && validChangePath(rec.Path) {
		if dir, err := imports.SafeDir(rec.Root, rec.Path); err == nil {
			aside, target = filepath.Join(dir, imports.AsideName(id)), filepath.Join(dir, path.Base(rec.Path))
		}
	}
	backup, berr := readFile(r, filepath.Join(key, id+".backup"), maxProjectFile)
	same := aside != "" && berr == nil && sameBytes(aside, backup)
	_, terr := os.Lstat(target)
	switch {
	case !rec.Done && same && errors.Is(terr, os.ErrNotExist):
		if os.Link(aside, target) == nil {
			_ = os.Remove(aside) // only a second name of the restored file
		}
	case expired && same:
		_ = os.Remove(aside)
	}
	if expired {
		_ = r.Remove(name)
		_ = r.Remove(filepath.Join(key, id+".backup"))
	} else if !rec.Done {
		if b, err := json.Marshal(asideRecord{Version: 1, Root: rec.Root, Path: rec.Path, Done: true}); err == nil {
			_ = writeAtomic(r, key, name, b)
		}
	}
}

// sameBytes reports whether the regular file p holds exactly want.
func sameBytes(p string, want []byte) bool {
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	got, err := os.ReadFile(p)
	return err == nil && string(got) == string(want)
}

// exists reports whether conversation id of project key has a file.
func (s *Store) exists(key, id string) bool {
	path, ok := s.path(key, id)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(false)
	if err != nil {
		return false
	}
	defer r.Close()
	info, err := r.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// read loads one file of project key. A file whose ID or project key does not match where it
// was asked for is refused: it was copied or planted from another project. Symbolic links (the
// file or its folders) are refused.
func (s *Store) read(key, id string) (savedDoc, error) {
	path, ok := s.path(key, id)
	if !ok {
		return savedDoc{}, refuse(RefuseNoChat)
	}
	s.mu.Lock()
	var b []byte
	r, err := s.root(false)
	if err == nil {
		if !s.plainDir(r, key) {
			err = errNotPlain
		} else {
			b, err = readFile(r, path, maxSavedFile)
		}
		r.Close()
	}
	s.mu.Unlock()
	if err != nil {
		return savedDoc{}, refuse(RefuseNoChat)
	}
	var doc savedDoc
	if json.Unmarshal(b, &doc) != nil || doc.Version != savedVersion || doc.ID != id || doc.ProjectKey != key {
		return savedDoc{}, refuse(RefuseNoChat)
	}
	return doc, nil
}

func (s *Store) list(key string, now time.Time) []SavedListed {
	out := []SavedListed{}
	if !projectKey.MatchString(key) {
		return out
	}
	s.mu.Lock()
	var ids []string
	if r, err := s.root(false); err == nil {
		if s.plainDir(r, key) {
			for _, f := range names(r, key) {
				id, ok := strings.CutSuffix(f, ".json")
				if !ok || !savedID.MatchString(id) {
					continue
				}
				if info, err := r.Lstat(filepath.Join(key, f)); err == nil && now.Sub(info.ModTime()) <= s.retention {
					ids = append(ids, id)
				}
			}
		}
		r.Close()
	}
	s.mu.Unlock()
	for _, id := range ids {
		doc, err := s.read(key, id) // one unreadable file is skipped; the others still list
		if err != nil {
			continue
		}
		out = append(out, SavedListed{ID: doc.ID, Title: doc.Title, Provider: doc.Provider, ProviderVersion: doc.ProviderVersion, Kind: doc.Kind,
			Workspace: doc.Workspace, CreatedAt: doc.CreatedAt, LastActivity: doc.LastActivity})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActivity > out[j].LastActivity || out[i].LastActivity == out[j].LastActivity && out[i].ID < out[j].ID
	})
	return out
}

// remove deletes one file. Callers hold the session lock, so no turn can write it again.
func (s *Store) remove(key, id string) error {
	path, ok := s.path(key, id)
	if !ok {
		return refuse(RefuseNoChat)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(false)
	if err != nil {
		return nil // nothing saved
	}
	defer r.Close()
	if !s.plainDir(r, key) {
		return nil // nothing saved, or a link the store never follows
	}
	if err := r.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = r.Remove(key)
	return nil
}

// removeAll deletes every conversation file of project key, readable or not, and nothing else.
func (s *Store) removeAll(key string) (int, error) {
	if !projectKey.MatchString(key) {
		return 0, refuse(RefuseInvalidArgs)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(false)
	if err != nil {
		return 0, nil
	}
	defer r.Close()
	if !s.plainDir(r, key) {
		return 0, nil
	}
	n := 0
	var errs []error
	for _, f := range names(r, key) {
		if !ours(f) {
			continue
		}
		if err := r.Remove(filepath.Join(key, f)); err != nil {
			errs = append(errs, err)
		} else if strings.HasSuffix(f, ".json") {
			n++
		}
	}
	_ = r.Remove(key)
	return n, errors.Join(errs...)
}

// readFile reads a regular file of at most limit bytes inside r, never through a final link.
func readFile(r *os.Root, name string, limit int64) ([]byte, error) {
	if info, err := r.Lstat(name); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errNotPlain
	}
	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("not a readable file")
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// writeAtomic writes path in r through a new temporary file in dir (exclusive create, 0600)
// renamed over it. Rename replaces a link at path itself, never its target. A crash before
// the rename leaves the old file and a temporary file that the next startup deletes; the folder
// is not synced, so a power loss can bring back the previous version.
func writeAtomic(r *os.Root, dir, path string, b []byte) error {
	tmp := filepath.Join(dir, ".conversation-"+randomID()+".tmp")
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, path); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// titleOf is the first user message, truncated, or the review or quick action prompt; never
// model output.
func titleOf(turns []Turn) string {
	title := ""
	for _, t := range turns {
		if t.Role == "user" {
			title = t.Text
			if t.Prompt != "" {
				title = "Review (" + t.Prompt + ")"
				if strings.HasPrefix(t.Prompt, "qa-") {
					title = "Quick action (" + t.Prompt + ")"
				}
			}
			break
		}
	}
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80]) + "…"
	}
	return title
}

func major(version string) string {
	m, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), ".")
	if _, err := strconv.Atoi(m); err != nil {
		return ""
	}
	return m
}

// docOf is what a saved conversation writes. Callers hold mu.
func (s *Session) docOf(c *conversation) savedDoc {
	doc := savedDoc{Version: savedVersion, ID: c.view.ID, Title: titleOf(c.view.Turns), ProjectKey: c.scope.ProjectKey, Project: c.scope.Project,
		Workspace: c.scope.Workspace, Provider: c.view.Provider, ProviderVersion: c.version, Kind: c.view.Kind, Prompts: []string{},
		CreatedAt: c.view.StartedAt, LastActivity: s.Now().UTC().Format(time.RFC3339), State: c.view.State, Resume: c.resume,
		Fingerprint: c.fingerprint, PackageSHA256: c.view.SHA256, Package: c.pkg, Turns: []savedTurn{}}
	if c.pkg == "" {
		doc.PackageSHA256 = "" // the hash of a project package attached later is in its turn
	}
	for _, t := range c.view.Turns {
		if t.Prompt != "" {
			doc.Prompts = append(doc.Prompts, t.Prompt)
		}
		doc.Turns = append(doc.Turns, savedTurn{Role: t.Role, Text: t.Text, At: t.At, Prompt: t.Prompt, SHA256: t.SHA256, Context: t.Context})
	}
	return doc
}

// newScan returns the secret scan of saved text: the package scan of user messages
// (builder.safe), with the project root and home as local paths. Hclaro's own marker next to a
// secret-like key ("password: [redacted]") and its file hashes are not secrets.
func newScan(root string) func(string) bool {
	b := newBuilder("message", Scope{Root: root})
	return func(text string) bool {
		text = strings.ReplaceAll(redactedPair.ReplaceAllString(hashMarker.ReplaceAllString(text, ""), ""), state.Redacted, "")
		return text == "" || b.safe("message", text, 0)
	}
}

// turnSafe is the check of one saved turn: a prompt turn must be exactly the registry text
// (Hclaro wrote it); every other text, replies included, must pass the scan.
func turnSafe(scan func(string) bool, t savedTurn) bool {
	if t.Role != "user" && t.Role != "assistant" && t.Role != "context" {
		return false
	}
	if t.Prompt != "" {
		return registryText(t.Prompt, t.Text) && scan(t.Context)
	}
	return scan(t.Text) && scan(t.Context)
}

// docSafe reports whether everything doc would write passes the scan it gets on load. Replies
// are not scanned when they arrive, so this is what keeps one that looks secret off the disk.
func docSafe(doc savedDoc, root string) bool {
	scan := newScan(root)
	if !scan(doc.Package) {
		return false
	}
	for _, t := range doc.Turns {
		if !turnSafe(scan, t) {
			return false
		}
	}
	return true
}

const unsafeSave = "The latest turn was not saved: it looks like it contains a secret, a credential or a local path. The saved copy stops at the last turn that passed; this conversation stays open in this session."

// persist rewrites the file of a saved conversation that is not waiting for a reply and is
// still open (one dropped by AI off or Close is not written again). Text that fails the scan is
// never written: the file keeps its last safe state and SaveError says so. Callers hold mu:
// writes of one conversation stay in order and cannot race a delete.
func (s *Session) persist(c *conversation) {
	if !c.view.Saved || c.view.State == StateRunning || s.Store == nil || s.conversations[c.view.ID] != c {
		return
	}
	c.view.SaveError = ""
	doc := s.docOf(c)
	if !docSafe(doc, c.scope.Root) {
		c.view.SaveError = unsafeSave
		return
	}
	if err := s.Store.write(doc); err != nil {
		c.view.SaveError = "Hclaro could not save this conversation to disk. It stays open in this session."
	}
}

// Save turns saving of conversation id in scope key on or off. Off deletes its file.
func (s *Session) Save(key, id string, on bool) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[id]
	switch {
	case !ok || c.key != key || !s.enabled:
		return Conversation{}, refuse(RefuseNoChat)
	case s.Store == nil || c.scope.ProjectKey == "":
		return Conversation{}, refuse(RefuseSaveOff)
	case on && c.view.NeedsReview:
		return Conversation{}, refuse(RefuseReadOnly) // hidden turns are never written back
	}
	if !on {
		if err := s.Store.remove(c.scope.ProjectKey, id); err != nil {
			return Conversation{}, err
		}
		c.view.Saved, c.view.SaveError = false, ""
		return s.snapshot(c), nil
	}
	c.view.Saved = true
	if c.view.State == StateRunning {
		return s.snapshot(c), nil // written when the reply arrives
	}
	doc := s.docOf(c)
	if !docSafe(doc, c.scope.Root) {
		c.view.Saved = false
		return Conversation{}, refuse(RefuseSaveUnsafe)
	}
	if err := s.Store.write(doc); err != nil {
		c.view.Saved = false
		return Conversation{}, refuse(RefuseSaveFailed)
	}
	c.view.SaveError = ""
	return s.snapshot(c), nil
}

// SavedList returns the saved conversations of project key; empty while AI is off.
func (s *Session) SavedList(key, project string) []SavedListed {
	s.mu.Lock()
	if !s.enabled || s.Store == nil {
		s.mu.Unlock()
		return []SavedListed{}
	}
	open := map[string]bool{}
	for id, c := range s.conversations {
		open[id] = c.key == key
	}
	s.mu.Unlock()
	out := s.Store.list(project, s.Now())
	for i := range out {
		out[i].Open = open[out[i].ID]
	}
	return out
}

// DeleteSaved deletes the file of conversation id of project. An open copy stays open,
// unsaved. mu is held across the delete, so a turn finishing meanwhile cannot write it again.
func (s *Session) DeleteSaved(project, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.Store == nil:
		return refuse(RefuseSaveOff)
	case !s.enabled:
		return refuse(RefuseDisabled)
	}
	if c, ok := s.conversations[id]; ok && c.scope.ProjectKey == project {
		c.view.Saved = false
	}
	return s.Store.remove(project, id)
}

// DeleteAllSaved deletes every saved conversation of project key, under mu like DeleteSaved.
func (s *Session) DeleteAllSaved(project string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.Store == nil:
		return 0, refuse(RefuseSaveOff)
	case !s.enabled:
		return 0, refuse(RefuseDisabled)
	}
	for _, c := range s.conversations {
		if c.scope.ProjectKey == project {
			c.view.Saved = false
		}
	}
	return s.Store.removeAll(project)
}

// Resume is what reopening needs from the scope it happens in: the scope, whether the user is
// still in it, and the current fingerprint of the project files.
type Resume struct {
	Scope       RunScope
	Bound       func() bool
	Fingerprint func() (string, bool)
}

// Reopen loads saved conversation id of r.Scope's project into scope key. It continues only
// when the replay will work: the same workspace, a headless-eligible assistant with the same
// ID and major version, context that can be checked again, and every turn passing the scan
// again. Otherwise it opens read-only and says why.
func (s *Session) Reopen(key, id string, r Resume) (Conversation, error) {
	if s.Store == nil {
		return Conversation{}, refuse(RefuseSaveOff)
	}
	if !s.Enabled() {
		return Conversation{}, refuse(RefuseDisabled)
	}
	doc, err := s.Store.read(r.Scope.ProjectKey, id)
	if err != nil {
		return Conversation{}, err
	}
	// Scanner rules may have improved since saving: a hit hides that turn. Only memory changes;
	// the file keeps its text until the user deletes it (OD-AI-11), and a conversation with
	// hidden turns is never written back.
	scan := newScan(r.Scope.Root)
	hidden := !scan(doc.Package)
	turns := make([]Turn, 0, len(doc.Turns))
	for _, t := range doc.Turns {
		turn := Turn{Role: t.Role, Text: t.Text, At: t.At, Prompt: t.Prompt, SHA256: t.SHA256, Context: t.Context}
		if !turnSafe(scan, t) {
			turn = Turn{Role: t.Role, At: t.At, Hidden: true}
			if turn.Role != "user" && turn.Role != "context" {
				turn.Role = "assistant"
			}
			hidden = true
		}
		turns = append(turns, turn)
	}
	// The mode follows the kind, so an edited file cannot continue a review of an earlier session.
	switch {
	case doc.Kind == KindChat && doc.Resume == resumeWorkspace && doc.Package == "":
	case doc.Kind == KindProject && doc.Resume == resumeProject && doc.Fingerprint != "":
	default:
		doc.Resume = resumeNone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.conversations[id]; ok {
		if c.key != key {
			return Conversation{}, refuse(RefuseNoChat)
		}
		return s.snapshot(c), nil
	}
	if !s.enabled {
		return Conversation{}, refuse(RefuseDisabled)
	}
	// Deleted while it was being read (deletes hold mu): a later turn must not write it again.
	if !s.Store.exists(r.Scope.ProjectKey, id) {
		return Conversation{}, refuse(RefuseNoChat)
	}
	// The conversation keeps the workspace it was saved in: saving it again from another
	// workspace must not move it there (it stays read-only outside its own workspace).
	scope := r.Scope
	scope.Workspace = doc.Workspace
	c := &conversation{key: key, scope: scope, pkg: doc.Package, resume: doc.Resume, fingerprint: doc.Fingerprint, version: doc.ProviderVersion, lastUsed: s.Now(),
		view: Conversation{ID: doc.ID, Provider: doc.Provider, Kind: doc.Kind, SHA256: doc.PackageSHA256, State: StateReady, Turns: turns,
			LimitBytes: MaxTranscript, StartedAt: doc.CreatedAt, Saved: true, NeedsReview: hidden}}
	c.view.UsedBytes = len(Transcript(c.pkg, turns))
	c.fresh = r.Bound
	if doc.Resume == resumeProject {
		fp := doc.Fingerprint
		c.attached = func() bool { now, ok := r.Fingerprint(); return ok && now == fp }
	}
	var status *ProviderStatus
	for i := range s.detected {
		if s.detected[i].ID == doc.Provider {
			status = &s.detected[i]
		}
	}
	name := doc.Provider
	if status != nil {
		name = status.Name
	}
	switch {
	case hidden:
		c.view.ReadOnly = "Some saved text now looks like a secret, a credential or a local path, so it is hidden and this conversation cannot continue. Read what is left, then delete it."
	case doc.Workspace != r.Scope.Workspace:
		c.view.ReadOnly = "This conversation belongs to workspace " + doc.Workspace + ". Switch to that workspace to continue it."
	case doc.Resume == resumeNone:
		c.view.ReadOnly = "It started from a plan, drift scan, import, resource or cost estimate of an earlier Hclaro session, which no longer exists here. You can read it; start a new review to continue."
	case s.strict:
		c.view.ReadOnly = refusalMessages[RefuseStrict]
	case status == nil || !s.eligible(doc.Provider) || !s.providerKnown(doc.Provider):
		c.view.ReadOnly = name + " cannot run headless here now, so this conversation cannot continue. You can still read it."
	case major(status.Version) == "" || major(status.Version) != major(doc.ProviderVersion):
		c.view.ReadOnly = "This conversation was saved with " + name + " " + doc.ProviderVersion + "; version " + status.Version + " is installed now. You can read it; start a new conversation to continue."
	}
	if err := s.evict(); err != nil {
		return Conversation{}, err
	}
	s.conversations[c.view.ID] = c
	return s.snapshot(c), nil
}

// registryText reports whether text is exactly the registry prompt id: Hclaro wrote it, so it
// is checked against the registry rather than scanned.
func registryText(id, text string) bool {
	p, ok := LookupPrompt(id)
	return ok && p.Text == text
}

// providerKnown reports whether this session has an adapter for id. Callers hold mu.
func (s *Session) providerKnown(id string) bool {
	for _, a := range s.providers {
		if a.ID() == id {
			return true
		}
	}
	return false
}

// versionOf is the detected version of provider id. Callers hold mu.
func (s *Session) versionOf(id string) string {
	for _, d := range s.detected {
		if d.ID == id {
			return d.Version
		}
	}
	return ""
}
