package gitctx

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Repository states of a GitContext.
const (
	StateRepository  = "repository"
	StateNotRepo     = "not_repository"
	StateUnavailable = "unavailable"
	StateError       = "error"
)

// GitFileStatus is one changed IaC file in the project scope. It never carries content.
type GitFileStatus struct {
	// ID is an opaque per-session file ID; the browser sends it back to ask for a diff.
	ID string `json:"id"`
	// Path is relative to the repository top level, in slash form.
	Path string `json:"path"`
	// Place is "root" (inside the project root) or "module" (a local module directory).
	Place  string `json:"place"`
	Module string `json:"module,omitempty"`
	Kind   string `json:"kind"`
	// Status is modified, added, deleted, untracked, typechange or conflicted.
	Status   string `json:"status"`
	Staged   bool   `json:"staged"`
	Unstaged bool   `json:"unstaged"`
	Diffable bool   `json:"diffable"`
	// Reason explains why a file cannot be diffed.
	Reason string `json:"reason,omitempty"`
}

// GitWarning is a notice about the working tree.
type GitWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// GitCounts summarizes changes; Outside counts changed IaC files elsewhere in the
// repository and Other counts changed non-IaC files anywhere.
type GitCounts struct {
	Changed   int `json:"changed"`
	Untracked int `json:"untracked"`
	Outside   int `json:"outside"`
	Other     int `json:"other"`
}

// Repository identifies the repository without its absolute location.
type Repository struct {
	// Name is the folder name of the repository top level.
	Name string `json:"name"`
	// ProjectPath is the project root relative to the top level ("." at the top).
	ProjectPath string `json:"projectPath"`
	// Worktree is set for a linked worktree.
	Worktree bool `json:"worktree"`
	// LocalModules is the number of local module directories in scope.
	LocalModules int `json:"localModules"`
}

// GitContext is the sanitized Git context of one project.
type GitContext struct {
	State      string          `json:"state"`
	Message    string          `json:"message,omitempty"`
	Repository *Repository     `json:"repository,omitempty"`
	Head       *Head           `json:"head,omitempty"`
	Clean      bool            `json:"clean"`
	Files      []GitFileStatus `json:"files"`
	Counts     GitCounts       `json:"counts"`
	Warnings   []GitWarning    `json:"warnings"`
	// Incomplete is set when the status output was too large to read in full.
	Incomplete bool   `json:"incomplete"`
	CheckedAt  string `json:"checkedAt"`
}

// Service reads Git context for project roots. It holds the per-process file ID key and
// the editor opener; a zero Service is not usable, use New.
type Service struct {
	Runner Runner
	// Environ supplies TF_DATA_DIR; nil uses the process environment.
	Environ func() []string
	// OpenURL hands a validated editor URL to the operating system; tests replace it.
	OpenURL func(ctx context.Context, url string) error
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	ids ids
}

// New returns a Service with the real Git runner and operating system URL opener.
func New() *Service {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Service{Runner: Exec{}, OpenURL: OpenURL, ids: ids{key: key}}
}

// UseKey replaces the file ID key. Only the synthetic browser harness uses it, because it
// serves each request from a fresh process; production keeps the random per-process key.
func (s *Service) UseKey(key []byte) { s.ids = ids{key: append([]byte(nil), key...)} }

// NewWith returns a Service with injected dependencies (tests).
func NewWith(r Runner, open func(context.Context, string) error) *Service {
	s := New()
	s.Runner, s.OpenURL = r, open
	return s
}

func (s *Service) environ() []string {
	if s.Environ != nil {
		return s.Environ()
	}
	return os.Environ()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const (
	maxStatusOutput = 2 << 20
	maxListed       = 500
)

// HeadOf reads HEAD of root's repository without running Git: the cheap badge data.
func (s *Service) HeadOf(root string) GitContext {
	out := GitContext{Files: []GitFileStatus{}, Warnings: []GitWarning{}, CheckedAt: s.now().UTC().Format(time.RFC3339)}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		out.State, out.Message = StateError, "The project folder could not be resolved."
		return out
	}
	top := FindGit(canonical)
	if top == "" {
		out.State, out.Message = StateNotRepo, "This project is not inside a Git repository."
		return out
	}
	head := ReadHead(canonical)
	if head == nil {
		out.State, out.Message = StateUnavailable, "The repository's HEAD could not be read."
		return out
	}
	out.State, out.Head = StateRepository, head
	rel, _ := filepath.Rel(top, canonical)
	out.Repository = &Repository{Name: displayName(filepath.Base(top)), ProjectPath: filepath.ToSlash(rel), Worktree: head.Worktree}
	return out
}

func displayName(name string) string {
	if hasControl(name) || SuspiciousText(name) || len(name) > 200 {
		return "repository"
	}
	return name
}

// topLevel asks Git for the canonical top level and whether this is a linked worktree.
func (s *Service) topLevel(ctx context.Context, root string) (top string, worktree bool, err error) {
	out, overflow, err := s.Runner.Run(ctx, root, 64<<10, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if overflow || len(lines) != 3 {
		return "", false, errMalformed
	}
	for _, l := range lines {
		if !filepath.IsAbs(l) || hasControl(l) {
			return "", false, errMalformed
		}
	}
	top, err = filepath.EvalSymlinks(lines[0])
	if err != nil {
		return "", false, errMalformed
	}
	gitDir, err1 := filepath.EvalSymlinks(lines[1])
	common, err2 := filepath.EvalSymlinks(lines[2])
	return top, err1 == nil && err2 == nil && gitDir != common, nil
}

var errMalformed = errors.New("malformed git output")

// gitFailure maps a runner error to a context state and message. Git's own output is never
// echoed: it can contain paths, remotes or configuration.
func gitFailure(err error) (string, string) {
	var exit *exec.ExitError
	switch {
	case errors.Is(err, ErrGitMissing):
		return StateUnavailable, "Git was not found on PATH, so the working tree cannot be read."
	case errors.Is(err, errMalformed):
		return StateError, "Git returned output Hclaro could not read safely. Nothing is shown."
	case errors.Is(err, context.DeadlineExceeded):
		return StateUnavailable, "Git did not answer within 10 seconds."
	case errors.As(err, &exit):
		return StateUnavailable, "Git refused to read this repository (for example, it is owned by another user and not listed in safe.directory)."
	}
	return StateUnavailable, "Git could not read this repository."
}

// Status runs `git status` once for root's repository and returns the IaC files changed in
// the project root and its local modules. It is read-only: no optional locks, no index
// refresh, no fsmonitor.
func (s *Service) Status(ctx context.Context, root string) GitContext {
	out, _ := s.status(ctx, root)
	return out
}

func (s *Service) status(ctx context.Context, root string) (GitContext, *Scope) {
	out := s.HeadOf(root)
	if out.State != StateRepository && out.State != StateUnavailable {
		return out, nil
	}
	if out.State == StateUnavailable && FindGit(root) == "" {
		return out, nil
	}
	canonical, _ := filepath.EvalSymlinks(root)
	top, worktree, err := s.topLevel(ctx, canonical)
	if err != nil {
		out.State, out.Message = gitFailure(err)
		return out, nil
	}
	scope, err := NewScope(canonical, top, s.environ())
	if err != nil {
		out.State, out.Message = StateError, "The project folder is not inside the repository Git reported."
		return out, nil
	}
	raw, overflow, err := s.Runner.Run(ctx, top, maxStatusOutput, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		out.State, out.Message = gitFailure(err)
		return out, nil
	}
	parsed, err := ParseStatus(raw, overflow)
	if err != nil {
		out.State, out.Message = gitFailure(errMalformed)
		return out, nil
	}
	out.State, out.Message = StateRepository, ""
	head := parsed.Head
	head.Worktree = worktree
	out.Head = &head
	out.Repository = &Repository{Name: displayName(filepath.Base(top)), ProjectPath: orDot(scope.RootRel), Worktree: worktree, LocalModules: len(scope.Modules)}
	out.Incomplete = parsed.Incomplete
	for _, e := range parsed.Entries {
		kind := Kind(e.Path)
		place, module := scope.Place(e.Path)
		switch {
		case kind == "":
			out.Counts.Other++
			continue
		case place == "outside":
			out.Counts.Outside++
			continue
		case !safePath(e.Path):
			// Credential-shaped or control-character names are counted, never shown.
			out.Counts.Other++
			continue
		}
		out.Counts.Changed++
		if e.Status == "untracked" {
			out.Counts.Untracked++
		}
		if len(out.Files) == maxListed {
			out.Incomplete = true
			continue
		}
		f := GitFileStatus{ID: s.ids.of(scope.Base, e.Path), Path: e.Path, Place: place, Module: module, Kind: kind, Status: e.Status, Staged: e.Staged, Unstaged: e.Unstaged}
		switch {
		case kind == KindVars:
			f.Reason = "Variable files can hold secrets; their contents are never shown."
		case kind == KindLock:
			f.Reason = "The dependency lock file is listed only."
		case e.Status == "conflicted":
			f.Reason = "Resolve the merge conflict in your editor first."
		default:
			f.Diffable = true
		}
		out.Files = append(out.Files, f)
	}
	// Clean describes the project scope; changes elsewhere are counted separately.
	out.Clean = out.Counts.Changed == 0 && !out.Incomplete
	out.Warnings = warnings(out)
	return out, scope
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

func safePath(p string) bool {
	return len(p) <= 400 && !hasControl(p) && !SuspiciousText(p) && !strings.Contains(p, "\\")
}

func warnings(c GitContext) []GitWarning {
	w := []GitWarning{}
	if c.Head.Detached {
		w = append(w, GitWarning{"detached_head", "HEAD is detached: plans are made from a commit that is not on a branch."})
	}
	if c.Head.Unborn {
		w = append(w, GitWarning{"unborn_branch", "The branch has no commits yet; every file is uncommitted."})
	}
	if c.Counts.Untracked > 0 {
		w = append(w, GitWarning{"untracked_iac", "Untracked IaC files are part of the configuration the engine reads, but they are not in Git."})
	}
	if c.Counts.Outside > 0 {
		w = append(w, GitWarning{"outside_changes", "IaC files changed elsewhere in the repository. They are not shown; they affect this project only if it reads them."})
	}
	if c.Incomplete {
		w = append(w, GitWarning{"status_incomplete", "The working tree has more changes than Hclaro lists; check git status yourself."})
	}
	return w
}

// StatusEntry is one parsed `git status --porcelain=v2` record.
type StatusEntry struct {
	Path     string
	Status   string
	Staged   bool
	Unstaged bool
}

// ParsedStatus is a parsed status document.
type ParsedStatus struct {
	Head       Head
	Entries    []StatusEntry
	Incomplete bool
}

// ParseStatus parses `git status --porcelain=v2 --branch -z` output. It refuses anything it
// does not recognize rather than guessing. When overflow is set, the final partial record
// is dropped and the result is marked incomplete.
func ParseStatus(b []byte, overflow bool) (ParsedStatus, error) {
	p := ParsedStatus{Incomplete: overflow}
	records := bytes.Split(b, []byte{0})
	if overflow && len(records) > 0 {
		records = records[:len(records)-1]
	}
	sawOID := false
	for i := 0; i < len(records); i++ {
		r := string(records[i])
		if r == "" {
			if i == len(records)-1 {
				break
			}
			return p, errMalformed
		}
		switch r[0] {
		case '#':
			key, value, ok := strings.Cut(strings.TrimPrefix(r, "# "), " ")
			if !ok {
				return p, errMalformed
			}
			switch key {
			case "branch.oid":
				sawOID = true
				if value == "(initial)" {
					p.Head.Unborn = true
				} else if commitPattern.MatchString(value) {
					p.Head.Commit = value
				} else {
					return p, errMalformed
				}
			case "branch.head":
				if value == "(detached)" {
					p.Head.Detached = true
				} else {
					p.Head.Branch = CleanBranch(value)
				}
			}
		case '1', '2', 'u':
			// 1 XY sub mH mI mW hH hI path; 2 adds a score and is followed by the source
			// path; u XY sub m1 m2 m3 mW h1 h2 h3 path.
			fields := map[byte]int{'1': 8, '2': 9, 'u': 10}[r[0]]
			parts := strings.SplitN(r, " ", fields+1)
			if len(parts) != fields+1 || len(parts[1]) != 2 || parts[fields] == "" {
				return p, errMalformed
			}
			e, err := entry(parts[1], parts[fields], r[0] == 'u')
			if err != nil {
				return p, err
			}
			p.Entries = append(p.Entries, e)
			if r[0] == '2' {
				i++
			}
		case '?':
			name := strings.TrimPrefix(r, "? ")
			if name == r || !validRepoPath(name) {
				return p, errMalformed
			}
			p.Entries = append(p.Entries, StatusEntry{Path: name, Status: "untracked", Unstaged: true})
		case '!':
			// Ignored files are not requested; skip if a configuration adds them.
		default:
			return p, errMalformed
		}
	}
	if !sawOID {
		return p, errMalformed
	}
	return p, nil
}

func validRepoPath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\x00") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func entry(xy, name string, unmerged bool) (StatusEntry, error) {
	if !validRepoPath(name) {
		return StatusEntry{}, errMalformed
	}
	x, y := xy[0], xy[1]
	valid := ".MTADRCU"
	if !strings.ContainsRune(valid, rune(x)) || !strings.ContainsRune(valid, rune(y)) {
		return StatusEntry{}, errMalformed
	}
	e := StatusEntry{Path: name, Staged: x != '.', Unstaged: y != '.'}
	switch {
	case unmerged || x == 'U' || y == 'U':
		e.Status = "conflicted"
	case x == 'D' || y == 'D':
		e.Status = "deleted"
	case x == 'A':
		e.Status = "added"
	case x == 'T' || y == 'T':
		e.Status = "typechange"
	default:
		e.Status = "modified"
	}
	return e, nil
}
