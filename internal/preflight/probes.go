package preflight

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"
	"unicode"

	"hclaro/internal/state"
)

// GitProbe reads the Git working tree status of root. Tests inject fakes.
type GitProbe func(ctx context.Context, root string) GitResult

const (
	gitTimeout     = 10 * time.Second
	maxProbeOutput = 1 << 20
	maxPaths       = 10
)

// insideRepository reports whether root or a parent has a .git entry. It reads only names.
func insideRepository(root string) bool {
	for dir := root; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// GitRunner runs one allowlisted, read-only Git command in dir and returns at most limit
// bytes of stdout and whether more was written. gitctx.Exec.Run is the real one: it disables
// fsmonitor, the untracked cache, optional locks and prompts, drops inherited GIT_*
// variables and discards stderr.
type GitRunner func(ctx context.Context, dir string, limit int, args ...string) ([]byte, bool, error)

// GitStatus runs `git status --porcelain=v1 -z --untracked-files=normal` in root through run,
// so status neither runs repository-configured programs nor writes to the repository, and
// an inherited GIT_DIR or GIT_INDEX_FILE cannot make it describe another repository.
func GitStatus(ctx context.Context, root string, run GitRunner) GitResult {
	if !insideRepository(root) {
		return GitResult{State: GitNotRepo, Paths: []string{}}
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, overflow, err := run(ctx, root, maxProbeOutput, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return GitResult{State: GitUnavailable, Paths: []string{}}
	}
	r := ParsePorcelain(out)
	r.Incomplete = overflow
	return r
}

// ParsePorcelain parses `git status --porcelain=v1 -z` output.
func ParsePorcelain(b []byte) GitResult {
	r := GitResult{State: GitClean, Paths: []string{}}
	fields := bytes.Split(b, []byte{0})
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		xy, name := string(f[:2]), string(f[3:])
		if xy == "??" {
			r.Untracked++
		} else {
			r.Modified++
		}
		// A rename or copy is followed by its source path.
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		if len(r.Paths) < maxPaths {
			if p := cleanPath(name); p != "" {
				r.Paths = append(r.Paths, p)
			}
		}
	}
	if r.Modified+r.Untracked > 0 {
		r.State = GitDirty
	}
	// Truncated means not every changed path is listed.
	r.Truncated = len(r.Paths) < r.Modified+r.Untracked
	return r
}

// cleanPath makes a repository path safe to display, or drops it.
func cleanPath(p string) string {
	if len(p) > 200 || state.Suspicious(p) {
		return ""
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return p
}
