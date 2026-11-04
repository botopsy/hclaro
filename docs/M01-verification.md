# M01 implementation and verification

**Status: M1 local acceptance passed on the development machine (2026-11-04).** The embedded UI is the production React/Vite/Tailwind/TanStack build. Remaining gates are listed under [Open items](#open-items); the main ones are a first remote CI run and real engine/backend certification in an approved disposable environment.

No real infrastructure, cloud accounts, remote state backends, init, plan, refresh, apply, destroy, import, replace, or state mutations were used. Every engine execution used a mock adapter, a Go test executable, or the synthetic CLI in `scripts/fake-engine`.

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.2, Node 24.2.0, npm 11.3.0, Playwright Chrome Headless Shell 145.

| Check | Result |
| --- | --- |
| `npm ci --prefix web` | Clean install from the committed `web/package-lock.json`. |
| `make test` | Passed. Go race tests for CLI, engine, discovery, parsing/redaction, HTTP security and embedded assets; Vitest 6/6. |
| `make lint` | Passed: `go vet`, `tsc -b`, ESLint with zero warnings. `gofmt -l` clean. |
| `make build` | Passed: TypeScript + Vite production bundle (`assets/index-*.js` 289 kB, 90 kB gzip; `index-*.css` 28 kB), third-party notices, embedded Go binary. |
| Bundle content | Only `/`-relative asset references (enforced by `internal/ui` test). The only URL strings in the JS are the SVG/XHTML namespaces and React's error-decoder link; none are fetched. |
| Reproducibility | Two Vite builds produced identical asset hashes; two `make binary` runs produced identical binary bytes with the same toolchain, target and version. |
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

1. **Remote CI** has not run. The workflow is updated but unexecuted on Linux/Windows runners.
2. **Native browser auto-open** was not exercised; the demo uses `--no-browser`.
3. **Cross-platform runtime**: Linux and Windows binaries compile but were not run.
4. **Real compatibility**: no real Terraform/OpenTofu/backend certification. The supported version range is tested through synthetic JSON and fake executables only.
5. **Dev-dependency advisories**: `npm audit --omit=dev` is clean. The full audit reports 15 advisories in build/test tooling only: critical in Vitest ≤4.1.10 (UI server/mocker file read), high in Vite ≤7.3.6 (dev server path traversal / `server.fs.deny` bypass), low in esbuild's Windows dev server, and moderate via minimatch in the ESLint toolchain. They affect `make dev` and test runs, not the embedded bundle. Patch fixes exist within the current majors (vite 7.3.7, vitest 4.1.12) and are the first item of the dependency-upgrade phase.
6. **Repository license** remains a product-owner decision.
