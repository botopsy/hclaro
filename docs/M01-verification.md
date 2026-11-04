# M01 implementation and verification

**Status: M1 local acceptance passed on the development machine (2026-11-04).** The embedded UI is the production React/Vite/Tailwind/TanStack build. Remaining gates are listed under [Open items](#open-items); the main ones are a first remote CI run and real engine/backend certification in an approved disposable environment.

No real infrastructure, cloud accounts, remote state backends, init, plan, refresh, apply, destroy, import, replace, or state mutations were used. Every engine execution used a mock adapter, a Go test executable, or the synthetic CLI in `scripts/fake-engine`.

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.2 (module minimum `go 1.26.0`), Node 24.2.0, npm 11.3.0, Playwright Chrome Headless Shell 145.

| Check | Result |
| --- | --- |
| `npm ci --prefix web` | Clean install from the committed `web/package-lock.json`. |
| `make test` | Passed. Go race tests for CLI, engine, discovery, parsing/redaction, HTTP security and embedded assets; Vitest 6/6. |
| `make lint` | Passed: `go vet`, `tsc -b`, ESLint with zero warnings. `gofmt -l` clean. |
| `make build` | Passed: TypeScript + Vite production bundle (`assets/index-*.js` 323 kB, 100 kB gzip; `index-*.css` 29 kB, after the dependency updates below), third-party notices, embedded Go binary. |
| Bundle content | Only `/`-relative asset references (enforced by `internal/ui` test). The only URL strings in the JS are the SVG/XHTML namespaces and React's error-decoder link; none are fetched. |
| Reproducibility | Two Vite builds produced identical asset hashes, also after a clean `npm ci` with npm 11.3.0; two `make binary` runs produced identical binary bytes with the same toolchain, target and version. |
| `make cross` | Passed: macOS amd64/arm64, Linux amd64/arm64, Windows amd64. Not executed on foreign operating systems. |
| Canary in binaries | `DO_NOT_LEAK_HCLARO_TEST_SECRET` absent from the host binary and all five cross binaries. |
| `--help`, `--version` | All documented flags shown; version `0.1.0-alpha.1`. |
| Live synthetic demo (`make demo` equivalent) | Server bound to `127.0.0.1` on a dynamic port and printed a fragment-credential URL. Index 200; API 200 with the session credential, 401 without, 403 for a forged `Host` or `Origin`, 405 for `POST`. |
| Live canary sweep | All 316 resource detail responses, both list pages and the workspace response fetched over loopback: the canary (318 occurrences in the fixture) appeared 0 times; 633 redaction markers. Server log contained no canary. |
| Ctrl+C | `SIGINT` stopped the server cleanly. |
| `make browser-test` | Passed: rendering, real Go handler redaction, inspector, copy address / copy redacted JSON, quoted `for_each` keys, filters, overview, light/dark themes, tablet layout, 1,000-resource search, no external requests. Screenshots in `docs/screenshots/`. |
| Parser fuzzing / benchmark (previous run) | Fuzzing ~26,500 executions; parse+redact+index of 316 instances ~8.7 ms. Not rerun for this update. |

## Fixes made while closing M01

- `scripts/vendor-notices.mjs` failed because `@radix-ui/react-compose-refs` publishes no license file. It now records the license declared in `package.json` with the source repository.
- `scripts/browser-smoke.mjs` used an ambiguous text locator (the inspector heading and a JSON value both read `node`). It now targets the heading role.
- Go test/vet use explicit package patterns. `./...` previously descended into `web/node_modules`, which can contain third-party Go sources.
- CI and `make install` use `npm ci` only.

## Dependency update (in-major), 2026-11-04

All direct dependencies moved to the newest release within their current major, except `@playwright/test` (held at 1.58.0, see open items) and `lucide-react`/`jsdom` (left for the major-upgrade phase). Notable runtime changes: React 19.3.1, TanStack Query 5.105.3, Tailwind CSS 4.3.4, tailwind-merge 3.7.0, Radix Slot 1.5.0. Re-ran `make test`, `make lint`, `make build` and `make browser-test`: all pass; screenshots unchanged apart from the snapshot timestamp. The JS bundle grew from 289 kB (90 kB gzip) to 322 kB (99 kB gzip), almost entirely from upstream react-dom 19.3 and query-core 5.105.

`npm install` on npm 11.3.0 crashes in peer-set resolution (`Cannot read properties of null (reading 'edgesOut')`); the lockfile was regenerated with npm 11.6.2 via `npx -y npm@11.6.2 install`. `npm ci` with the bundled npm works.

## Major dependency upgrades, 2026-11-04

One commit per step. Each target is the newest release in its major that was at least two weeks old, to limit exposure to freshly published packages.

| Step | Versions | Verification |
| --- | --- | --- |
| Build/test toolchain | Vite 8.3.5, @vitejs/plugin-react 6.1.2, Vitest 5.0.5 | All checks and browser test pass; Vite output deterministic. Dev server re-probed live: bad/`localhost` Host, foreign Origin and cross-site fetch 403; POST 405; `/@fs/` outside the root 403; `..` paths fall back to `index.html`; proxied API still requires the session credential. |
| Lint | ESLint 10.13.0, @eslint/js 10.0.1 | Lint passes; a probe file confirms hooks and TypeScript rules still report violations. |
| Icons | lucide-react 1.61.0 | All 30 imported icon names exist; pixel diff shows only minor icon-path refinements; screenshots refreshed. |
| Test DOM | jsdom 29.1.1, @testing-library/jest-dom 7.0.1 | Vitest/typecheck/lint pass; embedded bundle byte-identical. |
| Held | TypeScript 5.9.3 | TypeScript 7 is not supported by typescript-eslint 8.72 (`typescript <6.1`). |
| Held | @types/node 24.x | Matches the Node 24 LTS runtime. |

Final pass from a clean `npm ci`: uncached Go race tests, Vitest, lint, build (dist identical to the committed assets), `make cross`, binaries free of the canary, live loopback sweep (316/316 details, canary 0, Host/Origin/session/method checks, SIGINT shutdown) and `make browser-test` all pass.

## M01 task acceptance mapping

| Task criterion | Evidence |
| --- | --- |
| 1. Exact instance counts and addresses | Parser/API tests; UI shows 315 managed + 1 data, 7 child modules, indexed and quoted `for_each` addresses. |
| 2. Search/filter about 1,000 instances | Browser test searches a generated 1,000-instance fixture through the real handler. |
| 3. Missing initialization / CLI versus empty | Workspace regression tests and distinct empty/error rendering tests. |
| 4. Browse without mutation, automatic plan or refresh | Fixed read-only adapter methods; live POST returns 405; unknown routes 404. |
| 5. Canary absent from API/UI/logs | Live loopback sweep, browser body/clipboard checks, server log and binaries all clean. |
| 6. Reject invalid Host/Origin/session | Unit tests and live loopback probes. |
| 7. Embedded cross-platform output | Production assets embedded; five targets compile. |
| 8. Documentation, tests and builds complete | All local checks above pass. |

## Open items

1. **Remote CI** has not run. The workflow (Go 1.27.2, Node 24, SHA-pinned v7 actions, embedded-asset drift check on Linux) is unexecuted on Linux/Windows runners.
2. **Native browser auto-open** was not exercised; the demo uses `--no-browser`.
3. **Cross-platform runtime**: Linux and Windows binaries compile but were not run.
4. **Real compatibility**: no real Terraform/OpenTofu/backend certification. The supported version range is tested through synthetic JSON and fake executables only.
5. **Dev-dependency advisories**: `npm audit --omit=dev` is clean. The critical Vitest and high Vite advisories were fixed in the in-major update; the esbuild advisory disappeared with Vite 8. Remaining: one moderate minimatch ReDoS (GHSA-g3hv-75wg-m249, fixed in 11.0.1) that `npm audit` reports as 10 entries through ESLint 10 and typescript-eslint, which both require `minimatch@^10`. It is lint-time only with project-controlled globs; revisit when either toolchain moves to minimatch 11.
6. **Playwright pinned at 1.58.0**: 1.64 dropped macOS 13 browser support, which the development machine runs. CI runners could use a newer version; revisit when the development machine moves to macOS 14+.
7. **Repository license** remains a product-owner decision.
