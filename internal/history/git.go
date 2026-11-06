package history

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// maxGitFile bounds what is read from one repository file.
const maxGitFile = 1 << 20

// ReadGit returns the commit checked out in the repository that contains root, read from
// .git/HEAD and the refs. It never runs git and reads only HEAD, commondir, the checked-out
// ref and packed-refs, each bounded in size. It returns nil when anything is unexpected.
// Uncommitted changes are not detected.
func ReadGit(root string) *GitIdentity {
	gitDir := ""
search:
	for dir := root; ; {
		candidate := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(candidate); err == nil {
			switch {
			case fi.IsDir():
				gitDir = candidate
			case fi.Mode().IsRegular():
				// A worktree or submodule: .git is a file naming the real directory.
				b, err := readSmall(candidate)
				line, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
				if err != nil || !ok || line == "" {
					return nil
				}
				if !filepath.IsAbs(line) {
					line = filepath.Join(dir, line)
				}
				gitDir = filepath.Clean(line)
			default:
				return nil
			}
			break search
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
	head, err := readSmall(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return nil
	}
	text := strings.TrimSpace(string(head))
	if commitPattern.MatchString(text) {
		return &GitIdentity{Commit: text}
	}
	ref, ok := strings.CutPrefix(text, "ref: ")
	if !ok || !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") || !branchPattern.MatchString(ref) {
		return nil
	}
	branch := cleanBranch(strings.TrimPrefix(ref, "refs/heads/"))
	// A linked worktree keeps shared refs in the common directory.
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
				return &GitIdentity{Commit: commit, Branch: branch}
			}
		}
		if commit := packedRef(filepath.Join(dir, "packed-refs"), ref); commit != "" {
			return &GitIdentity{Commit: commit, Branch: branch}
		}
	}
	// A new repository has a branch but no commit yet.
	return nil
}

func cleanBranch(b string) string {
	if !branchPattern.MatchString(b) || strings.Contains(b, "..") {
		return ""
	}
	if clean(b, 200) != b {
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
