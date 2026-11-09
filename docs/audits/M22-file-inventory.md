# M22 file inventory and removal plan

Updated after the owner's decisions (second pass). Baseline: branch `m22-repository-cleanup` from `master` at `8c8de78` (M21 merged). 426 tracked files before M22; `.git` 64 MB on disk, of which 226 KiB is packed and about 63 MB loose objects (`git gc` would compact it; not run). Largest blobs in history are old embedded UI bundles (≤ 0.7 MB each). No file over 1 MB has ever been committed.

Stacks (verified): Go 1.26+ module `hclaro` with **no third-party Go modules** (`go.mod` has no `require`); React 19 + TypeScript + Vite 8 + Tailwind 4 in `web/`, built into `internal/ui/dist` and embedded with `go:embed` (`internal/ui`). Build entry points: `Makefile` (`build`, `binary`, `test`, `lint`, `cross`, `demo`, `demo-hub`, `dev`, `browser-test`, `brand-assets`). CI: CircleCI (`.circleci/config.yml`); GitHub Actions `.github/workflows/ci.yml` is a manual (`workflow_dispatch`) fallback.

## Categories

| Path | Tracked | Category | Evidence | Risk | Validation |
| --- | --- | --- | --- | --- | --- |
| `cmd/`, `internal/` (164 files) | yes | KEEP | Product code and tests; `internal/ui/dist` is embedded by `go:embed`. | — | `make test`, `make build` |
| `internal/ui/dist/` | yes | KEEP | Embedded UI; CI checks it equals a fresh build. Includes `THIRD-PARTY-NOTICES.txt` shipped in release archives. | — | `git diff --exit-code -- internal/ui/dist` after `make build` |
| `web/` (70 files) | yes | KEEP | UI source, tests, lockfile. | — | `make lint`, `make test` |
| `fixtures/` (63 files) | yes | KEEP | Synthetic state/plan/graph/drift/AWS/cost fixtures used by Go tests, the fake engines and the browser suite. Contain the synthetic canary `DO_NOT_LEAK_HCLARO_TEST_SECRET` and fake key shapes on purpose. | Deleting breaks tests and redaction regressions. | `make test` |
| `scripts/fake-*`, `scripts/fixture-http`, `scripts/hub-demo`, `scripts/browser-smoke.mjs`, `scripts/dev.mjs`, `scripts/brand-assets.mjs`, `scripts/vendor-notices.mjs` | yes | KEEP | Referenced by `Makefile`, `web/package.json` (`vendor-notices`), CI and tests. | — | `make demo`, `make browser-test` |
| `scripts/fixtures/generate-*.py` | yes | KEEP | Generators for committed fixtures (code-generation inputs). | — | — |
| `scripts/offline-render-test.mjs` | yes | **DELETE (done)** | Not referenced anywhere. Imports `web/node_modules/esbuild/lib/main.js`, which no longer exists since Vite 8 (rolldown): running it fails with `ERR_MODULE_NOT_FOUND`. | None: unreachable and broken. | `node scripts/offline-render-test.mjs` failed before deletion. |
| `scripts/build-offline-preview.mjs` | yes | **DELETE (done)** | M01-era no-network fallback; fails the same way (`esbuild` missing). It writes into `internal/ui/dist`, so a working copy could overwrite the release bundle with a substitute build. Mentions removed from README and `docs/architecture.md`; historical mention in `docs/archive/verification/M01-verification.md` kept. | Low: documented as "do not commit its output". | Failed before deletion; `make build` unaffected. |
| `docs/M01…M21-verification.md` (19 files) | yes | **ARCHIVE (done)** → `docs/archive/verification/` | Milestone evidence, valuable but internal. Relative links inside them and links from README, SECURITY.md, `docs/architecture.md`, CHANGELOG and `docs/design/BRAND.md` updated. | Broken links if missed. | Link check (see cleanup report). |
| `CODEX_START_HERE.md` | yes | **DELETE (done, owner decision 7)** | Bootstrap prompt for an AI coding agent for M1 only; referenced nowhere else. First archived, then removed as an agent-specific file; identical to `master`, so it stays recoverable from history. | None. | `git show master:CODEX_START_HERE.md` |
| `docs/PRD.md`, `docs/PRD-M05-M08-ADDENDUM.md`, `docs/architecture.md` | yes | KEEP | Product spec and architecture; used by the local workflow and CONTRIBUTING.md. The PRDs mention a coding agent as an audience and name `tasks/` files in plain text (no links): ordinary development context, not confidential. | — | — |
| `docs/design/` (BRAND.md, assets, screenshots) | yes | KEEP | Brand documentation and generated assets (`make brand-assets`). | — | — |
| `docs/design/reference/Cursor.dc.html`, `docs/design/reference/README.md` | yes | **DELETE (done, owner decision 2)** | Supplied M19 mockup with unconfirmed redistribution rights, and the README that only describes it. Nothing imports or reads it: `scripts/brand-assets.mjs` has the geometry inline, the app has none of it, and `internal/ui/embed_test.go` asserts it never reaches the bundle. Comments in `web/src/styles.css`, `web/src/brand/HclaroLogo.tsx`, `scripts/brand-assets.mjs` and `docs/design/BRAND.md` now point to BRAND.md. Removed from the local Git history as well (owner-approved rewrite, [record](M22-history-rewrite.md)); GitHub keeps the original until synchronized. | Licensing (history). | `make build` (bundle unchanged), Brand tests |
| `docs/screenshots/` (49 PNG, 10 MB) | yes | KEEP / REVIEW | 27 are regenerated by `make browser-test` or referenced by verification docs; the rest (e.g. `compare-*`, `drift-*`, `graph-*`, `history-*`) are manual M10–M13 captures referenced by verification docs by pattern and show the pre-M19 brand. **REVIEW**: refresh or drop before publicizing. | Stale visuals only. | — |
| `tasks/M01–M08` (tracked), `tasks/M09–M22` (untracked) | mixed | **UNTRACK (done, owner decision 7)** | Internal milestone specs and agent prompts. Removed from the index with `git rm --cached` and ignored (`/tasks/`); all 22 files stay on the maintainer's disk, so the local workflow is unchanged. CI's docs-only skip rule no longer mentions `tasks/`. Other clones lose their copies when this change is pulled. | None locally. | `ls tasks` (22 files) |
| `AGENTS.md` (tracked), `CLAUDE.md` (untracked) | mixed | **UNTRACK (done, owner decision 7)** | Coding-agent instructions. `AGENTS.md` removed from the index and ignored, file kept locally; CONTRIBUTING.md now states the development rules itself. `CLAUDE.md` was never tracked; now ignored, not touched. | None locally. | `ls AGENTS.md CLAUDE.md` |
| `TRADEMARKS.md` | new | ADD (owner decision 8) | Minimal name and logo notice; does not restrict Apache-2.0 rights. | — | — |
| `.github/workflows/ci.yml` | yes | KEEP | Manual fallback, least privilege; renamed from "M1 checks" to "CI (manual fallback)". | — | YAML parsed. |
| `.circleci/config.yml` | yes | KEEP (edited) | Release archives now include `LICENSE`; release build no longer restores caches. | Release job change. | `circleci config validate`. |
| `fixtures/backend/terraform.tfstate` | yes | KEEP | Synthetic backend metadata only (`{"backend":{"type":"local","config":{}}}`), explicitly un-ignored. | — | — |
| `bin/`, `dist/`, `.cache/`, `web/node_modules/`, `web/tsconfig.tsbuildinfo` | no (ignored) | local only | Build output and caches; `.cache/` also holds local demo registries. Not deleted (local user data). | — | — |

## Not changed

- No dependency was added, removed or upgraded; `go.mod`, `web/package.json` and `web/package-lock.json` are unchanged.
- No source file had license headers before M22; none were added (the project convention is a single root `LICENSE`).
- Git history was not rewritten.
