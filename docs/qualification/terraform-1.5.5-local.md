# Terraform 1.5.5 local engine run

Evidence for support matrix row RE-01, recorded on 2026-11-05 during the M02 plan work. No real infrastructure, cloud account or remote backend was used, and nothing was applied.

## Real engine run

Terraform 1.5.5 (darwin/amd64, the only engine installed; OpenTofu was not downloaded) planned a disposable project in the session scratch directory through `bin/hclaro` and its HTTP API. The project used only the built-in `terraform_data` resource and `terraform_remote_state` data source with the default local backend, so `init` downloaded nothing and no provider or network was involved. The recorded state was a hand-written synthetic file; nothing was applied, imported or unlocked.

The configuration covered update, no-op, `count` creates, a sensitive variable, both replacement orders (`triggers_replace`, with and without `create_before_destroy`), a `moved` block, a deleted resource, a module with `for_each`, a deferred data read, and sensitive and partly unknown outputs. The test secret was planted in the sensitive variable's var file, the recorded state (sensitive, deleted and output values) and the remote-state file.

| Run | Result |
| --- | --- |
| Plan with `prod.tfvars` | Succeeded in about 0.2 s. 13 changes classified as expected: 5 create, 2 update, 2 replace (`delete,create` and `create,delete`), 1 delete (`delete_because_no_resource_config`), 1 read (`read_because_dependency_pending`), 2 no-op including the move (`previousAddress` kept). Sensitive input update listed with the `sensitive` marker; forces-replacement path marked. Canary 0 in the SSE stream, plan, all 13 change details and the server log. Temp directory `0700` while planning, empty afterwards. |
| Failing plan (`file()` on a var-file path) | Status `plan_failed`, exit 1. Terraform's diagnostic echoed the var-file value; it arrived as `no file exists at "[redacted]"` with the rest of the message intact. |
| State locked by another Terraform process | Status `state_locked` from the structured diagnostic, exit 1, lock details shown, no unlock attempted. |
| Cancel during a 4,013-change plan | Terraform logged "Interrupt received" and "Stopping operation...", status `cancelled` 0.6 s after the request, no engine process left, lock file released, temp directory empty, no plan kept. |
| Context fingerprint | `contextChanged` false after the plan, true after editing `main.tf`, a module file or the selected var file, false again after reverting. |
| 4,013-change plan to completion | 2.6 s. Plan detail 1.06 MB (113 KB gzip), served in about 11 ms; server memory about 30 MB; log not truncated; canary 0. |

Defects found by this run and fixed (with regression tests):

- A project without a `backend` block (default local backend) has no backend metadata after `init`, so planning was disabled as "unverified backend". Initialized projects whose root files declare no `backend` or `cloud` block are now treated as local; any declaration keeps the backend unknown.
- A list output of new IDs is marked unknown per element by Terraform 1.5 and was shown as `[null, null]`; it now shows "known after apply" per element.
- Terraform's interrupt message starts with a newline, so the one-line log showed only "…"; leading and trailing blank lines are now trimmed.

## Repeating the run

The run needs a TCP listener and a real engine (Terraform 1.5.5 for this row), so it is not part of CI. To repeat it: create a disposable directory with only built-in resources and a synthetic `terraform.tfstate`, run `terraform init` there yourself, then start `bin/hclaro -path <dir>` and plan from the UI or API.
