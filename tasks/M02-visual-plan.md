# M02 — Explicit Plan Generation + Visual Diff (after M01)

**Goal:** Engineer explicitly requests a plan and sees the complete proposed infrastructure change without being forced to read unstructured terminal output.

## Requirements
- Plan action is explicit and shows a preflight that providers/backend may be contacted.
- Execute only supported engine CLI with argv arrays; save secure temp plan with restrictive permissions.
- Parse machine JSON with correct action order, before/after values, unknown/sensitive markers, nested modules and output changes.
- Stream bounded, redacted command logs over SSE; handle cancel, lock, credential and timeout errors.
- UI offers summary counts, list filters, before/after diff, full address, raw sanitized diagnostics, associated workspace and engine.
- No apply endpoint at this milestone.

## Acceptance
- Golden fixtures cover create/update/delete/replace/read/no-op and unexpected action combinations.
- UI shows *all* changes, including collateral effects.
- Secret canary never crosses API/UI/log boundary; temporary plan cleaned up.
- Failed or cancelled plan leaves no apply path available.
- Tests and production build pass.
