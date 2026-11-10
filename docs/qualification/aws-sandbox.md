# AWS sandbox qualification runbook

Manual, opt-in steps for qualifying Hclaro against a real AWS provider and an S3 backend with local execution (support matrix rows BE-01, PR-01, DS-01). **Nothing here runs in CI, and nobody runs it without the product owner's written approval for that specific run** (account, region, engine versions, date). This runbook has not been run yet.

## Before you start

1. **Approval.** Record the owner's approval (who, when, account alias, region, budget) in the evidence file before any command runs.
2. **A disposable account.** Use an AWS account or sandbox organization unit that holds nothing you need: no production, shared or pre-existing resources. If you cannot confirm the account is empty, stop.
3. **Budget.** Set an AWS Budget with an alert at USD 5 and a hard stop process at USD 10 for the account. Use only free-tier or near-zero resources (an S3 bucket, an SSM parameter, an IAM-free `terraform_data`).
4. **Least privilege.** Create a role for the run that allows only:
   - `s3:ListBucket`, `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` on the state bucket and the `hclaro-qual/*` prefix;
   - `dynamodb:GetItem`, `PutItem`, `DeleteItem` on the lock table (or the S3 native lock file for Terraform 1.10+ `use_lockfile`);
   - create/read/delete on the one disposable resource type, restricted by the tag `hclaro-qual=<run-id>`;
   - read-only calls the discovery feature uses, scoped to the region.
   Deny everything else, including IAM, Organizations and billing changes.
5. **Short-lived credentials.** Use an SSO session or `aws sts assume-role` with a one-hour duration in a dedicated profile (`hclaro-qual`). Never export long-lived keys, and never paste credentials into Hclaro, files in this repository or the evidence.
6. **Account and region review.** Run `aws sts get-caller-identity --profile hclaro-qual` and `aws configure get region --profile hclaro-qual`. Compare the account ID and region to the approval. In the evidence write only the account **alias** and region, never the account ID or ARN.
7. **Isolation.** Work in a fresh directory outside this repository, with its own `TF_DATA_DIR` and the profile set only in that shell (`AWS_PROFILE=hclaro-qual`). Build `bin/hclaro` from the commit under test and record the commit.

## Run

Name the run `<engine>-<version>-<yyyymmdd>` (the run ID). Tag every resource `hclaro-qual=<run-id>`. Capture each command and its exit status in the evidence file; keep Hclaro's redacted output, not raw engine output.

| Step | Check | Expected |
| --- | --- | --- |
| Identity | Hub context for the project shows the `hclaro-qual` profile and region; the account alias matches the approval. | Profile and region visible; no credential values shown. |
| Init | You run `init` yourself, then Hclaro shows backend `s3`, execution `local-cli`. | No automatic init; classification matches. |
| State read | Read the empty workspace, then after the first apply. | `state_absent`, then a snapshot with the one resource. |
| Plan | Plan creating one tagged resource. | One create; redaction canary absent from UI, history and logs. |
| State lock | Hold the lock from a second shell (`terraform plan` with `-lock-timeout=0` left open, or a manual lock item), then plan in Hclaro. | `state_locked` with lock details; Hclaro never unlocks. |
| Apply | Apply the reviewed saved plan once. | One resource created; the plan file is consumed and removed. |
| Import | Create a second tagged resource with the CLI, import it through a reviewed plan. | Import block reviewed, applied once. |
| Drift | Change the tag outside Terraform, run a drift scan. | Drift reported; nothing refreshed into state. |
| Discovery | Run AWS discovery for the region. | Only read calls; the tagged resources listed. |
| Failure paths | Expired session, wrong region, missing bucket. | Clear errors; no retries that write; no secrets in messages. |
| Cleanup | `terraform destroy` **from your own shell** after the owner reviews its plan; delete the state objects and lock items; confirm no `hclaro-qual` tagged resources remain (`aws resourcegroupstaggingapi get-resources --tag-filters Key=hclaro-qual`). | Account back to empty; cost within budget. |

Stop at the first unexpected write, unknown resource or cost alert, and record it as FAIL.

## Evidence

Add a section to a new file `docs/qualification/<run-id>.md` with: the approval record, commit, engine name and exact version, OS/architecture, account alias, region, each command with its exit status, the table above with results, and the cleanup confirmation. Review it for account IDs, ARNs, bucket names that identify the account, keys, tokens and session names before committing. Then add or update the matrix rows in [support-matrix.md](../support-matrix.md), linking to that section; `TestSupportMatrix` refuses a PASS row without it.
