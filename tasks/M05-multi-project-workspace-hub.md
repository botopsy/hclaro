# M05 — Multi-Project & Workspace Hub

**Milestone:** M05  
**Depends on:** M01 (Explorer), optionally M02–M04; these are already defined and must not be rewritten.  
**Deliverable:** One local Hclaro process can discover, register, select, and navigate multiple independently configured Terraform/OpenTofu root projects and their CLI workspaces. **Read-only for infrastructure and state.**

## User problem and outcome

Today engineers repeatedly `cd` between `dev`, `qa`, and `prod`, change CLI workspaces, and relaunch inspection tools. Hclaro must offer a persistent, local project directory and a safe workspace context switcher without assuming that a Terraform *root directory* is the same thing as a Terraform *CLI workspace*.

A project is one canonical root-module path plus an engine and optional display metadata; a CLI workspace is a state namespace inside that project. Do **not** treat a provider account, `AWS_PROFILE`, backend, or `terraform.workspace` as an interchangeable concept. The UI must show exactly what it knows and report uncertainty.

## Scope and capabilities

1. **Hub navigation.** Add a `Projects`/`Workspace Hub` view accessible from every screen. Running `hclaro` from a valid Terraform root preserves M01's open-current-project flow, but offers the Hub; running outside a recognized root can open the Hub instead of failing; add an explicit `hclaro --hub` option. Keep existing `--path` flags and behavior backward-compatible.
2. **Explicit project registry.** User can add a project via local directory selection or typed root path, alias/rename it, reorder/pin it, view last-used metadata, and remove it from Hclaro. Persist only non-sensitive metadata in the per-user OS config directory; JSON with versioned schema and atomic writes is sufficient. Removing an entry must not delete IaC files, cache, state, or backend records.
3. **Selection and path safety.** Canonicalize paths (`EvalSymlinks` where applicable) and reject duplicates; verify existing directory, readable Terraform/OpenTofu root files, and safe traversal. Never scan an entire home directory recursively without explicit consent. Treat untrusted path and name strings as data; no shell. Allow disconnected/moved projects to remain listed with repair/remove actions.
4. **Project cards.** Show alias, canonical path, engine and version (or unavailable), initialization status, workspace count (when known), backend kind when safely detectable, last-opened time, and actionable errors. Avoid guessing a cloud account from the profile name. Treat remote backend credentials and `terraform.tfstate` as sensitive.
5. **Workspace navigator.** For initialized projects, retrieve CLI workspace names via supported official CLI behavior and render as tabs/list/dropdown with loading/empty/error states. A UI-selected workspace is **tab/session context**, not a global `terraform workspace select`. Support simultaneous tabs for `dev` and `prod` from one project without hidden global switching.
6. **Explorer integration.** All subsequent state reads use immutable `(projectId, canonicalRoot, engineBinary, selectedWorkspace, per-project data directory, safe execution profile)` snapshots. For child commands set `TF_WORKSPACE=<selected>` for supported non-interactive reads; document behavior when inherited `TF_WORKSPACE` conflicts. Project switching must never leak state/resources from the prior selection.
7. **UX.** Fast search across projects and workspaces, recently used project selection, keyboard-friendly navigation, accessible focus behavior, project error and onboarding states. Offer visible warning for production-like labels but never infer that a label guarantees environment criticality.

## Suggested API and local storage (illustrative)

- `GET /api/projects`: summaries; never return credentials or raw environment variable maps.
- `POST /api/projects`: explicit canonical path + optional alias, after request authentication and CSRF checks.
- `PATCH /api/projects/{id}`: alias/pin/metadata only.
- `DELETE /api/projects/{id}`: unregister registry entry only.
- `GET /api/projects/{id}/workspaces`: safe CLI read/list, with capabilities and errors.
- `GET /api/projects/{id}/resources?workspace=<name>`: scoped Explorer data; do not accept a raw path in query parameters.
- Client route: `/projects` and `/projects/:id/workspaces/:workspace/resources`; percent-encode route segments and never trust them for file access without server validation.

Registry example (not a schema commitment):

```json
{"version":1,"projects":[{"id":"uuid","alias":"Production infra","root_path":"/absolute/root","engine_preference":"terraform","pinned":true}]}
```

## Security and concurrency invariants

- All endpoints preserve M01 loopback binding, session authentication, Origin/Host checks and CSRF policy.
- No automatic `init`, workspace creation, `apply`, `destroy`, refresh, or state mutation on navigation.
- Treat Hclaro workspace selection as UI-only. `TF_WORKSPACE` should be set **per child process**, never mutate Hclaro process-global environment.
- Respect `TF_DATA_DIR` as part of the *project execution context*. Changing it can silently change backend initialization and available state. Show it read-only where safe; do not rewrite it on add-project.
- Keep project directory and engine executable identity stable for each operation; reject stale context or request that races with project deletion/unregister.
- Prevent symlink/path tricks, unauthorized registry edits, cross-project resource responses, and state secret leaks; server-side sanitize every resource view.

## Test plan (fake CLI, synthetic state only)

- Add two root projects (`terraform` + `tofu`), same display alias, different canonical roots: registry IDs and resources stay separate.
- Duplicate root via symlink rejected; missing/unreadable directory produces actionable error without discarding saved record.
- One project has workspaces `default`, `dev`, `prod`; opening `dev` and `prod` in different tabs produces correctly isolated state and never executes `workspace select`.
- Simultaneous project changes, unregister during an active read, unreadable registry, corrupt registry recovery, cross-site POST, request missing token, malicious paths.
- Empty state, remote backend unavailable, missing executable, excluded/unsupported CLI version, injected secret canary in CLI output are handled honestly and safely.
- Verify M01–M04 behavior remains unaffected; no invocation outside fixtures.

## Definition of Done

- User can register at least three distinct project roots from one Hclaro instance, reopen the app and find them preserved.
- User can switch project + workspace contexts without changing their terminal's selected CLI workspace.
- Explorer resources strictly match the chosen project/workspace; all secrets masked and permission checks enforced.
- No hidden init/mutation, no generic command API, all documented tests pass.

**Explicitly out of scope:** running init, creating/deleting workspaces, setting CLI-default workspace, multi-user sync, accounts/RBAC, SaaS. These are M06/M07 or later.
