# M22 security and supply-chain report

Scope: working tree and all reachable Git history (`git log --all`, 101 commits, 466 paths ever added), CI configuration, runtime dependencies and licenses, and local-first behavior. No raw secret values appear in this report. This is an engineering review, **not legal advice**. Publisher: BotOpsy (the author's publishing name; see the checklist, row 1).

## 1. Secrets

**Tooling.** Gitleaks v8.21.2 (default ruleset, built with `go run` from the Go module proxy, verified against the Go checksum database) was run with the owner's approval; results in [1a](#1a-gitleaks-results). A rule-based scanner, run from a temporary location and not added to the repository, applied Gitleaks-style rules to every added line in history and every tracked/untracked file: AWS key IDs and secret assignments, private keys, GitHub/Slack/Google/Stripe/npm/Terraform Cloud/Infracost tokens, JWTs, generic `password|secret|token|api_key = "…"` assignments, session URLs (`#session=<64 hex>`), the maintainer's employer e-mail domain and home-directory paths.

| Rule | Hits | Classification |
| --- | --- | --- |
| AWS access key ID | 3 distinct values (`AKIAIOSFODNN7EXAMPLE` from AWS documentation, `AKIAABCD…`, `AKIAFAKE…`) in fixtures and tests | **Synthetic**, used to test redaction. |
| Private key | 1, `internal/state/state_test.go` | **Synthetic** truncated header (`-----BEGIN RSA PRIVATE KEY-----\nMIIB`) for a redaction test. |
| GitHub token | 2, `internal/state/state_test.go`, `internal/gitctx/gitctx_test.go` | **Synthetic** (`ghp_abcdef…xyz0123456789`, `ghp_1a2B3c…`). |
| JWT | 1, `internal/state/state_test.go` | **Synthetic** (`{"alg":"HS256"}` / `{"sub":"1"}` / `signature`). |
| Generic assignment | 329 hits, 3 distinct values | The canary `DO_NOT_LEAK_HCLARO_TEST_SECRET` (328) and an all-zero test ID. |
| Session URL token | 0 | — |
| Employer e-mail domain | 0 | — |
| Home-directory path | 1 distinct value, `/Users/me/` | Documentation example in code comments and a UI placeholder. |

History contains no `.env`, `*.tfvars`, `*.tfplan`, private keys, archives or executables; the only `*.tfstate` ever added is the synthetic `fixtures/backend/terraform.tfstate`.

**Result of the rule-based scan: no real secret found.**

### 1a. Gitleaks results

| Scan | Command | Scope | Findings |
| --- | --- | --- | --- |
| History | `go run github.com/zricethezav/gitleaks/v8@v8.21.2 git --redact -v --log-opts="--all" --report-format json --report-path .cache/m22/gitleaks-git.json .` | All 63 non-merge commits of every local and remote-tracking branch (the other 38 reachable commits are merges without their own changes) | 16 |
| Working tree | `go run github.com/zricethezav/gitleaks/v8@v8.21.2 dir --redact -v --report-format json --report-path .cache/m22/gitleaks-dir.json .` | Every file on disk, including ignored ones | 32: the same 16 in tracked files, plus 16 in `.cache/m22/history.txt`, a local ignored dump of `git log -p` made for this audit (not part of the repository) |

Every finding was inspected at its file and line. None is a real secret:

| Rule (Gitleaks) | Affected files | Value | Severity | Classification |
| --- | --- | --- | --- | --- |
| `aws-access-token` (11 in history, 11 in tree) | `fixtures/drift/mixed.json`, `fixtures/drift/security-group.json`, `scripts/fixtures/generate-drift.py`, `internal/api/drift_test.go` (2), `internal/drift/drift_test.go`, `internal/operations/drift_test.go`, `internal/compare/compare_test.go`, `internal/gitctx/gitctx_test.go`, `web/src/drift/Drift.test.tsx`, `docs/archive/verification/M10-verification.md` | `AKIAABCD…MNOP` (shortened here): the alphabet in sequence | Informational (false positive) | Synthetic key shape that redaction tests must hide. |
| `github-pat` (2) | `internal/state/state_test.go`, `internal/gitctx/gitctx_test.go` | `ghp_abcd…6789` (the alphabet and digits in sequence) and `ghp_1a2B…7q8R` (shortened here) | Informational (false positive) | Patterned test strings for redaction. |
| `gitlab-pat` (1) | `internal/state/state_test.go` | a `glpat-` string in the same list of redaction cases | Informational (false positive) | Synthetic. |
| `generic-api-key` (2) | `fixtures/aws/ec2-describe-vpcs-1.json`, `internal/projects/tools_test.go` | a pagination `NextToken` that decodes to `{"NextToken": "page-2"}`; `plain-unflagged-secret-7f3a` | Informational (false positive) | Synthetic AWS paging token and a test canary. |

The commits involved (`7dbd433`, `33055c5`, `defb1cb`, `b64b1a4`, `4b24b93`, `8bde2d5`) add those test files. **No credential needs rotation, and no history remediation is needed for secrets.**

Remediation recommendations (not applied; findings are not suppressed):
- Optional: add a `.gitleaks.toml` that allowlists exactly these synthetic values (by value, not by path) so that a future CI secret scan reports only new findings. This is a maintainer decision, since it is a form of suppression.
- Turn on GitHub secret scanning with push protection when the repository becomes public.
- Delete the local `.cache/m22/` folder after the audit; it holds history dumps and scan reports. Personal data note: commit metadata contains the maintainer's personal e-mail address (two spellings of the author name); it becomes public with the repository. Rewriting history to change it is a separate, approval-only operation.

## 2. Git history review (owner request)

All 101 commits (`git log --all -p`, 33 MB of patches and messages) were searched for content that should not become public, in addition to the secret rules.

| Looked for | Found | Assessment |
| --- | --- | --- |
| Employer name or domain, internal hostnames | none (the 34 `.internal` matches are a JavaScript property name in old UI bundles and synthetic `db.internal` test strings) | Ordinary |
| Real AWS account IDs / ARNs | only documentation placeholders (`123456789012`, `111122223333`, `444455556666`, `999999999999`, `000000000000`, `210987654321`) | Synthetic |
| Private network addresses | RFC 1918 example CIDRs in fixtures (`10.0.0.0/16`, …) | Synthetic |
| Home directories or local paths | only `/Users/me/` examples | Ordinary |
| Words like confidential/proprietary | none in a confidential sense | — |
| Commit authors | one person, two spellings of the name, a personal e-mail address; merges committed by `GitHub <noreply@github.com>` | Becomes public; personal data, not confidential |
| Internal planning notes | `tasks/M01–M08`, `AGENTS.md`, `CODEX_START_HERE.md`: milestone specs and coding-agent prompts | **Ordinary development history.** Untracked or removed from the working tree (owner decision 7) but still readable in history. Nothing confidential found in them. |
| Third-party asset | `docs/design/reference/Cursor.dc.html` (the M19 design mockup) and its README, added in commit `4f920f6` (M19) | **Removed from the local history** (owner-approved, [record](M22-history-rewrite.md)). Remote branches synchronized on 2026-11-09 ([M22.1](M22.1-remote-history-remediation.md)); the path is absent on every GitHub branch. Reviewed privately: generated for hclaro (owner: ChatGPT), no secrets (Gitleaks), no third-party notices or copied code, not distributed. Its closed-PR references (#36, #37) remain: **accepted, documented historical risk**. |

No secrets, credentials or internal infrastructure information were found in history.

## 3. CI/CD

**CircleCI (`.circleci/config.yml`)** runs pull requests, `master` and tag releases.

| Check | Finding | Action |
| --- | --- | --- |
| Toolchains | Go and Node downloaded from go.dev/nodejs.org at pinned versions and verified against pinned SHA-256 sums; Docker image pinned by digest; no orbs. | Good. |
| Secrets | One credential, `GITHUB_RELEASE_TOKEN`, in context `hclaro-release`, used only by `release-publish` on tags. | Restrict the context (tag-only expression or security group) in CircleCI settings. |
| Fork pull requests | Builds run repository code from the PR. If "Build forked pull requests" is on, **"Pass secrets to builds from forked pull requests" must be off**. | Manual setting (see GitHub/CI settings doc). |
| Cache poisoning | PR jobs save Go/npm/Playwright caches that the release build used to restore; a poisoned Go build cache could feed a published binary. | **Fixed:** `release-build` no longer restores caches. npm installs are integrity-checked by `npm ci`. |
| License in archives | Release archives shipped README, SECURITY, CHANGELOG and notices but not `LICENSE` (Apache-2.0 §4(a)). | **Fixed:** `LICENSE` added to every archive. |
| Release publishing | Draft release, upload, then publish; tag must exist; pre-release flag from suffix. | Good. |

**GitHub Actions (`.github/workflows/ci.yml`)**: `workflow_dispatch` only, `permissions: contents: read`, actions pinned to commit SHAs, `persist-credentials: false`, no `pull_request_target`, no secrets. Renamed to "CI (manual fallback)". Set the repository's default `GITHUB_TOKEN` permission to read-only.

## 4. Dependencies and licenses

**Go:** standard library only; nothing third-party is compiled into the binary, so the only Go vulnerability exposure is the toolchain's standard library. `govulncheck` v1.1.4 was run with the owner's approval: `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./cmd/... ./internal/... ./scripts/...` → **"No vulnerabilities found."** (exit 0), with Go 1.27.3 and the vulnerability database as of the scan date.

Go version alignment: go.dev lists **1.27.3** as the latest stable release (checked 2026-11-09), the toolchain govulncheck validated. CircleCI (`go-version` and the three pinned SHA-256 sums) and the GitHub Actions fallback now use 1.27.3; the sums from go.dev's release list match the separately published `dl.google.com/go/<file>.sha256` files. Resolved. CI builds with Go 1.27.2; local builds used 1.27.3.

**Runtime JavaScript (bundled into the binary):**

| Package | Version | License | NOTICE file |
| --- | --- | --- | --- |
| react, react-dom, scheduler | 19.3.1 | MIT | no |
| @tanstack/react-query, query-core | 5.105.3 | MIT | no |
| @radix-ui/react-slot, react-compose-refs | 1.5.0 | MIT | no |
| class-variance-authority | 0.7.1 | Apache-2.0 | no |
| clsx | 2.1.1 | MIT | no |
| tailwind-merge | 3.7.0 | MIT | no |
| lucide-react | 1.61.0 | ISC | no |
| @fontsource/jetbrains-mono (Latin 700) | 5.2.8 | OFL-1.1 (font) | no |

All are permissive and compatible with distribution under Apache-2.0. Their license texts, including the full OFL-1.1, are generated into `internal/ui/dist/THIRD-PARTY-NOTICES.txt` by `scripts/vendor-notices.mjs`, which ships inside the binary and in every release archive. No dependency ships a `NOTICE` file, so **no root `NOTICE` is required**; none was created. Apache-2.0 for Hclaro does not change these components' own licenses.

`npm audit --omit=dev`: **0 vulnerabilities**. Full `npm audit`: 10 moderate findings, all through `minimatch` (ReDoS-style) in ESLint/typescript-eslint **development** tooling; not shipped. The suggested fix is a forced major upgrade, so it was not applied (out of scope); revisit with the next lint-tooling update.

**Other assets to review (maintainer decision):**

- `docs/design/reference/Cursor.dc.html`: deleted from the working tree (owner decision 2); still in Git history (section 2).
- Brand assets and logo (`docs/design/assets`, favicons): original to the project per M19; Apache-2.0 grants no trademark rights. A trademark policy is optional.
- Code written with AI assistance: the project's former agent instructions show coding agents produced much of it. The owner has confirmed hclaro is an independent personal project published as BotOpsy (checklist, row 1); contractual questions outside the repository were not assessed.

## 5. Local-first behavior (verified in code)

- The server listens with `net.Listen("tcp4", "127.0.0.1:<port>")` only (`internal/cli/cli.go`), with Host/Origin validation, a per-launch bearer credential in the URL fragment, CSRF for mutating routes and a same-origin CSP (`SECURITY.md`).
- No telemetry or update checks: the Go code contains no outbound HTTP client apart from engine/AWS/Infracost CLIs started on explicit user action; the only hard-coded external URL is a documentation link shown in the cost-estimate disclosure. Child `TF_LOG*` and checkpoint checks are disabled.
- Optional third-party data flows are opt-in and documented: AWS CLI for discovery (on request), Infracost CLI for cost estimates (explicit disclosure and opt-in; `--strict-local` disables it).
- Redaction happens server-side before HTTP responses; history and logs store redacted metadata only. Heuristic limits are documented in `SECURITY.md`.

No remediation needed in this area.

## Verdict for this report

### After the history rewrite

| Check | Result |
| --- | --- |
| Gitleaks `git --log-opts=--all` on the rewritten mirror | 16 findings, identical rules and files as before (all synthetic) |
| Gitleaks `git --log-opts="--branches --tags"` on this repository | 16, identical |
| Gitleaks `dir` on the working tree | 16, identical (this report shortens the synthetic values it quotes, so it adds none) |
| govulncheck v1.1.4, Go 1.27.3 | No vulnerabilities found |

No security finding in the code, configuration, dependencies or history: Gitleaks reports only synthetic test values (16, informational; documented above, not allowlisted) and govulncheck finds no vulnerabilities. Security validation is **complete**. After the remote synchronization (M22.1): Gitleaks over all branches, tags and remote-tracking refs again reports the same 16 synthetic findings; govulncheck reports none. The historical PR references are an accepted, documented risk. GitHub's Dependabot alert #1 is the dev-only `minimatch` finding listed in section 4.
