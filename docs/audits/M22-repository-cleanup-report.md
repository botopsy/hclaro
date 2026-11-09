# M22 repository cleanup report

Branch `m22-repository-cleanup`, based on `master` at `8c8de78`. Changes are in the working tree only: nothing was committed, pushed, tagged, released or changed on GitHub or CircleCI.

## Files deleted, moved and added

| Change | Files | Reason |
| --- | --- | --- |
| Deleted | `scripts/build-offline-preview.mjs`, `scripts/offline-render-test.mjs` | Broken (import `esbuild`, gone since Vite 8) and unreferenced, or referenced only as a legacy fallback that writes into the release bundle. See [file inventory](M22-file-inventory.md). |
| Moved | `docs/M01…M21-verification.md` → `docs/archive/verification/` (19 files) | Milestone evidence kept as history, out of the public entry points. All links updated. |
| Deleted (second pass) | `docs/design/reference/Cursor.dc.html`, `docs/design/reference/README.md` | Owner decision 2. Nothing reads them; references now point to `docs/design/BRAND.md`. Still in Git history. |
| Deleted (second pass) | `CODEX_START_HERE.md` | Owner decision 7: agent bootstrap prompt. Identical to `master`; recoverable from history. |
| Untracked, kept locally (second pass) | `tasks/M01–M08`, `AGENTS.md`; `CLAUDE.md` and `tasks/M09–M22` were never tracked | Owner decision 7. `git rm --cached` plus `.gitignore` entries (`/tasks/`, `/AGENTS.md`, `/CLAUDE.md`): the files stay on disk and the local workflow is unchanged. CONTRIBUTING.md now states the development rules itself; CircleCI's docs-only skip rule no longer names `tasks/`. |
| Added (second pass) | `TRADEMARKS.md` | Owner decision 8: minimal name and logo notice; explicitly keeps all Apache-2.0 rights. Linked from README. |
| Edited (second pass) | README, CONTRIBUTING, BETA, issue chooser | Discussions links for questions and ideas (owner decision 6); CONTRIBUTING says PRs are limited to collaborators (decision 5). |
| Added | `LICENSE` | Official Apache License 2.0 text, downloaded from apache.org; MD5 `3b83ef96387f14655fc854ddc3c6bd57` (the canonical file) and identical terms to the copy shipped by `class-variance-authority`. |
| Added | `docs/GETTING_STARTED.md`, `docs/BETA.md`, `docs/RELEASING.md` (draft) | Onboarding, beta expectations and privacy cautions, release procedure. |
| Added | `.github/ISSUE_TEMPLATE/{bug_report,feature_request,config}.yml`, `.github/DISCUSSION_TEMPLATE/ideas.yml` | Issue forms that require a redaction confirmation; private security contact link. |
| Added | `docs/audits/M22-*.md` (5) | Audit deliverables. |
| Edited | `README.md` | New introduction (capabilities, beta status, screenshot, install, quick start, license, copyright, feedback); removed the deleted script's paragraph; localStorage note and release archive contents corrected. |
| Edited | `CONTRIBUTING.md` | Feedback via Issues; no external PRs during the beta, explicitly not a license restriction; development rules for forks. |
| Edited | `SECURITY.md` | "Reporting a vulnerability" section (private reporting); old "Reporting" section with "no private address" removed; design content kept under "Security design". |
| Edited | `CHANGELOG.md` | M22 entry under Unreleased. Existing release history (0.1.0-alpha.1 to alpha.6, matching the `v0.1.0-alpha.2`…`alpha.6` tags) unchanged. |
| Edited | `.gitignore` | Terraform/OpenTofu plans (`*.tfplan`, `*.tfplan.json`), variable files (`*.tfvars`, `*.tfvars.json`, except under `fixtures/`), crash logs, override files, CLI config (`.terraformrc`, `terraform.rc`), `.env.*` (except `.env.example`), keys/certificates, `.aws/`, `.netrc`, `/release/`, `coverage/`, editor/OS files. `.terraform.lock.hcl` is not ignored. No tracked file became ignored (`git ls-files -ci --exclude-standard` is empty). |
| Edited | `.circleci/config.yml`, `.github/workflows/ci.yml` | `LICENSE` in every release archive; release build does not restore caches; Go 1.27.3 with verified SHA-256 sums (third pass). `circleci config validate` passes. |
| History rewrite (third pass) | 7 local commits from `4f920f6` on | Owner-approved removal of the mockup and its README from all local branches; see [M22-history-rewrite.md](M22-history-rewrite.md). |
| Edited | `.github/workflows/ci.yml` | Name only: "CI (manual fallback)". |
| Edited | `docs/architecture.md`, archived verification docs | Link paths and the deleted script's mention. |

Dependencies: **none changed** (`go.mod`, `web/package.json`, `web/package-lock.json` untouched). Product code: **unchanged** (no file under `cmd/`, `internal/`, `web/src/` modified; `internal/ui/dist` is byte-identical to a fresh build).

## Checks run

Environment: macOS 13 (amd64), Go 1.27.3, Node 24.2, Playwright Chromium. All checks use synthetic fixtures and fake engines.

| Command | Result |
| --- | --- |
| `make lint` (`go vet`, `tsc -b`, ESLint `--max-warnings 0`) | exit 0 |
| `make test` (`go test -race` on 21 packages; Vitest) | exit 0; Vitest 160/160 in 18 files (same as before M22) |
| `make build` then `git diff --exit-code -- internal/ui/dist` | exit 0; embedded bundle unchanged |
| `make browser-test` | exit 0; includes the M21 navigation checks (all 14 sidebar entries, switcher, compact mode, drawer) and every M01–M17 flow: explorer, plans, replace, apply gating, import, Git, discovery, cost, 1,000-resource search, no external requests. Regenerated screenshots were reverted (timestamp-only differences). |
| `make cross VERSION=0.1.0-beta.0-dryrun` | exit 0; 5 targets; `dist/hclaro-darwin-amd64 --version` prints `hclaro 0.1.0-beta.0-dryrun` |
| Reproducibility | A second `linux/amd64` build with the same flags has the same SHA-256 as the `make cross` output. |
| CLI smoke | `--help` exit 0; unknown flag exit 2; empty folder exits with "no Terraform/OpenTofu project found". With the fake engine on the synthetic project: listens on `127.0.0.1` only, UI shell 200, API without token 401, foreign `Host` 403. |
| `circleci config validate` | valid; issue/discussion templates and workflows parse as YAML |
| Markdown links | 37 files, every relative link and `#anchor` resolves (script in the session, not committed) |
| `npm audit --omit=dev` | 0 vulnerabilities; full audit: 10 moderate, dev-only (`minimatch` via ESLint) |
| Secret scan | Rule-based scan of tree and history: synthetic test values only. See [security report](M22-security-report.md). |
| Gitleaks v8.21.2 (`git --log-opts=--all`, `dir`) | exit 1 with 16 findings in history and the same 16 in tracked files: all synthetic test values (see security report). |
| govulncheck v1.1.4 (`./cmd/... ./internal/... ./scripts/...`) | exit 0, "No vulnerabilities found." (Go 1.27.3). |
| Second pass, repeated | `make lint`, `make test` (160/160 Vitest, 21 Go packages with `-race`), `make build` with the bundle unchanged, `make browser-test`, `circleci config validate`, Markdown links (36 files, 0 broken), rule-based secret scan (same synthetic values), CLI smoke on `bin/hclaro` (shell 200, API with token 200, without token 401, foreign Host 403), and a local copy of the CircleCI packaging step: five archives, each with `LICENSE`, `README.md`, `SECURITY.md`, `CHANGELOG.md` and `THIRD-PARTY-NOTICES.txt`; checksums verify; the extracted macOS binary prints its version. All passed. |
| After the history rewrite (third pass) | Repeated: `make lint`, `make test` (160/160 Vitest, 21 Go packages), `make build` (bundle unchanged), `make browser-test`, `make cross` (5 targets), release packaging simulation (5 archives with `LICENSE`, checksums OK, binary runs), CLI smoke (200/200/401/403), Markdown links (36 files, 0 broken), `circleci config validate`, Gitleaks (16 synthetic, unchanged), govulncheck (none). All passed. |
| Not run | CircleCI release workflow (would publish). |

No pre-existing failures were observed.

## Decision log

| Decision | Choice | Why |
| --- | --- | --- |
| Archive vs delete milestone notes | Archive verification docs; untrack `tasks/` and agent files but keep them locally | Evidence is valuable for users; task specs and agent prompts are internal (owner decision 7) and the local workflow must keep working. |
| NOTICE file | Not created | No bundled dependency ships a NOTICE; third-party license texts are already in `THIRD-PARTY-NOTICES.txt`. |
| File license headers | Not added | No existing convention; a root `LICENSE` is sufficient for Apache-2.0. |
| Copyright line | `Copyright 2026 BotOpsy` / `Licensed under the Apache License, Version 2.0` (fourth pass) | The owner confirmed hclaro is independently developed and chose BotOpsy, an unregistered publishing name and pseudonym, as the public publisher. The earlier personal-name line and its TODO caveat were replaced. BotOpsy is not a separate legal entity, so this is attribution under a pen name, not a copyright transfer. Git authorship, dates and signatures were not changed and history was not rewritten for this. |
| CODE_OF_CONDUCT.md | Deferred | Owner decision 9: no public contact channel yet. |
| TRADEMARKS.md | Added, minimal | Owner decision 8. States the marks are unregistered and that Apache-2.0 rights are unaffected. |
| External PR policy | No external PRs during the beta; GitHub "Collaborators only" recommended | Owner decisions 4 and 5. A contribution policy, not a license restriction. |
| Version | Not bumped | Release timing is the maintainer's call; `docs/RELEASING.md` recommends `v0.1.0-beta.1`. |
| Dev-dependency audit findings | Not fixed | Requires forced major upgrades of lint tooling; not shipped. |

## Unresolved risks

- Ownership: confirmed by the owner as an independent project published as BotOpsy. Residual, owner-only check: agreements outside the repository (not assessed here; not legal advice).
- The design mockup is removed from the local history (approved rewrite, [record](M22-history-rewrite.md), completed and verified); GitHub branches were synchronized on 2026-11-09 ([M22.1](M22.1-remote-history-remediation.md)); the closed-PR references #36/#37 keep the original commits reachable (accepted risk).

- Older manual screenshots show the pre-M19 brand.
- Real-engine coverage is limited (see `docs/BETA.md`).

Future hosted or team services, if any, may have separate terms; code already released under Apache-2.0 stays available under that license for everyone who received it.
