# Architecture (M1 explorer, M2 plans, M3 replace and apply, M4 import, M5 Workspace Hub, M6 guided init)

## Runtime and trust boundaries

`cmd/hclaro` parses flags, validates one explicit project root, resolves one engine executable, binds `127.0.0.1`, generates 256 random session bits, and serves embedded assets. State loading runs asynchronously so a slow backend does not delay the HTTP UI. Each child has a 45-second deadline and cancellation; Ctrl+C/SIGTERM cancels reads and shuts HTTP down with a bounded grace period.

Launch uses only the read interface: `Version`, `Workspace`, and `State`. A separate `Planner` interface adds `Plan` and `ShowPlan`, and a separate `Applier` interface has one method, `Apply(path)`, which applies a saved plan file and accepts no options. Both are used only by the operation manager after explicit, CSRF-protected requests (see [Plans and operations](#plans-and-operations) and [Saved-plan apply](#saved-plan-apply)). No interface has an import or state-mutation method: imports are import blocks planned through `Plan`, whose only addition is an optional `-generate-config-out` file name. `internal/engine.CLI` is the shared official-CLI adapter for the common Terraform/OpenTofu dialect. Its explicit engine name selects compatibility policy and version fields. It uses `exec.CommandContext` and argv, never a shell. Selection prefers Terraform, then OpenTofu, unless `--engine` forces one. A mock adapter and a compiled fake CLI keep tests independent of installed engines and credentials.

Without `--hub`, the current directory (or `--path`) is the **launch project**, exactly as before. With `--hub`, or when an implicit current directory is not a project, there is no launch project: the launch store reports `no_launch_project` and the operation manager has no planner. An explicit `--path` that is not a project still fails. See [Workspace Hub](#workspace-hub) for registered projects.

The HTTP layer has no direct reference to an engine or filesystem root. It reads the snapshot store and embedded assets, and it can ask the operation manager to start one plan, approve or apply a reviewed plan, or cancel a running operation. Every GET route is side-effect free; browsing can never invoke a command. Hub inspection and opening a Hub workspace run read-only engine commands, so they are CSRF-protected POSTs. It can also ask the manager to preview an import (no side effects) or to write a reviewed import block and plan it.

## Packages

| Path | Responsibility |
| --- | --- |
| `cmd/hclaro`, `internal/cli` | Flags, lifecycle, loopback listener, browser argv |
| `internal/engine` | Resolution, compatibility, bounded subprocess output, injectable adapter |
| `internal/workspace` | Root/metadata discovery, loading status, actionable safe diagnostics, immutable snapshot publication |
| `internal/state` | Official JSON decoding, module traversal, normalization, redaction, indexes |
| `internal/plan` | `show -json` plan decoding, action classification, attribute diffs, redaction, outputs and drift |
| `internal/imports` | Import target address parsing, HCL rendering, collision and configuration checks, no-overwrite file writes, generated-configuration display redaction; never runs an engine |
| `internal/projects` | Workspace Hub: versioned project registry with atomic writes, path canonicalization, filesystem-only status, explicit inspection (`version`, `workspace list`), immutable read-only contexts and the init preflight/start (`init.go`) |
| `internal/rootlock` | One exclusive claim per canonical project root, shared by the launch operation manager and Hub init |
| `internal/operations` | One-at-a-time plan and apply runner, private temp artifacts, approval tokens, bounded sanitized logs, cancellation, history |
| `internal/security` | Random session and CSRF tokens; exact Host/Origin, method, content-type and body policy |
| `internal/api` | Authenticated typed snapshot, resources, plans, operations and SSE responses |
| `internal/ui` | Go embedding of `dist/`; no frontend runtime server required |
| `web/src` | React UI, TanStack Query reads, accessible native disclosures, shadcn-style Radix Button, local appearance preferences |
| `fixtures`, `scripts/fake-engine` | Explicit synthetic state, plans, plan logs and executable; `scripts/fixtures/generate-plans.py` regenerates plan fixtures. `fixtures.WriteHubProjects` writes synthetic Hub projects with per-workspace state; `scripts/hub-demo` seeds them into a throwaway registry |
| `scripts/fixture-http`, `scripts/browser-smoke.mjs` | Browser requests bridged to the real Go handler without a real engine or TCP backend |

## Discovery and compatibility

Discovery never walks parents or writes files. The selected root is resolved through symlinks and must contain a recognized project/state filename. Initialization is inferred from the selected data directory or existing local state. Only the initialized backend **type** is read, through a bounded regular-file JSON read; config, credentials, absolute paths and environment values are never returned. `TF_DATA_DIR` can be relative to the selected root or absolute. The lockfile is reported by presence only. Backend configuration is never parsed. When the project is initialized but has no backend metadata, which is what `init` leaves for the default local backend, the root module's `.tf`/`.tofu`/`.tf.json` files are scanned textually for a `backend` or `cloud` block. No match means the default local backend. Any match, including a commented-out block or an unreadable file, keeps the backend unknown. This cannot route a plan to a remote backend: a configured but uninitialized backend makes the engine refuse to plan.

Workspace selection comes from the CLI, including inherited `TF_WORKSPACE`; an error never substitutes `default`. Remote/cloud backend metadata is labeled `remote-exec`. Unknown backend execution mode stays `unsupported`. Read capability is enabled only after a successful state read/parse. Plan capability is enabled only for `local-cli` execution after the version and workspace reads succeed; it stays enabled when no state is recorded yet. A `planReason` explains why planning is unavailable. Saved-plan apply (`canApplySavedPlan`) is enabled only where planning is and the adapter implements `Applier`; remote execution is refused with an `applyReason`. `supportsImportBlocks` and `supportsGeneratedConfig` come from the engine and version (Terraform 1.5+, OpenTofu 1.6+, which is the whole supported range); `canImport` additionally needs `canPlan`, otherwise `importReason` explains why only the manual recipe is offered.

Stable Terraform 1.5–1.17 and OpenTofu 1.6–1.13 are the intended compatibility range. Terraform 1.5.5 has been exercised against a disposable local project (see [M02 evidence](M02-verification.md#real-engine-run)); other versions and OpenTofu only with synthetic output. Version prereleases and unsupported majors/minors are refused; format major 1 is accepted with unknown fields ignored. The common `show -json` subset is documented by [Terraform](https://developer.hashicorp.com/terraform/cli/commands/show) and [OpenTofu](https://opentofu.org/docs/cli/commands/show/). Real providers/versions need a disposable-workspace certification matrix before release.

Inherited credential, workspace and CLI configuration variables are retained. Injected `TF_CLI_ARGS` for the active command are rejected unless all arguments are `-no-color`, preventing file-output/path overrides. `TF_LOG*` is removed, `TF_LOG=OFF` and `CHECKPOINT_DISABLE=1` are forced, and raw stderr is discarded. Diagnostics explain safe troubleshooting categories rather than returning secret-bearing CLI text. This deliberately trades diagnostic specificity for protection in M1.

## Snapshot parsing and redaction

The parser consumes at most 64 MiB of official `show -json` output. It refuses plan JSON, trailing JSON, duplicate/invalid instances, more than 100,000 resources, and module nesting beyond 128. Module paths and resource addresses are retained **exactly as emitted by the CLI**. Dots inside quoted keys are never split. Deterministic opaque IDs derive from canonical addresses; maps index IDs, resource types and provider sources. Root/child module indexes include their direct instance counts. Managed and data modes remain separate.

Attributes are copied through the redactor before a snapshot is published. Values are shown unless they are likely secrets: anything Terraform/OpenTofu marks sensitive (at any depth), attributes whose names contain secret-like words (`password`, `secret`, `token`, `private_key`, `access_key`, `connection_string`, `user_data`, …), and credential-shaped strings (PEM blocks, JWTs, bearer tokens, AWS access key IDs, URLs with embedded passwords, known vendor token prefixes, and long random-looking strings, including plain hex of 20+ characters). Booleans and numbers are shown unless marked or under a secret-like name; strings over 16 KiB are hidden. Nested objects remain navigable; excessively deep values and suspicious map keys are masked. State outputs and raw JSON are never exposed. Potentially unsafe address/provider metadata rejects the snapshot rather than returning misleading altered addresses.

This matches what `terraform plan` itself prints, plus heuristics for secrets a provider failed to mark. It replaced an allowlist of 25 attribute names, which hid most real provider values (tags, enums, booleans, IDs) and made plan review unable to show what changed. The tradeoff: a secret that is not marked sensitive, sits under an ordinary name and does not look random (for example a short human-chosen password in a `description`) is shown. Hex digests such as etags are hidden because plain-hex API keys look the same. There is no reveal switch. Raw subprocess bytes exist only transiently in memory; no state, logs, operation history or credentials are written by Hclaro. Installed engine/provider plugins remain trusted local executables with their own behavior.

## Plans and operations

A plan runs only after `POST /api/v1/plans` with `acknowledgeBackendAccess: true`, which the UI sends only from its preflight dialog. The operation manager allows one operation at a time and refuses new work during shutdown.

1. **Checking workspace.** `workspace show` must still match the workspace recorded at launch; otherwise the run fails with `workspace_changed`.
2. **Planning.** `os.MkdirTemp` creates a private `0700` directory under the OS temp directory (never the project). The engine runs `plan -input=false -json -out=<dir>/plan.tfplan [-var-file=<name>…]` through `exec.CommandContext`. `-lock=false`, `-refresh=false`, `-target`, `-destroy` and arbitrary flags cannot be requested; `TF_CLI_ARGS`/`TF_CLI_ARGS_plan` may only contain `-no-color`. Variable files are bare names chosen from a server-side list of `*.tfvars`/`*.tfvars.json` files in the project root (auto-loaded files, symlinks and unusual names excluded).
3. **Reading plan.** The artifact is set to `0600`, hashed (SHA-256), read back with `show -json <path>` and parsed. If the plan can be applied (see below), the directory is kept as the single retained artifact; otherwise it is removed at once. It is always removed on failure, cancellation, timeout and shutdown.

A replacement plan is the same command with one `-replace=<address>` per selected instance appended. The request carries opaque resource IDs; the manager resolves them against the current snapshot, accepts only `managed` instances, refuses duplicates and more than 50, and passes each exact address as one argv element (quotes and brackets reach the engine verbatim). `-target` is never used. The stored plan records `kind: "replace"` and the requested addresses, and gets a warning for each requested address the plan does not actually replace.

Cancellation sends an interrupt so the engine can stop providers and release a state lock, then kills it after 15 seconds. On Windows the engine starts in its own process group and receives `CTRL_BREAK`, which Go programs such as Terraform and OpenTofu handle as an interrupt; if Hclaro has no console to send it from, the engine is killed immediately. Plans time out after one hour. Shutdown cancels a running plan and waits up to 20 seconds for cleanup. Planning never writes state; a forcefully stopped engine may leave a lock, which Hclaro never removes.

### Saved-plan apply

A plan is **eligible** when the engine reports it applyable, it is not errored, has no deferred changes and has at least one resource (other than read/no-op) or output change, and the workspace capability allows apply. Only the most recent eligible plan keeps its artifact; a newer one supersedes it. An artifact expires after one hour (timer plus a check on every request).

1. **Status.** `GET /api/v1/plans/{id}` includes `apply: {available, reason, artifactRetained, expiresAt}`, computed per request: artifact still held for this plan, not expired, no operation running, workspace not loading, engine/version/workspace unchanged, and the [context fingerprint](#plan-review-model) recomputed and equal.
2. **Approval.** `POST /plans/{id}/approval` repeats those checks and returns a 256-bit token valid for five minutes, the action counts, the requested replacements, the command preview and, for destructive plans (delete, replace, forget or unknown actions), the phrase `apply <workspace>`. A new approval replaces an unused one.
3. **Apply.** `POST /plans/{id}/apply {confirmationToken, confirmation}` consumes the token on any use (a mismatch or replay needs a new approval), compares token and phrase in constant time, repeats the checks, then starts an `apply` operation. The run confirms `workspace show` still matches and that the file's SHA-256 equals the reviewed digest, then executes `apply -input=false -json <file>`. `-auto-approve` is not passed: a saved plan never prompts. `TF_CLI_ARGS_apply` may only contain `-no-color`.
4. **After.** The artifact is deleted after the attempt whatever the result, so a plan is applied at most once; there is no fallback re-plan. If the engine ran, state is re-read through the normal read path. The operation reports `outcome`: `applied`, `not_applied` (refused before the engine ran, or a stale plan/lock refused by the engine) or `uncertain` (failed or interrupted after starting), plus `stateReloaded`.

On startup Hclaro deletes `hclaro-plan-*` directories in the temp directory that are older than plan expiry plus the apply timeout. They can only be left by a process that was killed; the age limit keeps another running instance's artifact safe, and symlinks are not followed.

The engine's own stale-plan check (state serial/lineage) is the secondary defense for changes Hclaro cannot observe, such as another process writing state; it is classified as `stale_plan`. Cancelling an apply interrupts the engine like a plan; this can leave resources partly changed, which the UI says. Applies time out after two hours.

### Imports

An import is an import block in a new configuration file plus an ordinary plan; Hclaro never runs the `import` command.

1. **Preview.** `POST /api/v1/imports/preview {address, id, provider, file, generateConfig}` is side-effect free (POST only so the ID never appears in a URL). `internal/imports` parses the address into a strict subset of resource instance addresses: optional `module.<name>[key]` steps, then a managed `type.name[key]`; string keys may not contain quotes, backslashes, `]` or template sequences, so the address is written verbatim. The ID must be 1–2048 characters without control characters or surrounding spaces; credential-shaped IDs (`state.Suspicious`) are refused because they would be written to configuration in plain text. It is rendered as an HCL string with `\`, `"` escaped and `${`/`%{` doubled. The destination must be a new bare `*.tf` name in the root that is not an override file. Checks against the current snapshot and configuration:
   - **State.** The address must not be in state; another managed instance of the same type whose recorded `id` equals the import ID is a collision (the comparison uses the redacted snapshot, so an `id` hidden as secret-like cannot be matched). If the snapshot is unreadable, the import is blocked, except when no state has been recorded at all.
   - **Configuration.** The resource block is found by a textual scan of the declaring module's `.tf`/`.tofu` files (and `resource` objects in `.tf.json`). Child modules are located through `.terraform/modules/modules.json`; module directories outside the project and data directory are not read. A missing block blocks the import unless configuration generation is requested and possible; an unreadable or unknown module is a warning. Top-level `count`/`for_each` in the block (strings, comments and heredocs skipped) is compared with the instance key; mismatches are warnings because the scan is not an HCL parser.
   - **Generation.** Allowed only for root-module, unindexed addresses with no existing block and a capable engine. The engine writes `<file stem>_generated.tf`, which must not exist.
   - **Duplicates.** A `to = <address>` line in a root file (import or moved block) blocks a second import block.
   The response includes the exact file content, a SHA-256 over the destination names and content, and a manual recipe (block, `plan -out`, `show`, `apply` of the saved file; the legacy `import` command labeled as writing state without a plan, with POSIX shell quoting).
2. **Start.** `POST /api/v1/imports` repeats the request with `previewSha256`, `acknowledgeWrite: true` and `acknowledgeBackendAccess: true`. Under the operation lock the manager re-runs every check, requires the digest to match, and writes the file: a `0644` temporary file in the root (`.hclaro-import-*.tmp`) is written and synced, then hard-linked to the final name, which fails if anything (including a dangling symlink) exists there; the project directory must still resolve to the launch root. Where hard links are unavailable it falls back to an exclusive create. Then the plan starts as in [Plans and operations](#plans-and-operations), with `-generate-config-out=<name>` when requested.
3. **Review.** The stored plan has `kind: "import"`, the target, and `imports` (changes with `importing`, whatever their action). A warning is added if the plan does not import the target. A generated file is read back (regular file, at most 1 MiB) and returned only as a display copy with values under secret-like names, credential-shaped or random-looking values, known input values and heredoc bodies replaced. The [context fingerprint](#plan-review-model) is taken before the plan with the generated file excluded; after the plan it is retaken the same way and, only if unchanged, replaced by the full fingerprint including the new file. Any other edit made while planning therefore makes the plan unapplicable, and any later edit to either file does too.
4. **Apply.** As for any plan. An import-only plan is a `no-op` action, so imports count as changes for eligibility. Because an import writes state, the typed phrase is required whenever `imports > 0`. After an apply, state is re-read and the imported instance appears in the explorer.

Hclaro never deletes the files it wrote: they are the user's configuration once approved. Recovery after a failed plan or apply is described in the UI (retry with any plan, or delete the files to back out). Apart from configuration the engine generates on request, the import file and its short-lived temporary file are the only writes Hclaro makes in the project root.

### Progress logs

The engine's machine-readable UI stream (`-json`) is translated, not forwarded. Hclaro renders only typed events: refresh start/complete (address only, no IDs), planned change (address, action, reason), drift, apply start/complete/errored (address and action, no IDs), change summary, output count, diagnostics and generic log messages. Apply progress ticks and provisioner output are dropped; provisioner output can echo secrets. Addresses that look like secrets are replaced. Free text (diagnostic summaries/details and log messages) is cleaned in two steps. First, known input values are replaced with `[redacted]`: `TF_VAR_*` environment values and string values (quoted literals, heredoc lines, JSON strings) from the selected variable files and the files the engine loads automatically (`terraform.tfvars`, `*.auto.tfvars`, with `.json` variants). Values shorter than four characters are skipped. Collection over-matches, so map keys and non-secret inputs such as a region name are redacted too. The values are held only while the plan runs. Then a line sanitizer withholds credential-shaped lines and assignments to risky attribute names, strips control characters, and truncates. The line checks are still heuristic: a secret that is neither a known input nor recognizable, such as one returned by a provider API, can still be shown. Non-JSON stdout is withheld. stderr is never returned; it is only keyword-classified (`state_locked`, `credentials`, `init_required`, `network`).

Each operation keeps a ring buffer of at most 10,000 events or 2 MiB, whichever is reached first; dropped events are counted and reported. Logs for the 10 most recent operations and metadata for 100 are kept in memory only. Nothing is persisted.

### Plan review model

`internal/plan` accepts only `show -json` plan documents (format major 1 with `planned_values`; state documents are refused) up to 64 MiB and 100,000 changes. Actions are classified from the ordered list: `create`, `update`, `delete`, `read`, `no-op`, `forget`, and `replace` for both `delete,create` (destroy before create) and `create,delete` (create before destroy). Any other combination is `unknown`, counted, and flagged as potentially destructive. CLI order is preserved. Every resource change is kept, including no-ops, data reads, deposed objects, moves (`previous_address`) and imports. Drift (`resource_drift`) is kept separately. Output changes are kept; their values go through the same policy, with the output name as the key. `deferred_changes` produce a warning. Errored plans are flagged.

Attribute diffs are computed on **raw** values, then redacted, so a change to a sensitive value is still listed as a change. Each row has a path (`tags["Name"]`, `ingress[0].cidr_blocks`), before/after values through the state redaction policy (applying the nested sensitivity marker at that path, so a row whose value is an object still hides its marked children), and markers for `unknown` (`[known after apply]`), `sensitive` (from `before_sensitive`/`after_sensitive`), and `forcesReplacement` (from `replace_paths`). Rows are capped at 1,000 per change. Secret-shaped addresses, output names or deposed keys reject the plan rather than display altered identifiers.

A stored plan records the engine, workspace, variable files, requested replacements, artifact SHA-256 and a context fingerprint taken **before** the engine runs, so edits made while planning count as a change. The fingerprint is a SHA-256 over:

- engine, version, workspace and selected variable files;
- configuration, variable and lock files under the root (hidden directories skipped);
- from the data directory (`.terraform` or `TF_DATA_DIR`): backend metadata, the selected workspace file, the module manifest and installed module sources;
- the planning environment: `TF_VAR_*`, `TF_WORKSPACE`, `TF_DATA_DIR`, `TF_CLI_ARGS`, `TF_CLI_ARGS_plan` and `TF_CLI_CONFIG_FILE`.

The plan detail response recomputes it and reports `contextChanged`; approval and apply refuse any mismatch. Hclaro's environment is fixed for its lifetime, so the environment part matters only once fingerprints are compared across launches. Provider plugin binaries are covered only through the lock file's checksums. The fingerprint does not detect provider-side or state changes; the engine's stale-plan check covers state.

## Workspace Hub

### Vocabulary

A **project** is one canonical root folder, an optional engine preference and display metadata. A **CLI workspace** is a state namespace inside a project. The **CLI-default** workspace is the one recorded in the project's data directory (`environment`), which `workspace list` marks. A **UI-selected** workspace exists only in a browser tab's URL and in the read context it opens. A profile, account, backend or `terraform.workspace` value is none of these, and nothing is inferred from names except a visible "production-like name" hint (`prod`, `production`, `prd`, `live` as words), which never implies criticality.

### Registry

`projects.json` lives in `os.UserConfigDir()/hclaro` (or `HCLARO_CONFIG_DIR`, absolute only). It is `{"version":1,"projects":[{id, alias, root_path, engine_preference, pinned, added_at, last_opened_at, last_workspace}]}`; array order is the user's order. IDs are 128 random bits. Nothing else is stored: no environment values, credentials, backend configuration, engine paths or state.

Writes go to a `0600` temporary file in the same directory, are synced and renamed over the old file. Every change re-reads the file first, so another Hclaro process's edits survive unless both write at the same instant (last writer wins; the file is never torn). On startup, a file that cannot be decoded (invalid JSON, unknown fields, invalid entries, over 1 MiB) is renamed to `projects.json.corrupt-<UTC time>` and the registry starts empty, with a notice. A file from a newer schema, an unreadable file, or a file that becomes undecodable during the session is never overwritten: the registry is read-only until restart. Up to 200 projects.

Adding a project requires an absolute path (no `~`), cleaned and resolved with `EvalSymlinks`, that is a readable directory with a recognized file (`workspace.Root`). Duplicates are refused by canonical path and by `os.SameFile`, so a symlink or case variant of a registered folder is rejected with the existing ID. Aliases are 1–80 characters with no control or format characters (bidi overrides included) and no surrounding spaces; the default is the folder name. Repairing a path validates the new one the same way. Removing an entry deletes only the entry.

The folder chooser (`POST /projects/browse`) lists the subdirectory names of one folder, default the home directory: hidden names and `node_modules` skipped, no files, at most 500 entries, and only the listed folder itself is checked for project files. It never recurses.

### Status and inspection

`GET /projects` runs no command. Each card's status comes from the file system: `missing`, `unreadable`, `path_changed` (the stored canonical path now resolves elsewhere, for example a parent replaced by a symlink), `not_project` or `ready`; ready projects add `workspace.Inspect` (initialized, backend type, lockfile).

`POST /projects/{id}/inspect`, which the UI sends when a project page opens, resolves the engine (preference, else Terraform then OpenTofu) and runs `version -json` and, for initialized projects, `workspace list`. Both drop any inherited `TF_WORKSPACE`; for `workspace list` this makes the marked entry the persisted CLI default. The list parser accepts the shared text format (`* ` marks the selected one), returns at most 1,000 names matching `^[A-Za-z0-9_-]{1,90}$` that do not look like credentials, and counts the rest as hidden. The result is cached in memory for the session.

### Read contexts

`POST /contexts {projectId, workspace}` returns an immutable context: project ID, canonical root, engine name and executable, workspace and `TF_DATA_DIR`, plus its own `workspace.Store`. The same tuple returns the same context (`reused: true`) unless `reload: true`, which closes it and starts a fresh read. At most eight contexts are kept; the oldest is closed first. Reads run in the background, three at a time, under a context cancelled on shutdown.

A context's engine is an `engine.CLI` with `SelectedWorkspace` set: each child's environment drops the inherited `TF_WORKSPACE` and gets `TF_WORKSPACE=<workspace>`. Hclaro's process environment is never modified, and no route runs `workspace select`, `new` or `delete`. The store is created with `workspace.NewScoped`; its read is:

1. `version -json`; then the usual initialization check.
2. `workspace list` (no `TF_WORKSPACE`): the workspace must exist, otherwise `workspace_missing` and nothing else runs.
3. `workspace show` must print the selected workspace. This proves the engine honored `TF_WORKSPACE`; otherwise `workspace_mismatch` and no state is read.
4. `show -json`, parsed and redacted exactly like the launch project.
5. Before publishing, the read must not have been cancelled and the root must still be the same directory (`os.SameFile` and `EvalSymlinks`); otherwise `path_changed` and the snapshot is discarded.

The engine is wrapped so only `Version`, `Workspace` and `State` are reachable; scoped capabilities are always `canPlan`, `canApplySavedPlan` and `canImport` false with one reason. Plans, applies and imports stay bound to the launch store and its CLI-selected workspace.

An absolute `TF_DATA_DIR` applies to every project at once, so a shared data directory could pair one project's configuration with another's backend initialization: inspection reports `shared_data_dir` and contexts are refused. A relative value resolves inside each root and is shown read-only. `TF_CLI_ARGS*` keeps the M1 rule (only `-no-color`).

Unregistering a project, repairing its path or changing its engine closes its contexts at once and cancels reads in progress; their IDs answer `404 context_closed` with the reason (`project_removed`, `project_changed`, `reloaded`, `evicted`). A context ID only ever reads its own store, so a resource ID from one context is never found through another. Context responses carry no executable path or environment values.

### Initialization

`GET /projects/{id}/init-status` is the read-only preflight. It resolves the engine with `exec.LookPath` (no execution), inspects the folder (`workspace.Inspect`, plus a textual check for a declared `backend`/`cloud` block) and returns the canonical root, engine, the session's inspected version and CLI default if any, data directory, lock file presence, the display command, notices about inherited settings (names only), the latest init operation, and either `canInit` with a `contextId` or a reason (`missing`/`path_changed`/… project status, `shared_data_dir`, `missing_cli`, `unsafe_cli_args`, `busy`, `too_large`).

The context ID is SHA-256 over: project ID, canonical root, engine preference, resolved engine name and executable path, the executable's size/mode/mtime, `workspace.Fingerprint` of the root (configuration, variable and lock files plus the data directory's backend metadata, `environment` and module manifest) and every `TF_*` variable except `TF_VAR_*` and `TF_LOG*`. Values are hashed, never returned.

`POST /projects/{id}/operations/init {mode:"standard", expectedContextId}`:

1. Recomputes the preflight; refuses with its reason, `project_busy` or `context_changed`.
2. Claims the root in the shared `rootlock.Set` (`project_busy` if held), snapshots `os.Environ()`, resolves an `engine.CLI` with `Environ` pinned and `IgnoreInheritedWorkspace`, stats the root and executable and recomputes the context ID with the pinned values.
3. Starts an `operations.Inits` operation (status `queued`) and returns 202. In the background: `version -json` (unsupported or injected flags fail here), choose JSON output (`engine.InitJSON`: Terraform ≥ 1.9 only; OpenTofu's `init -json` has not been verified, so it gets human output), then `Revalidate` (root still resolves to itself and `os.SameFile`, executable `os.SameFile`, context ID unchanged) and only then `init`.
4. The engine runs in its own process group (`Setpgid` on Unix, `CREATE_NEW_PROCESS_GROUP` on Windows). Cancellation sends an interrupt to the group and kills the group after the 15-second grace period. A 30-minute limit applies.
5. JSON mode renders `version`, `init_output`, `log` and `diagnostic` events through the plan translator; other stdout lines are withheld with one notice. Human mode and stderr go through `humanLine`: control and ANSI sequences and diagnostic box borders stripped, `Error:`/`Warning:` split off and recorded as diagnostics, the rest `sanitize`d. The scrub list is the plan's known inputs plus values of credential-shaped environment variables. Raw output is never stored; classification uses the sanitized diagnostics and lines, then a keyword hint from the engine's bounded output tail (`engine.ClassifyInit`).
6. Outcome: `succeeded` only for exit 0 and an existing data directory; exit 0 without one is `unknown` (`unverified`); cancel, shutdown and timeout are `cancelled`/`failed` with their own codes; otherwise `failed` with `backend_migration`, `input_required`, `state_locked`, `lockfile_upgrade`, `version_mismatch`, `registry`, `credentials`, `network`, `backend_config` or `init_failed`. Whether `.terraform.lock.hcl` changed is reported once the engine has run. `Finished` drops the project's inspection and closes its contexts (`initialized`), then the claim is released.

While the root is claimed, `inspect` and `contexts` for that project answer `409 project_busy`, and the launch manager's plan, import and apply refuse to start in that root (and vice versa). Init operations share `/operations/{id}`, `/events` and `/cancel` with launch operations; the last 50 are kept, with logs for the last 10. Statuses add `queued`, `cancelling` and `unknown`. The browser disconnecting never changes an operation. Removing or repairing a project while its init runs does not stop it; the operation stays pinned to the root it validated.

### Client

The UI routes are `/projects`, `/projects/<id>` and `/projects/<id>/workspaces/<name>/<resources|overview|modules|providers>`; the server serves the app shell only for paths matching exactly that shape (32-hex ID, workspace-name pattern) and 404s everything else as before. The client parses the same shape, percent-encodes segments and never sends a path from the URL; it sends the project ID and workspace name in a POST body, and the server resolves both. Each project/workspace gets its own `Workbench` instance (React `key`) and query keys that include the context ID, so filters, selections and cached resources cannot carry over. A new tab receives the session credential in its URL fragment, like the launch URL, because sessionStorage is per tab.

## HTTP contract

Every API endpoint requires `Authorization: Bearer <launch-token>`:

| Route | Response |
| --- | --- |
| `GET /api/v1/health` | Process/version status |
| `GET /api/v1/workspace` | Loading/ready/empty/error, safe context, capabilities, summary indexes, diagnostic |
| `GET /api/v1/resources` | Summary page (default 50, max 200) and total matches |
| `GET /api/v1/resources/{opaque-id}` | One redacted instance |
| `GET /api/v1/session` | Per-launch CSRF token and version |
| `GET /api/v1/plans/options` | Plan and apply availability and reasons, engine, workspace, backend, selectable variable files |
| `POST /api/v1/plans` | Start a plan: `{"acknowledgeBackendAccess":true,"varFiles":[],"replace":[<resource id>…]}` → 202 operation; 409 if one is running or planning is unavailable; 400 `invalid_replace` |
| `GET /api/v1/plans`, `GET /api/v1/plans/{id}` | Plan summaries; one redacted plan with metadata, `contextChanged` and `apply` status |
| `POST /api/v1/plans/{id}/approval` | `{}` → 200 one-time approval; 409 `{error, reason}` when the plan cannot be applied |
| `POST /api/v1/plans/{id}/apply` | `{"confirmationToken":…,"confirmation":…}` → 202 apply operation; 409 `{error, reason}` (`approval_required`, `approval_invalid`, `confirmation_mismatch`, `context_changed`, `plan_expired`, `not_applicable`, `operation_running`) |
| `GET /api/v1/plans/{id}/changes/{change-id}` | One change with attribute diff and redacted before/after trees |
| `GET /api/v1/operations`, `GET /api/v1/operations/{id}` | Operation metadata: kind, status, stage, duration, exit code, redacted command, replacements, apply outcome, diagnostics |
| `GET /api/v1/operations/{id}/events` | SSE: `log`, `status`, `truncated`, `end`; resume with `?after=<seq>` or `Last-Event-ID` |
| `POST /api/v1/operations/{id}/cancel` | Request cancellation (`{}` body) → 202; 409 if not running |
| `GET /api/v1/imports/options` | Import availability and reason, import-block and generation support, apply availability, engine, workspace, backend, variable files |
| `POST /api/v1/imports/preview` | `{address, id, provider, file, generateConfig}` → 200 preview: issues (error/warning), `blocked`, config status, exact content, `sha256`, recipe. No side effects |
| `GET /api/v1/projects` | Registry status and notice, launch root, inherited environment flags (`TF_WORKSPACE` name when valid, `TF_DATA_DIR`, whether `TF_CLI_ARGS*` is set) and project summaries. Runs no command |
| `POST /api/v1/projects` | `{path, alias?, engine?}` → 201 summary; 400 `invalid_path`/`invalid_alias`/`invalid_engine` with reason; 409 `duplicate_project` with `existingId`, `registry_unavailable`, `too_many_projects` |
| `PATCH /api/v1/projects/{id}` | `{alias?, pinned?, engine?, rootPath?}` → 200 summary; same errors, 404 `project_not_found` |
| `DELETE /api/v1/projects/{id}` | `{}` → 200; unregisters only |
| `POST /api/v1/projects/order` | `{ids}` (a permutation) → 200; 400 `invalid_order` |
| `POST /api/v1/projects/{id}/inspect` | `{}` → 200 summary with `inspection` (engine, version, workspaces, `cliDefault`, hidden count, diagnostic) |
| `POST /api/v1/projects/browse` | `{path}` (empty for home) → 200 folder listing; 400 `browse_failed` |
| `POST /api/v1/contexts` | `{projectId, workspace, reload?}` → 200 context with `reused`; 400 `invalid_workspace`; 404 `project_not_found`; 409 `project_unavailable`, `shared_data_dir` |
| `GET /api/v1/contexts/{cid}`, `…/resources`, `…/resources/{id}` | Same shapes as `/workspace` (plus `context`), `/resources` and `/resources/{id}`, scoped to the context; 404 `context_closed` with reason |
| `GET /api/v1/projects/{id}/init-status` | Read-only init preflight: context, notices, display command, latest init operation, `canInit` + `contextId` or `reasonCode`/`reason`. Runs no command |
| `POST /api/v1/projects/{id}/operations/init` | `{"mode":"standard","expectedContextId":…}` → 202 init operation; 400 `invalid_mode`/`invalid_context`/`invalid_request`; 404 `project_not_found`; 409 `context_changed`, `project_busy` or the preflight reason (`unsafe_cli_args`, `missing_cli`, `shared_data_dir`, …) |
| `POST /api/v1/imports` | Preview fields plus `previewSha256`, `varFiles`, `acknowledgeWrite`, `acknowledgeBackendAccess` → 202 plan operation; 400 `acknowledgement_required`; 409 `{error, reason}` (`import_blocked`, `preview_changed`, `import_unavailable`, `file_exists`, `write_failed`, `operation_running`) |

List parameters: `query`, exact `module`, `provider`, `type`, `mode`, `sort=asc|desc`, `offset`, `limit`. An absent module filter means all modules; `module=` selects root resources. Queries search full address, name, provider, type and module. Details use an ID map; no raw address becomes a command argument.

The guard accepts only the exact bound authority and, when present, the exact matching Origin. Cross-site fetch metadata is refused, no CORS headers are emitted and queries are bounded. GET/HEAD requests may not carry bodies. POST is accepted only on the plan, apply, cancel, import, project, browse, inspect, init and context routes above, and PATCH and DELETE only on `/api/v1/projects/{id}`; each additionally requires: an `Origin` header equal to the bound origin, `Sec-Fetch-Site` absent or `same-origin`, `Content-Type: application/json`, an `X-Hclaro-CSRF` header matching the per-launch CSRF token (constant-time compare), a non-empty body of at most 16 KiB, and strict JSON decoding with unknown fields rejected. Every other method/path combination returns 405. Unknown API paths cannot fall back to SPA HTML. HTTP header/body/idle/write limits bound requests. Assets cannot be directory-listed. CSP permits only local scripts/styles/fonts/connections and denies framing/forms; referrers and caching are disabled.

A private launch fragment bootstraps bearer authorization. The UI removes it from history immediately, keeps the token in sessionStorage for reload, and sends it only in headers. The token expires when the process stops; new launches use independent tokens. No cookies are used. Because the bearer token is never sent automatically by a browser, cross-site requests cannot authenticate; the CSRF header and Origin checks are additional, independent defenses for the mutating routes.

SSE streams are read with `fetch` (not `EventSource`) so the bearer token stays in a header. Request contexts derive from the server lifetime, so open streams end on shutdown; each event extends the write deadline.

## Frontend and performance

The UI requests workspace status while loading, then uses the immutable snapshot. TanStack Query caches reads without focus/reconnect refresh. Search is debounced 180 ms, filters compose on the server, and resource pages render at most 50 rows. This bounds DOM work independently of total instances. Only selected details are downloaded. Resource selection stays mounted during detail changes, preserving list scroll. Module/attribute structures use native disclosures, resource buttons support arrow navigation, and reduced-motion preferences are respected.

No runtime fonts, CDNs, analytics or hosted APIs are used. Display preferences are validated before use and are the only localStorage data. The sidebar, dashboard, modules and providers all derive from real snapshot counts.

The Resources view adds a checkbox to managed rows only; the selection (kept across pages and filters, at most 50) opens the replace preflight, which lists the exact addresses. The Plans view shows plan availability, a preflight dialog (providers and backend contacted, possible state lock, no infrastructure change), the live operation log with cancel, failures with sanitized diagnostics, and the review: metadata, for replacements the requested instances next to a count of every other change, apply status (ready with expiry, or the reason it is review only), action counts that double as filters, the complete change list (no-ops behind a toggle, 200 rows per page), a change inspector with attribute diff, outputs, and drift. The apply dialog requests the approval when it opens, shows counts, command and digest, and keeps **Apply plan** disabled until the typed phrase matches; Cancel has initial focus. After an apply the explorer queries are invalidated and the outcome is shown separately from the exit status. Actions are distinguished by icon and text, not color alone. The Operations view lists session plans and applies and replays retained logs. The Import view is a single page with numbered steps: the request form, the server preview (issues, configuration status, the new file as an added-lines diff, a manual recipe), and, only when nothing blocks, a consent checkbox before **Write file and plan import**. Any edit discards the preview. Import plans get a summary of the target, other changes and the redacted generated configuration; failures show recovery steps.

The build is TypeScript + Vite + Tailwind, with React, TanStack Query, Lucide and a shadcn-style Button. Vite writes content-hashed files to `internal/ui/dist/assets/` plus `THIRD-PARTY-NOTICES.txt` for runtime dependencies; the Go binary embeds that directory. `scripts/build-offline-preview.mjs` remains only as a no-network fallback and must not be committed as release assets.
