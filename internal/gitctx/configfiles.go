package gitctx

import (
	"context"
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
