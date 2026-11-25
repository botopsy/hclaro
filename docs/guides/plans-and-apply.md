# Plans and apply

Plan, review and apply. Hclaro applies only a reviewed saved plan after explicit confirmation. Related: [Import and replace](import-and-replace.md), [Drift detection](drift-detection.md).

## Plans

Open **Plans** and choose **Run plan**. A preflight dialog explains that the engine will contact providers and the backend with your existing credentials, may lock the state, and will not change infrastructure. You can add optional variable files from the project root; `terraform.tfvars` and `*.auto.tfvars` load automatically as usual.

Hclaro then runs `plan -input=false -json -out=<private temp file>` and streams a sanitized progress log. You can cancel at any time: the engine is interrupted so it can release its lock. Afterwards Hclaro reads the saved plan with `show -json`, records its SHA-256, and deletes it.

The review shows:

- Counts per action (create, update, replace, delete, read, forget, unsupported, no change), which also filter the list. A plan with an unsupported action combination in a resource or output change can be reviewed but never applied: the server refuses it with `unknown_actions`, even with the destructive confirmation. See [Plan actions](../plan-actions.md).
- Every resource change in engine order, including collateral deletions, data reads, moves, imports and deposed objects. Unchanged resources are behind a toggle.
- Per change: the full address, the replacement order (destroy-then-create or create-then-destroy), the reason, and an attribute diff marking redacted, sensitive, `known after apply`, and `forces replacement` values. A change to a redacted value is still listed.
- Output changes and changes made outside Terraform/OpenTofu (drift).
- Metadata: engine, workspace, variable files, plan SHA-256, the Git source (branch, commit, uncommitted IaC files), and a warning when configuration or the source revision changed since the plan.

Planning is available only for local execution (remote/cloud backends are refused) and one operation runs at a time. **Operations** lists the session's plans and applies with status, duration, exit code and their logs; this list is in memory only (the last 100 operations, logs of the last 10). Redacted metadata of every operation is also kept in [Operation history](history-and-git.md#operation-history).

## Apply

Any plan, not only a replacement, can be applied when all of these hold:

- the workspace uses local execution (remote/cloud execution stays plan-only);
- the plan is not errored, incomplete or deferred, and has changes;
- it is the most recent applicable plan, less than one hour old;
- configuration, variable files, the lock file, the engine version and the workspace are unchanged since planning.

Only then is the saved plan kept, in a private `0700` temp directory. **Review and apply…** opens the apply preflight. The server checks, and shows with evidence and a fix for each failure:

- **Blocking (always):** engine, version and workspace match the plan; the execution profile and the context fingerprint are unchanged; the saved file is still held, unexpired and has the reviewed SHA-256; the engine version is supported; the backend runs locally; no other Hclaro operation is running in the project; the project policy file (if any) is valid.
- **Source revision:** the IaC files of the project and its local modules (including modules outside the project folder, which the context fingerprint does not cover) must match the plan's [source fingerprint](history-and-git.md#git-changes); a moved HEAD with identical files is a warning.
- **Policy (configurable):** a dirty Git working tree (default: warning), a production-like workspace, profile or AWS profile name (default: warning for names such as `prod`, `*-prod`, `production`), a limit on destructive changes, and an AWS account allow-list.
- **Plan scope:** a replace or import plan that changes other resources is a warning; every change in the whole plan is listed, with the requested ones marked, and high-risk deletions and replacements are listed separately.
- **AWS identity:** shown when the plan uses AWS. It is *not verified* until you choose **Verify AWS identity**, which calls STS GetCallerIdentity (read-only, contacts AWS) with the plan's credentials. Hclaro never infers an account from a profile name.

Apply stays disabled while anything blocks. Each warning has its own acknowledgement checkbox; there is no "accept all". Plans that delete, replace or forget anything also require typing `apply <workspace>`. The dialog shows the exact command and plan digest. When you apply, the server issues a one-time approval for that preflight, evaluates every check again, re-checks the workspace and the file's SHA-256 and runs `apply -input=false -json <saved plan>` (with the profile's `-parallelism` and `-lock-timeout`, if set). With an `awsAccounts` policy rule and a verified identity, it also asks STS again and refuses the apply if the account changed. If anything changed since the preflight (a new blocker or an unacknowledged warning), nothing is applied and the dialog runs the checks again. A saved plan never prompts, so this dialog is the only confirmation. Closing it applies nothing.

To tune the advisory rules for a project, add `.hclaro/policy.json` (see [`fixtures/preflight/policy.json`](../../fixtures/preflight/policy.json) and [architecture](../architecture.md#apply-preflight)). It is data only (version 1, strict schema); an invalid file blocks applies until fixed, and it cannot relax the built-in blocking checks.

The saved plan is deleted after one apply attempt, whatever the result, and also when it expires, when a newer plan replaces it, and when Hclaro stops. There is no automatic re-plan: if the engine reports the plan as stale, run a new one. A saved plan stays applicable for 1 hour; a refused apply names the reason (newer plan, expired, no changes, errored/incomplete/deferred/unsupported actions, changed configuration, variable files, engine, workspace, profile or backend, changed saved file, missing approval). A replace plan never uses `-target`: the whole saved plan is applied, including unrelated changes, and zero downtime is never promised.

After an apply, the result lists three things separately: what the **engine** reported (**applied**, **not applied**, or **uncertain** when an apply failed or was interrupted after starting), whether **state** was read again, and **resource health**, which Hclaro never checks (it has no health probe). An engine success whose state read failed is shown as a warning, not a success. An uncertain outcome lists manual checks (state list and a new plan in your terminal, the provider console, any remaining lock) and is never retried; the saved plan is gone.
