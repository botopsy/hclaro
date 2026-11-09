# M14 implementation and verification

**Status: draft.** Safe apply and preflight checks are implemented as a Preflight Engine on the existing saved-plan apply flow (M03/M04), not a second apply path: `internal/preflight` (typed checks, deterministic evaluation, versioned local policy, Git and AWS STS probes), `operations.Manager.Preflight` with a short-lived binding, a stricter `Approve`, a full re-evaluation inside `StartApply`, `POST /api/v1/plans/{id}/preflight`, and a rebuilt apply dialog.

Design and trade-offs: [architecture](../../architecture.md#apply-preflight), [security](../../../SECURITY.md), [README](../../../README.md#replace-and-apply).

## Scope decisions

- **One apply path.** Normal, replace and import plans all go through the same `Approve` → `StartApply` → `executeApply`. The preflight adds checks and a binding in front of the approval; the run itself still hashes the file and confirms `workspace show` just before the engine (defense in depth, tested by tampering after the last evaluation).
- **Endpoint names follow the repository**: `POST /api/v1/plans/{id}/preflight` (the task's `/operations/{operationId}/preflight` is illustrative); the existing approval endpoint now takes `{preflightBinding, acknowledged}`.
- **Per-warning acknowledgement, no blanket value.** The approval must list exactly the warning check IDs the preflight asked for; extra or missing IDs are refused. Destructive plans keep the typed `apply <workspace>` phrase; the destructive check is informational so the phrase is not duplicated by a checkbox, unless a policy limit applies.
- **Re-evaluation at apply** covers every non-network check (context, profile, fingerprint, eligibility, digest, expiry, engine, backend, conflicts, policy, Git, labels, scope). The AWS identity result is reused from the binding rather than calling AWS again.
- **Policy is per project** (`.hclaro/policy.json`), versioned, strict and data-only. It tunes four advisory rules and cannot relax built-in blockers. An invalid file blocks (fail closed). There is no per-user policy file yet.
- **Git dirty detection runs `git`** (fixed argv, fsmonitor/untracked cache disabled, no optional locks, no prompts, timeout). M13's history still reads `.git` without running Git; it is unchanged. The whole repository's status counts.
- **Identity** uses the AWS CLI because no AWS SDK is a dependency. Missing CLI, failures and timeouts are `unknown` with a generic message; output other than account and ARN is dropped.
- **History** records the apply's `preflight` summary only on the in-memory operation and one sanitized log line (which reaches the history excerpt); check evidence, Git paths and identities are never recorded. The history entry schema is unchanged.

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.3, Node 24, Playwright Chromium.

| Check | Result |
| --- | --- |
| `make test` (`go test -race`, Vitest) | Passed: all Go packages with `-race`; Vitest 100/100 in 13 files. New: `preflight/preflight_test.go` (13: every built-in blocker, unknown-is-not-pass, labels, destructive limit, impact with collateral and deposed objects, AWS check, policy parsing incl. unknown fields/URLs/scripts, fail-closed loading incl. symlink and size, porcelain parsing incl. credential-shaped paths, identity parsing, real Git in a disposable repo, example policy fixture), `operations/preflight_test.go` (9), `api/plans_test.go` `TestPreflightOverHTTP`, security guard rows for the preflight route. |
| Existing apply tests | All M03/M04/M09/M13 apply, import, profile and history tests pass through the new preflight (helpers acknowledge warnings exactly as the UI does). The tampered-artifact test now shows the preflight catching it, and a second case tampers after the last evaluation to prove the run's own digest check. |
| Vitest | `plans/Plans.test.tsx` 17 passed: preflight on open without identity probe, per-warning checkboxes and phrase gating, approval body `{preflightBinding, acknowledged}`, blocker rendering with remediation and disabled Apply, explicit identity verification, re-run after a refused apply, preflight error. |
| `make lint`, `GOOS=windows go vet`, `gofmt` | Passed. |
| `make build`, `make browser-test` | Passed; the smoke test now asserts the context identity, high-risk list, per-warning acknowledgement and that Apply stays disabled until all warnings and the phrase are given. Screenshots refreshed. |
| Live loopback run | `bin/hclaro --engine terraform --path fixtures/project` with only the synthetic CLI and a scratch `HCLARO_CONFIG_DIR`: plan → preflight (13 checks, real Git probe reported this branch's uncommitted files as a warning) → approval without acknowledgements refused (`acknowledgement_required`) → approval → apply of `<reviewed saved plan 8fd04b5c44a5>` succeeded, operation `preflight: {policy: default, acknowledged: [git.worktree]}`. |

## M14 acceptance mapping

| Criterion | Evidence |
| --- | --- |
| Preflight Engine on the existing apply flow: fingerprint, digest/TTL, backend, operation conflicts, dirty Git, destructive counts, optional identity | `preflight.Evaluate`, `Manager.facts`; `TestEvaluateBlocksEveryCriticalMismatch`, `TestPreflightBlocksContextAndPlanChanges`, `TestPreflightDirtyGitFollowsPolicy` |
| `{id, severity, status, evidence, remediation}`; unknown ≠ pass; context/digest mismatch and remote execution block; dirty Git and production labels configurable | `preflight.Check`; `TestUnknownIsNeverPass`, `TestProductionLabels`, policy tests |
| Exact counts, high-risk resources and whole-plan effects; remediation; typed destructive confirmation; no "approve all" | `Impact`, `TestImpactMarksRequestedAndCollateral`, `TestReplacePreflightIncludesCollateralChanges`, UI tests, `acknowledgement_required` with `"*"`/extra IDs |
| Identity probe disabled without user intent; honest status; no inference from `AWS_PROFILE` | `TestIdentityProbeIsExplicitAndNotLogged`, `TestAWSIdentityCheck`, UI `verifies the AWS identity only when asked` |
| Tiny versioned local policy; no scripts or fetched policies | `ParsePolicy`, `LoadPolicy`, `TestParsePolicy`, `TestLoadPolicyFailsClosed` |
| Re-run critical checks before apply; same artifact; no transparent re-plan | `StartApply` re-evaluation, `TestApplyReevaluatesPreflight`, `TestContextChangeBetweenPreflightAndApplyIsRejected`, `TestApplyUsesOnlyTheReviewedArtifact` |
| Mismatched workspace/context, changed digest, expired review token, unrecognized engine/backend prevent apply | `TestPreflightBlocksUnsupportedBackendAndEngine`, `TestApplyRechecksWorkspaceAndArtifact`, binding/plan expiry subtests |
| Mock executor: only the reviewed saved plan, never `apply -auto-approve` | `TestApplyUsesOnlyTheReviewedArtifact` (applied bytes equal the artifact, one `plan` call, command without `-auto-approve`) |
| Sensitive checks, provider IDs and raw plan contents not logged | `TestIdentityProbeIsExplicitAndNotLogged` (account, ARN fragment and canary absent from logs, operations and recorder records); HTTP canary assertion on every response in `api` tests and the browser smoke |

## Manual verification

1. `make demo`, open the printed URL, **Plans → Run plan**, then **Review and apply…**.
2. Check the context identity, the Git warning (this repository is usually dirty), the impact lists and that Apply stays disabled until each warning is ticked and `apply demo` is typed. Close the dialog: nothing is applied.
3. Add `fixtures/project/.hclaro/policy.json` with `{"version":1,"rules":{"gitDirty":"block"}}`, choose **Run checks again**: Apply is blocked with remediation. Write invalid JSON: the policy check blocks. Remove the file afterwards.
4. Edit a `.tf` file in `fixtures/project` after the preflight and apply: the server refuses with *context changed* and nothing runs. Revert the edit.
5. Do not use **Verify AWS identity** with real credentials while developing; it is exercised with a fake probe in tests.

## Unresolved

- Real-engine and real AWS CLI certification: the identity probe was not run against AWS (no live credentials by policy); its parsing and gating are tested with synthetic output.
- Other cloud identities (Azure, GCP) are not probed.
- The Git check reports the whole repository; monorepos with unrelated changes will warn. Sibling module directories outside the project root are covered only through this repository-wide status.
- Locks held by other tools (another terminal running Terraform) are not detected before apply; the engine's own state lock is the guard.
- The preflight summary is not yet shown in the History view's apply details.
