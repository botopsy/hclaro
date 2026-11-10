# Support matrix

What has actually been run against real engines, backends and providers, and what has not. A row is **PASS** only when a real run is recorded in the linked evidence; everything exercised only with fake CLIs or synthetic output is **NOT_TESTED** here, however thoroughly the automated suite covers it. One passing row never qualifies a neighbouring version, platform, backend or operation.

`internal/engine/matrix_test.go` checks this file: a PASS or FAIL row needs a date, a non-synthetic test environment and an evidence link to a section of this repository that names the tested version and records the commands or results. Add rows by appending evidence first, then the row.

## Accepted versions

The version check (`engine.Supported`) accepts stable **Terraform 1.5–1.17** and **OpenTofu 1.6–1.13** (any patch, no prereleases). *Accepted* means Hclaro will run the engine; it is not certification. The ranges were chosen to match the documented `show -json` and machine-readable UI formats, which tests reproduce synthetically. Widening a range needs a real run recorded below and a test of the new edge; `TestVersionCompatibility` pins the current edges.

## Statuses

| Status | Meaning |
| --- | --- |
| PASS | Run against a real engine (and, where named, a real backend or provider) with the result recorded in the evidence. |
| FAIL | Run for real and found broken; the evidence names the defect. |
| NOT_TESTED | Not run for real. Automated tests may still cover it with fakes. |
| BLOCKED | Hclaro refuses the operation by design, so there is nothing to qualify. The refusal itself is covered by the named automated test. |

## Matrix

| ID | Engine | Version | Platform | Backend | Execution mode | Operation | Test environment | Date | Status | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| RE-01 | Terraform | 1.5.5 | darwin/amd64 | local (default) | local-cli | State read, plan, failing plan, lock contention, cancel, 4,013-change plan | Real engine, disposable project, built-in `terraform_data` only, no providers | 2026-11-05 | PASS | [M02 real engine run](archive/verification/M02-verification.md#real-engine-run) |
| RE-02 | Terraform | 1.5.5 | any | local | local-cli | Saved-plan apply, `-replace` | — | — | NOT_TESTED | — |
| RE-03 | Terraform | 1.5.5 | any | local | local-cli | Import blocks, generated config | — | — | NOT_TESTED | — |
| RE-04 | Terraform | 1.5.5 | any | local | local-cli | `init`, workspace new/select/delete, per-context `TF_WORKSPACE` | — | — | NOT_TESTED | — |
| RE-05 | Terraform | 1.6–1.17 | any | any | any | Every operation | Accepted by the version check; synthetic output only | — | NOT_TESTED | — |
| RE-06 | OpenTofu | 1.6–1.13 | any | any | any | Every operation | Accepted by the version check; synthetic output only | — | NOT_TESTED | — |
| RE-07 | Terraform, OpenTofu | any | linux, windows | any | any | Every operation | Fake engine only (CI) | — | NOT_TESTED | — |
| BE-01 | any | any | any | s3 | local-cli | State read, plan, state lock, apply, import | [AWS sandbox runbook](qualification/aws-sandbox.md), not yet run | — | NOT_TESTED | — |
| BE-02 | any | any | any | azurerm, gcs, http, consul, kubernetes, oss, cos, pg, oci, swift | local-cli | Every operation | — | — | NOT_TESTED | — |
| BE-03 | any | any | any | remote, cloud (HCP Terraform, Terraform Enterprise) | remote-exec | State read, `TF_WORKSPACE` mapping | — | — | NOT_TESTED | — |
| BE-04 | any | any | any | remote, cloud | remote-exec | Plan, apply, import, workspace changes | Refused by Hclaro: `TestBackendClassification`, `TestWorkspaceRefusalsRunNothing` | — | BLOCKED | — |
| BE-05 | any | any | any | unlisted type, unreadable or non-regular metadata | unsupported | Plan, apply, import, workspace changes | Refused by Hclaro: `TestBackendClassification`, `TestImplicitLocalBackend` | — | BLOCKED | — |
| PR-01 | any | any | any | any | local-cli | AWS provider (`hashicorp/aws`): plan, drift, import | — | — | NOT_TESTED | — |
| PR-02 | any | any | any | any | local-cli | Any other provider | — | — | NOT_TESTED | — |
| DS-01 | — | — | any | — | — | AWS discovery through a real `aws` CLI and account | — | — | NOT_TESTED | — |
| HUB-01 | any | any | any | any | local-cli | `--experimental-hub-execution` plan, apply, import, drift, graph | — | — | NOT_TESTED | — |

## Test tiers

CI step names say which tier ran. They never stand for each other.

| Tier | Where | What it proves |
| --- | --- | --- |
| Mocked | CI on every pull request and on master (Linux, macOS, Windows) | Behavior against fake Terraform/OpenTofu/AWS/Infracost CLIs and synthetic state and plans. Says nothing about real engine output. |
| Local real-engine | Manual, on a maintainer machine, disposable project, local backend, no providers that reach a network | Real engine output parses and the flows work end to end (rows RE-*). |
| Sandbox cloud | Manual, owner-approved, [AWS sandbox runbook](qualification/aws-sandbox.md) | Real backends, providers and credentials (rows BE-*, PR-*, DS-*). Never in routine CI. |

## Remote and cloud execution

`remote` and `cloud` backends are classified `remote-exec` from the initialized backend metadata and stay read-only whatever the UI sends: the server computes plan, apply and import capabilities from that classification, and every plan, apply, import, drift and graph endpoint checks them. Any backend type Hclaro does not recognise, or metadata it cannot read, is `unsupported` and also read-only. Changing that needs its own qualification and is out of scope for the beta.
