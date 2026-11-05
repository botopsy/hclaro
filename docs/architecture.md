# Architecture (M1 explorer, M2 plans)

## Runtime and trust boundaries

`cmd/hclaro` parses flags, validates one explicit project root, resolves one engine executable, binds `127.0.0.1`, generates 256 random session bits, and serves embedded assets. State loading runs asynchronously so a slow backend does not delay the HTTP UI. Each child has a 45-second deadline and cancellation; Ctrl+C/SIGTERM cancels reads and shuts HTTP down with a bounded grace period.

Launch uses only the read interface: `Version`, `Workspace`, and `State`. A separate `Planner` interface adds `Plan` and `ShowPlan`; it is used only by the operation manager after an explicit, CSRF-protected request (see [Plans and operations](#plans-and-operations)). Neither interface has an apply, import or state-mutation method. `internal/engine.CLI` is the shared official-CLI adapter for the common Terraform/OpenTofu dialect. Its explicit engine name selects compatibility policy and version fields. It uses `exec.CommandContext` and argv, never a shell. Selection prefers Terraform, then OpenTofu, unless `--engine` forces one. A mock adapter and a compiled fake CLI keep tests independent of installed engines and credentials.

The HTTP layer has no direct reference to an engine or filesystem root. It reads the snapshot store and embedded assets, and it can ask the operation manager to start one plan or cancel a running operation. Every GET route is side-effect free; browsing can never invoke a command. No apply, import or replace abstraction exists yet.

## Packages

| Path | Responsibility |
| --- | --- |
| `cmd/hclaro`, `internal/cli` | Flags, lifecycle, loopback listener, browser argv |
| `internal/engine` | Resolution, compatibility, bounded subprocess output, injectable adapter |
| `internal/workspace` | Root/metadata discovery, loading status, actionable safe diagnostics, immutable snapshot publication |
| `internal/state` | Official JSON decoding, module traversal, normalization, redaction, indexes |
| `internal/plan` | `show -json` plan decoding, action classification, attribute diffs, redaction, outputs and drift |
| `internal/operations` | One-at-a-time plan runner, private temp artifacts, bounded sanitized logs, cancellation, history |
| `internal/security` | Random session and CSRF tokens; exact Host/Origin, method, content-type and body policy |
| `internal/api` | Authenticated typed snapshot, resources, plans, operations and SSE responses |
| `internal/ui` | Go embedding of `dist/`; no frontend runtime server required |
| `web/src` | React UI, TanStack Query reads, accessible native disclosures, shadcn-style Radix Button, local appearance preferences |
| `fixtures`, `scripts/fake-engine` | Explicit synthetic state, plans, plan logs and executable; `scripts/fixtures/generate-plans.py` regenerates plan fixtures |
| `scripts/fixture-http`, `scripts/browser-smoke.mjs` | Browser requests bridged to the real Go handler without a real engine or TCP backend |

## Discovery and compatibility

Discovery never walks parents or writes files. The selected root is resolved through symlinks and must contain a recognized project/state filename. Initialization is inferred from the selected data directory or existing local state. Only the initialized backend **type** is read, through a bounded regular-file JSON read; config, credentials, absolute paths and environment values are never returned. `TF_DATA_DIR` can be relative to the selected root or absolute. The lockfile is reported by presence only. Backend configuration is never parsed. When the project is initialized but has no backend metadata, which is what `init` leaves for the default local backend, the root module's `.tf`/`.tofu`/`.tf.json` files are scanned textually for a `backend` or `cloud` block. No match means the default local backend. Any match, including a commented-out block or an unreadable file, keeps the backend unknown. This cannot route a plan to a remote backend: a configured but uninitialized backend makes the engine refuse to plan.

Workspace selection comes from the CLI, including inherited `TF_WORKSPACE`; an error never substitutes `default`. Remote/cloud backend metadata is labeled `remote-exec`. Unknown backend execution mode stays `unsupported`. Read capability is enabled only after a successful state read/parse. Plan capability is enabled only for `local-cli` execution after the version and workspace reads succeed; it stays enabled when no state is recorded yet. A `planReason` explains why planning is unavailable. Apply capability is false everywhere.

Stable Terraform 1.5–1.17 and OpenTofu 1.6–1.13 are the intended compatibility range. Terraform 1.5.5 has been exercised against a disposable local project (see [M02 evidence](M02-verification.md#real-engine-run)); other versions and OpenTofu only with synthetic output. Version prereleases and unsupported majors/minors are refused; format major 1 is accepted with unknown fields ignored. The common `show -json` subset is documented by [Terraform](https://developer.hashicorp.com/terraform/cli/commands/show) and [OpenTofu](https://opentofu.org/docs/cli/commands/show/). Real providers/versions need a disposable-workspace certification matrix before release.

Inherited credential, workspace and CLI configuration variables are retained. Injected `TF_CLI_ARGS` for the active command are rejected unless all arguments are `-no-color`, preventing file-output/path overrides. `TF_LOG*` is removed, `TF_LOG=OFF` and `CHECKPOINT_DISABLE=1` are forced, and raw stderr is discarded. Diagnostics explain safe troubleshooting categories rather than returning secret-bearing CLI text. This deliberately trades diagnostic specificity for protection in M1.

## Snapshot parsing and redaction

The parser consumes at most 64 MiB of official `show -json` output. It refuses plan JSON, trailing JSON, duplicate/invalid instances, more than 100,000 resources, and module nesting beyond 128. Module paths and resource addresses are retained **exactly as emitted by the CLI**. Dots inside quoted keys are never split. Deterministic opaque IDs derive from canonical addresses; maps index IDs, resource types and provider sources. Root/child module indexes include their direct instance counts. Managed and data modes remain separate.

Attributes are copied through the redactor before a snapshot is published. The redactor honors sensitivity trees at map, list, and whole-value levels, masks credential-like keys regardless of markers, masks credential-shaped strings even in permitted fields, and masks **all unknown scalar values**. A short allowlist exposes ordinary identity/location/status attributes. Nested objects remain navigable; excessively deep values and suspicious keys are masked. Outputs and raw JSON are never exposed. Potentially unsafe address/provider metadata rejects the snapshot rather than returning misleading altered addresses.

This policy intentionally hides arbitrary tags, policies and provider-specific fields. Sensitivity cannot be inferred perfectly from arbitrary strings: resource identity metadata and selected allowlisted values must not themselves be used as secret stores. There is no reveal switch. Raw subprocess bytes exist only transiently in memory; no state, logs, operation history or credentials are written by Hclaro. Installed engine/provider plugins remain trusted local executables with their own behavior.

## Plans and operations

A plan runs only after `POST /api/v1/plans` with `acknowledgeBackendAccess: true`, which the UI sends only from its preflight dialog. The operation manager allows one operation at a time and refuses new work during shutdown.

1. **Checking workspace.** `workspace show` must still match the workspace recorded at launch; otherwise the run fails with `workspace_changed`.
2. **Planning.** `os.MkdirTemp` creates a private `0700` directory under the OS temp directory (never the project). The engine runs `plan -input=false -json -out=<dir>/plan.tfplan [-var-file=<name>…]` through `exec.CommandContext`. `-lock=false`, `-refresh=false`, `-target`, `-destroy` and arbitrary flags cannot be requested; `TF_CLI_ARGS`/`TF_CLI_ARGS_plan` may only contain `-no-color`. Variable files are bare names chosen from a server-side list of `*.tfvars`/`*.tfvars.json` files in the project root (auto-loaded files, symlinks and unusual names excluded).
3. **Reading plan.** The artifact is set to `0600`, hashed (SHA-256), read back with `show -json <path>`, and the directory is removed **before** the JSON is parsed. Because M2 has no apply, the artifact is never retained. It is also removed on failure, cancellation, timeout and shutdown.

Cancellation sends an interrupt so the engine can stop providers and release a state lock, then kills it after 15 seconds. On Windows the engine starts in its own process group and receives `CTRL_BREAK`, which Go programs such as Terraform and OpenTofu handle as an interrupt; if Hclaro has no console to send it from, the engine is killed immediately. Plans time out after one hour. Shutdown cancels a running plan and waits up to 20 seconds for cleanup. Planning never writes state; a forcefully stopped engine may leave a lock, which Hclaro never removes.

### Progress logs

The engine's machine-readable UI stream (`-json`) is translated, not forwarded. Hclaro renders only typed events: refresh start/complete (address only, no IDs), planned change (address, action, reason), drift, change summary, output count, diagnostics and generic log messages. Addresses that look like secrets are replaced. Free text (diagnostic summaries/details and log messages) is cleaned in two steps. First, known input values are replaced with `[redacted]`: `TF_VAR_*` environment values and string values (quoted literals, heredoc lines, JSON strings) from the selected variable files and the files the engine loads automatically (`terraform.tfvars`, `*.auto.tfvars`, with `.json` variants). Values shorter than four characters are skipped. Collection over-matches, so map keys and non-secret inputs such as a region name are redacted too. The values are held only while the plan runs. Then a line sanitizer withholds credential-shaped lines and assignments to risky attribute names, strips control characters, and truncates. The line checks are still heuristic: a secret that is neither a known input nor recognizable, such as one returned by a provider API, can still be shown. Non-JSON stdout is withheld. stderr is never returned; it is only keyword-classified (`state_locked`, `credentials`, `init_required`, `network`).

Each operation keeps a ring buffer of at most 10,000 events or 2 MiB, whichever is reached first; dropped events are counted and reported. Logs for the 10 most recent operations and metadata for 100 are kept in memory only. Nothing is persisted.

### Plan review model

`internal/plan` accepts only `show -json` plan documents (format major 1 with `planned_values`; state documents are refused) up to 64 MiB and 100,000 changes. Actions are classified from the ordered list: `create`, `update`, `delete`, `read`, `no-op`, `forget`, and `replace` for both `delete,create` (destroy before create) and `create,delete` (create before destroy). Any other combination is `unknown`, counted, and flagged as potentially destructive. CLI order is preserved. Every resource change is kept, including no-ops, data reads, deposed objects, moves (`previous_address`) and imports. Drift (`resource_drift`) is kept separately. Output changes are kept with values always redacted. `deferred_changes` produce a warning. Errored plans are flagged.

Attribute diffs are computed on **raw** values, then redacted, so a change to a sensitive value is still listed as a change. Each row has a path (`tags["Name"]`, `ingress[0].cidr_blocks`), before/after values through the state redaction policy, and markers for `unknown` (`[known after apply]`), `sensitive` (from `before_sensitive`/`after_sensitive`), and `forcesReplacement` (from `replace_paths`). Rows are capped at 1,000 per change. Secret-shaped addresses, output names or deposed keys reject the plan rather than display altered identifiers.

A stored plan records the engine, workspace, variable files, artifact SHA-256 and a context fingerprint taken **before** the engine runs, so edits made while planning count as a change. The fingerprint is a SHA-256 over:

- engine, version, workspace and selected variable files;
- configuration, variable and lock files under the root (hidden directories skipped);
- from the data directory (`.terraform` or `TF_DATA_DIR`): backend metadata, the selected workspace file, the module manifest and installed module sources;
- the planning environment: `TF_VAR_*`, `TF_WORKSPACE`, `TF_DATA_DIR`, `TF_CLI_ARGS`, `TF_CLI_ARGS_plan` and `TF_CLI_CONFIG_FILE`.

The plan detail response recomputes it and reports `contextChanged`. Hclaro's environment is fixed for its lifetime, so the environment part matters only once fingerprints are compared across launches. Provider plugin binaries are covered only through the lock file's checksums. The fingerprint is groundwork for apply gating in a later milestone; it does not detect provider-side or state changes.

## HTTP contract

Every API endpoint requires `Authorization: Bearer <launch-token>`:

| Route | Response |
| --- | --- |
| `GET /api/v1/health` | Process/version status |
| `GET /api/v1/workspace` | Loading/ready/empty/error, safe context, capabilities, summary indexes, diagnostic |
| `GET /api/v1/resources` | Summary page (default 50, max 200) and total matches |
| `GET /api/v1/resources/{opaque-id}` | One redacted instance |
| `GET /api/v1/session` | Per-launch CSRF token and version |
| `GET /api/v1/plans/options` | Plan availability and reason, engine, workspace, backend, selectable variable files |
| `POST /api/v1/plans` | Start a plan: `{"acknowledgeBackendAccess":true,"varFiles":[]}` → 202 operation; 409 if one is running or planning is unavailable |
| `GET /api/v1/plans`, `GET /api/v1/plans/{id}` | Plan summaries; one redacted plan with metadata and `contextChanged` |
| `GET /api/v1/plans/{id}/changes/{change-id}` | One change with attribute diff and redacted before/after trees |
| `GET /api/v1/operations`, `GET /api/v1/operations/{id}` | Operation metadata: status, stage, duration, exit code, redacted command, diagnostics |
| `GET /api/v1/operations/{id}/events` | SSE: `log`, `status`, `truncated`, `end`; resume with `?after=<seq>` or `Last-Event-ID` |
| `POST /api/v1/operations/{id}/cancel` | Request cancellation (`{}` body) → 202; 409 if not running |

List parameters: `query`, exact `module`, `provider`, `type`, `mode`, `sort=asc|desc`, `offset`, `limit`. An absent module filter means all modules; `module=` selects root resources. Queries search full address, name, provider, type and module. Details use an ID map; no raw address becomes a command argument.

The guard accepts only the exact bound authority and, when present, the exact matching Origin. Cross-site fetch metadata is refused, no CORS headers are emitted and queries are bounded. GET/HEAD requests may not carry bodies. POST is accepted only on the two routes above and additionally requires: an `Origin` header equal to the bound origin, `Sec-Fetch-Site` absent or `same-origin`, `Content-Type: application/json`, an `X-Hclaro-CSRF` header matching the per-launch CSRF token (constant-time compare), a non-empty body of at most 16 KiB, and strict JSON decoding with unknown fields rejected. Every other method/path combination returns 405; there is no apply route. Unknown API paths cannot fall back to SPA HTML. HTTP header/body/idle/write limits bound requests. Assets cannot be directory-listed. CSP permits only local scripts/styles/fonts/connections and denies framing/forms; referrers and caching are disabled.

A private launch fragment bootstraps bearer authorization. The UI removes it from history immediately, keeps the token in sessionStorage for reload, and sends it only in headers. The token expires when the process stops; new launches use independent tokens. No cookies are used. Because the bearer token is never sent automatically by a browser, cross-site requests cannot authenticate; the CSRF header and Origin checks are additional, independent defenses for the two mutating routes.

SSE streams are read with `fetch` (not `EventSource`) so the bearer token stays in a header. Request contexts derive from the server lifetime, so open streams end on shutdown; each event extends the write deadline.

## Frontend and performance

The UI requests workspace status while loading, then uses the immutable snapshot. TanStack Query caches reads without focus/reconnect refresh. Search is debounced 180 ms, filters compose on the server, and resource pages render at most 50 rows. This bounds DOM work independently of total instances. Only selected details are downloaded. Resource selection stays mounted during detail changes, preserving list scroll. Module/attribute structures use native disclosures, resource buttons support arrow navigation, and reduced-motion preferences are respected.

No runtime fonts, CDNs, analytics or hosted APIs are used. Display preferences are validated before use and are the only localStorage data. The sidebar, dashboard, modules and providers all derive from real snapshot counts.

The Plans view shows plan availability, a preflight dialog (providers and backend contacted, possible state lock, no infrastructure change, no apply), the live operation log with cancel, failures with sanitized diagnostics, and the review: metadata, a review-only notice, action counts that double as filters, the complete change list (no-ops behind a toggle, 200 rows per page), a change inspector with attribute diff, outputs, and drift. Actions are distinguished by icon and text, not color alone. The Operations view lists session operations and replays retained logs.

The build is TypeScript + Vite + Tailwind, with React, TanStack Query, Lucide and a shadcn-style Button. Vite writes content-hashed files to `internal/ui/dist/assets/` plus `THIRD-PARTY-NOTICES.txt` for runtime dependencies; the Go binary embeds that directory. `scripts/build-offline-preview.mjs` remains only as a no-network fallback and must not be committed as release assets.
