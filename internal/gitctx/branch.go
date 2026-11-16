package gitctx

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// Branch switching (M29): list existing local branches, preview a switch and run it. A
// switch never creates, fetches, merges, stashes, resets or discards anything: it is refused
// unless the whole working tree is clean, the target is an existing local branch and no
// ignored file would be overwritten, and it is checked again under a claim on the tree.

// Branch is one existing local branch.
type Branch struct {
	Name    string `json:"name"`
	Commit  string `json:"commit"`
	Current bool   `json:"current"`
}

// BranchList is the local branch list of a project's repository.
type BranchList struct {
	State      string      `json:"state"`
	Message    string      `json:"message,omitempty"`
	Repository *Repository `json:"repository,omitempty"`
	Head       *Head       `json:"head,omitempty"`
	Branches   []Branch    `json:"branches"`
	// Hidden counts branches whose names Hclaro does not show or switch to (unusual
	// characters); Incomplete is set when there are more than maxBranches.
	Hidden     int    `json:"hidden"`
	Incomplete bool   `json:"incomplete"`
	CheckedAt  string `json:"checkedAt"`
}

// SourceChanges counts the files that differ between HEAD and the target branch: all of
// them, the IaC files, and the IaC files inside the project folder.
type SourceChanges struct {
	Files     int `json:"files"`
	IaC       int `json:"iac"`
	InProject int `json:"inProject"`
}

// WorkingTree counts uncommitted changes anywhere in the working tree.
type WorkingTree struct {
	Clean     bool `json:"clean"`
	Staged    int  `json:"staged"`
	Modified  int  `json:"modified"`
	Untracked int  `json:"untracked"`
}

// SwitchPreview is what a switch would do. It authorizes nothing: Switch checks again.
type SwitchPreview struct {
	From      *Head         `json:"from"`
	To        Branch        `json:"to"`
	Tree      WorkingTree   `json:"workingTree"`
	Source    SourceChanges `json:"sourceChanges"`
	CanSwitch bool          `json:"canSwitch"`
	// Blocked explains why the switch is refused; Warnings are shown with the confirmation.
	Blocked   []GitWarning `json:"blocked"`
	Warnings  []GitWarning `json:"warnings"`
	CheckedAt string       `json:"checkedAt"`
}

// SwitchResult is a finished switch.
type SwitchResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	Head *Head  `json:"head"`
}

// SwitchError is a refused or failed switch. Code is stable; Message is safe to show and
// never contains Git's own output.
type SwitchError struct {
	Code    string
	Message string
	// Ran is set when git switch was started.
	Ran bool
}

func (e *SwitchError) Error() string { return e.Message }

func refuse(code, message string) *SwitchError { return &SwitchError{Code: code, Message: message} }

// Switcher runs the one Git write. Exec is the real one; tests inject fakes.
type Switcher interface {
	Switch(ctx context.Context, dir, branch string) error
}

const (
	maxBranches     = 1000
	maxBranchOutput = 1 << 20
	maxTreeDiff     = 4 << 20
)

// ValidBranchName returns b when it is a plain local branch name Hclaro may pass to Git:
// no leading "-", no "..", no revision or refspec syntax, no control characters. It does
// not check that the branch exists.
func ValidBranchName(b string) string {
	switch {
	case CleanBranch(b) == "", strings.HasPrefix(b, "-"), strings.HasPrefix(b, "/"), strings.HasSuffix(b, "/"),
		strings.HasSuffix(b, "."), strings.HasSuffix(b, ".lock"), strings.Contains(b, "//"), strings.Contains(b, "/."),
		strings.HasPrefix(b, "."), b == "HEAD", strings.HasPrefix(b, "refs/"):
		return ""
	}
	return b
}

// locate resolves root and the top level of its working tree, and checks that root is inside.
func (s *Service) locate(ctx context.Context, root string) (canonical, top, rel string, err *SwitchError) {
	canonical, e := filepath.EvalSymlinks(root)
	if e != nil {
		return "", "", "", refuse("project_unavailable", "The project folder could not be resolved.")
	}
	if FindGit(canonical) == "" {
		return "", "", "", refuse("not_repository", "This project is not inside a Git repository.")
	}
	top, _, e = s.topLevel(ctx, canonical)
	if e != nil {
		_, message := gitFailure(e)
		return "", "", "", refuse("git_unavailable", message)
	}
	rel, e = filepath.Rel(top, canonical)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", "", refuse("outside_repository", "The project folder is not inside the repository Git reported.")
	}
	return canonical, top, filepath.ToSlash(rel), nil
}

// Branches lists the existing local branches of root's repository. It runs read-only Git.
func (s *Service) Branches(ctx context.Context, root string) BranchList {
	base := s.HeadOf(root)
	out := BranchList{State: base.State, Message: base.Message, Repository: base.Repository, Head: base.Head, Branches: []Branch{}, CheckedAt: base.CheckedAt}
	if base.State != StateRepository {
		return out
	}
	_, top, _, err := s.locate(ctx, root)
	if err != nil {
		out.State, out.Message = StateUnavailable, err.Message
		return out
	}
	branches, hidden, incomplete, e := s.listBranches(ctx, top)
	if e != nil {
		out.State, out.Message = gitFailure(e)
		return out
	}
	for i := range branches {
		branches[i].Current = !base.Head.Detached && branches[i].Name == base.Head.Branch
	}
	out.Branches, out.Hidden, out.Incomplete = branches, hidden, incomplete
	return out
}

// listBranches reads refs/heads. Git's output is untrusted: names are checked, and a line
// that is not "<commit> refs/heads/<name>" refuses the whole list.
func (s *Service) listBranches(ctx context.Context, top string) (branches []Branch, hidden int, incomplete bool, err error) {
	raw, overflow, err := s.Runner.Run(ctx, top, maxBranchOutput, "for-each-ref", branchFormat, "refs/heads/")
	if err != nil {
		return nil, 0, false, err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if overflow {
		lines, incomplete = lines[:len(lines)-1], true
	}
	branches = []Branch{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		commit, ref, ok := strings.Cut(line, " ")
		name, isHead := strings.CutPrefix(ref, "refs/heads/")
		if !ok || !isHead || !commitPattern.MatchString(commit) {
			return nil, 0, false, errMalformed
		}
		switch {
		case ValidBranchName(name) == "":
			hidden++
		case len(branches) == maxBranches:
			incomplete = true
		default:
			branches = append(branches, Branch{Name: name, Commit: commit})
		}
	}
	return branches, hidden, incomplete, nil
}

// PreviewSwitch checks a switch of root's repository to target without changing anything.
// A *SwitchError means the request itself is invalid (unknown branch, not a repository);
// reasons that the working tree blocks a switch are in the preview.
func (s *Service) PreviewSwitch(ctx context.Context, root, target string) (SwitchPreview, error) {
	if ValidBranchName(target) == "" {
		return SwitchPreview{}, errInvalidBranch
	}
	_, top, rel, err := s.locate(ctx, root)
	if err != nil {
		return SwitchPreview{}, err
	}
	return s.preview(ctx, top, rel, target)
}

var errInvalidBranch = refuse("invalid_branch", "Choose an existing local branch from the list. Hclaro never creates branches or checks out commits, tags or remote branches.")

func (s *Service) preview(ctx context.Context, top, rel, target string) (SwitchPreview, error) {
	p := SwitchPreview{Blocked: []GitWarning{}, Warnings: []GitWarning{}, CheckedAt: s.now().UTC().Format(time.RFC3339)}
	branches, _, _, e := s.listBranches(ctx, top)
	if e != nil {
		_, message := gitFailure(e)
		return p, refuse("git_unavailable", message)
	}
	found := false
	for _, b := range branches {
		if b.Name == target {
			p.To, found = b, true
		}
	}
	if !found {
		return p, refuse("unknown_branch", "There is no local branch with this name. Hclaro switches only to existing local branches; it never creates one or fetches remote branches.")
	}
	raw, overflow, e := s.Runner.Run(ctx, top, maxStatusOutput, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal", "--ignored=traditional", "--no-renames", "--ignore-submodules=all")
	if e != nil {
		_, message := gitFailure(e)
		return p, refuse("git_unavailable", message)
	}
	st, e := ParseStatus(raw, overflow)
	if e != nil {
		_, message := gitFailure(errMalformed)
		return p, refuse("git_unavailable", message)
	}
	head := st.Head
	p.From = &head
	if !head.Detached && head.Branch == target {
		return p, refuse("already_current", "This branch is already checked out.")
	}
	block := func(code, message string) { p.Blocked = append(p.Blocked, GitWarning{code, message}) }
	for _, e := range st.Entries {
		switch {
		case e.Status == "untracked":
			p.Tree.Untracked++
		case e.Staged:
			p.Tree.Staged++
		default:
			p.Tree.Modified++
		}
	}
	p.Tree.Clean = len(st.Entries) == 0 && !st.Incomplete
	switch {
	case st.Incomplete:
		block("status_incomplete", "The working tree has more changes than Hclaro can read, so it cannot rule out losing them. Commit or stash them with your Git client first.")
	case !p.Tree.Clean:
		block("dirty_tree", "The working tree has uncommitted changes (modified, staged or untracked files anywhere in the repository). Switching could overwrite them, so it is blocked. Commit or stash them with your preferred Git client, then try again. Hclaro never stashes, commits or discards changes.")
	}
	if head.Detached {
		p.Warnings = append(p.Warnings, GitWarning{"detached_head", "HEAD is detached. Commits made on it that are not on a branch become harder to find after switching (git reflog still lists them)."})
	}
	if head.Unborn || head.Commit == "" {
		block("unborn_branch", "The current branch has no commits yet. Make a first commit with your Git client before switching.")
		return finish(p), nil
	}
	if head.Commit == p.To.Commit {
		p.Warnings = append(p.Warnings, GitWarning{"same_commit", "The branch points at the commit that is checked out now; no files change."})
		return finish(p), nil
	}
	raw, overflow, e = s.Runner.Run(ctx, top, maxTreeDiff, "diff-tree", "-r", "-z", "--name-only", "--no-renames", head.Commit, p.To.Commit)
	if e != nil || overflow {
		block("changes_unknown", "Hclaro could not list the files that differ between the branches, so it cannot rule out overwriting ignored files. Switch with your Git client.")
		return finish(p), nil
	}
	changed := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(raw) == 0 {
		changed = nil
	}
	for _, name := range changed {
		if !validRepoPath(name) {
			block("changes_unknown", "Git listed the differing files in a form Hclaro could not read safely. Switch with your Git client.")
			return finish(p), nil
		}
		p.Source.Files++
		if Kind(name) != "" {
			p.Source.IaC++
			if rel == "." || strings.HasPrefix(name, rel+"/") {
				p.Source.InProject++
			}
		}
	}
	if n := overwritesIgnored(changed, st.Ignored); n > 0 {
		block("ignored_overwrite", "The target branch tracks files at paths that hold ignored files here (for example a local variables file or .terraform folder). Git would overwrite or delete them, so switching is blocked. Move them aside with your file manager or Git client first.")
	}
	if rel != "." {
		out, _, e := s.Runner.Run(ctx, top, 64, "cat-file", "-t", p.To.Commit+":"+rel)
		if e != nil || strings.TrimSpace(string(out)) != "tree" {
			block("project_missing", "The project folder does not exist on this branch. Switching would remove it from disk, so Hclaro does not switch from this project.")
		}
	}
	return finish(p), nil
}

func finish(p SwitchPreview) SwitchPreview {
	p.CanSwitch = len(p.Blocked) == 0
	return p
}

// overwritesIgnored counts changed paths that are, contain, or lie inside an ignored path.
func overwritesIgnored(changed, ignored []string) int {
	if len(ignored) == 0 {
		return 0
	}
	ign, chg := map[string]bool{}, map[string]bool{}
	for _, i := range ignored {
		ign[strings.TrimSuffix(i, "/")] = true
	}
	for _, c := range changed {
		chg[c] = true
	}
	n := 0
	for _, c := range changed {
		for p := c; p != "." && p != ""; p = parent(p) {
			if ign[p] {
				n++
				break
			}
		}
	}
	// An ignored file inside a folder that becomes a file on the target branch.
	for i := range ign {
		for p := parent(i); p != "." && p != ""; p = parent(p) {
			if chg[p] {
				n++
				break
			}
		}
	}
	return n
}

func parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// Switch switches root's repository to the existing local branch target. claim must take
// an exclusive claim on the whole working tree (every project root inside it) or refuse.
// Under the claim the HEAD the user reviewed (expectedBranch, "" when detached, and
// expectedCommit) is compared with HEAD now, and the preview checks run again; nothing runs
// unless all pass. It never forces, creates, stashes or discards.
func (s *Service) Switch(ctx context.Context, root, target, expectedBranch, expectedCommit string, claim func(top string) (func(), bool)) (SwitchResult, error) {
	if ValidBranchName(target) == "" {
		return SwitchResult{}, errInvalidBranch
	}
	if s.Switcher == nil {
		return SwitchResult{}, refuse("git_unavailable", "Branch switching is not available in this session.")
	}
	canonical, top, rel, err := s.locate(ctx, root)
	if err != nil {
		return SwitchResult{}, err
	}
	release, ok := claim(top)
	if !ok {
		return SwitchResult{}, refuse("operation_running", "A Terraform/OpenTofu operation, initialization or project tool is running in this repository. Wait for it to finish, then switch.")
	}
	defer release()
	p, e := s.preview(ctx, top, rel, target)
	// A stale review is reported as such, even when the target is now checked out.
	if p.From != nil && (p.From.Commit != expectedCommit || p.From.Branch != expectedBranch || p.From.Detached != (expectedBranch == "")) {
		return SwitchResult{}, refuse("head_changed", "The checked-out branch or commit changed since you reviewed this switch. Nothing was switched. Review it again.")
	}
	if e != nil {
		return SwitchResult{}, e
	}
	if !p.CanSwitch {
		return SwitchResult{}, refuse(p.Blocked[0].Code, p.Blocked[0].Message)
	}
	from := p.From.Branch
	// Once started, the switch is not tied to the request: a closed tab or a request
	// deadline must not kill Git halfway through the checkout.
	if e := s.Switcher.Switch(context.WithoutCancel(ctx), top, target); e != nil {
		return SwitchResult{}, &SwitchError{Code: "switch_failed", Ran: true, Message: "Git did not switch branches (for example, the branch is checked out in another worktree, a file could not be written or Git is too old to have git switch). Check the working tree with git status."}
	}
	head := ReadHead(canonical)
	if head == nil || head.Branch != target || head.Commit != p.To.Commit {
		return SwitchResult{}, &SwitchError{Code: "switch_unverified", Ran: true, Message: "Git reported success, but the checked-out branch could not be confirmed. Check it with git status."}
	}
	return SwitchResult{From: from, To: target, Head: head}, nil
}
