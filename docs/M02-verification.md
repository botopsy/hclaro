# M02 implementation and verification

**Status: M2 plan visualizer implemented; local acceptance passed on the development machine (2026-11-05).** No real infrastructure, cloud accounts, remote backends or real Terraform/OpenTofu executables were used. Every plan in tests and demos came from synthetic fixtures through a Go test helper process, the in-process `engine.Fake`, or the synthetic CLI in `scripts/fake-engine`.

Design and trade-offs are documented in [architecture](architecture.md#plans-and-operations) and [security](../SECURITY.md).

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.2, Node 24.2.0, npm 11.3.0, Playwright 1.58 Chrome Headless Shell 145.

| Check | Result |
| --- | --- |
| `go test -race -count=1` (all project packages) | Passed: plan parser, operations manager, engine planner subprocess, security guard, API, CLI, state, workspace, embedded UI. |
| Golden plan fixtures | `fixtures/plan/*.golden.json` pin the complete redacted review (summary, changes, drift, outputs, warnings, per-change details) for all-actions, collateral, no-changes and errored plans. Regenerate with `go test ./internal/plan -run TestGolden -update` and review the diff. |
| Plan fuzzing | `FuzzParse` 60 s plus earlier 30 s runs (~220,000 executions per 30 s), no failures. The canary never appears unless present in the input. |
| Parser benchmark | ~0.4 ms and 106 KB allocated per parse of the 13-change fixture. |
| `make lint` / `gofmt` | Passed: `go vet`, `tsc -b`, ESLint with zero warnings; gofmt clean. |
| Vitest | 14/14: explorer tests plus plan review (all actions, collateral, no-op toggle, no apply control), action filters, replacement attribute diff, outputs, drift, preflight required before POST with CSRF header, unavailable reason, loading-to-ready enablement, failed plan, prior plan labeled under a failed run, live log streaming and cancel. The plan tests consume the Go parser's golden output. |
| `make build` / `make cross` | Passed; five targets embed the production bundle (JS 357 kB / 109 kB gzip). Canary absent from all binaries. |
| `make browser-test` | Passed: previous checks plus plan review rendered from the real Go operations manager and parser, replacement inspector, preflight dialog (opened and dismissed), operation log replayed over SSE from the real handler, no apply control, no canary, no external requests. Screenshots: `docs/screenshots/plans-dark.png`, `plan-preflight-dark.png`. |
| Engine subprocess (Go test helper as fake CLI) | Exact argv `plan -input=false -json -out=<abs> [-var-file=<name>]`; names with spaces stay one argument (no shell); 3 MiB line truncated to 1 MiB; stderr containing the canary is never returned, only classified (`state_locked`); cancel delivers an interrupt the helper observes before exit; unsafe `TF_CLI_ARGS_plan` (`-destroy`) refused, `-no-color` accepted; relative paths and flag-like var-file names refused. |
| Operations manager | One operation at a time (409/`ErrBusy`); success, cancel, timeout, shutdown, workspace changed, unsafe args, lock, credentials, unreadable plan and show failure paths; temp directory empty after every path; no plan kept after failure or cancel; variable files limited to the root candidate list; fingerprint reproducible and changes with configuration; 10 MB of log output kept within the 100-event/10 KB test bound with drop accounting and bounded replay; workspace loading refuses planning. |
| Security guard | 18 POST cases: exact Origin required, `localhost` and foreign origins refused, `same-site` refused, CSRF token required and matched, form/text content types 415, empty and >16 KiB bodies 413, missing session 401, PUT/DELETE/unknown POST paths and `/plans/{id}/apply` 405. |
| API over HTTP | Acknowledgement required; unknown fields, trailing JSON and bad variable files rejected before the engine runs; full plan flow with SSE (`log`/`status`/`end`), resume from `after`, every change and drift detail retrieved, canary absent from every response; GET routes never invoke the engine; cancel flow; remote-execution workspace refused with a reason. |
| Live loopback demo (`bin/hclaro` + synthetic CLI) | Plan started with curl using Origin + CSRF; POST without Origin or CSRF 403, form body 415, missing acknowledgement 400. Temp directory created `0700` during planning and empty afterwards. All 14 change/drift details, plan, operations, options, session, workspace and resources responses plus the SSE stream: canary 0 (318 in the fixture state). Apply route 405. Slow plan: second plan 409, cancel interrupted the synthetic CLI gracefully ("Interrupt received"), status `cancelled`, no plan, temp removed. Ctrl+C during a plan: exit under 1 s, temp removed, no engine processes left. Server log canary 0. |
| Live browser run (one-off Playwright script against the live demo) | A real browser opened Plans, confirmed the preflight, streamed the log, rendered the review, then started and cancelled a second plan. The server accepted the browser's POSTs, which proves the browser sent the exact Origin and CSRF headers; no page errors, no external requests, no canary. This script needs a TCP listener and is not part of `make browser-test`. |

## Defects found and fixed during verification

- The action filter stayed applied on the drift tab and hid all drift records (caught by Vitest).
- The change table inherited M1 column widths and truncated addresses (caught in screenshots); addresses now wrap.
- A cancelled or failed run shown above an older plan was ambiguous; the older plan is now labeled.
- Plans opened during the startup read stayed disabled until reload, and the error said "unavailable"; options now refetch when the workspace finishes loading, and the API returns `workspace_loading`.

## M02 acceptance mapping

| Criterion | Evidence |
| --- | --- |
| Plan action is explicit with a preflight about providers/backend | Preflight dialog required (Vitest, browser); API requires `acknowledgeBackendAccess: true`; launch never plans. |
| Supported engine CLI only, argv arrays, secure temp plan | Fixed argv via `exec.CommandContext`; helper-process argv test; `0700` dir and `0600` file in OS temp, never the project. |
| Parse JSON with action order, before/after, unknown/sensitive, nested modules, outputs | Golden fixtures and targeted tests: both replacement orders, quoted `for_each` keys in nested modules, unknown and sensitive markers, replace paths, outputs, drift, deposed, moved, import. |
| Bounded, redacted logs over SSE; cancel, lock, credential and timeout errors | Ring buffer bounds and replay; sanitizer tests; cancel, timeout, lock and credential classification tests; live cancel. |
| UI: summary counts, filters, before/after diff, full address, sanitized diagnostics, workspace and engine | Vitest and browser test; screenshots. |
| No apply endpoint | No route or engine method; `POST …/apply` returns 405 in guard, API and live tests; UI has no apply control. |
| Golden fixtures cover create/update/delete/replace/read/no-op and unexpected combinations | `all-actions` (plus forget and `update,delete` as unknown), `collateral`, `no-changes`, `errored`. |
| UI shows all changes including collateral effects | All 13 changes rendered (no-ops behind a toggle with counts visible); collateral fixture: two replacements plus a count-index deletion. |
| Canary never crosses API/UI/log boundary; temporary plan cleaned up | Canary checks in parser, manager, API, Vitest, browser and live sweeps; temp emptiness asserted on every path. |
| Failed or cancelled plan leaves no apply path | No plan stored on failure/cancel (tests); UI states "No plan was kept… nothing to review or apply". |
| Tests and production build pass | See evidence table. |

## Open items

1. **Real engine certification.** `plan -json` and `show -json <plan>` follow documented Terraform and OpenTofu behavior, but no real engine, provider or backend has been run. Plan JSON fields beyond the documented common subset (for example `deferred_changes`) produce warnings, not rendering.
2. **This branch's CI run** on Linux and Windows is pending until it is pushed.
3. **Windows cancellation** kills the engine immediately (no interrupt available), which can leave a state lock. Documented; not exercised on Windows.
4. **Diagnostics are heuristic.** Free-text engine and provider messages are sanitized line by line; an unrecognized secret inside an error message can still be shown.
5. **UI reconnection.** The SSE client resumes from the last sequence number; server-side replay is tested, but a dropped connection mid-plan was not simulated in a browser.
6. **Context fingerprint** covers configuration, variable and lock files, engine, version and workspace. It does not cover environment variables (`TF_VAR_*`), provider plugin binaries or remote state; it exists to warn, and to support apply gating later.
7. **Large plans.** The plan detail response includes all change summaries (up to 100,000) in one payload; the list renders 200 rows per page. Not measured with real large plans.
8. **Playwright 1.58 / macOS 13** and the dev-only minimatch advisory carry over from M1.
