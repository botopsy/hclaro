// Package gitctx reads the local Git context of a project: HEAD, branch, working tree
// status of IaC files, redacted line diffs of HCL files, a source fingerprint for plans,
// best-effort source locations of resources, and validated editor links. It never changes
// a repository: Git runs only through Runner, which accepts a fixed set of read-only
// subcommands with argv arrays, and never through a shell.
package gitctx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
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
	"status": {"--porcelain=v2": true, "--porcelain=v1": true, "--branch": true, "-z": true, "--untracked-files=all": true, "--untracked-files=normal": true, "--no-renames": true, "--ignore-submodules=all": true},
	"diff":   {"--no-ext-diff": true, "--no-textconv": true, "--no-color": true, "--no-renames": true, "--unified=3": true, "HEAD": true, "--": true},
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

// bounded keeps at most limit bytes and records whether more were written.
type bounded struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
			b.overflow = true
		} else {
			b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.overflow = true
	}
	return len(p), nil
}

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
	exe, err := exec.LookPath("git")
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
	argv := append([]string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "--no-pager", "--no-optional-locks", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.Env = Environ(os.Environ())
	out := &bounded{limit: limit}
	cmd.Stdout = out
	// stderr is discarded: it can echo paths, remotes or configuration.
	if err := cmd.Run(); err != nil {
		return nil, false, err
	}
	return out.buf.Bytes(), out.overflow, nil
}
