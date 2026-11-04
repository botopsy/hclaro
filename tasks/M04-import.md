# M04 — Guided Import Wizard (after M03)

**Goal:** Engineer can associate an existing remote resource with Terraform/OpenTofu state through a reviewed declarative import plan.

## Requirements
- Gather import ID, provider/type, target resource instance address, intended config location.
- Detect if address already exists and whether resource block/import syntax is supported in selected engine version.
- Preview new import block and resource HCL generation when supported; show file diff before any writes.
- Write to new project HCL file only with explicit consent; atomic write, no overwrite, path/symlink escape detection.
- Plan full import operation, show imports and unrelated changes; saved-plan apply only with exact review and confirmation.
- For unsupported import-block semantics, output a manual recipe, not an unsafe one-click `terraform import` command.
- Preserve user configuration; show recovery steps after failed plan/apply.

## Acceptance
- No file, state or infrastructure modification before explicit approval.
- Import collision and missing configuration block detected.
- New HCL diff visible; successful disposable import appears in explorer.
- Errors never cause automatic state manipulation; sensitive IDs redacted if needed.
