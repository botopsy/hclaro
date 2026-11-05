# M08 — Project Utilities: Validate, Format Check, Providers & Outputs

**Milestone:** M08  
**Depends on:** M05 project contexts and a secure operation runner from M06.  
**Deliverable:** A dependable `Project Tools` panel for common non-apply maintenance and diagnostics across registered Terraform/OpenTofu roots.

## UI and functional scope

1. **Validate** — explicit `terraform/tofu validate -json` when supported, fallback to supported human diagnostics. Show structured `error`, `warning`, source location (when present), file/line link (local display only), execution status and elapsed time. `validate` is **not** drift detection, remote verification or proof the plan can apply. Do not call `init` automatically if required; offer an Init action instead.
2. **Format Check** — explicit read-only `terraform/tofu fmt -check -no-color`; an optional, clearly labeled recursive check may use `-recursive`. Show list of files requiring formatting; optionally show sanitized `-diff` if user requests it and project-local paths are validated. **Do not** invoke plain `fmt` or write HCL in M08.
3. **Providers** — invoke supported `terraform/tofu providers` and show declared/configured requirements and warnings. Clarify that provider requirements do not prove active cloud account/identity. Do not download/upgrade anything automatically.
4. **Outputs** — show names, types, `sensitive` flags, and read/capability errors using a protected output inspection adapter. `terraform/tofu output -json` can return secret values in plaintext; **never** forward this raw output to the UI, cache, logs, or history. Default to key/metadata-only, with values omitted. Value reveal is **not included** in this milestone; build testable redaction from the backend before displaying anything.
5. **Operation center** — show active/completed operation status scoped to project and workspace, refresh, bounded sanitized log/diagnostic view, cancel where supported, and quick links to relevant project cards. History is local-only, opted-in or ephemeral, and never contains raw secrets.

## Execution contract

- Commands constructed server-side from a small whitelist, with argv arrays and fixed approved flags; no generic CLI textbox, arbitrary `-chdir`, file paths, shell, or executable override endpoint.
- Capture an immutable project context at request submission; use per-child `TF_WORKSPACE` for workspace-dependent reads; a command should remain bound even if UI selection changes mid-flight.
- Clearly mark formatting as **check-only**. Be honest that `providers` may reveal requirements even when no backend is initialized; `validate` might require installed providers.
- Explicitly enumerate what config files are traversed for optional recursive check; disallow traversing symlinked directories outside the chosen canonical root.
- JSON diagnostics can be preceded by plain-text failures; robustly handle non-JSON, invalid format, unsupported versions, exit codes and missing engines.
- Reject/reconcile inherited `TF_CLI_ARGS`, command-specific equivalents, conflicting `TF_WORKSPACE`, and untrusted paths. Keep auth/config secrets out of logs and frontend output.

## Suggested API

- `POST /api/projects/{id}/operations/validate` `{ "context_id": "..." }`
- `POST /api/projects/{id}/operations/fmt-check` `{ "context_id": "...", "recursive": false }`
- `POST /api/projects/{id}/operations/providers` `{ "context_id": "..." }`
- `POST /api/projects/{id}/operations/outputs-metadata` `{ "context_id": "..." }`
- Shared read-only authenticated GET for operation status and streaming sanitized events.

## Tests

- Valid/invalid HCL with structured warnings, errors and source ranges; mocked unsupported `validate -json` fallback; post-failure UI clearly says not validated.
- `fmt -check` returns changed-file list with no file modifications; directory traversal/symlink case blocked; optional recursive only when toggled.
- Providers command with missing plugin, missing binary, missing init, and version mismatch.
- Outputs JSON fixture containing a sensitive flag, a non-flagged secret and `DO_NOT_LEAK_HCLARO_TEST_SECRET`: no values or canary ever reach response, history, event stream, browser rendering, telemetry or local registry.
- Two parallel project runs and workspace switch mid-run maintain correct targeting; cancellation and partial process output are classified accurately.
- API/auth/CSRF/Host/Origin protections inherited and checked for each operation.

## Definition of Done

- User can run Validate, Format Check, Providers and Outputs Metadata for registered project via visible, truthful action controls.
- No operation writes HCL or applies cloud changes; outputs never leak values by default.
- All operations have protected, sanitized results and reliable exit statuses; genuine errors are not hidden.
- Automated Go tests, frontend tests/typecheck, cross-platform build and README instructions pass.

**Out of scope:** auto-format/writing HCL, `init -upgrade`, provider upgrades, cost estimates, outputs value reveal, state surgery, generic shell terminal and any cloud mutation.
