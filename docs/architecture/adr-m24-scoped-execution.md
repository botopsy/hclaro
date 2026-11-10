# ADR M24.03: scoped execution for Workspace Hub projects

- **Status:** Accepted (design only). No runtime behavior changes in this change.
- **Date:** 2026-11-10
- **Scope:** Hub plan, apply, import, replace, drift and graph for registered projects in one Hclaro session.
- **Implemented by:** M24.04 (registry, identity, lifecycle, locks, capability gate), M24.05 (scoped plans), M24.06 (scoped preflight, approval, apply), M24.07 (UI), M24.08/.09 (import, replace, drift, graph), M24.12 (artifacts and session security). Real-engine qualification is M24.02.

## Context

Hclaro serves one local web UI per process. Today, two execution models coexist:

- **Launch project.** `internal/cli` builds one `workspace.Store`, one full engine adapter and one `operations.Manager` for the root Hclaro was started with (`operations.NewManager(store, planner, root)`). The manager runs plans, imports, drift scans, graphs and saved-plan applies for that root only, in the CLI-selected workspace. Execution profiles bind its plans through `Hub.LaunchBinder()`.
- **Hub read contexts.** `projects.Hub.OpenWith` creates an immutable read scope (`projects.Context`): project ID, canonical root, engine name and executable, UI-selected workspace, data directory and optional profile context, with its own `workspace.Store`. The engine is wrapped in `readOnly`, so only `Version`, `Workspace` and `State` are reachable, and the scoped capabilities are always `canPlan`, `canApplySavedPlan` and `canImport` false with `projects.ReadOnlyReason`.

Hub writes that exist today are reviewed init, reviewed workspace commands and read-only project tools, all through `operations.Inits` and the shared `rootlock.Set` (`Hub.Locks`, also given to the launch manager).

Enabling plan or apply buttons in Hub tabs without a server-side scope would let a request from one tab run against another project's root, workspace or environment. This ADR decides how Hub execution is scoped before any code is written.

### What the review of the current code found

Reviewed: `internal/projects/{hub,execution,init,workspaces,tools}.go`, `internal/operations/{manager,apply,preflight,context,init}.go`, `internal/rootlock`, `internal/workspace/context.go`, `internal/profiles/{profiles,resolve}.go`, `internal/security/security.go`, `internal/api/{plans,projects}.go`, `internal/cli/cli.go`.

Already sound and reused by this design:

1. `operations.Manager` keeps all plan, artifact, approval and apply policy behind one type: `checkLocked`, `ineligible` (including M24.01's `unknown_actions`), `Approve`, `StartApply`, preflight binding, single retained artifact, one-hour `PlanMaxAge`, five-minute `ApprovalTTL`, single-use constant-time token compare, typed phrase, private `0700` temp directories and `SweepStale`.
2. `workspace.FingerprintExcept` hashes engine name, version, workspace, variable files, `TF_VAR_*`/`TF_WORKSPACE`/`TF_CLI_ARGS*`/`TF_CLI_CONFIG_FILE`/`TF_DATA_DIR`, the configuration tree, `.terraform.lock.hcl`, and the data directory's backend metadata (`terraform.tfstate`), `environment` and `modules`.
3. Execution profiles carry a revision and a fingerprint; a changed profile already makes plans `context_changed` and closes read contexts (`Hub.ProfileChanged`). Profiles cannot set `TF_WORKSPACE`, `TF_DATA_DIR`, `TF_CLI_ARGS*`, `TF_LOG*` or AWS profile/region variables directly (`profiles.controlled`).
4. `rootlock.Set` keys claims by canonical root and supports exclusive and shared claims. Launch plan, import, drift, graph and apply, and Hub init and workspace commands, take exclusive claims; Hub tools take shared claims.
5. `security.Guard` enforces exact Host, exact Origin and `Sec-Fetch-Site` on mutating routes, a per-launch CSRF header, a bearer session token on every `/api/` request, JSON bodies of at most 16 KiB, and an explicit allowlist (`mutating`) of mutating paths.
6. Remote/cloud backends are classified `remote-exec` by `workspace.Inspect`, and `canPlan`/`canApply` are false for them; unknown backend types are `unsupported`.

Gaps that the implementation must close (assigned below):

| # | Gap | Where | Assigned |
|---|---|---|---|
| G1 | Hub `Inspect` and `OpenWith` check `Locks.Holder` and then run engine commands without holding a claim (check-then-run). A plan or init can start in between. | `projects/hub.go` | M24.04 |
| G2 | The plan fingerprint does not include the root's file identity, the engine executable's identity, or non-`TF_*` variables that change the target (for example `AWS_*` set outside a profile). | `workspace/context.go`, `operations/manager.go` | M24.04 |
| G3 | `operations.Manager` holds one retained artifact and one plan list for the whole process; IDs are global. Nothing scopes ownership of plans, operations or approvals to a Hub context. | `operations` | M24.04/.05 |
| G4 | Context eviction (`MaxContexts = 8`, oldest first) would cancel a context regardless of a running operation or retained artifact. | `projects/hub.go` | M24.04 |
| G5 | `rootlock` is in-process only. A second Hclaro process on the same root is not excluded (the engine's state lock still protects backend state). | `rootlock` | Documented residual risk; see [Residual risks](#residual-risks) |
| G6 | `GET /api/v1/session` reports no capabilities, so the UI cannot learn whether scoped execution is enabled. | `api/plans.go` | M24.04 |

## Decision

### Options considered

**(A) Per-scope managers inside the existing server.** The Hub keeps a server-owned registry of execution scopes. Each scope owns one `operations.Manager` instance (the existing type, unchanged in policy) bound to that scope's store, root, full engine adapter and composed environment. All scopes share the process-wide `rootlock.Set` with the launch manager.

**(B) Per-project worker subprocesses.** The server starts one Hclaro worker process per project (or per scope) and forwards requests over a local IPC channel. Each worker is a launch-mode manager for one root.

| Criterion | (A) In-process managers | (B) Worker subprocesses |
|---|---|---|
| Reuse of reviewed policy (`checkLocked`, approvals, preflight, artifacts) | Same code path, same tests | Same code inside the worker, plus a new IPC protocol whose requests must be re-validated |
| Root-lock coordination with launch, init, workspace commands, tools | Existing `rootlock.Set`, already shared | Needs a cross-process lock (file locks in project or config dir), with crash and stale-lock handling on three OSes |
| New attack surface | None beyond new HTTP routes behind the existing Guard | Worker auth channel (socket/pipe permissions, tokens), process spawn, argument passing, Windows named-pipe ACLs |
| Environment isolation | Already per child: each engine child gets an explicit `Environ`; Hclaro's process environment is never modified | Same, at higher cost |
| Crash isolation | A panic in one scope takes the server down; engine children are separate processes anyway | Worker crash is contained |
| Shutdown and cleanup | One `Close` path, existing `SweepStale` | Must reap workers, forward cancellation, and sweep each worker's artifacts |
| Memory | One manager per scope (bounded, see lifecycle) | One Go runtime and HTTP stack per worker |
| Rollback | Remove routes and gate; launch untouched | Same, plus worker binary/mode |

**Chosen: (A).** The safety-critical logic already lives in `operations.Manager` and `rootlock`, both in-process. Option B adds an authenticated IPC boundary and a cross-process lock, which are new security-sensitive components, to gain crash isolation that Go panics in Hclaro's own code do not justify (engine commands already run as child processes). Option B stays a possible future direction if a scope needs a different OS user or sandbox; nothing in this design prevents moving a scope's manager behind a process boundary later, because all scope inputs are resolved server-side and passed as data.

### Rules that follow

1. Hub execution never uses the launch manager, and launch routes never touch a Hub scope. There is no fallback in either direction.
2. A scope is resolved and pinned **only on the server**, from a registry project ID, a validated workspace name and an optional profile ID. Requests never carry a root path, executable, argv, environment value or temp file name.
3. A scope's identity is immutable. Anything that would change it creates a new scope; the old one fails closed.
4. Plans, operations, logs, approvals and artifacts belong to exactly one scope. An ID from another scope is indistinguishable from a missing one.
5. Every action that contacts the engine revalidates the scope first; state-changing commands revalidate again immediately before the engine starts, with the root claim held.

## Scope identity

A scope is created from an open Hub read context (`projects.Context`) on the first execution request and is attached to it. It records two kinds of values.

### Identity (immutable; any difference means a different scope)

| Field | Source | Revalidated by |
|---|---|---|
| Project ID | Registry record | Registry lookup; missing → `context_closed` (`project_removed`) |
| Canonical root path | `Record.RootPath` (absolute, `EvalSymlinks`) | Registry path unchanged; `EvalSymlinks(root) == root` |
| Root file identity | `os.Stat(root)` at scope creation | `os.SameFile` with a fresh stat (detects a folder replaced at the same path) |
| Engine name | Detected or preferred engine | Re-detection gives the same name |
| Engine executable | Absolute resolved path (`EvalSymlinks`), plus size, modification time and file identity at creation, plus SHA-256 computed once at creation | Stat identity on every revalidation; SHA-256 recomputed immediately before apply |
| Engine version | `version -json` at scope creation | Re-run before plan and before apply (already part of `checkLocked`) |
| UI-selected workspace | Validated name (`engine.ValidWorkspace`), passed only as child `TF_WORKSPACE` | `workspace list` contains it; `workspace show` prints it (existing read steps) |
| Execution profile | Profile ID, revision and `ExecutionContext.Fingerprint`, or "none" | Profile store lookup; revision and fingerprint unchanged |
| Data directory | Resolved `TF_DATA_DIR` (relative resolves inside the root; absolute is refused as today) | Recomputed from the composed environment |
| Environment digest | HMAC-SHA-256 with a per-process random key over the sorted names and values of the composed child environment entries that can change the target: `TF_*`, `AWS_*`, `GOOGLE_*`, `CLOUDSDK_*`, `ARM_*`, `AZURE_*`, `KUBECONFIG`, `HTTP(S)_PROXY`, `NO_PROXY`, `SSL_CERT_FILE` | Recomputed before plan and apply. The digest and key are never returned, logged or persisted |
| Backend type and execution mode | `workspace.Inspect` | Recomputed; must remain `local-cli` with a known backend type |

The identity is held as a struct inside the server. Its internal key (a digest of all fields) is used only for map lookups. The API exposes a random 128-bit scope-bound context ID and a display view (alias, root path, engine and version, workspace, profile name and revision, backend type, execution mode), never the executable path, environment values or digests.

### Content fingerprint (per plan; a difference makes a plan unapplicable)

Unchanged from today: `workspace.FingerprintExcept` over engine, version, workspace, selected variable files, configuration tree, lockfile and data-directory backend metadata, plus the profile fingerprint and the Git source fingerprint (M15). Each plan also records the saved artifact's SHA-256. A content change does not invalidate the scope; it invalidates plans made in it (`context_changed`), exactly as for the launch project.

### Behavior when something changes

| Event | Effect |
|---|---|
| Project removed from the registry | Contexts closed (`project_removed`); scope's running operation cancelled; artifacts deleted; later requests `404 context_closed` |
| Project path repaired or engine preference changed | Same, reason `project_changed` |
| Root folder moved, deleted or replaced, or a parent swapped for a symlink | Revalidation fails: `409 scope_changed` (detail `root_changed`); nothing runs; the context is closed |
| Profile edited or deleted | Contexts using it closed (existing `Hub.ProfileChanged`, reasons `profile_changed`/`profile_removed`); retained artifact deleted |
| Engine binary replaced, upgraded or removed from PATH | `409 scope_changed` (detail `engine_changed`); reopen required |
| Workspace deleted (by Hclaro or outside it) | Hclaro deletes: context closed (`workspace_deleted`). Outside: next revalidation fails `scope_changed` (detail `workspace_missing`) |
| Backend block changed, or `init` re-run with another backend | Backend type or mode change: `scope_changed` (`backend_changed`). Same type, new metadata: plans become `context_changed` via the content fingerprint |
| Configuration, variable files or lockfile edited | Plans `context_changed`; scope stays open |
| Process environment differs (restart) | All scopes are gone after restart; see [Crash recovery](#crash-recovery) |

## Lifecycle

**Create.** `POST /api/v1/contexts` opens a read context as today. No execution scope or engine planner exists yet. The first scoped execution request (plan options, plan, import preview, drift, graph) creates the scope, which builds a full engine adapter for exactly the context's root, executable, workspace and composed environment, and an `operations.Manager` bound to the context's store. Read contexts that never execute cost nothing extra.

**Resolve.** Every scoped request names a context ID in the URL. The server looks up the context, then its scope. Missing, closed or evicted contexts answer `404 context_closed` with the stored reason, as today.

**Revalidate.** Before any engine command, the scope runs the identity checks above (cheap filesystem and registry checks first, engine `version` and `workspace list` where required). Immediately before `apply`, with the exclusive root claim held, it repeats them, recomputes the executable SHA-256 and the content fingerprint, and re-verifies the artifact digest (existing `checkLocked` and `executeApply` steps).

**Close.** User close, project removal or change, profile change, workspace deletion and failed revalidation close the context and its scope: cancel any running operation (as `Manager.Cancel` does), wait for it to reach a terminal state, delete the retained artifact, drop approvals and preflight bindings, release claims. Plans remain readable until the context ID is gone; after that their IDs answer `404 context_closed`.

**Evict.** The cap stays at eight read contexts. Eviction picks the oldest context that is **idle**: no running or cancelling operation, no claim held. A context with a retained artifact may be evicted (artifact deleted, its plans noted "The workspace tab was closed. Run a new plan."). If no context is idle, opening another answers `409 capacity_exceeded` instead of cancelling work. A separate cap of four execution scopes bounds managers; it follows the same idle-first rule.

**Memory bounds.** Per scope, the existing manager limits apply (100 operations, 10 logs of at most 2 MiB, 10 plans, one retained artifact). Across the process: at most eight contexts and four execution scopes, so at most four retained plan artifacts and four running operations, of which at most one per root.

**Artifact ownership.** Each retained artifact lives in its own private `0700` directory created by its scope's manager under the shared temp base with the existing `hclaro-plan-` prefix, so the existing `SweepStale` at startup covers scoped artifacts. Artifact paths never leave the server.

**Cancel.** `POST .../operations/{oid}/cancel` cancels only an operation owned by that scope. Cancelling an apply follows the existing behavior: the engine is interrupted, the outcome is reported as `uncertain` unless proven otherwise, and the artifact is deleted.

**Shutdown.** Hub `Close` cancels all scopes' operations and waits with the same 20-second grace as the launch manager, then deletes artifacts. Any apply interrupted by shutdown is recorded as `uncertain`.

### Crash recovery

Approvals, preflight bindings and scopes are memory-only; a crash or restart loses them, which fails closed (no apply can resume). Leftover `hclaro-plan-*` directories are removed by `SweepStale` at the next start. Operation history already marks entries left `running` by an earlier process as interrupted (`history.Open`); scoped operations are recorded through the same `Recorder`, with project ID and workspace, so an interrupted Hub apply shows as interrupted with outcome unknown. Engine state locks left behind by a killed child are the engine's to report; Hclaro never runs `force-unlock`.

## Lock discipline

Locks stay keyed by **canonical root** in the single process-wide `rootlock.Set`, shared by the launch manager, Hub init, workspace commands, tools and all execution scopes.

| Operation | Claim | Why |
|---|---|---|
| Plan (incl. replace, import plan), drift scan, apply | Exclusive, held from revalidation until the child exits and the artifact is retained or deleted | May take the backend state lock, write the data directory, refresh state |
| Graph | Exclusive (as today) | Shares the data directory; keeps the existing rule |
| Init, workspace `new`/`select`/`delete` | Exclusive (as today) | Rewrites data directory and backend metadata |
| Project tools (`validate`, `fmt -check`, `providers`, output metadata) | Shared (as today) | Read-only |
| Hub inspection (`version`, `workspace list`) and read-context state reads | **Shared, held for the whole read** (closes G1) | Read-only, but must not overlap an exclusive operation that changes what they read |

Decisions:

1. **One exclusive operation per canonical root**, across launch and Hub, all workspaces and all tabs. Two workspaces in the same root share `.terraform`, providers, modules and backend metadata, so they serialize. A second request answers `409 project_busy` (Hub) or `409 operation_running` (launch, unchanged), with the holder kind (`plan`, `apply`, `init`, ...), never the other tab's details.
2. **Safe parallel reads may proceed:** shared claims run alongside each other in the same root and in different roots. Different roots never block each other.
3. A scope's manager also keeps its own one-operation-at-a-time rule (`Manager.running`). The root claim is the cross-scope guard; the manager rule is the in-scope guard.
4. Revalidation that runs the engine happens **after** the claim is acquired, so the check and the command see the same root state.
5. Claims are released on every exit path: success, failure, timeout, cancellation, panic recovery in the operation goroutine, context close and shutdown. Release functions are idempotent (`sync.Once`), as today.
6. Cross-process exclusion is out of scope (see G5 and residual risks).

## API contract

Existing launch routes (`/api/v1/plans`, `/api/v1/plans/{id}/{preflight,approval,apply}`, `/api/v1/operations/...`, `/api/v1/imports`, `/api/v1/drift-scans`, `/api/v1/graphs`) keep their paths, bodies and codes. Hub execution uses a new prefix bound to a read context:

| Method | Path | Task |
|---|---|---|
| GET | `/api/v1/contexts/{cid}/plans/options` | M24.05 |
| POST | `/api/v1/contexts/{cid}/plans` | M24.05 |
| GET | `/api/v1/contexts/{cid}/plans`, `/plans/{pid}`, `/plans/{pid}/changes/{change}` | M24.05 |
| POST | `/api/v1/contexts/{cid}/plans/{pid}/preflight`, `/approval`, `/apply` | M24.06 |
| GET | `/api/v1/contexts/{cid}/operations`, `/operations/{oid}`, `/operations/{oid}/events` | M24.05 |
| POST | `/api/v1/contexts/{cid}/operations/{oid}/cancel` | M24.05 |
| POST | `/api/v1/contexts/{cid}/imports/preview`, `/imports` | M24.08 |
| POST | `/api/v1/contexts/{cid}/drift-scans`, `/graphs` (and their GETs) | M24.09 |

Path segments `{cid}`, `{pid}`, `{oid}` are 32 lowercase hex characters. Each mutating path is added to `security.mutating` explicitly; the Guard (Host, Origin, `Sec-Fetch-Site`, CSRF header, bearer token, JSON, 16 KiB) applies unchanged.

**Request bodies** carry only the same fields as the launch equivalents: variable file references from the context's `plans/options` list, replace and import selections by opaque snapshot IDs, the backend-access acknowledgment, the profile fingerprint the UI displayed, preflight acknowledgements by check ID, the approval token and the typed phrase. Unknown fields are refused (`400 invalid_request`). There is no field for a path, executable, argv, flag, environment name or value, `-target`, or a plan file.

**Responses** include a `scope` view on context, plan, operation and approval objects: `contextId`, `projectId`, `alias`, `rootPath`, `engine`, `version`, `workspace`, `profile` (name and revision, or null), `backend`, `executionMode`. Approvals additionally return the artifact SHA-256, engine, workspace and execution context, as today.

**Capabilities.** `POST /api/v1/contexts` and `GET /api/v1/contexts/{cid}` return `capabilities` from the server: `canPlan`, `canApplySavedPlan`, `canImport` with a reason when false. They are true only when the gate is on **and** the backend is a known `local-cli` backend **and** the engine version is supported **and** the project is initialized. `GET /api/v1/session` adds `{"capabilities": {"scopedExecution": bool}}` (closes G6). The UI must treat capabilities as hints; the server enforces each one on every request.

**Stable error codes.** Existing codes keep their meaning: `context_closed` (404, with reason), `plan_not_found`, `operation_not_found`, `change_not_found` (404), `project_busy`, `operation_running`, `workspace_loading`, `context_changed`, `plan_expired`, `not_applicable`, `unknown_actions`, `approval_required`, `approval_invalid`, `confirmation_mismatch`, `acknowledgement_required` (409/400 as today), `shutting_down` (503). New codes:

| Code | Status | Meaning |
|---|---|---|
| `scoped_execution_disabled` | 404 | Gate off; the scoped route is not available in this session. Nothing ran. |
| `execution_unsupported` | 409 | Backend is `remote-exec`, unknown or unsupported, engine version unsupported, or project not initialized. Reason text says which. |
| `scope_changed` | 409 | Identity revalidation failed. `detail` is one of `root_changed`, `engine_changed`, `profile_changed`, `workspace_missing`, `backend_changed`, `data_dir_changed`, `environment_changed`. The context is closed; reopen it. |
| `capacity_exceeded` | 409 | Every context or execution scope is busy; nothing was evicted. |

A plan, operation or change ID that exists in another scope (or in the launch manager) answers exactly like a missing one (`404 plan_not_found` / `operation_not_found`), with the same body and timing class. Error messages never contain a root path other than the context's own, an executable path, environment values or engine stderr (redacted engine diagnostics keep the existing rules).

## Trust boundaries and abuse cases

Trust boundaries: (1) browser ↔ loopback HTTP server (Guard, session token, CSRF); (2) HTTP handlers ↔ Hub/scope registry (IDs and validated names only); (3) scope ↔ engine child process (argv arrays from the allowlisted adapter, explicit `Environ`); (4) server ↔ filesystem (canonical roots, private temp dirs, registry and profile files); (5) engine ↔ backends and providers (outside Hclaro's control; read-only for remote execution).

| Abuse case | Mitigation in this design | Tests (owner task) |
|---|---|---|
| Replaying a plan, operation or approval ID from context A in context B, or in launch routes | IDs are owned per scope; lookups never cross scopes; approval tokens are bound to scope, plan ID and artifact digest | Go: A/dev plan ID and token in B/prod, A/prod, different profile, launch route → 404 or `approval_invalid`, fake engine `apply` never invoked (M24.05/.06) |
| Stale approval after context reload, profile edit or scope change | Close drops approvals; `checkLocked` + profile fingerprint; approval consumed on any use | Go: approve, edit profile / reload / change workspace, apply → refused (M24.06) |
| TOCTOU between check and command | Claim acquired before revalidation; revalidation repeated under claim before apply; artifact digest re-verified | Go: race test that starts init between preflight and apply → refused or serialized (M24.04/.06) |
| Root replaced through a symlink or a folder swap | `EvalSymlinks(root) == root` and `os.SameFile` on every revalidation; read publish check (existing) | Go: replace root with symlink / new folder between plan and apply → `scope_changed` (M24.04) |
| Engine binary swapped on PATH or in place | Executable identity stat on revalidation, SHA-256 before apply | Go: fake engine replaced between plan and apply → `scope_changed`, no apply (M24.04/.06) |
| Credentials leaking through the API | Profiles hold no secrets (existing); environment only as private HMAC digest; responses use the scope display view; redaction rules unchanged | Go: response-shape tests for every scoped route with synthetic secret environment and plan fixtures (M24.05/.12) |
| Malicious local web page | Exact Host/Origin, `Sec-Fetch-Site`, CSRF header, bearer token, JSON-only bodies; new routes listed explicitly in `security.mutating` | Go: Guard tests for each new route without token/Origin/CSRF; browser: cross-origin page fetch blocked (M24.05/.12) |
| Concurrent operations from two tabs or launch + Hub on one root | One exclusive claim per canonical root; per-scope manager rule | Go: two scopes same root different workspaces → second gets `project_busy`; launch plan vs Hub plan same root (M24.04) |
| Navigation retargeting (UI switches project/workspace while a plan or approval is in flight) | Every request names its context in the URL; server never derives the target from "current" UI state; scope immutable | Browser: switch tabs mid-flow, back/forward, stale deep link → refused/reopen, never launch (M24.07) |
| Leaked or orphaned plan artifacts | Private `0700` dirs, deleted on apply, expiry, supersede, close, eviction and shutdown; `SweepStale` at start | Go: artifact removed after each path, including eviction and context close (M24.04/.12) |
| Remote/cloud execution backends | `execution_unsupported`; remote execution stays read-only; unknown backend metadata rejected | Go: `remote`, `cloud`, unknown backend fixtures → no plan child started (M24.05) |
| Shared absolute `TF_DATA_DIR` | Refused at context open (existing `shared_data_dir`) and at scope creation | Go: absolute `TF_DATA_DIR` → refused (M24.04) |
| Unknown plan actions | Existing `unknown_actions` refusal in `ineligible`/`checkLocked`, reused by scoped routes | Go: scoped equivalent of `TestUnknownActionsRefusedOverHTTP` (M24.06) |

## Rollout, gate and rollback

1. **Gate.** M24.04 adds a server-side capability `scopedExecution`, **off by default**, set only at process start (a hidden CLI flag or test configuration; it cannot be turned on through HTTP or the UI). With the gate off, every scoped route answers `404 scoped_execution_disabled`, contexts report the current read-only capabilities and `ReadOnlyReason`, and nothing else changes.
2. **Order.** M24.04 (registry, identity, locks, gate; no routes) → M24.05 (scoped plans) → M24.02 real-engine qualification of plans → M24.06 (scoped apply) → M24.07 (UI) → M24.12. The gate is not enabled by default before M24.02 qualification of both plan and apply and the M24.25 release gate. A user-facing flag name and default are an owner decision recorded at M24.06.
3. **Fail closed.** Any unknown backend type, execution mode, engine version, missing capability field or revalidation error disables execution for that scope with a reason. The UI never enables plan or apply from frontend state alone.
4. **Rollback.** Turning the gate off (or removing the routes) restores today's behavior exactly: launch-only plan/apply/import, read-only Hub contexts. The launch manager, launch routes and their tests are not modified by the scoped work except to share helpers without behavior change.

## Residual risks

- **Second Hclaro process on the same root** (G5) is not excluded. The engine's backend state lock protects state writes; the data directory and saved plans are not protected. Documented; a lock file in the per-user config directory may be considered later and would need stale-lock handling.
- **Panics** in scope code would stop the whole server (accepted with option A). Engine children are separate processes; operation goroutines must recover and release claims.
- **Environment drift outside the digest list.** Provider-specific variables not in the list could change targets without invalidating a scope. The list is deliberately broad and lives in one function so it can be extended; plans still require explicit review.
- **Heuristic redaction** is unchanged and not perfect; M24.12 covers adversarial fixtures.

## Consequences

- Hub plan and apply become possible without a second Hclaro process, using the same reviewed policy code as the launch project.
- The launch workflow is unchanged and stays the fallback.
- Hub reads become slightly more serialized (shared claims are now held during reads), which is the intended safety trade-off.
- Implementation tasks have a fixed contract: scope identity fields, lock table, routes, error codes, gate and the test matrix above.
