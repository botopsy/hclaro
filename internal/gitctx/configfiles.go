package gitctx

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConfigFilesArgs lists the configuration files Git does not ignore under the working
// directory: tracked ones plus untracked ones not excluded by any .gitignore, .git/info/exclude
// or core.excludesFile. Paths are relative to the working directory. Files in nested
// repositories and submodules are never listed.
var ConfigFilesArgs = []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.tf", "*.tofu", "*.tf.json", "*.tofu.json", ".terraform.lock.hcl"}

// ConfigFiles reports which configuration files under root Git does not ignore (AI-01.09), as
// root-relative slash paths. It is an allowlist: a file Git does not list (ignored, inside a
// nested repository or submodule, or named differently than Git reports it) is not visible.
// repo is false when root is not inside a repository. ok is false when root is in a repository
// but the list could not be read completely: Git missing, failing or over the output limit.
func (s *Service) ConfigFiles(ctx context.Context, root string) (files map[string]bool, repo, ok bool) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false, false
	}
	if FindGit(canonical) == "" {
		return nil, false, true
	}
	if _, _, err := s.topLevel(ctx, canonical); err != nil {
		return nil, true, false
	}
	raw, overflow, err := s.Runner.Run(ctx, canonical, maxStatusOutput, ConfigFilesArgs...)
	if err != nil || overflow {
		return nil, true, false
	}
	files = map[string]bool{}
	for _, p := range strings.Split(string(raw), "\x00") {
		if p == "" {
			continue
		}
		if !validRepoPath(p) || hasControl(p) {
			return nil, true, false
		}
		files[p] = true
	}
	return files, true, true
}

// Ignored reports whether Git ignores rel, a root-relative slash path that need not exist
// (AI-01.12 new files): by a .gitignore in any folder on its way, .git/info/exclude or
// core.excludesFile, including a rule matching one of its parent folders; a negated rule
// (!name) that re-includes it counts as not ignored. Root must be inside a repository. An
// error means Git could not tell: callers treat it as ignored.
func (s *Service) Ignored(ctx context.Context, root, rel string) (bool, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	// Exit 0: ignored; 1: not ignored; anything else is a failure.
	_, _, err = s.Runner.Run(ctx, canonical, 64<<10, "check-ignore", "--", rel)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}
