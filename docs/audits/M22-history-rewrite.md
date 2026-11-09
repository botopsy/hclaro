# M22 local history rewrite: removal of the design mockup

Authorized by the owner on 2026-11-09: remove `docs/design/reference/Cursor.dc.html` and its `docs/design/reference/README.md` from the complete Git history, **locally only**. Nothing was pushed in M22. The remote branches were synchronized afterwards in [M22.1](M22.1-remote-history-remediation.md) (2026-11-09); the PR #36/#37 references keep the original commits.

## Scope

Only one commit ever touched the two files: `4f920f6` ("Document the M19 brand identity") added them, and no later commit changed or deleted them until M22. No tag contains that commit. Seven commits descend from it and therefore contain the files:

| Original | Rewritten | Commit |
| --- | --- | --- |
| `4f920f6` | `cf1bfad` | Document the M19 brand identity |
| `bf55d7f` | `e7d02f6` | Normalize line endings in the brand stylesheet test (tip of `m19-brand-identity`) |
| `96f4e8a` | `b36f704` | Merge #36: Add hollow-cursor brand identity (M19) |
| `60f1bb4` | `fbb8a83` | Regroup the sidebar around a project switcher (M21) |
| `85e25cf` | `c195927` | Rebuild embedded UI with the M21 navigation |
| `83cfd6e` | `390ec5f` | Document the M21 navigation and refresh screenshots (tip of `m21-navigation-ia`) |
| `8c8de78` | `35c77ee` | Merge #37: Regroup navigation around a project switcher (M21) (tip of `master` and base of `m22-repository-cleanup`) |

Refs moved in this repository: `master`, `m19-brand-identity`, `m21-navigation-ia`, `m22-repository-cleanup`. Unchanged: the other 11 local branches, all 5 tags (`v0.1.0-alpha.2` … `v0.1.0-alpha.6`) and every commit before `4f920f6`, with the same hashes. The published releases therefore still point to valid, unchanged commits.

Side effect: the two GitHub merge commits (`96f4e8a`, `8c8de78`) were signed by GitHub; their rewritten versions are unsigned, so GitHub will no longer show them as *Verified*. Author, committer, dates and messages of all seven commits are unchanged.

## Procedure that was run

1. **Backup** in the maintainer's private backup folder `hclaro-backups/m22-history-rewrite-2026-11-09/` next to the working copy (outside the repository; never delete without the owner's approval):
   - `hclaro-all-refs.bundle`: `git bundle create … --all` of all 34 refs; `git bundle verify` reported it okay.
   - `hclaro-codex-handoff-full.tar.gz`: the whole working directory including `.git`, the index, the uncommitted M22 work and untracked files (`tasks/`, `CLAUDE.md`), excluding only dependency and build caches.
   - `staged.patch`, `unstaged.patch`, `status-before.txt`, `refs-before.txt`, `refs-after.txt`, and the pre- and post-rewrite scan reports; `SHA256SUMS` for each set.
2. **Isolated rewrite** in a temporary mirror clone (`git clone --mirror`, origin removed):

   ```sh
   FILTER_BRANCH_SQUELCH_WARNING=1 git filter-branch \
     --index-filter 'git rm -q --cached --ignore-unmatch docs/design/reference/Cursor.dc.html docs/design/reference/README.md' \
     --tag-name-filter cat -- --all ^8c311efa8f7f7863fbdae0bef9d3a7a64a7b036f
   ```

   The negative revision (the parent of `4f920f6`) limits the rewrite to the seven affected commits. A first attempt without it was discarded: `filter-branch` drops signatures, so it changed the hashes of all 37 GitHub-signed merges and the 5 tags even though their content was unaffected.
3. **Verification in the clone**, before and after removing `refs/original`, expiring reflogs and `git gc --prune=now`:
   - each rewritten ref differs from its original only by `D docs/design/reference/Cursor.dc.html` and `D docs/design/reference/README.md`;
   - all seven commits keep author, committer, dates and message; merge parents map to the rewritten commits (`b36f704` → `299e493`, `e7d02f6`; `35c77ee` → `b36f704`, `390ec5f`);
   - both file blobs (`7f7f679e0aab…`, `402ab3a9dd8a…`) and commit `4f920f6` are absent; `git log --all -- docs/design/reference` is empty; no branch or tag tree contains the path; `git fsck --full` is clean; 101 commits (63 non-merge), as before.
4. **Integration** into this repository without touching the index or working tree:

   ```sh
   git fetch --update-head-ok <mirror> \
     +refs/heads/m19-brand-identity:refs/heads/m19-brand-identity \
     +refs/heads/m21-navigation-ia:refs/heads/m21-navigation-ia \
     +refs/heads/master:refs/heads/master \
     +refs/heads/m22-repository-cleanup:refs/heads/m22-repository-cleanup
   git config --local pull.ff only   # safeguard, see below
   ```

   Afterwards `git status` differed from before only by the two staged mockup deletions disappearing (the new `HEAD` no longer has the files); the unstaged diff is byte-identical to the backup and the staged diff matches it minus those two files. No local branch or tag contains the files.

**Not changed:** the remote-tracking refs `origin/master`, `origin/m19-brand-identity` and `origin/m21-navigation-ia` still point to the original commits, because they describe GitHub's state. Together with the local reflogs they keep the old objects in this repository's `.git` until the cleanup step below. No commit of the original history was merged into the rewritten branches.

## Safeguard until GitHub is synchronized

Do **not** run `git pull`, `git merge origin/<branch>` or `git rebase` onto `origin/master`, `origin/m19-brand-identity` or `origin/m21-navigation-ia`: they still contain the mockup and would bring it back. `pull.ff only` (set locally) makes an accidental `git pull` on these branches fail instead of merging. Remove it later with `git config --local --unset pull.ff`.

## Synchronizing GitHub (owner action, not done)

**Superseded by [M22.1](M22.1-remote-history-remediation.md)**, which keeps the existing repository, verifies the current remote state and gives the exact, dry-run-tested command. The general notes below remain for reference.

1. Commit the M22 work on `m22-repository-cleanup` (it is based on the rewritten `master`), review it, and decide whether it goes through a pull request after the force-push or is merged locally first.
2. Make sure nobody else pushes meanwhile, and temporarily allow force pushes on `master` if a ruleset blocks them.
3. Force-push the three rewritten branches, each guarded by the exact old hash so nothing on GitHub that you have not seen is overwritten:

   ```sh
   git push --force-with-lease=master:8c8de781cf6c8f1987a905b0e760005120ffd9a2 origin master
   git push --force-with-lease=m19-brand-identity:bf55d7f03e27f348fdc452d7bdd1a98670bf15b3 origin m19-brand-identity
   git push --force-with-lease=m21-navigation-ia:83cfd6e8f9a3bb7c5b268b068e2beddcac11c0c6 origin m21-navigation-ia
   git push origin m22-repository-cleanup   # after committing M22
   ```

   Tags need no push: they are unchanged.
4. **GitHub still keeps the old commits** through the pull-request refs of PR #36 (head `bf55d7f`) and PR #37 (head `83cfd6e`, merge `8c8de78`), which repository owners cannot delete, and through cached views. Before the repository becomes public, either:
   - ask GitHub Support to remove those pull-request references and run garbage collection, following GitHub's *Removing sensitive data from a repository* guide (affected commits: the seven listed above), or
   - publish from a **new repository** pushed from the rewritten history (no old pull requests), and keep or archive the current private one.
5. Re-enable branch protection; let CircleCI run on the new `master`.
6. Check on GitHub: `docs/design/reference` absent on every branch, and the old commit URLs no longer reachable (after Support's cleanup) or the new repository has no trace of them.

## Other clones

Every existing clone still has the original history. For each clone (other machines, CI checkouts that persist):

1. Push or back up any local work (and copy `tasks/` and `AGENTS.md`, which M22 stops tracking).
2. Preferably delete the clone and clone again after the force-push.
3. Otherwise: `git fetch origin`, then for each rewritten branch `git checkout <branch> && git reset --hard origin/<branch>` (this discards local commits on it), delete local branches built on the old commits, and never merge them back.

## Local cleanup after synchronization (optional, not done)

After GitHub is synchronized: `git fetch --prune origin` moves `origin/*` to the rewritten commits. The old objects then remain only in reflogs; `git reflog expire --expire=now --all && git gc --prune=now` removes them from this `.git`. The backup bundle still contains the original history, so keep it access-restricted, and delete it only when the owner decides.

The temporary mirror clone lives in the session's scratch directory and can be discarded; this repository's refs are the result.
