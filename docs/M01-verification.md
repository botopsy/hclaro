# M01 implementation and verification

**Status: implementation delivered with an embedded offline preview; M1 release acceptance remains incomplete.** No real infrastructure, cloud accounts, remote state backends, init, plan, refresh, apply, destroy, import, replace, or state mutations were used during development.

The backend, CLI, explorer source, synthetic fixtures, build tooling, and documentation are implemented. The full requested frontend dependency build and interactive runtime acceptance are blocked by the development sandbox. Do not describe this checkout as a verified production Vite/TanStack/shadcn alpha.

## Evidence collected

Environment: macOS amd64, Go 1.27.2, Node 24.2.0, npm 11.3.0. All engine executions in tests use a mock adapter, a Go test executable, or the explicitly synthetic CLI.

| Check | Result |
| --- | --- |
| `make test-go` (`CGO_ENABLED=1 go test -race ./...`) | Passed: CLI arguments, deterministic engine selection, fake subprocesses/errors/cancellation, discovery, recursive parsing, indexing, redaction, HTTP security, and embedded assets. |
| `go vet ./...` with workspace-local GOCACHE | Passed. |
| `gofmt -l` over Go sources | Clean. |
| Parser fuzzing, 5 seconds / 1 worker | Passed, approximately 26,500 executions. |
| 316-instance fixture | Exact 315 managed + 1 data, 8 modules including root; nested module instances, count and quoted for_each addresses preserved. Every detail response checked for the planted secret. |
| Generated 1,000-instance fixture | Parser count/uniqueness, actual handler search, 50-row page bound, and React server rendering passed. Interactive typing latency remains unverified. |
| Sensitive data | Nested sensitivity maps/lists/whole values, escaped key names, heuristic masking, unknown scalar masking, safe diagnostics, and all resource detail responses covered. The canary is intentionally present only in synthetic fixture/test/helper source. It is absent from the production binary. |
| Host/Origin/session | Missing/bad credentials, wrong schemes, forged hosts/origins, cross-site fetch metadata, request bodies/chunking and excessive queries rejected; no wildcard CORS. Unknown operation routes return 404 and POST is refused. |
| Subprocess policy | Only version/workspace/show methods exist. Inherited output overrides rejected; TF_LOG variables suppressed; secret-bearing stderr discarded. |
| ESLint + React hooks | Passed using existing cached official ESLint/TypeScript-ESLint/hooks libraries. This is supplemental; the manifest's npm-installed lint toolchain is unavailable. |
| Offline frontend compiler | Passed using cached React 19.3.0, Lucide 1.56.0 and esbuild 0.28.2. Explicit local query/button adapters substitute missing packages. Bundled third-party license notices are included. |
| Supplemental real React rendering | Passed with responses from the real Go HTTP handler: resource table, 50-row bound, redacted attribute JSON, empty/error states, HTML escaping and generated 1,000-instance data. Uses the preview query adapter; does not replace Vitest or browser testing. |
| Native binary, `--help`, `--version` | Build and flag execution passed. Embedded static references are checked without Node by a Go test. |
| Identical binary rebuild | Identical bytes observed with the same toolchain, assets, version and build flags. This does not establish a reproducible frontend dependency graph. |
| `make cross` | Passed: macOS amd64/arm64, Linux amd64/arm64, Windows amd64, all with embedded preview assets. These binaries were not run on foreign operating systems. |
| Fixture parser benchmark | Approximately 8.7 ms and 1.47 MB allocated for parsing, redacting and indexing 316 instances on the development machine. This is not a browser or backend-network benchmark. |

## Explicit blockers

1. **npm access:** `npm install --prefix web` fails with `ENOTFOUND registry.npmjs.org`. No dependency lockfile could be generated. Direct versions are pinned; transitive resolution and reproducibility are unresolved. `make lint` reaches TypeScript and fails because `tsc` is unavailable; `make test-web` fails because `vitest` is unavailable; `make frontend`/the production build cannot run. The React/Vite/Tailwind/shadcn/TanStack implementation source is present, but that combination has not been built or tested here.
2. **Loopback permission:** `make demo` reaches startup and fails with “loopback TCP is denied by this environment; Hclaro requires permission to listen on 127.0.0.1”. A valid disposable fixture launch, browser auto-open, and Ctrl+C shutdown through a live TCP server remain unverified. Handler/process cancellation tests pass without a listener.
3. **Browser permission:** Playwright was attempted with installed Chrome, but the browser aborts under the sandbox (`SIGABRT` / permission errors). Interactive search/filter/copy, focus behavior, theme changes, tablet layout, screenshots, and the browser smoke suite remain unverified. No screenshots are claimed.
4. **Real compatibility:** No real engine/provider/backend certification was attempted. The version range is an explicit intended read-only subset, tested through synthetic JSON/fake executables. Remote execution is never enabled for mutation.

## M01 task acceptance mapping

| Task criterion | Evidence / remaining gate |
| --- | --- |
| 1. Exact instance counts and addresses | Parser and API tests pass; 316 instances with root, child, nested, indexed and data resources. |
| 2. Search/filter about 1,000 instances | Actual handler search and bounded rendering pass; browser responsiveness is blocked. |
| 3. Missing initialization / CLI versus empty | Workspace regressions and error/empty rendering pass; missing state and unavailable state have separate diagnostics. |
| 4. Browse without mutation/automatic plan/refresh | API has no engine reference; fixed read-only adapter methods and endpoint/method regressions pass. Live navigation blocked. |
| 5. Canary absent from API/UI/logs | Every fixture detail response and rendered redacted JSON checked; secret-bearing subprocess stderr withheld. Live browser/server logs blocked. |
| 6. Reject invalid Host/Origin/session | Security and API tests pass. |
| 7. Embedded cross-platform output | All five targets compile with preview assets; the production frontend build and OS runtime certification are unresolved. |
| 8. Documentation, tests and builds complete | Go/race/vet, supplemental frontend checks and preview builds pass. Full npm-based checks/build and runtime gates remain blocked as described above. |

## Changed and added files

| Area | Files |
| --- | --- |
| CLI | `cmd/hclaro/main.go`, `internal/cli/cli.go`, CLI tests, `go.mod` |
| Engine and discovery | `internal/engine/engine.go`, `internal/workspace/workspace.go`, fake-process/discovery tests |
| Parsing and security | `internal/state/state.go`, `internal/security/security.go`, parser/fuzz/security tests |
| API and embedding | `internal/api/api.go`, API tests, `internal/ui/embed.go`, embedded-asset test, `internal/ui/dist/*` |
| Frontend | `web/package.json`, TypeScript/Vite/ESLint configuration, entry HTML/favicon, `web/src/App.tsx`, `api.ts`, `types.ts`, JSON viewer, shadcn-style Button, stylesheet and component tests |
| Synthetic fixtures | `fixtures/state.json`, `empty.json`, `embed.go`, isolated project marker and backend metadata |
| Tooling | `Makefile`, `.gitignore`, fake-engine/fixture-HTTP helpers, development server, browser suite, explicit offline-preview/render checks, license-notice generator, CI configuration |
| Documentation | Rewritten `README.md`, architecture notes, this evidence ledger, `SECURITY.md`, `CONTRIBUTING.md`, `CHANGELOG.md` |

The PRD, agent rules and later milestone task files were preserved. The repository's own license decision remains deferred to the product owner. CI is configured but was not executed remotely.

## Safe completion sequence on an unrestricted development machine

```sh
make install
# Review and commit the generated web/package-lock.json; use npm ci thereafter.
make test
make lint
make build
make cross
make demo
```

The demo is synthetic and never selects a real engine. Separately install a Playwright browser and run `make browser-test`. Check both themes and tablet sizing, command-search focus, selection/scroll preservation, copying and redaction; then verify native browser auto-open and Ctrl+C in the disposable fixture. The cross-platform binaries need OS runtime validation. Real backend/provider certification requires a separately approved disposable setup, never the user's infrastructure.
