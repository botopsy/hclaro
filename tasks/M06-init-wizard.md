# M06 — Guided Terraform/OpenTofu Init Wizard

**Milestone:** M06  
**Depends on:** M01 and M05 for project registry and immutable project context.  
**Deliverable:** Deliberate, transparent `terraform init` / `tofu init` from the Hub for user-selected root directories, with sanitized streaming logs and no implicit upgrades or backend migrations.

## User story

As a DevOps engineer, I can add an uninitialized project to Hclaro, inspect the project/engine/backend context, click **Initialize**, review the exact high-level operation and expected local effects, consent, and watch the result without switching to a terminal.

## UX flow

1. Detect not-initialized or needs-reinitialization and show **Initialize** explicitly. Do not initialize during project add, page load, refresh, or Explorer navigation.
2. Wizard summarizes selected alias, canonical path, engine + version, backend type if determined, workspace context where meaningful, local `TF_DATA_DIR` context, lockfile status, and warning that init can contact external registries/backends and write `.terraform/` and `.terraform.lock.hcl`.
3. Explain risks: credentials/backend access, plugin/module downloads, possible lockfile changes. Provide safe default `init -input=false -no-color`; explain that CLI/env flags may change semantics. Never display raw secrets or store backend credentials.
4. Confirm via user-triggered button with session token/CSRF protection. API creates an immutable operation record and obtains **exclusive project-root lock**; no concurrent init/upgrade/migration/other write action for same root.
5. Stream line/event updates over SSE or equivalent, with server-side redaction, bounded in-memory buffers, progress and cancel; never stream raw unredacted stdout/stderr, even briefly.
6. On exit refresh init status and project metadata; show success/error/cancel status, engine exit code, next steps and sanitized diagnostics. No false success states. Cancel must terminate process/process group and clean up as appropriate.
7. If CLI requests backend migration/reconfiguration or interactive input, stop and show guided remediation. **Do not** automatically retry with `-migrate-state`, `-force-copy`, `-reconfigure`, `-upgrade`, `-lock=false`, or `-backend=false`.

## Safe execution

- Build argv array, not shell command strings. `exec.CommandContext` bound to canonical allowed root. No API accepting arbitrary argv or executable paths.
- Force/verify `-input=false` for non-interactive operation. Use capability/version adapter for JSON events if supported, otherwise parse human output conservatively; never parse success based on text alone.
- Override or reject inherited `TF_CLI_ARGS` and `TF_CLI_ARGS_init` that would add hidden flags. Preserve user-authenticated credential environment without writing it to disk/logs and disclose any ignored implicit flags. Never accept secret `-backend-config` values through generic API fields.
- Pin engine path, root, `TF_DATA_DIR`, environment snapshot and config fingerprint when operation is enqueued. Revalidate before execute; reject changed project or root path.
- Distinguish authentication errors, network errors, unavailable provider registry, wrong backend config, version mismatch, existing state lock, and reconfiguration/migration required.
- Do not promise offline init: initialized providers/modules or remote backends may require network. Do not assume every backend permits workspace switching.

## Proposed endpoints

- `GET /api/projects/{id}/init-status` (read-only)
- `POST /api/projects/{id}/operations/init` with `{ "mode": "standard", "expected_context_id": "..." }`; server builds arguments itself; protected against CSRF.
- `GET /api/operations/{operationId}`; `GET /api/operations/{operationId}/events`; `POST /api/operations/{operationId}/cancel`.

Operation statuses: `queued`, `running`, `succeeded`, `failed`, `cancelling`, `cancelled`, `unknown`. Never use `succeeded` solely because the browser disconnected.

## Tests and negative cases

- Successful init with fake engine creates expected metadata, sanitized progress, supports retry and post-init Explorer navigation.
- Backend requires migration/reconfigure, interactive question, init fails due to credentials/network, version unsupported, CLI missing, timeout, stale root symlink, cancellation, browser closed while running.
- `TF_CLI_ARGS_init=-upgrade`, `TF_WORKSPACE` inherited, sensitive backend credentials in stdout/stderr, output containing `DO_NOT_LEAK_HCLARO_TEST_SECRET`, concurrent commands targeting same project.
- Two-project parallel operations prove project isolation. A failed command never triggers `apply`/`destroy` or a hidden retry.

## Definition of Done

- A real **user gesture** is required to init any project.
- Init is run for the correct project/engine using the explicitly reviewed safe mode; nothing initializes automatically.
- Streaming updates and cancellation work, no output leaks sensitive data, errors classify correctly, registry stays consistent.
- CI uses fake CLIs and synthetic fixtures only; no cloud account / real infrastructure access.

**Out of scope:** init upgrade, backend migration, forced copy, arbitrarily editing backend config, provider cache browser, `apply`/`destroy`. If needed, add a separately approved advanced milestone.
