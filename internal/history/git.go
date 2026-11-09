package history

import "hclaro/internal/gitctx"

// ReadGit returns the commit checked out in the repository that contains root, read from
// .git/HEAD and the refs by gitctx.ReadHead. It never runs git. It returns nil when anything
// is unexpected and for a branch without commits. Uncommitted changes are not detected.
func ReadGit(root string) *GitIdentity {
	head := gitctx.ReadHead(root)
	if head == nil || head.Commit == "" {
		return nil
	}
	return &GitIdentity{Commit: head.Commit, Branch: cleanBranch(head.Branch)}
}

func cleanBranch(b string) string {
	if b == "" || clean(b, 200) != b {
		return ""
	}
	return b
}
