// Package gitctx reads the local Git context of a project: HEAD, branch, working tree
// status of IaC files, redacted line diffs of HCL files, a source fingerprint for plans,
// best-effort source locations of resources, and validated editor links. It never changes
// a repository through Runner, which accepts a fixed set of read-only subcommands with argv
// arrays, and never through a shell. The one write is a reviewed switch to an existing local
// branch (branch.go), which runs through Exec.Switch with its own exact argv.
package gitctx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"hclaro/internal/bounded"
	"hclaro/internal/proc"
	"hclaro/internal/redact"
)

// Runner runs one read-only Git command in dir and returns its standard output, at most
// limit bytes, and whether more was written. Tests inject fakes.
type Runner interface {
	Run(ctx context.Context, dir string, limit int, args ...string) (out []byte, overflow bool, err error)
}

// Errors returned by runners.
var (
	ErrGitMissing = errors.New("git was not found on PATH")
	ErrRefused    = errors.New("git command is not on the read-only allowlist")
)

// readOnly lists the only Git subcommands Hclaro runs, with the options each may carry.
// Nothing here writes refs, the index, objects, configuration or the working tree:
// `status` is run with GIT_OPTIONAL_LOCKS=0 so it does not refresh the index either.
var readOnly = map[string]map[string]bool{
	"rev-parse": {"--path-format=absolute": true, "--show-toplevel": true, "--git-dir": true, "--git-common-dir": true},
	// --porcelain=v1 and --untracked-files=normal are the apply preflight's status.
	"status": {"--porcelain=v2": true, "--porcelain=v1": true, "--branch": true, "-z": true, "--untracked-files=all": true, "--untracked-files=normal": true, "--no-renames": true, "--ignore-submodules=all": true, "--ignored=traditional": true},
	// Branch switching reads: the local branch list, the paths two commits differ in, and
	// whether a path exists in a commit (exact shapes checked in Allowed).
	"for-each-ref": {branchFormat: true, "refs/heads/": true},
	"diff-tree":    {"-r": true, "-z": true, "--name-only": true, "--no-renames": true},
	"cat-file":     {"-t": true},
	"ls-files":     {}, // exact shape checked in Allowed
	"check-ignore": {}, // exact shape checked in Allowed
	"diff":         {"--no-ext-diff": true, "--no-textconv": true, "--no-color": true, "--no-renames": true, fullContext: true, "HEAD": true, "--": true},
}

// Allowed reports whether args is a permitted read-only command: a known subcommand, only
// its listed options, and, for diff, exactly one literal pathspec after "--".
func Allowed(args []string) bool {
	if len(args) == 0 {
		return false
	}
	options, ok := readOnly[args[0]]
	if !ok {
		return false
	}
	switch args[0] {
	case "for-each-ref":
		return slices.Equal(args, []string{"for-each-ref", branchFormat, "refs/heads/"})
	case "diff-tree":
		// diff-tree -r -z --name-only --no-renames <commit> <commit>
		return len(args) == 7 && slices.Equal(args[:5], []string{"diff-tree", "-r", "-z", "--name-only", "--no-renames"}) && commitPattern.MatchString(args[5]) && commitPattern.MatchString(args[6])
	case "ls-files":
		return slices.Equal(args, ConfigFilesArgs)
	case "check-ignore":
		// check-ignore -- <path>: one plain relative path (Git refuses pathspec magic here).
		return len(args) == 3 && args[1] == "--" && validRepoPath(args[2]) && !redact.HasControl(args[2]) && !strings.HasPrefix(args[2], ":") && !strings.HasPrefix(args[2], "-")
	case "cat-file":
		// cat-file -t <commit>:<path>
		if len(args) != 3 || args[1] != "-t" {
			return false
		}
		commit, p, ok := strings.Cut(args[2], ":")
		return ok && commitPattern.MatchString(commit) && validRepoPath(p) && !redact.HasControl(p)
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if args[0] == "diff" && args[i-1] == "--" {
			// The pathspec: one literal path, last.
			return i == len(args)-1 && strings.HasPrefix(a, ":(literal)") && len(a) > len(":(literal)") && !strings.ContainsRune(a, 0)
		}
		if !options[a] {
			return false
		}
	}
	return args[0] != "diff"
}

// Exec is the real runner. Every command gets a fixed prefix that disables repository
// features able to run configured programs (fsmonitor) or write caches (untracked cache),
// and an environment without inherited GIT_* variables, so GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE, GIT_EXTERNAL_DIFF or GIT_CONFIG_* cannot redirect or extend it.
type Exec struct {
	Timeout time.Duration
	// Observe, when set, is called with each permitted argv before it runs (tests).
	Observe func(args []string)
}

// branchFormat lists each local branch as "<commit> refs/heads/<name>".
const branchFormat = "--format=%(objectname) %(refname)"

// Environ returns environ without GIT_* variables, plus the fixed settings Hclaro uses.
func Environ(environ []string) []string {
	out := make([]string, 0, len(environ)+4)
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") || strings.EqualFold(key, "LC_ALL") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "LC_ALL=C")
}

func (e Exec) Run(ctx context.Context, dir string, limit int, args ...string) ([]byte, bool, error) {
	if !Allowed(args) {
		return nil, false, ErrRefused
	}
	return e.run(ctx, dir, limit, args)
}

// SwitchTimeout bounds one git switch.
const SwitchTimeout = 10 * time.Minute

// SwitchArgs is the only Git write Hclaro runs: a switch to an existing local branch. It
// never creates a branch (no -c/-C, --no-guess stops remote-tracking guesses), never forces
// or discards (no --force/--discard-changes/--merge) and does not touch submodules.
func SwitchArgs(branch string) []string {
	return []string{"switch", "--no-guess", "--no-recurse-submodules", "--", branch}
}

// Switch runs SwitchArgs(branch) in dir. The caller has validated branch against the local
// branch list and checked the working tree under a claim on the whole tree.
func (e Exec) Switch(ctx context.Context, dir, branch string) error {
	if ValidBranchName(branch) == "" {
		return ErrRefused
	}
	// Checking out a large tree (or through a filter such as Git LFS) can take a while; a
	// killed switch would leave a half-updated tree and an index.lock.
	e.Timeout = max(e.Timeout, SwitchTimeout)
	_, _, err := e.run(ctx, dir, 64<<10, SwitchArgs(branch))
	return err
}

func (e Exec) run(ctx context.Context, dir string, limit int, args []string) ([]byte, bool, error) {
	exe, err := proc.LookPath("git")
	if errors.Is(err, proc.ErrBatchFile) {
		return nil, false, err
	}
	if err != nil {
		return nil, false, ErrGitMissing
	}
	if e.Observe != nil {
		e.Observe(append([]string(nil), args...))
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// core.hooksPath is the null device, which is never a folder, so no repository hook
	// (such as post-checkout) runs.
	argv := append([]string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "submodule.recurse=false", "--no-pager", "--no-optional-locks", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.Env = Environ(os.Environ())
	out := &bounded.Buffer{Limit: limit}
	cmd.Stdout = out
	// stderr is discarded: it can echo paths, remotes or configuration.
	if err := cmd.Run(); err != nil {
		return nil, false, err
	}
	return out.Bytes(), out.Overflow, nil
}
