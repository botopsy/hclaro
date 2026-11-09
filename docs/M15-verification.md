# M15 implementation and verification

**Status: draft.** Git-aware infrastructure is implemented as a new read-only package `internal/gitctx` (allowlisted Git runner, HEAD reader, status and diff parsers, IaC scope with local modules, line redaction, plan source fingerprints, best-effort source locations, editor URLs), Git routes for the launch project and Hub projects, a `git.source` preflight check on the existing M14 apply path, and a connected **Git** view, context-bar badge, plan source banner and resource **Configuration source** link in the UI.

Design and trade-offs: [architecture](architecture.md#git-context), [security](../SECURITY.md), [README](../README.md#git-changes).

## Scope decisions

- **POST for anything that runs Git.** The repository rule is that GET routes never run a command. Reading HEAD (badge) and locating a resource's source only read files, so they are GETs; `git status` and `git diff` are CSRF-protected POSTs (`POST …/git/status`, `POST …/git/diff {fileId}`), like Hub inspection. The task's `GET …/git/status` and `GET …/git/diff?fileID=` are illustrative.
- **Routes for both kinds of project.** The launch project's routes are at the API root (`/api/v1/git/…`, `/api/v1/editor/open`, `/api/v1/resources/{id}/source`); a Hub project's are under `/api/v1/projects/{id}/…` and `/api/v1/contexts/{cid}/resources/{rid}/source`. One `gitctx.Service` serves both, so file IDs agree across views.
- **Scope = project root + local modules.** Local module directories come from the manifest `init` writes (`./`, `../` sources), may lie outside the project root but must stay inside the repository. Changes elsewhere in a monorepo are counted, not named or diffed. Downloaded modules are excluded.
- **Opaque file IDs.** HMAC-SHA256 (per-process random key) of the base-relative path; the server resolves an ID against a fresh status (diffs) or the current scope (editor). The browser never sends a path.
- **Source fingerprint without Git.** HEAD is read from `.git` files and the content hash from the files themselves, so comparing a plan's source on every plan detail or preflight runs no Git command. Only plan creation runs one `git status` to record whether IaC files were uncommitted. M13's `history.ReadGit` now uses the same HEAD reader.
- **Severity.** Changed IaC content (root or local modules) or an incomputable hash blocks (`git.source`, block); a moved HEAD with identical content needs an acknowledgement (warn). The check is part of `preflight.Evaluate`, so `StartApply` re-evaluates it. The M14 context fingerprint is unchanged; the source fingerprint adds local modules outside the root, which it did not cover.
- **Editor links through URL handlers.** `vscode://file/<path>:<line>` / `cursor://…`, percent-encoded, handed as one argument to the OS opener. Hclaro does not run `code`/`cursor` executables (on Windows those are `.cmd` scripts interpreted by `cmd.exe`).
- **Diff content.** `.tf`, `.tf.json` (and the OpenTofu `.tofu`, `.tofu.json` equivalents) only; `.tfvars` and the lock file are listed by name. Diffs are returned in the response only and never logged or written to history.

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.3, Node 24, Git 2.41.0, Playwright Chromium.

| Check | Result |
| --- | --- |
| `make test` (`go test -race`, Vitest) | Passed: all Go packages; Vitest 106/106 in 14 files. New: `gitctx/gitctx_test.go` (18 tests), `operations/source_test.go` (5), `api/git_test.go` (5), `preflight` `TestSourceCheck`, security guard rows for the new routes, `web/src/git/Git.test.tsx` (6). |
| Synthetic Git repositories | Built with Git in `t.TempDir()` as test setup only. Every command run by the code under test goes through `gitctx.Exec` with an `Observe` recorder; each test fails if anything other than an allowlisted `rev-parse`/`status`/`diff` ran. `TestAllowedIsReadOnly` shows `checkout`, `reset`, `pull`, `push`, `commit`, `fetch`, `stash`, `config`, `update-index`, `gc`, `worktree`, extra options, non-literal or multiple pathspecs and `--output` are refused before Git is looked up. |
| Existing tests | All M01–M14 Go, Vitest and browser tests pass unchanged (the M14 Git probe, history Git identity and apply flows included). |
| `make lint`, `GOOS=windows go vet ./...`, `gofmt` | Passed. |
| `make build`, `make browser-test` | Passed; the smoke test now uses a synthetic repository in `.cache/git-fixture` through the real Go handler: badge, Git view, tfvars listed without content, redacted diff (canary asserted absent from every API response and the page), **Open in VS Code** (opener stubbed), resource source link `main.tf:5` and the plan's source line. New screenshots `git-changes-dark.png`, `resource-source-dark.png`. |
| Live loopback run | `bin/hclaro --engine terraform --path <scratch repo>/app` with only the synthetic CLI on `PATH` and a scratch `HCLARO_CONFIG_DIR`; project root `app/` with a local module `../modules/net`. `GET git/head` → `main`, project path `app`; `POST git/status` clean; after adding `password = "…canary…"` to the module (outside the root): the module file listed as `place: module`, the diff returned `password = "[redacted]"` (canary count 0); `editor: "sh"` → 400 `invalid_editor`; status without CSRF → 403; source of `aws_s3_bucket.artifacts` → `app/main.tf:5`. Plan → `source {commit, branch: main, dirty: false, files: 2, localModules: 1}`, apply available; after editing the module: `sourceStatus: changed`, apply unavailable, preflight blocked with **`context.fingerprint` pass** and **`git.source` block** (the local module is outside the context fingerprint). No editor was opened in the live run. |

## M15 acceptance mapping

| Criterion | Evidence |
| --- | --- |
| Detect a Git worktree; show repository (scoped), branch/HEAD, dirty/clean, modified IaC filenames, warnings for detached HEAD and untracked IaC | `Service.Status`, `HeadOf`; `TestCleanBranch`, `TestDirtyUntrackedAndVariableFiles`, `TestDetachedHead`, `TestLinkedWorktree`, `TestNestedRootAndLocalModules`, `TestNotARepositoryAndMissingGit`; UI tests for badge, summary and warnings |
| Git Changes view; safe line diff for explicitly selected `.tf`/`.tf.json`; secrets redacted before the response; no `.tfvars` values; no diffs in history | `Service.Diff`, `LineRedactor`; `TestDiffRedactsSecrets`, `TestLineRedactor`, `TestLaunchGitStatusDiffAndEditor` (canary check on every response), UI test (diff only after a click; tfvars not requested); nothing in `operations`/`history` stores diffs |
| Plan ↔ commit and working-tree fingerprint; "different source revision"; apply invalidated per M14 binding rules | `PlanSourceFingerprint`, `Manager.SourceStatus`, `git.source`; `TestPlanRecordsSourceFingerprint`, `TestModifiedTrackedFileAfterPlanBlocksApply`, `TestLocalModuleChangeOutsideRootBlocksApply`, `TestHeadMoveWithSameFilesNeedsAcknowledgement`, `TestPlanSourceStatusOverHTTP`, `TestSourceCheck`, UI plan banner test |
| Resource → probable file and block, labeled best effort; Git diff is not proof of resource changes | `Service.Locate`; `TestLocate` (root, data, module with instance key, JSON, remote module, missing), Hub context test; UI labels **Best effort** and states that only a plan shows changes |
| Open in editor (VS Code/Cursor) with validated paths/lines; no executable/URL injection | `OpenEditor`, `EditorURL`; `TestOpenEditor` (shell metacharacters percent-encoded, invalid editor/line/ID, non-IaC file, opener failure), HTTP refusals of `sh`, absolute paths, extra `url`/`path` fields |
| Monorepos, worktrees, symlinks; nothing outside the approved scope; no checkout/reset/pull/commit/push | `Scope`; `TestNestedRootAndLocalModules`, `TestLinkedWorktree`, `TestSymlinkEscapeIsRefused` (file and directory links), `TestHubGitIsolationAndSource` (IDs do not work across projects), allowlist tests and command recorders |
| Malicious path, malformed Git output, large/binary diff | `TestMalformedGitOutputIsRefused`, `TestParseStatusRecords`, `TestParseDiff`, `TestBinaryAndLargeFiles`, `invalid_file` / `file_not_found` HTTP cases |
| Authorization and CSRF | `TestGitRoutesNeedSessionOriginAndCSRF`, security guard table (new POST routes need Origin and CSRF; `git/checkout`, `git/commit`, `editor/run` are 405) |

## Manual verification

1. `make build`, then in a disposable copy of a project inside a Git repository run `bin/hclaro --path <project>` (or `make demo` and open the Hub). Do not use real credentials or backends.
2. Check the context-bar badge (`branch @ commit`). Open **Git**: repository name, project path, HEAD and changed IaC files. Edit a `.tf` file and add `password = "x"`; **Refresh**, select the file: the value shows as `[redacted]`. Select a `.tfvars` file: no content is shown.
3. In **Settings → Source editor** pick VS Code or Cursor; **Open in …** should open the file at the line (requires the editor's URL handler).
4. In **Resources**, open a resource defined in the root module: **Configuration source** shows `file:line` labeled *Best effort*.
5. Run a plan, then edit a `.tf` file of the project or a local module: the plan shows *Plan generated from a different source revision* and **Review and apply…** is unavailable; the preflight lists *Plan matches the current source revision* as blocking. Revert the edit and commit something unrelated: the check becomes a warning to acknowledge.
6. Confirm `git status`/`git log` in the repository are unchanged by Hclaro (no new commits, stashes, index changes or branch switches).

## Unresolved

- Real editor hand-off was verified only with a stubbed opener in tests and the browser smoke; the OS URL handlers (`open`, `xdg-open`, `rundll32`) were not exercised against installed editors in CI.
- Source locations are textual: blocks generated by `for_each` over modules, `dynamic` constructs, overrides (`*_override.tf`) or unusual formatting may be missed or approximate; resources in registry/Git modules are not linked.
- Line redaction is heuristic, like the state redactor: a secret under an ordinary name that does not look random is shown. Heredocs that begin before a hunk are followed only from the hunk start.
- The badge reads HEAD from `.git` files; exotic layouts (reftable refs, `GIT_DIR` set outside Hclaro) report HEAD as unavailable while the Git view still works.
- Requires Git ≥ 2.31 for `rev-parse --path-format`; older Git reports the context as unavailable. Tested with 2.41 only.
- The M14 `git.worktree` check still counts the whole repository; it was left unchanged.
- History records only the commit and branch (M13); the source fingerprint is not persisted across restarts, matching plans, which are in memory.
