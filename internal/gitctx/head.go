package gitctx

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	refPattern    = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// maxGitFile bounds what is read from one repository metadata file.
const maxGitFile = 1 << 20

// Head is the checked-out commit of the repository containing a folder, read from .git
// files without running Git.
type Head struct {
	// Commit is empty for a branch with no commit yet (Unborn).
	Commit string `json:"commit,omitempty"`
	// Branch is empty when HEAD is detached.
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Unborn   bool   `json:"unborn"`
	// Worktree is set for a linked worktree or submodule (.git is a file).
	Worktree bool `json:"worktree"`
}

// FindGit returns the directory holding the .git entry for root, or "" outside a
// repository. It reads only names.
func FindGit(root string) string {
	for dir := root; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ReadHead reads HEAD, commondir, the checked-out ref and packed-refs of the repository
// containing root, each bounded in size. It returns nil outside a repository or when
// anything is unexpected. It never runs Git and does not detect uncommitted changes.
func ReadHead(root string) *Head {
	top := FindGit(root)
	if top == "" {
		return nil
	}
	candidate := filepath.Join(top, ".git")
	fi, err := os.Lstat(candidate)
	if err != nil {
		return nil
	}
	gitDir, worktree := "", false
	switch {
	case fi.IsDir():
		gitDir = candidate
	case fi.Mode().IsRegular():
		// A linked worktree or submodule: .git is a file naming the real directory.
		b, err := readSmall(candidate)
		line, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if err != nil || !ok || line == "" {
			return nil
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(top, line)
		}
		gitDir, worktree = filepath.Clean(line), true
	default:
		return nil
	}
	head, err := readSmall(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return nil
	}
	text := strings.TrimSpace(string(head))
	if commitPattern.MatchString(text) {
		return &Head{Commit: text, Detached: true, Worktree: worktree}
	}
	ref, ok := strings.CutPrefix(text, "ref: ")
	if !ok || !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") || !refPattern.MatchString(ref) {
		return nil
	}
	branch := CleanBranch(strings.TrimPrefix(ref, "refs/heads/"))
	dirs := []string{gitDir}
	if common, err := readSmall(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(common))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		dirs = append(dirs, filepath.Clean(c))
	}
	for _, dir := range dirs {
		if b, err := readSmall(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			if commit := strings.TrimSpace(string(b)); commitPattern.MatchString(commit) {
				return &Head{Commit: commit, Branch: branch, Worktree: worktree}
			}
		}
		if commit := packedRef(filepath.Join(dir, "packed-refs"), ref); commit != "" {
			return &Head{Commit: commit, Branch: branch, Worktree: worktree}
		}
	}
	// A new repository has a branch but no commit yet.
	return &Head{Branch: branch, Unborn: true, Worktree: worktree}
}

// CleanBranch returns b when it is a displayable branch name, else "".
func CleanBranch(b string) string {
	if !refPattern.MatchString(b) || strings.Contains(b, "..") || len(b) > 200 || SuspiciousText(b) {
		return ""
	}
	return b
}

func packedRef(path, ref string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, 16<<20))
	for scanner.Scan() {
		commit, name, ok := strings.Cut(scanner.Text(), " ")
		if ok && name == ref && commitPattern.MatchString(commit) {
			return commit
		}
	}
	return ""
}

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(io.LimitReader(f, maxGitFile))
}

// hasControl reports control or format characters (bidi overrides included).
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}
