# M03 implementation and verification

**Status: draft.** Single/bulk replace and saved-plan apply are implemented and pass the automated suite and a live loopback run against the synthetic CLI (2026-11-05). **Apply has not been run against a real Terraform/OpenTofu engine.** Per `AGENTS.md`, that needs explicit approval from the product owner and a disposable test environment. No real infrastructure, cloud accounts or remote backends were used.

Design and trade-offs: [architecture](../../architecture.md#saved-plan-apply), [security](../../../SECURITY.md).

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.2, Node 24.2.0, Playwright 1.58 Chrome Headless Shell 145.

| Check | Result |
| --- | --- |
| `make test` (`go test -race`, Vitest) | Passed: all Go packages; Vitest 20/20. |
| `make lint` / `gofmt` | Passed: `go vet`, `tsc -b`, ESLint with zero warnings; gofmt clean. |
| `make build` / `make cross` | Passed; five targets embed the bundle (JS 371 kB / 113 kB gzip). Canary absent from all binaries. |
| `make browser-test` | Passed: previous checks plus managed-only checkboxes (data sources not selectable), selection across a filter, replace preflight listing the exact quoted `for_each` address, **Review and apply…** on the fixture plan, apply dialog with the server-issued phrase and **Apply plan** disabled until typed, Escape declines. Screenshots: `replace-preflight-dark.png`, `apply-dialog-dark.png`. |
| Engine adapter (Go helper process as fake CLI) | Two addresses (one with `["blue.eu"]` and an escaped quote) arrive as exactly two `-replace=` argv elements after `-var-file`; `-destroy`, data sources, newlines, duplicates and empty addresses refused. Apply argv is exactly `apply -input=false -json <abs path>`; relative paths refused; injected `TF_CLI_ARGS_apply=-auto-approve` refused; "Saved plan is stale" classified as `stale_plan`. |
| Compiled synthetic CLI (`scripts/fake-engine`) through the real `engine.CLI` | Two selected instances → spy records one plan with exactly two `-replace` flags and no `-target`/`-destroy`/`-auto-approve`; one apply with the saved file under the private temp directory; state reloaded; temp directory empty afterwards. |
| Operations manager | Reviewed artifact is the one applied, once; second approval refused; missing, forged, expired, replayed tokens and wrong phrase refused without calling the engine; edited `main.tf` → `context_changed` for approval and apply; workspace switched between approval and run → `workspace_changed`, not applied; tampered saved file → `artifact_changed`, not applied; engine stale → `stale_plan`, outcome `not_applied`; engine failure → `apply_failed`, outcome `uncertain`; interrupted apply → `cancelled`, outcome `uncertain`, state reloaded; newer plan supersedes the older artifact; expiry by age and by timer deletes the file; errored and no-change plans keep no artifact; apply refused while a plan runs; remote execution reports `canApplySavedPlan: false` with a reason. Retained artifact is `0700` dir / `0600` file on macOS/Linux (Windows has no POSIX modes; the user temp directory ACL applies). Canary absent from apply logs (fixture provisioner output and diagnostic detail contain it). |
| Security guard | Approval and apply routes require session, exact Origin, same-origin fetch metadata and CSRF (403 otherwise); malformed plan IDs and other `/plans/{id}/…` POSTs 405. |
| API over HTTP | Full flow: plan → `apply.available` → apply without approval 409 `approval_required` → unknown body field 400 → approval (phrase `apply demo`, digest, command) → wrong phrase 409 → token replay 409 → forged token 409 → engine not called → valid apply 202 → `applied` → plan no longer applicable. Replace: data-source ID, duplicate ID, raw address and flag-like value → 400 `invalid_replace` before the engine runs; two IDs → two exact addresses in the request and command; collateral fixture warns that both requested instances are not replaced while all 4 changes stay listed. |
| Live loopback demo (`bin/hclaro` + synthetic CLI, curl) | Apply POST without CSRF 403. Replace plan for `module.compute.aws_instance.main[10]` and `[11]`: spy shows `plan … -replace=module.compute.aws_instance.main[10] -replace=module.compute.aws_instance.main[11]`; artifact dir `drwx------` while retained; wrong phrase refused; apply succeeded with outcome `applied` and `stateReloaded: true`; spy shows `apply -input=false -json <temp>/plan.tfplan`; second approval refused ("applied once"); temp dir empty after Ctrl+C; canary 0 in all API responses, both SSE streams and the server log. |
| CI (PR #10) | Passed on ubuntu-24.04, macos-latest and windows-latest (runs 42036424634, 42036435850; merge to master 42039156163). The first two pushes failed: POSIX permission checks ran on Windows, and a drawer-width assertion read the width before the resize settled. Both fixed in `bfe0eb7`. |

## M03 acceptance mapping

| Criterion | Evidence |
| --- | --- |
| Only managed instances selectable; never whole modules | UI checkbox only on managed rows (Vitest, browser); server resolves opaque IDs and refuses data sources (manager, API). There is no module selection. |
| Multiple selections → multiple exact `-replace` argv entries, no `-target` | Helper-process argv test, compiled synthetic CLI spy, API request/command checks, live spy. |
| Display all plan changes and warn about downtime/deletion | Full change list unchanged from M2; replace summary counts other changes and calls out unselected deletions/replacements; downtime/data-loss copy in review and apply dialog; requested-but-not-replaced warning. No zero-downtime promise. |
| Apply only the eligible saved plan; no auto-replan | Only the retained artifact can be applied; digest re-verified; artifact deleted after any attempt; stale plan reported, never re-planned. |
| Verify session, workspace, engine, digest, context; reject stale plans and concurrent operations | Session/CSRF guard; fingerprint, version, workspace, age checks at approval and apply; `workspace show` and SHA-256 at run time; busy refusal; engine stale-plan classification. |
| One-time approval bound to the reviewed plan; typed confirmation for delete/replace | 256-bit token per plan, five minutes, consumed on any use; phrase `apply <workspace>` for destructive plans; tests for every refusal path. |
| Stream apply logs; uncertain outcome after cancellation; refresh resources | Apply events translated over the same SSE log; `outcome: uncertain` for failed/interrupted applies; state re-read and explorer queries invalidated. |
| No apply when approval declined, token stale, workspace changed, or plan failed | Declined dialog sends no apply (Vitest); expired/replayed token, workspace change, context change tests; failed plans keep no plan or artifact (M2 tests still pass). |
| Entire blast radius visible; no promise of zero downtime | As above. |
| Remote execution without verified saved-plan contract is read-only and explained | `canApplySavedPlan: false` with `applyReason` (workspace test); plans there are not planned at all, as in M2. |

## Open items

1. **No real-engine apply.** Saved-plan apply, `-replace`, the stale-plan message and apply JSON events are exercised only with synthetic output modeled on Terraform 1.9's documented machine-readable UI. A disposable-environment run (for example Terraform 1.5.5 with `terraform_data` and the local backend, as in M2) needs product-owner approval before it is done.
2. **OpenTofu** apply output and stale-plan wording are not verified; classification relies on substrings that may differ.
3. **Killed process.** If Hclaro is killed rather than stopped, a retained saved plan (which can contain secrets) stays in the OS temp directory until the next Hclaro start deletes it. Another instance may share the temp directory, so the startup sweep removes only `hclaro-plan-*` directories older than plan expiry plus the apply timeout (three hours); a younger leftover waits for a later start or the OS. Symlinks are never followed (`TestSweepStale*`).
4. **State not observable by Hclaro.** Another process changing state between plan and apply is caught only by the engine's own stale-plan check, not by Hclaro's fingerprint.
5. **Interrupted apply** in a real engine (partial resources, leftover lock) and Windows `CTRL_BREAK` during apply are untested beyond the helper process.
6. ~~**Approval UX.**~~ Resolved: after a failed apply request the dialog keeps the error, requests a new approval and asks for the phrase again; if the plan can no longer be applied, that refusal's reason is shown instead. Only the response to the latest approval request is used, because each one replaces the previous token (Vitest: "keeps the dialog usable after a failed apply request").
7. M02 open items 1–7 still apply.
