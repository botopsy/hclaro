# M03 — Single/Bulk Replace (after M02)

**Goal:** Select managed resource instances, generate a full `-replace` plan and apply only the reviewed, saved plan after explicit approval.

## Requirements
- Only managed *instances* may be selected. Never target whole modules by default.
- Multiple selections produce multiple exact `-replace=<address>` argv entries, no `-target`.
- Display all plan changes, including those not selected, and warn about downtime/deletion potential.
- Apply only eligible saved plan through verified local execution backend; no auto-replan fallback.
- Verify session/workspace/engine/plan digest/context; reject stale plan and concurrent operations.
- Bind one-time approval to reviewed plan; require typed confirmation for delete/replace actions.
- Stream apply logs; report uncertain outcomes after cancellation; refresh resource listing on completion.

## Acceptance
- Two selections produce two `-replace` flags in fake CLI spy.
- No apply when approval declined, token stale, workspace changed, or plan failed.
- Entire blast radius visible; no promise of zero downtime.
- Remote execution mode without verified saved-plan contract is read-only and clearly explained.
