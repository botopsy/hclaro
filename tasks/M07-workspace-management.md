# M07 — CLI Workspace Lifecycle & Safe Switching

**Milestone:** M07  
**Depends on:** M05 registry/navigation and M06 operation engine (or an equivalent shared authenticated command runner).  
**Deliverable:** Discover workspaces, create a workspace, choose the **Hclaro UI context** and intentionally change the **CLI-default workspace**, with optionally guarded deletion of empty non-default workspaces.

## Critical distinction

- **Switch in Hclaro UI**: select which state Hclaro reads/operates on for one tab; implemented through per-operation `TF_WORKSPACE`, **without** running `workspace select`.
- **Set CLI default**: explicitly run `terraform/tofu workspace select NAME`; this modifies the local CLI workspace selection for the working directory. Never conflate these buttons.
- **Create**: `terraform/tofu workspace new NAME` creates a workspace **and changes local CLI selection**. Warn before running; don't falsely promise creation is side-effect free. Terraform/OpenTofu may reject `workspace new` with inherited `TF_WORKSPACE`; clear/resolve it only for controlled create/select commands and explain the behavior. After creation, refresh both UI and CLI selection state.
- **Delete**: `workspace delete NAME` only for an existing non-default, non-current, confirmed empty workspace. Never allow `-force`; state might still exist remotely and deletion is irreversible. Prefer deferring deletion behind an Advanced/opt-in guard rather than making it a primary action.

## Core UI

- Hub shows per-project list, current CLI-default (if accurately detectable), currently selected Hclaro UI workspace, and backend capability/limitations.
- **New workspace wizard**: validate name using supported CLI rules; show requested engine/root/backend; explain that new state is empty and CLI current selection can change. Confirmation before execution.
- **Switch UI workspace**: immediate read-only selection, independent per tab; explicit sticky selection in local Hclaro metadata if desired. Does not call `workspace select`.
- **Set CLI Default** (advanced): separate confirmation with `terraform/tofu workspace select NAME` command, warning that other terminal commands in same data directory will now target another state.
- **Delete empty workspace** (guarded): show name and workspace context, check state emptiness via permitted read, require exact-name typed confirmation, command without force, show backend failure clearly. Server-side permissions and lock apply. Disable for default/active/unknown state or unsupported backend.
- All mutate actions offer streaming sanitized result and clear recovery state; no automatic recreate on failed switch/delete.

## Implementation constraints

- Scope every request to immutable `(project, engine, canonical root, TF_DATA_DIR, target workspace, expected registry revision)` and serialize local default-modifying operations per project root. Concurrent reads can continue only when they do not rely on ambiguous shared local selection. Respect backend's own locking/capabilities.
- Child processes doing read, plan, or resource operations explicitly set `TF_WORKSPACE` on their own environment; never use process-global `os.Setenv` in the server.
- For `workspace new` and `workspace select`, **do not inherit a conflicting `TF_WORKSPACE`**, and remove or reject hidden `TF_CLI_ARGS` / per-command flags that alter reviewed execution. Do not attempt to 'undo' a CLI selection automatically without communicating possible failures and checking context.
- Treat creation success-but-list-failure as a partially complete operation; preserve accurate result and offer refresh. Fail closed when workspaces cannot be inspected.
- Do not delete actual cloud resources; nevertheless, deleting workspace state is destructive to management capability, so require safeguards and no `-force`.
- Root directory and `TF_DATA_DIR` must remain stable through operation; commands that modify shared selection may affect user's other terminals. Explain explicitly.

## Test scenarios

- `dev` and `prod` open in two browser tabs: both show correct resources while CLI-default stays `default`.
- Switching UI selection never emits `workspace select` and does not modify `.terraform/environment`.
- Creating `staging` emits exactly `workspace new staging` against intended project; inherited `TF_WORKSPACE=dev` handled intentionally; after completion UI shows new workspace and documents any changed local CLI default.
- Duplicate/invalid names, workspace missing, uninitialized project, backend lacking CLI workspaces, remote workspace API unsupported, wrong engine binary, unexpected error, partial success, cancellation and concurrent create/select all tested.
- Attempt to delete `default`, selected workspace, non-empty workspace, or uncertain remote state is blocked. No `-force` path exists.
- `DO_NOT_LEAK_HCLARO_TEST_SECRET` in process output never reaches API/UI/logs; malicious workspace names cannot inject arguments.

## Definition of Done

- Hclaro UI workspace switching is context-local and independent of local CLI default.
- User can create a workspace through a reviewed command and sees post-operation workspace state accurately.
- Setting CLI default, if implemented, is clearly separated and explicitly confirmed.
- Empty-only deletion is either fully guarded/tested or explicitly deferred/disabled — never fake functionality.
- All tests pass on synthetic mock projects for Terraform and OpenTofu.
