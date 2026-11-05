# PRD Addendum — Hclaro M05–M08 (Post-M04 Expansion)

**Version:** 1.2  
**Date:** 2026-11-05  
**Status:** Additional milestones approved as requirements, not yet implemented  
**Scope rule:** M01–M04 remain intact, in their original order, and their existing acceptance criteria are not altered. The new milestones are incremental follow-ups; they must not be injected into the first Codex task. This section extends the base PRD, rather than changing v0.1 deliverables.

## Product objective

Introduce a local **Workspace Hub** that manages multiple independent Terraform/OpenTofu project roots and CLI workspaces in one Hclaro process. Add explicitly invoked basic CLI maintenance operations (`init`, `validate`, `fmt -check`, workspace create/select/delete, provider metadata and output metadata) while preserving the original local-first, transparent, secure product philosophy.

### Key vocabulary

| Term | Correct interpretation | Not interchangeable with |
|---|---|---|
| Project root | One root module directory, its backend and CLI data directory | CLI workspace or cloud account |
| CLI workspace | Named state namespace inside a Terraform/OpenTofu project | Separate root module or AWS profile |
| UI-selected workspace | Hclaro tab/session target for reads and operations via child env `TF_WORKSPACE` | Persistent local CLI default |
| CLI-default workspace | Selection persisted in project's Terraform/OpenTofu CLI data directory | UI tab selection |
| Execution context | Immutable engine, version, root path, data directory, workspace and allowed env for a single command | Mutable application global state |

### End-to-end user stories

- I can register `infra/dev`, `infra/qa`, `infra/prod` and any other Terraform/OpenTofu root; Hclaro remembers those paths without storing secrets.
- I can open multiple projects and CLI workspaces from the same browser session and search/filter resources without accidentally mixing the state.
- For a newly cloned project, I can explicitly invoke Init, inspect that it will download providers/modules and possibly update `.terraform.lock.hcl`, watch safe streamed progress and retry after a recoverable error.
- I can create a workspace with clear warnings that `workspace new` changes CLI's local selected workspace; selecting another workspace in UI alone never does.
- I can Validate and Format Check before editing/applying; view providers and metadata-only outputs without exfiltrating values.

## Dependency and sequencing

**M01 → M02 → M03 → M04 → M05 → M06 → M07 → M08.** Codex runs each task individually. Later tasks may reuse/extend existing secure execution abstractions. Do not rewrite previously delivered functionality or modify initial `CODEX_START_HERE.md`.

## Functional requirements by new milestone

### M05: Multi-Project & Workspace Hub

- Project registry: explicit add/unregister/rename/pin, canonical paths and stable IDs, versioned local metadata, no recursive discovery without consent.
- Hub screen with search, project health, backend/init status, engine version, workspace list, clear missing/unauthorized states, fast navigation.
- Per-tab/project workspace selection using per-child `TF_WORKSPACE`; no global process env mutation, no automatic `workspace select` or `init`.
- Existing `hclaro` from a valid project still opens that project; add an explicit Hub path/flag and fallback when not in a project.
- Resource Explorer always scopes reads to selected project and workspace; immutable project context and backend/data-directory guard.
- Success: 3 projects and multiple workspaces are navigable with isolated state, authenticated API, no cross-project confusion or credential leak.

### M06: Guided Init

- Add Initialize action to an uninitialized project; preflight lists root, engine, backend context, writable paths and expected effects.
- Standard, non-interactive `terraform/tofu init -input=false` with no hidden upgrade, migration, forced copy or reconfigure; implement safely with official CLI argv, explicit confirmation and per-root exclusive lock.
- Sanitized progress/events, cancellation, bounded logs, honest exit code, no auto retries; refresh post-init capabilities.
- Handle missing credentials/providers, network/backends, lockfile changes, backend migration required, unsupported versions and inherited `TF_CLI_ARGS` safely.
- Success: project only initializes on a user click; no state or credential exposure and no impact on unrelated root.

### M07: Workspace Lifecycle

- List and inspect CLI workspaces, switch **UI context** without altering the persistent CLI default.
- Create workspace via confirmed CLI operation; highlight that `workspace new` also selects it at the CLI level.
- Separate, explicit **Set CLI Default** action using `workspace select` when supported, with warning that user's terminal is affected.
- Optionally delete **empty** non-default workspace only with explicit name confirmation and no force. If safety can't be established, disable deletion and explain why.
- Success: tab-local selection is isolated; create/switch actions are correctly scoped, and no non-empty state is ever deleted by Hclaro.

### M08: Project Utilities

- Validate via capability-aware CLI JSON diagnostics, fallback robustly when JSON is unavailable.
- `fmt -check` only (optional recursive toggle), report affected paths without writing files.
- Providers list/report without auto-init or upgrades.
- Outputs **metadata only by default**; never forward raw `output -json` because it may expose unmasked secrets. Show names/type/sensitivity status when safely available; no value-reveal in M08.
- Common project-scoped Operation Center with status/history, sanitized logs and cancellation.
- Success: all basic actions are transparent and safe; never write IaC code, replace resources or expose secrets.

## API and data contract constraints

New endpoints must share M01's loopback-only session authentication, CSRF, Host and Origin protections. Resource and operation routes must resolve stored project IDs, not arbitrary client paths. Server creates a whitelist of CLI commands; it must reject arbitrary executable/args and hidden `TF_CLI_ARGS` variants that alter reviewed semantics. Each operation must bind an immutable context ID or revision, pin engine/root/workspace/TF_DATA_DIR and hold appropriate locks. Remote state and credential data stay local; only redacted summaries ever reach the browser. Never persist raw plan/state/outputs/history containing secrets.

Suggested domain entities: `Project`, `WorkspaceSummary`, `ExecutionContext`, `Operation`, `OperationEvent`, `InitStatus`, `Diagnostic` and `ProviderSummary`. Operations are either `read`, `local-write` or `state-affecting` and must have a visible risk classification. A local `init` is **not pure read-only**; it can write local metadata and contact remote backends.

## Security and acceptance gates

- Default commands never modify remote infrastructure as a side effect of opening a page.
- M06–M08 commands require explicit user gesture, engine whitelisting and appropriate confirmation for local/state changes.
- Commands honor per-project engine and backend context but never use a cross-project global `TF_WORKSPACE`.
- Sanitization occurs **before** output crosses process boundary to browser, operation events or logs. Redaction tests include a canary in state/outputs/CLI stderr.
- CLI workspace creation and default changes are acknowledged as effects; no silent switching. No `workspace delete -force`.
- Keep compatibility matrix for Terraform/OpenTofu versions and supported backends; errors should be honest when CLI behavior differs.
- Synthetic fixtures and fake CLI only in CI, never live cloud access from automated tests.

## Detailed Codex task specifications

- `tasks/M05-multi-project-workspace-hub.md`
- `tasks/M06-init-wizard.md`
- `tasks/M07-workspace-management.md`
- `tasks/M08-project-utilities.md`

## Deferred beyond M08

Provider upgrades and `init -upgrade`, backend reconfiguration/migration/force-copy, destructive state surgery, outputs value reveal, variable editing, remote policy enforcement, global multi-user state, and advanced approval infrastructure require separate milestones and explicit safety reviews.

## Authoritative references

- Terraform init: https://developer.hashicorp.com/terraform/cli/commands/init
- Terraform CLI env vars (`TF_WORKSPACE`, `TF_DATA_DIR`): https://developer.hashicorp.com/terraform/cli/config/environment-variables
- OpenTofu CLI env vars: https://opentofu.org/docs/cli/config/environment-variables/
- Terraform `workspace new`: https://developer.hashicorp.com/terraform/cli/commands/workspace/new
- Terraform `validate`: https://developer.hashicorp.com/terraform/cli/commands/validate
- Terraform `fmt`: https://developer.hashicorp.com/terraform/cli/commands/fmt
- Terraform `show` sensitive JSON warning: https://developer.hashicorp.com/terraform/cli/commands/show
