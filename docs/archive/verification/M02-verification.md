# M02 implementation and verification

**Status: M2 plan visualizer implemented; local acceptance passed on the development machine (2026-11-05), CI passed on Linux, macOS and Windows.** No real infrastructure, cloud accounts or remote backends were used. Automated tests use synthetic fixtures through a Go test helper process, the in-process `engine.Fake`, or the synthetic CLI in `scripts/fake-engine`. A follow-up ran real Terraform 1.5.5 against a disposable local project with no providers to download ([real engine run](#real-engine-run)); nothing was applied.

Design and trade-offs are documented in [architecture](../../architecture.md#plans-and-operations) and [security](../../../SECURITY.md).

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
| CI (PR #2) | Passed on ubuntu-24.04, macos-latest and windows-latest (runs 41986644861, 41986651371). The first Windows run failed because the checkout converted golden fixtures to CRLF; `.gitattributes` now keeps `fixtures/**` LF. |
| Live browser run (one-off Playwright script against the live demo) | A real browser opened Plans, confirmed the preflight, streamed the log, rendered the review, then started and cancelled a second plan. The server accepted the browser's POSTs, which proves the browser sent the exact Origin and CSRF headers; no page errors, no external requests, no canary. This script needs a TCP listener and is not part of `make browser-test`. |

## Real engine run

Terraform 1.5.5 (darwin/amd64, the only engine installed; OpenTofu was not downloaded) planned a disposable project in the session scratch directory through `bin/hclaro` and its HTTP API. The project used only the built-in `terraform_data` resource and `terraform_remote_state` data source with the default local backend, so `init` downloaded nothing and no provider or network was involved. The recorded state was a hand-written synthetic file; nothing was applied, imported or unlocked.

The configuration covered update, no-op, `count` creates, a sensitive variable, both replacement orders (`triggers_replace`, with and without `create_before_destroy`), a `moved` block, a deleted resource, a module with `for_each`, a deferred data read, and sensitive and partly unknown outputs. The test secret was planted in the sensitive variable's var file, the recorded state (sensitive, deleted and output values) and the remote-state file.

| Run | Result |
| --- | --- |
| Plan with `prod.tfvars` | Succeeded in about 0.2 s. 13 changes classified as expected: 5 create, 2 update, 2 replace (`delete,create` and `create,delete`), 1 delete (`delete_because_no_resource_config`), 1 read (`read_because_dependency_pending`), 2 no-op including the move (`previousAddress` kept). Sensitive input update listed with the `sensitive` marker; forces-replacement path marked. Canary 0 in the SSE stream, plan, all 13 change details and the server log. Temp directory `0700` while planning, empty afterwards. |
| Failing plan (`file()` on a var-file path) | Status `plan_failed`, exit 1. Terraform's diagnostic echoed the var-file value; it arrived as `no file exists at "[redacted]"` with the rest of the message intact. |
| State locked by another Terraform process | Status `state_locked` from the structured diagnostic, exit 1, lock details shown, no unlock attempted. |
| Cancel during a 4,013-change plan | Terraform logged "Interrupt received" and "Stopping operation...", status `cancelled` 0.6 s after the request, no engine process left, lock file released, temp directory empty, no plan kept. |
| Context fingerprint | `contextChanged` false after the plan, true after editing `main.tf`, a module file or the selected var file, false again after reverting. |
| 4,013-change plan to completion | 2.6 s. Plan detail 1.06 MB (113 KB gzip), served in about 11 ms; server memory about 30 MB; log not truncated; canary 0. |

Defects found by this run and fixed (with regression tests):

- A project without a `backend` block (default local backend) has no backend metadata after `init`, so planning was disabled as "unverified backend". Initialized projects whose root files declare no `backend` or `cloud` block are now treated as local; any declaration keeps the backend unknown.
- A list output of new IDs is marked unknown per element by Terraform 1.5 and was shown as `[null, null]`; it now shows "known after apply" per element.
- Terraform's interrupt message starts with a newline, so the one-line log showed only "…"; leading and trailing blank lines are now trimmed.

The run needs a TCP listener and a real engine, so it is not part of CI. To repeat it: create a disposable directory with only built-in resources and a synthetic `terraform.tfstate`, run `terraform init` there yourself, then start `bin/hclaro -path <dir>` and plan from the UI or API.

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

1. **Engine coverage.** Only Terraform 1.5.5 with the local backend and built-in resources has run for real, on macOS. OpenTofu, newer Terraform releases, real providers (including credential and network failures) and remote backends are not certified. Plan JSON fields beyond the documented common subset (for example `deferred_changes`) produce warnings, not rendering.
2. **Windows cancellation** now sends `CTRL_BREAK` to the engine's own process group; CI covers it with the helper process on `windows-latest`. It has not been run against a real engine on Windows. If Hclaro has no console (for example, started detached), the engine is killed immediately and a lock may remain.
3. **Diagnostics are partly heuristic.** Known inputs (`TF_VAR_*`, loaded variable files) are now replaced. Other secrets in free text, such as values a provider API returns inside an error, are caught only by the line heuristics. Redaction of known values also hides non-secret inputs (for example a region) in error messages.
4. **UI reconnection.** The SSE client resumes from the last sequence number; server-side replay is tested, but a dropped connection mid-plan was not simulated in a browser.
5. **Context fingerprint** now covers configuration, variable and lock files, the data directory (backend metadata, workspace, modules) and planning environment variables. It does not cover provider plugin binaries beyond lock-file checksums, or remote state. The environment cannot change while Hclaro runs, so that part matters only once fingerprints are compared across launches.
6. **Large plans.** A 4,013-change plan was measured (above). Larger plans, up to the 100,000-change limit, were not measured; the plan detail response sends all change summaries in one payload.
7. **Playwright 1.58 / macOS 13** and the dev-only minimatch advisory carry over from M1.
