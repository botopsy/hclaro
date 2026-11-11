# M24.20 GitHub settings checklist

Repository `botopsy/hclaro`. Audited on **2026-11-11** with read-only `gh api` calls as the admin `vidojeg`, plus logged-out `curl` requests for public reachability. The audit itself changed nothing. Afterwards, on 2026-11-11, the owner approved four changes, which were applied and read back through the API (C3, C6 and the two settings marked *Applied* or *Enabled* below). The settings were applied with the owner's approval in M25 on 2026-11-09 ([M22-github-settings.md](M22-github-settings.md)). This checklist verifies them again.

**Launch rule:** a public release (tag push and publishing the draft) is blocked while any **critical** row is FAIL or NOT_VERIFIED, except rows marked non-blocking. Re-run the commands in the evidence column before each release and update the date.

## Critical controls

| # | Control | Evidence (2026-11-11) | Owner action | Result |
| --- | --- | --- | --- | --- |
| C1 | Visibility matches docs | `gh api repos/botopsy/hclaro` → `visibility: public`, license `Apache-2.0`. Docs describe a public repository; no "private repository" or collaborator-only wording found. | None | PASS |
| C2 | `master` ruleset | Ruleset `master` (id 26890116), `enforcement: active`, `bypass_actors: []`, target `~DEFAULT_BRANCH`. Rules: `deletion`, `non_fast_forward`, `pull_request` (0 approvals), `required_status_checks` `changes`, `linux`, `windows`, `macos` (GitHub Actions app 15368, not strict). | Decide whether to require 1 approval once there is a second reviewer. With one maintainer, 0 is deliberate. | PASS |
| C3 | `v*` tag rulesets | Ruleset `release tags` (id 26890118), `active`, no bypass, `refs/tags/v*`: `deletion`, `non_fast_forward`, `update`. **Applied 2026-11-11 (owner-approved):** new ruleset `release tag creation` (id 27020723), `active`, `refs/tags/v*`, rule `creation`, bypass only for the Repository admin role. So only admins can push a `v*` tag, which starts `release.yml`. It is a separate ruleset so that the admin bypass does not also allow deleting or moving tags. | None | PASS |
| C4 | Private vulnerability reporting | `GET .../private-vulnerability-reporting` → `{"enabled":true}`. Logged out, `/security/policy` returns 200, and `/security/advisories/new` returns 200 after a redirect to sign-in (a GitHub account is needed to report). | None | PASS |
| C5 | Reporting fallback is private | SECURITY.md fallback changed in this task. Before: "open a public issue". That did not work, because blank issues are disabled and the bug form requires steps to reproduce, which invites details. Now: a Q&A discussion that contains only a request for a private contact. The request is public, but no details are sent. There is still no private email address. | **Decision:** publish a private security email (would also unblock the code of conduct, M22 row 10). | PASS (note) |
| C6 | Secret scanning + push protection | `security_and_analysis`: `secret_scanning: enabled`, `secret_scanning_push_protection: enabled`. Alerts: 1, resolved `used_in_tests` (synthetic `glpat-` value). Non-provider patterns: requested 2026-11-11 (owner-approved), but the API accepted the request and still reads `disabled`, so it is **not available** on the current plan. Validity checks: disabled. | None | PASS |
| C7 | Actions token least privilege | Default `GITHUB_TOKEN`: `read`, `can_approve_pull_request_reviews: false`. `ci.yml` and `release.yml` declare `contents: read` at the top level. Only the `publish` job gets `contents: write`, `id-token: write` and `attestations: write`, and it runs only on `push` after `validate`, `build` and `smoke`. All third-party actions are pinned by commit SHA. | None | PASS |
| C8 | No long-lived release secrets | Repository Actions secrets: 0. Environments: 0. Deploy keys: 0. Webhooks: 0. CircleCI retired (M25.14), `GITHUB_RELEASE_TOKEN` removed. | None | PASS |
| C9 | Outside contributor workflows | `fork-pr-contributor-approval` → `all_external_contributors`. `pull_request_creation_policy: collaborators_only`. | Test once with a non-collaborator account that a PR cannot be opened (M22 row 12). | NOT_VERIFIED (non-blocking) |
| C10 | Collaborators | `vidojeg` (admin), `botopsy-admin` (admin). No other users. **Owner attestation, 2026-11-11:** the repository owner confirmed manually that they own and control both accounts and that both have MFA enabled. This was not verified through the GitHub API, which does not show it for personal accounts. | Confirm again if collaborators change. | PASS (owner attestation) |
| C11 | Public release assets | Logged out: the repository, `/releases`, `/releases/tag/v0.1.0-beta.3`, `/discussions`, `/issues/new/choose` and all 7 `v0.1.0-beta.3` assets (5 archives, checksums, SBOM) return 200. Every `github.com/botopsy/...` link in tracked Markdown/YAML returns 200. `v0.1.0-beta.1` is still a draft (built while private, never reused). | None | PASS |

**Status 2026-11-11: no critical row blocks a release.** C10 passes on the owner's attestation. The only remaining NOT_VERIFIED row is C9, which is non-blocking.

## Other settings (not launch-blocking)

| Setting | Evidence | Result |
| --- | --- | --- |
| Dependabot alerts | `vulnerability-alerts` → 204 (on) | PASS |
| Dependabot security-update PRs | **Enabled 2026-11-11** (owner-approved): `automated-security-fixes` → `enabled: true`. The PRs go through the `master` ruleset and CI like any other PR. | PASS |
| Code scanning | `code-scanning/default-setup` → `not-configured` | Optional |
| Allowed actions | **Applied 2026-11-11** (owner-approved): `sha_pinning_required: true` (`allowed_actions: all`). Every `uses:` in `.github/` is a 40-character SHA or the local `./.github/actions/verify`. | PASS |
| Features | Issues and Discussions on; Wiki and Projects off; forking allowed | PASS |

## Private vs public differences

These are the differences seen in M22/M23 while the repository was private (Free plan). They matter only if the repository is ever made private again.

- Rulesets returned 403. With the rulesets gone, `master` and `v*` tags would lose force-push and deletion protection.
- Private vulnerability reporting and secret scanning/push protection were unavailable (404/null). SECURITY.md would then point to a form that does not exist.
- Logged-out users could not reach release assets, README links or `/security/advisories/new`, so the install instructions in README and GETTING_STARTED would fail.
- Actions minutes are limited on private repositories, so the 3-OS CI matrix and the release smoke jobs would use up the quota.

## Commands used

```sh
R=botopsy/hclaro
gh api repos/$R --jq '{visibility,security_and_analysis,pull_request_creation_policy}'
gh api repos/$R/rulesets; gh api repos/$R/rulesets/<id>
gh api repos/$R/private-vulnerability-reporting
gh api repos/$R/actions/permissions/workflow
gh api repos/$R/actions/permissions/fork-pr-contributor-approval
gh api repos/$R/collaborators --jq '.[]|{login,role_name}'
gh api repos/$R/actions/secrets; gh api repos/$R/environments; gh api repos/$R/keys; gh api repos/$R/hooks
gh api repos/$R/secret-scanning/alerts
gh api repos/$R/actions/permissions; gh api repos/$R/automated-security-fixes
curl -s -o /dev/null -w '%{http_code}' -L <url>   # logged out
```
