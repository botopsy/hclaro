# M22 proposed GitHub and CI settings

Repository: `botopsy/hclaro`. Updated with the owner's decisions. Current state (read with `gh repo view`, nothing changed): **private**, default branch `master`, Issues on, Discussions off, Wiki off, no license detected (until `LICENSE` is merged). None of the settings below can be applied by files in the repository; each is a manual action by the maintainer.

## Applied on 2026-11-09 (M25, owner-approved)

Read back through the GitHub API after each change. The repository was made **public** on 2026-11-09; the tables below are the M22 proposal, kept for reference.

| Setting | Live value |
| --- | --- |
| Visibility | Public. `v0.1.0-beta.1` (built while private) was turned back into a draft first; `v0.1.0-beta.2` is the first public release. |
| Ruleset `master` (default branch) | Active, no bypass: no deletion, no force push, pull request required (0 approvals), required checks `changes`, `linux`, `windows`, `macos` from GitHub Actions (not strict). |
| Ruleset `release tags` (`refs/tags/v*`) | Active, no bypass: no deletion, no update, no force push. |
| Secret scanning / push protection | On / on. One alert (a synthetic `glpat-` value in `internal/state/state_test.go`) resolved as *used in tests*. |
| Private vulnerability reporting | On. |
| Dependabot | Alerts on; automatic security-update PRs off (decision open). |
| Code scanning | Not configured (optional). |
| Actions | Default token read-only, cannot approve pull requests; approval required for workflows from all external contributors. CI and releases run on GitHub Actions (`ci.yml`, `release.yml`). |
| Collaborators | `vidojeg` (admin), `botopsy-admin` (admin). |
| Features | Issues and Discussions on; Wiki and Projects off; forking allowed (Apache-2.0). |
| CircleCI | No release workflow; `GITHUB_RELEASE_TOKEN` removed by the owner. Pull request/`master` workflows remain until retired (M25.14). |

On the same day, the `Co-Authored-By` trailers of four commits were removed by a history rewrite of `master` from `88ad6d5`. The rulesets were disabled for a few minutes for that force push and the move of the then-unpublished `v0.1.0-beta.2` tag, then re-enabled. Code trees, authors and dates are unchanged. Closed PRs #40–#43 still show the original commits.

## Before making the repository public

| Setting | Recommendation | Where | Status |
| --- | --- | --- | --- |
| Visibility | Public, **only after** the public-beta checklist is complete. A public repository can be forked by anyone under GitHub's terms, and the code is usable under Apache-2.0. | Settings → General → Danger zone | Manual, not during M22 |
| Issues | **On** (owner decision 6), with the forms in `.github/ISSUE_TEMPLATE/` (blank issues disabled; contact links for private security reports and Discussions). Create the labels `bug` and `enhancement` if missing. | Settings → General → Features | On |
| Discussions | **On** (owner decision 6). Keep the default "Ideas" and "Q&A" categories; `.github/DISCUSSION_TEMPLATE/ideas.yml` applies to Ideas. README, CONTRIBUTING, BETA and the issue chooser already link to `/discussions`, so enable it **before** going public or those links 404. | Settings → General → Features | **On** (M23, 2026-11-09) |
| Pull requests | **Collaborators only** (owner decision 5): GitHub's pull-request access setting (February 2026) lets everyone see pull requests but only users with write access create them. It matches CONTRIBUTING.md and does not affect forking. Verify after enabling that a non-collaborator account cannot open a PR. | Settings → General → Pull requests | **Set** (M23); verify after public |
| Wiki, Projects | Off unless used. | Features | Both off (Projects off in M23) |
| Private vulnerability reporting | **On**: SECURITY.md and the issue-form contact link point to `/security/advisories/new`. | Settings → Code security | Manual, required |
| Dependabot alerts and security updates | On. Version updates optional (would open PRs from Dependabot, which is fine under the contribution policy). | Settings → Code security | Manual |
| Secret scanning and push protection | On (free for public repositories). | Settings → Code security | Manual |
| Code scanning | Optional: CodeQL default setup for Go and JavaScript/TypeScript. | Settings → Code security | Decision |
| Branch protection / ruleset for `master` | Require a pull request, require CircleCI status checks (`ci/circleci: linux`, `windows-unit`, `macos-unit`), block force pushes and deletion, require linear history only if you stop using merge commits (current history uses merge commits). | Settings → Rules | Manual |
| Tag protection | Ruleset restricting creation of `v*` tags to the maintainer, since a tag triggers a release. | Settings → Rules | Manual |
| Actions | Default `GITHUB_TOKEN` permissions: read-only; do not allow Actions to create or approve pull requests; require approval for workflows from first-time/outside contributors. The only workflow is a manual fallback. | Settings → Actions → General | Manual |
| Collaborators | Least privilege; review who has write/admin access before going public. | Settings → Collaborators | Manual |
| Releases | Delete the leftover **draft** `Hclaro 0.1.0-alpha.5` (a published release with the same tag exists). Decide whether alpha pre-releases stay visible. | Releases page | Draft deleted (M23); alpha visibility open |
| Social preview, description, topics | Description such as "Local-first web UI for Terraform and OpenTofu"; topics `terraform`, `opentofu`, `infrastructure-as-code`, `local-first`; social preview from `docs/design/assets/hclaro-icon-512.png` or a screenshot. | Settings → General | Manual |
| License detection | Automatic once `LICENSE` is on the default branch. | — | After merge |

## CircleCI (project settings)

| Setting | Recommendation |
| --- | --- |
| Build forked pull requests | Optional. If on, keep **"Pass secrets to builds from forked pull requests" off**. Fork builds run untrusted code on CircleCI resources. |
| Context `hclaro-release` | Restrict to the release job: add an expression restriction such as `pipeline.git.tag starts-with "v"` and/or a security group containing only the maintainer. |
| `GITHUB_RELEASE_TOKEN` | Fine-grained personal access token, this repository only, **Contents: read and write**, with an expiry. Rotate on any doubt. |
| Project environment variables | None needed; keep it that way so fork builds have nothing to read. |
| Cache | Release builds no longer restore caches (changed in M22). |

## Not enforceable by files

Everything in the tables above, plus: who may push tags, whether PRs are visible, whether Discussions exist, vulnerability-reporting availability and repository visibility. The files in this repository (CONTRIBUTING.md, SECURITY.md, issue forms) only describe these policies.

## Code of conduct

Deferred (owner decision 9) until a public contact channel exists. When it does, adopt Contributor Covenant 2.1 with that contact and enable it in Settings → Community standards.

## History synchronization (after the M22 local rewrite)

- A ruleset on `master` that blocks force pushes must be relaxed briefly for the three `--force-with-lease` pushes in [M22-history-rewrite.md](M22-history-rewrite.md), then restored.
- Pull-request references of PR #36 and #37 keep the original commits (with the design mockup) on GitHub; repository owners cannot delete them. Ask GitHub Support to remove them and cached views, or publish from a new repository, **before** changing visibility.
- After the force-push, CircleCI builds the new `master`; older pipelines still list the original commit hashes, which is harmless (no source is stored in CircleCI caches or artifacts beyond screenshots).
