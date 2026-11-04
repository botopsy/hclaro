# Hclaro — Product Requirements Document

**Product name:** Hclaro (`hclaro` CLI). Selected project name; domain and trademark availability remain to be verified.  
**Product:** Local-first visual workspace for Terraform and OpenTofu  
**Version:** PRD 1.0 / implementation plan for v0.1  
**Date:** 2026-11-04  
**Audience:** Product owner, developers, security reviewers, Codex coding agent  
**Status:** Approved direction; implementation may start at Milestone 1

---

## 1. Executive summary

Hclaro is a single-install CLI that starts a local HTTP service and opens a browser-based UI for an existing Terraform or OpenTofu working directory. It is designed for infrastructure engineers who already run Terraform/OpenTofu from their laptop and want a better way to explore resources, inspect state, understand plans, and deliberately perform resource operations (notably import and replace) without adopting a hosted control plane, moving credentials to a SaaS service, or rewriting their workflow.

**Core promise:** `cd infra/prod && hclaro` opens a fast, comprehensible, safe visual workspace using the locally installed Terraform/OpenTofu executable and the user's existing credentials, working directory, modules, provider plugins, selected workspace, and state backend.

**Product pillars:**
1. **Local first:** no account, external server, cloud synchronization, or telemetry required.
2. **Familiar execution:** use the official Terraform/OpenTofu CLI as the execution engine; do not reimplement planning, import, or apply.
3. **Preview first:** no infrastructure-modifying operation without explicit user initiation, a reviewed plan when supported, and informed confirmation.
4. **State-aware:** show accurately which resources are in state and which configuration blocks exist; do not pretend state and configuration are the same thing.
5. **Low friction:** one cross-platform binary serving an embedded frontend; no Node.js runtime needed by end users.

## 2. Problem and opportunity

Engineers often use local Terraform/OpenTofu for planning and applying infrastructure changes. Reasons include speed, debugging, using existing AWS/Azure/GCP local identity/SSO and VPN, avoiding complex pipeline setup for smaller projects, and keeping hands-on control of changes. Traditional CLI output becomes difficult for large modules, long resource addresses, multiresource replacements, unknown/computed values, and imports. A local browser interface can improve comprehension while retaining the existing CLI workflow.

The product should **not** imply that running Terraform locally is inherently safer than CI/CD. Teams requiring auditability, separation of duties, policy gates, and controlled runners may correctly prefer centrally managed deployment systems. Hclaro optimizes the existing local workflow and exposes its limitations honestly.

### Primary pain points
- Listing and searching hundreds of state resources across nested modules.
- Understanding exactly what a plan changes (including hidden collateral effects).
- Inspecting a resource and determining which state address, module, provider, and configuration it belongs to.
- Replacing one or several resource instances, confidently reviewing side effects.
- Importing an existing remote object into configuration/state, especially where addresses are nested or indexed.
- Avoiding state corruption and destructive mistakes caused by opaque CLI operations.

### Product differentiation
- Developer-tool ergonomics: searchable resource tree, contextual details, keyboard-first navigation, side panels, command palette.
- First-class, guided **Import Wizard** and **Single/Bulk Replace** instead of only plan visualization.
- Capability-aware, safety-conscious execution with official CLI output, validation, and an explicit action audit view.
- Entire UI served locally from one CLI binary, for Terraform *and* OpenTofu.

## 3. Target audience and jobs to be done

### Personas
**P1: Independent DevOps/SRE engineer (primary).** Manages multiple Terraform workspaces, uses local CLI with SSO/VPN, values speed and predictable control. Wants context and safe workflows, not a new hosted system.

**P2: Platform engineer.** Works with many modules/accounts, frequently investigates drift, refactors, imports, and replaces resources. Wants dependency awareness, precise operations, and search.

**P3: Terraform newcomer (secondary).** Understands IaC concepts but struggles with opaque state addresses and diff output. Benefits from read-only visual explanation; must not be able to accidentally click through a dangerous operation.

### Core jobs
- "Show me what this workspace currently manages."
- "Find the EKS node group and inspect its recorded attributes."
- "Show me every change this plan will make, especially deletes/replacements."
- "Replace these instances, but show me *all* actual effects first."
- "Import this existing cloud resource into the correct address and configuration."
- "Let me see the generated CLI commands and execution logs."

## 4. Goals, non-goals and product principles

### v0.1 goals
G1. Start the app from a valid initialized Terraform/OpenTofu project with no custom configuration.  
G2. View state resources (including modules/indexed instances), attributes, metadata, search, filter, navigation.  
G3. Generate or load a machine-readable plan and inspect every change.  
G4. Perform safe **single and bulk replace** via plan → review → explicit confirmation → apply saved plan where backend permits.  
G5. Guide imports using declarative import blocks where supported and verified; require durable configuration before applying.  
G6. Provide streaming logs, actionable error states, and minimal local execution history.  
G7. Work on macOS, Linux, Windows in browsers with consistent behavior.  
G8. Avoid telemetry, credential uploads, or persistent storage of unredacted state/plan.

### Explicit non-goals for v0.1
- Hosted multiuser app; team accounts; RBAC; approvals and policy orchestration.
- CI/CD, GitHub/GitLab pipelines or state backend replacement.
- Cloud-provider SDK inventory and editing resources directly via cloud APIs.
- Terraform/HCL authoring IDE or generic drag-and-drop infrastructure editor.
- Automatic drift detection, scheduled refresh, costs, AI, MCP, distributed tracing.
- Destroy-all or arbitrary `state rm`, `state mv`, `force-unlock`, state push/pull editing from UI.
- Implicit apply after a speculative plan; silent mutation of HCL; automatic `terraform init` or `tofu init`.
- Support for remote-execution backends where a saved-plan review/apply contract cannot be guaranteed; these remain read-only/plan-only until a validated adapter exists.

### Core principles
- Read-only on launch. Nothing that contacts a provider or mutates a backend should run silently.
- A selected resource is **not** authorization to replace it.
- Successful process exit does not always mean the desired infrastructure state exists: show command status and post-apply state separately.
- Configuration is source of truth; do not equate "remove from state" with "delete remotely".
- Use deterministic capability checks and fail closed on unsupported or ambiguous formats.

## 5. Scope and release plan

| Stage | Name | Essential deliverable | Operation risk |
|---|---|---|---|
| M1 (v0.1-alpha) | CLI + Read-only Explorer | local server, engine detection, state tree, inspector, secure local UI, sample fixture | Read only |
| M2 (v0.1-beta) | Visual Plan | explicit plan invocation, saved plan parsing, change grouping, filters, diffs, stream | Reads providers/state; state locking may occur |
| M3 (v0.1-rc) | Replace | choose single/multiple instances, plan with repeated `-replace`, complete diff, confirmation, saved-plan apply where supported | Infrastructure mutation |
| M4 (v0.1) | Import Wizard | capability-aware import block workflow, HCL preview, collision checks, full plan review, apply | State and possibly infrastructure mutation |
| M5 (post-v0.1) | State refactoring | planned `moved`/`removed` blocks and controlled migration wizard | High |
| M6 (post-v0.1) | Graph and impact | dependency graph, blast radius, diff comparison, multi-project discovery | Moderate |

M1 must be usable independently. No implementation of M3/M4 must be started before M1/M2 tests and the security review checklist pass.

## 6. UX and information architecture

### 6.1 App shell
Desktop-oriented responsive UI with a fixed app rail and split-panel layout:
- **Header:** project folder name, engine (Terraform/OpenTofu) and version, workspace, backend status, last snapshot time, operation in progress, Refresh button (explicit), theme selector.
- **Left sidebar:** navigation (Overview, Resources, Plans, Operations), resource tree, compact search/filter, module grouping.
- **Main content:** overview widgets or selected resource/plan details.
- **Right inspector (optional):** metadata, attributes, state and planned diffs.
- **Bottom drawer:** current command, execution output, errors, progress, cancel button (where cancellable).
- **Command palette:** `Cmd+K`/`Ctrl+K`, navigate, find resource, initiate plan. Do not auto-run sensitive commands from search.

Visual direction: highly legible, compact, polished developer tool; dark/light mode; restrained color. Destructive operations require strong semantics, not color alone. Keyboard accessible.

### 6.2 Screens
**Overview:** workspace identity, resource count, module/provider breakdown, last refresh/plan time, warnings; not fake live cloud data.  
**Resources:** hierarchical module tree, flat/table toggle, provider/type filters, names and full addresses; pagination/virtualization for large states.  
**Resource Inspector:** full address with copy button; mode (managed/data), type, provider reference, module path, instance key, attributes, sensitive-value placeholders, source/config association when available; copy *redacted* JSON and copy address.  
**Plans:** summary by action (create, update, delete, replace, no-op, read, unknown), searchable change list, before/after structural diff, computed/sensitive labels, provider/module grouping; plan source/time and whether apply is enabled.  
**Operations:** list current and previous **redacted** local operation metadata; stdout/stderr streaming, stage, duration, exit code, cancel/error details.  
**Import Wizard:** target resource address/config validation, import ID (masked if needed), preview generated import block/HCL, plan, review, explicit apply.  
**Replace Wizard:** selected addresses, full-plan summary with any secondary changes, review and confirmation, saved-plan apply.

### 6.3 Empty and error states
- Not a Terraform/OpenTofu project: explain expected files and how to navigate to an initialized root module.
- Both CLIs installed: choose via deterministic default documented below, allow `--engine` override.
- Backend not initialized: show instructions for the user to run `terraform init`/`tofu init` themselves; never invoke silently.
- Credentials missing, SSO expired, network/VPN unavailable: display relevant redacted CLI error and retry instructions.
- State is empty, unreachable, or does not exist: distinct states; do not present unreachable as "0 resources".
- Unsupported engine version/capability/remote execution: show supported read-only subset and reason.
- Operation conflicts/state lock: show the original CLI diagnostic, do not recommend force unlock as default.

### 6.4 Interaction and accessibility
- Search by name, full address, type, provider, module; filters compose and reset easily.
- Preserve user selection and scroll when switching between resources.
- Support keyboard tree navigation, visible focus, proper labels, screen-reader status for running operations.
- No optimistic success state for apply/import/replace.
- For 1,000 resource instances, initial list interaction should remain responsive through indexing and virtualization.

## 7. CLI requirements

### Core invocation
```
hclaro                         # Use current working directory
 hclaro --path ./infra/prod     # Set root working directory
 hclaro --port 9273             # Explicit local port; fail clearly if occupied
 hclaro --engine terraform      # Force Terraform executable
 hclaro --engine tofu           # Force OpenTofu executable
 hclaro --no-browser            # Serve only; print URL
 hclaro --version               # Print version/build metadata
 hclaro --help                  # Usage and exit codes
```

Whitespace before commands above is purely illustrative; actual CLI should accept exactly `hclaro` and flags.

**Default executable selection:** if `terraform` executable is available and project is ambiguous, prefer Terraform; if only `tofu`, use OpenTofu; override with `--engine`. Detect versions and surface compatibility status. Executable resolution must avoid shell interpolation and be deterministic. CLI's `cwd` must be the explicit validated project root, not arbitrary data-controlled paths.

**Launch behavior:** validate path and engine; bind a loopback-only port (prefer selected port or fallback dynamic if no explicit port); generate unpredictable session secret; serve embedded UI; open OS default browser after readiness; print URL and shutdown instructions; `Ctrl+C` gracefully cancels idle operations and closes server. Offer `--no-browser` for SSH/devcontainer use, but do not expose remote listening in v0.1.

**Important:** no auto `init`, `plan`, `refresh`, `apply` or provider credential resolution beyond displaying local state on launch. Reading a remote backend via CLI may involve network access; show that it reads the configured backend and never treat as offline.

## 8. Core functional requirements with acceptance criteria

### FR-01 Workspace detection (M1)
- Detect Terraform/OpenTofu executable, semver, project root, configured backend type if available, workspace, presence of `.terraform` and configured provider locks.
- Do not scan parent directories without explicit user selection. Avoid filesystem writes during discovery.
- Workspace status reflects errors instead of silently substituting `default`.
- Capability flags control UI actions, e.g. `canReadState`, `canPlan`, `canApplySavedPlan`, `supportsImportBlocks`, `supportsGeneratedConfig`.

**Acceptance:** Given an initialized fixture, header shows engine/version/workspace and backend with correct capability badges; missing `init` shows actionable error; both `terraform`/`tofu` paths can be selected.

### FR-02 Read-only state explorer (M1)
- Call supported official CLI JSON state inspection (`show -json`, plus version-specific adapter if required), parse resources/modules recursively and resource instances accurately.
- Support `managed` and `data` instances; robustly parse `count` indexes, `for_each` string keys (including escaped names), nested module instances and provider source addresses.
- Flatten to canonical resource instance records, keeping full address and raw path if available.
- Group by module/provider/type and support full-address search.
- Sensitive values must not be sent raw to the frontend. A conservative masking policy is required because not every sensitive secret is reliably tagged.
- Never fetch provider API data while rendering inspector; state is state, not fresh cloud truth.

**Acceptance:** 300+ resource fixture renders correct counts with no duplicate instances; indexed addresses are exact; sensitive fixture value never appears in UI, API responses, terminal logs or local history.

### FR-03 Plan engine and plan viewer (M2)
- User explicitly initiates plan. Backend performs `terraform/tofu plan -input=false -out=<private-temp-path>` (plus safe user-selected args such as `-var-file`, through validated arrays). Do not silently use `-lock=false` or `-refresh=false`.
- Stream human-readable progress to UI; on completion convert saved plan to JSON via appropriate CLI invocation; parse `resource_changes`, before/after values, `change.actions`, `replace_paths`, unknowns and sensitivity markers. Preserve action ordering; `delete,create` and `create,delete` are both replacements with different ordering.
- Display all planned changes, including unrelated and dependent side effects; data source reads and output changes are not silently discarded.
- Distinguish normal, destroy, create, replace, read, no-op and ambiguous action combinations; unknown actions are shown as unknown (never assumed safe).
- Show plan metadata: engine version, workspace, creation time, source path, state lineage/serial if available, SHA-256 of saved artifact, configuration fingerprint and whether artifact is still valid for action.
- For v0.1, all plans are generated locally; remote execution variants unsupported for apply unless explicit verified adapter implements equivalent guarantees.

**Acceptance:** Golden JSON fixtures correctly classify actions; complete plan diff can be navigated; CLI errors display without secrets; a plan from a different workspace cannot be applied.

### FR-04 Single/Bulk Replace (M3)
- Replace action is only available on managed resource **instances** already represented in state, not on data sources or whole modules. For nested modules/indexes, pass exact resource addresses as separate argument values.
- Multi-select n instances, confirm selection, then generate full plan with one `-replace=<address>` argument per selection; never use `-target` as routine behavior.
- Display not just selected replacements but **entire resulting plan**. Prominent warning for dependent deletions/replacements, destroy-before-create, downtime, data-loss risk. Do not claim blast radius beyond what plan proves.
- Apply must use the same saved plan artifact reviewed by the user where supported. Before apply: verify in-process session, engine, cwd, workspace, plan digest, age/context, and that no other local operation is underway. If state changed or checks fail, reject and regenerate/review plan.
- Require explicit approval modal summarizing actual creates/updates/deletes/replaces, typed confirmation for any destructive plan, and a visible command preview. A *saved-plan apply* does not prompt within Terraform, so the UI is responsible for confirmation.
- Queue/serialize mutating operations per workspace; observe state lock handling; allow cancellation without implying rollback.

**Acceptance:** selecting two resource instances produces exactly two `-replace` plan flags; all unexpected changes surface; declining confirmation causes no apply; stale plan cannot apply; no mutation when plan fails.

### FR-05 Import Wizard (M4)
- Prefer declarative `import` blocks and plan/apply so user sees effect before modification. Detect capability by engine/version rather than assuming equality between Terraform and OpenTofu.
- Flow: select engine workspace → enter import ID, provider/type, destination *resource instance* address and config location → validate address and no existing state binding → inspect HCL/config correspondence → preview proposed import block, and resource config generation **only where CLI and config support it** → stage file changes safely and show diff → initiate full plan → review → explicit apply.
- Never silently write to tracked `.tf` files or overwrite user content. Provide reversible generation into a new named file with confirmation; use atomic writes and reject path traversal/symlink escapes.
- CLI import is a possible advanced fallback if declarative import not available, but **must not be enabled as a one-click action** in v0.1 because it modifies state outside the reviewed plan flow. Present a generated/manual CLI recipe instead.
- Detect collisions: address already managed, duplicate import ID if confidently detectable, missing target resource configuration, invalid instance keys, unsupported generated configuration or remote backend restrictions.
- If user elects to generate HCL, show resulting plan even if it includes updates or replacements to unrelated objects; do not claim import always results in only an import.
- On successful apply, refresh the state view; clean up only app-owned temporary files, never delete user-authored import/config files without asking.

**Acceptance:** resource already in state cannot be reimported inadvertently; import block is shown before writing; user can back out without changing tracked files or infrastructure; successful import displays the new resource; failed plan does not execute apply.

### FR-06 Execution and history (M1–M4)
- One operation at a time per workspace in process, globally unique operation IDs. Track queued/running/completed/failed/cancelled; labels include engine, action, time, elapsed, exit code and redacted argv.
- Use subprocess argument arrays (no shell). Stream bounded, redacted stdout/stderr to SSE or WebSocket; persist only optional minimal operation metadata (no plan bytes, secrets, state values, environment variables or full raw logs by default).
- Cancelling sends a graceful interrupt and then a bounded termination if needed; clearly state cancellation may leave partially modified infrastructure/state. Never auto-unlock state.
- Captured errors include a safe diagnostic code and actionable retry or manual CLI command.

**Acceptance:** 10MB command log stream does not exhaust memory; client reconnection can request bounded recent redacted events; cancellation accurately displays uncertain/partial outcome.

### FR-07 Settings (M1)
- Theme (system/light/dark), preferred grouping, compact/comfortable density, default CLI override if explicitly chosen.
- Store only safe preferences locally. No telemetry. No credential import or storage.

### FR-08 Future-proof extensions
- Adapter interface encapsulates CLI dialect/version differences.
- Backend execution modes are explicit (`local-cli`, `remote-exec`, `unsupported`).
- Do not overgeneralize dependency graph or AI in v0.1; reserve interface extension points.

## 9. Primary user flows

### Flow A: inspect state
1. Open terminal and `cd` into initialized root module.
2. Run `hclaro`; browser opens loopback URL.
3. See workspace context and status. No plan/apply occurs.
4. Type `eks_node_group`, select instance, see attributes and full address.
5. Copy full address. Sensitive values remain redacted.

### Flow B: plan and inspect
1. Click **Run Plan** and choose optional var-file path already inside/project-approved path.
2. See confirmation that this contacts providers/remote backend and may acquire state lock.
3. View streamed output, progress and failure if any.
4. See summary of all actions and inspect JSON diff tree; receive a clear read-only plan result.
5. Explicitly choose replace or another operation; no automatic apply.

### Flow C: bulk replace
1. Select two managed instances, choose **Replace**.
2. Inspect preflight information; app generates a full saved plan with two `-replace` flags.
3. Review full plan including collateral effects, and risk summary.
4. Type a visible confirmation phrase when destructive changes exist.
5. Apply exactly that saved plan if backend supports it, otherwise show "Apply unavailable" with reason; do not fall back to a new unreviewed plan.
6. Show live output, terminal status, final resource list reload and any errors.

### Flow D: import
1. Open **Import Wizard**, choose existing resource type/address, enter remote ID.
2. UI validates workspace, format and existing binding.
3. Preview import block and any generated resource HCL in a diff.
4. Approve writing new HCL file; run full plan.
5. Verify import step and any extra actions; approve saved-plan apply.
6. Confirm state now includes imported address, preserve generated HCL for future plans.

## 10. Architecture

### Tech stack
- **Backend:** Go (recent supported stable version; pin version in `go.mod`), CLI using Cobra or standard `flag`; `net/http` APIs; Go `embed` includes built Vite static assets.
- **Frontend:** React + TypeScript + Vite; Tailwind and shadcn/ui or equivalent accessible component kit; TanStack Query for network state; simple route layer; virtualization for resource list.
- **Distribution:** one Go binary per target platform/architecture; no Node runtime for users. Node only for frontend build. GitHub Releases in future.
- **State:** transient process memory and small sanitized preferences/history file in OS app-data; no database required in v0.1.
- **Events:** SSE for one-way command stream, HTTP for control endpoints. No arbitrary command endpoint.

### Module boundaries
```
cmd/hclaro/            CLI parsing / launch / lifecycle
internal/workspace/     project discovery, engine choice, backend/workspace
internal/engine/        common adapter interface, terraform/, tofu/
internal/state/         JSON parsing, normalized resources, redaction
internal/plan/          planning, plan artifact metadata, diff normalization
internal/operations/    serialized command runner, queue, history, cancel
internal/importer/      declarative import staging, validation, file diff
internal/security/      session token, local host validation, sensitive filtering
internal/api/           HTTP handlers, request validation, SSE
web/                    React SPA and tests
internal/ui/            embedded compiled SPA assets
fixtures/               sanitized, reproducible test state/plan fixtures
```

### Official CLI interface
- Discover through `terraform version -json` where supported or parse `terraform version`, `tofu version` safely.
- Read state via CLI machine-readable output suitable to detected version (`show -json` for common Terraform/OpenTofu versions; capability adapter handles documented divergences).
- Plan with `plan -input=false -out=<secure-temp-file>`, then appropriate `show -json <plan>`/version-specific flags.
- Apply reviewed artifact only when supported, without `-auto-approve` or shell expansion. Note that saved-plan apply is intrinsically non-interactive.
- **No cloud-provider CRUD or direct backend file edits.**
- Respect existing `TF_CLI_ARGS`, `TF_DATA_DIR`, `TF_WORKSPACE`, credentials and other inherited environment settings, but show warning when injected flags may subvert a reviewed plan; for write operations reject or sanitize unsafe overrides as appropriate. Explicit effective argv display omits secrets.
- Detect `HCP Terraform` or other remote-run execution; do not incorrectly claim local saved-plan lifecycle works. Feature gate safely.

### Backend contract (suggested HTTP API; iterate as needed)
- `GET /api/v1/session` → version, session expiry, capability flags.
- `GET /api/v1/workspace` → engine, version, cwd label, backend, workspace, init status.
- `GET /api/v1/resources?query=&module=&provider=&type=&cursor=` → normalized, redacted resources (optional paging).
- `GET /api/v1/resources/{opaque-id}` → redacted normalized instance details; IDs map to exact addresses server side.
- `POST /api/v1/plans` `{kind:"normal"|"replace", resourceIds?:string[], varFiles?:string[]}` → operation ID, never executes apply.
- `GET /api/v1/plans/{id}` → summary, safe change tree, review status.
- `POST /api/v1/plans/{id}/apply` `{confirmationToken:string}` → operation ID after server-side authorization checks.
- `POST /api/v1/imports/preview` → validated import block, HCL diff, warnings; no writes.
- `POST /api/v1/imports/stage` → explicit approved file staging operation; revalidate content and paths.
- `GET /api/v1/operations` and `GET /api/v1/operations/{id}` → sanitized metadata.
- `GET /api/v1/operations/{id}/events` → SSE bounded replay.
- `POST /api/v1/operations/{id}/cancel` → cancellation requested.

All mutating endpoints: session authorization, CSRF/origin checks, content-type validation, referer-independent origin verification, rate limiting where sensible, methods restricted, request body size bound, no wildcard CORS. Design confirmation tokens short-lived and bound to an exact immutable plan digest, session, workspace identity, and operation. The browser must never send arbitrary CLI flags or binaries to execute.

### Data model (illustrative)
```
Workspace {
  id, displayPath, engine, engineVersion, workspaceName,
  backendType, executionMode, initialized, capabilities, warnings
}
ResourceInstance {
  id, address, mode, type, provider, modulePath, instanceKey,
  name, statePresence, attributesRedacted, sensitivityStatus
}
Plan {
  id, kind, createdAt, engine, workspaceId, artifactHash,
  contextFingerprint, actionCounts, resourceChanges[],
  warnings[], status, applyEligibility
}
ResourceChange {
  address, actions[], actionClassification, beforeRedacted,
  afterRedacted, afterUnknownPaths[], sensitivePaths[], replacePaths[]
}
Operation {
  id, kind, startedAt, completedAt, status, exitCode,
  commandDisplayRedacted, associatedPlanId, diagnostics[]
}
```

Avoid serializing complete raw state or raw plan into HTTP responses. Normalize server-side and allow precise resource attributes only after redaction.

## 11. Security and safety requirements (release blocking)

**S-01 Threat model:** Untrusted local websites may try to call loopback endpoints (CSRF/DNS rebinding), workspace HCL/modules/providers can contain unexpected values, and state/plan may contain credentials. Local machine malware is out of scope but sensitive file permissions are still required.

**S-02 Network binding:** HTTP listen only on `127.0.0.1` (optionally also `::1` with equivalent protections); no `0.0.0.0` flag in v0.1. Reject unexpected `Host` headers and DNS rebinding, strict allowed origins and no permissive CORS. Random session secret per launch, HTTP-only SameSite cookie or secure equivalent; CSRF nonce for writes. Restrictive CSP, no external fonts/analytics/assets.

**S-03 Credential handling:** Inherit configured environment/identity, never ask for or persist cloud passwords/keys. Never upload state or logs. Redact sensitive values based on schema markers *and* key/path heuristics. Since JSON can reveal secrets even with `sensitive` fields, default to deny-display for unrecognized high-risk raw values; no raw-state API.

**S-04 Temporary artifacts:** saved plans contain secrets; create in OS private temp dir with `0700` directory, `0600` files on POSIX, appropriate ACL on Windows; exclude from Git and backups if possible; never store in working directory by default; delete on session close and expiration. Do not log absolute sensitive temp filenames if avoidable.

**S-05 Command execution:** allowlisted commands and validated argv arrays; no shell; limit child execution concurrency; validate filesystem roots, symlinks and file permissions; prevent arbitrary binary path execution via API; handle stdout injection (render plain text only).

**S-06 Apply gating:** no mutate-on-GET; confirmations bound to exact reviewed plan and session; reject mismatched workspace, backend, state serial/lineage when observable, operation overlap, stale digest. Use engine errors (e.g. stale plan) as secondary defense. Warn if state may have changed externally, provider version changed, or remote run excludes guarantee.

**S-07 Environment and variable files:** `TF_CLI_ARGS*`, `TF_VAR_*`, var-files, working directory, binary versions, credentials can change results. Capture a nonsecret context fingerprint before planning; apply only saved plan. Do not print environment variable values. Parse/mask effective command-line args, especially `-var`.

**S-08 Destructive operations:** replace can destroy data; show risk but avoid guarantees of no downtime. Require typed confirmation on plans with delete/replace. `-target`, `-destroy`, `-lock=false`, direct state mutation and `force-unlock` excluded from standard UI.

**S-09 Import HCL:** atomic new-file creation after explicit consent; no arbitrary path escapes or overwrites; show diff and Git dirty warning. Never edit module files outside chosen root without explicit authorization.

**S-10 Diagnostics/history:** persist operation metadata only (timestamp/action/status/obfuscated target counts) with configurable deletion. No raw secrets in browser devtools/network payloads; test with planted sentinel credentials.

## 12. Performance and reliability

- Cold launch target: UI reachable within ~2 seconds on reference development machine **excluding CLI/backend access**. Clearly separate startup from state-fetch latency.
- Render/search 1,000 instances without input lag noticeable to typical developers; use server or client indexes and virtualized lists.
- Run one mutating operation per workspace; avoid racing plan/apply/state reads.
- Bound stdout event ring buffer and UI memory; tail 10,000 lines or capped bytes, whichever first; acknowledge truncation.
- Accept slow/unavailable remote backends without freezing app; cancellable requests and timeouts with clear states.
- Recover gracefully when browser reloads during a process; in-process session continues until shutdown, SSE can reconnect.
- No guarantee of process/job survival after CLI/server crash. Document recovery: inspect CLI/state manually and replan.
- Cross-platform path and process termination behavior must be tested, particularly Windows.

## 13. Test strategy and QA gates

### Unit tests
- Engine version and capability detection; version mismatch fallback.
- Recursive state module flattening; indexed instance address parsing; mode distinctions.
- Plan action categorization; JSON null/unknown/sensitive; odd action orders.
- Redaction of nested maps, lists, computed values, free-text output and logs.
- Request validation, session origin policy, stale confirmation tokens, workspace mismatch.
- Import HCL generation/preview and safe filesystem boundaries.

### Golden fixtures
`fixtures/` must include synthetic, nonsecret data representing:
1. Empty state and no-state error.
2. Root resources, nested modules, count, for_each and quoted keys.
3. Data sources and managed resources.
4. Plans with create/update/delete/replace/read/no-op, computed and sensitive fields.
5. Two replacements plus collateral deletion.
6. Existing import target, invalid ID, missing config.
7. A planted canary string (e.g. `DO_NOT_LEAK_HCLARO_TEST_SECRET`) to assert not present in any API/UI/log output.

### Integration tests
- Fake engine executable for safe reproducible stdout, errors, delays and cancellation.
- Real Terraform/OpenTofu runs against isolated local test resources only, after explicit test setup.
- Test CLI in initialized and uninitialized project; auto-browser disabled in CI.
- Confirm GET requests can never invoke plan/apply/import.
- Confirm commands are not shell-expanded when addresses contain quotes/brackets.
- Confirm reusing saved-plan ID after context change is rejected; no apply after decline.
- Browser E2E for tree navigation, plan inspection, replace wizard, keyboard support, SSE reconnect.

### Security QA gate
- Canary secrets absent from network responses and persistent files.
- Other origins cannot send modifying requests even if they can reach loopback.
- DNS rebinding Host denied.
- `plan` artifacts private and removed on exit.
- Fuzz JSON decoder on unusual nested/large values.
- No uncontrolled raw HCL/command execution via API.

## 14. Release and deployment

- `make dev`: starts Vite dev server and Go backend with separate local ports, restricted CORS just for local dev.
- `make build`: build frontend, embed static assets, produce Go binary.
- `make test`: unit + backend + frontend tests; `make lint`: staticcheck/vet/eslint/tsc as configured.
- Cross-compile CI jobs for macOS arm64/amd64, Linux amd64/arm64, Windows amd64; tests as supported by CI runners.
- Signed/checksummed release packages and package managers are later milestones.
- Repository includes README quickstart, CONTRIBUTING, SECURITY.md, LICENSE decision, CHANGELOG and `docs/PRD.md`.
- Provide clear warning that this is an early tool and users should first test against a disposable workspace.

## 15. Success metrics (privacy-preserving)

No automatic telemetry. Use opt-in user interviews, local test sessions, and optional anonymous survey. Objectives:
- A new user opens an initialized project and finds a resource in under 60 seconds.
- 95%+ of synthetic plans classified without missing change instances.
- Zero confirmed critical safety violations in release test suite.
- Single replace workflow requires fewer manual copy/paste steps than CLI while preserving plan visibility.
- Import flow is reversible until explicit write/approve step.

Metrics are internal usability objectives, not promises.

## 16. Decisions, assumptions and open questions

### Decisions already made
- **CLI + local web UI** first, not native desktop.
- **Go backend**; embedded React/TypeScript frontend, one distributable binary.
- **Terraform + OpenTofu** both supported through adapters.
- **Local-first**, no SaaS requirement.
- **Import and Replace** are priority differentiators.
- **Preview-first** architecture and read-only launch.

### Assumptions to validate while implementing
- Default workspace engine preference can be Terraform unless overridden; verify behavior for existing OpenTofu projects.
- Imported resource details cannot be discovered from remote provider without CLI planning or provider SDK; guide user to enter IDs and addresses.
- Supported CLI versions vary in JSON output flags/structures; maintain tested matrix rather than assuming latest flags are universal.
- Browser's `http://localhost` origin and cookies need explicit local security design; do not disable security because "only local".

### Decisions deferred
- Final product name/domain, open-source license and brand.
- Pricing, hosted collaboration, plugins and cost estimate.
- Advanced remote-execution adapters and enterprise approvals.
- Graph topology, state surgery and AI assistance.

## 17. Milestone definitions and acceptance / Definition of Done

### M1: Read-only usable alpha (Codex should start here)
Deliver CLI entry point, local secure server, engine detection/capabilities, state parsing fixtures, resources API, resource tree/search, inspector, theme and error states. Finish with macOS/Linux/Windows build instructions, passing tests, and screenshots if browser runner exists. **Do not implement actual plan/apply/import/replace mutation.**

**M1 DoD:** `hclaro --path ./fixtures/project --no-browser` starts (fixture must be isolated or mocked); UI shows engine/workspace/state; correct canonical addresses; no secrets leaked; no mutating endpoints reachable; tests pass and README explains setup.

### M2: Plan visualizer
Implement explicit plan requests and JSON plan parser; safe temp storage; event stream; change summary/filter/diff; read-only plan review. Do not enable apply yet.

**M2 DoD:** verified create/update/replace/delete classification, whole-plan summary, all planned side effects visible, sensitive strings redacted, cancellation and error behavior tested.

### M3: Replace workflows
Implement selection, `-replace` plan, risk review, confirmation, saved-plan apply for eligible execution backends, state reload. Include multi-instance batch and stale-plan guards.

**M3 DoD:** two selections → two replace flags, full diff, signed/opaque plan review token bound to artifact, no unreviewed apply, all destructive warnings and tests pass.

### M4: Import wizard
Implement declarative import preview and file staging, version gated generation, plan/apply review, and collision checks.

**M4 DoD:** no HCL mutation before consent, no state mutation without reviewed plan, successful disposable fixture import visible in explorer, failure recovery documented, tests pass.

### Final v0.1 release gate
A clean clone builds reproducibly, no external frontend runtime is required, secure default configuration is validated, all stated acceptance tests pass on supported platforms, release limitations are documented, and all mutation flows have a manual smoke test.

## 18. Appendix: authoritative behavior references

- Terraform `show` and sensitive JSON: https://developer.hashicorp.com/terraform/cli/commands/show
- Terraform `plan`, `-replace`, `-target`, `-out`: https://developer.hashicorp.com/terraform/cli/commands/plan
- Terraform `apply` and saved-plan no-prompt semantics: https://developer.hashicorp.com/terraform/cli/commands/apply
- OpenTofu `show` (version-specific CLI): https://opentofu.org/docs/cli/commands/show/
- OpenTofu import blocks and plan/apply: https://opentofu.org/docs/language/import/
- Terraform import blocks: https://developer.hashicorp.com/terraform/language/import

**Critical warning:** do not hardcode one vendor's latest syntax as applicable to every supported engine version. Probe/document capabilities, maintain fixtures for tested version combinations, and fail closed on incompatible CLI output.
