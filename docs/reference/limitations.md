# Status, testing and limitations

Hclaro, published by [BotOpsy](https://github.com/botopsy), is free and open source under the [Apache License 2.0](../../LICENSE). It **needs real-world testing**: the first release exists to collect real usage and issues so they can be fixed. The maintainer has used it on AWS projects, but that use is not part of the recorded evidence below. Read this page to see what has been tested and what has not, and start with non-production projects.

## Stability and support

- Features may change, move or be removed between releases. Configuration files Hclaro writes (`projects.json`, `profiles.json`, `history.json`, `cost-estimates.json`) are versioned: Hclaro never overwrites a file written by a newer version and keeps a copy before any future format upgrade, but there is no automatic migration promise yet. See [config-compatibility.md](../config-compatibility.md).
- The software is provided "as is", without warranty, as stated in the license.
- There is no support agreement or response-time commitment.

## What has been tested

| Area | Evidence |
| --- | --- |
| Every feature | Go and web unit tests, plus a real-browser suite against the real Go server, all on **synthetic** state, plans and fake Terraform/OpenTofu/AWS/Infracost CLIs. Run on Linux, macOS and Windows in CI. |
| Real engines | Terraform 1.5.5, 1.5.7 and 1.17.2 and OpenTofu 1.6.3 and 1.13.9, with the local backend, built-in resources and the `random` and `local` providers from a local mirror, against disposable projects. Every version on macOS Intel; Terraform 1.17.2 and OpenTofu 1.13.9 also on macOS Apple silicon, Linux amd64/arm64 and Windows amd64. |
| No recorded qualification | Any cloud provider, any remote backend, Terraform Cloud/HCP, and every Terraform or OpenTofu version not named above (other patches and the versions in between are not inferred). Row by row: [support-matrix.md](../support-matrix.md). |
| Platforms | Every release archive (macOS arm64/amd64, Linux amd64/arm64, Windows amd64) is cross-compiled on Linux, then checksum-verified, run with `--version` and started natively. The full test suite runs on Linux amd64, macOS arm64 and Windows amd64 only; Linux arm64 and macOS Intel get the smoke test only. See [Platforms](../support-matrix.md#platforms). |

## What is qualified, what is not yet run for real and what is off

| Status | Features |
| --- | --- |
| **Qualified with a real engine** | Terraform 1.5.5, 1.5.7 and 1.17.2 and OpenTofu 1.6.3 and 1.13.9, local backend, project opened from the terminal: state read, plan, apply of a reviewed saved plan, replace, import blocks and generated config, guided init, workspace new/select/delete, drift scans and the dependency graph, on macOS Intel (rows RE-01 to RE-04, RE-11 to RE-26). Terraform 1.17.2 and OpenTofu 1.13.9 also on macOS Apple silicon, Linux amd64 and Linux arm64 (RE-27 to RE-38). Windows amd64 is partial and its network isolation was not verified (RE-39 to RE-42). Each row lists the scenarios not run, such as an unlock failure, a never-initialized project and an engine outliving the shutdown grace period. |
| **Not yet run for real** (works with fake engines in CI) | Workspace comparison, project tools, the tflint, Trivy and Checkov [scanner cards](../guides/scanners.md) (only tflint 0.47 was run by hand for its output format; Trivy and Checkov flags and output are from their documentation), every other Terraform or OpenTofu version, every other platform and version combination, every remote backend for reading state, AWS discovery and Infracost estimates. |
| **Hub execution** (default) | Plans, applies, replace, imports, drift scans, graphs and AWS discovery also run in Hub workspaces. Run with real engines on the local backend only (rows HUB-02 to HUB-17; Windows network isolation not verified); every other backend and engine pair is row HUB-01, not run with a real engine. |
| **Launch project only** (`--no-hub-execution`) | Plans, applies, replace, imports, drift scans, graphs and AWS discovery run only for the project Hclaro was started in. Hub workspaces are read-only apart from a reviewed init, reviewed workspace commands and read-only project tools. |
| **Disabled by design** | Plan, apply, import and workspace changes for remote/cloud execution backends (HCP Terraform, Terraform Enterprise, `remote`) and for unrecognized backends; `destroy`, `state rm/mv`, `force-unlock`, `refresh`/`apply -refresh-only` that writes state, and any automatic init, plan or apply. |
| **Not planned** | cloud resource types beyond those in [Cloud discovery](../guides/cloud-discovery.md), package managers other than the Homebrew tap, native installers, auto-update. |

The demo (`make demo`, `make demo-hub`) runs only a fake engine against synthetic fixtures. It shows how the UI behaves; it is not evidence that a real engine, backend or provider works.

**Start with non-production projects.** Plans, drift scans and graphs contact your providers and backend with your credentials and can take a state lock. An apply changes real infrastructure and cannot be undone by Hclaro.

## Data, privacy and security

- Hclaro runs only on your machine and listens only on `127.0.0.1` with a per-launch credential. It has no telemetry, accounts or update checks. The credential is in the URL Hclaro prints: anyone with that URL and access to your machine can use the session, so never share it, paste it in an issue or screen-share it.
- It reads state through your Terraform/OpenTofu CLI, which may contact your backend and load provider plugins. It reaches AWS only through your `aws` CLI when you start a discovery, and Infracost only when you opt in to cost estimates (`--strict-local` disables that).
- Secrets are redacted on the server before anything reaches the browser, using sensitivity markers and heuristics. The one exception is a single value you ask to reveal in the resource inspector ([Resource Explorer](../guides/resource-explorer.md)). **Heuristics can miss secrets**, for example a short password stored under an ordinary attribute name, and they hide some ordinary values to be safe. Treat what you see as potentially sensitive. Saved plan files are plaintext and can contain secrets; they live only in a private temp folder and are deleted after apply, on expiry, on cancel and on shutdown (see [SECURITY.md](../../SECURITY.md#limits)).
- Saved plans can contain plaintext secrets. Hclaro keeps them in a private temporary folder, deletes them after use, and never writes them to your project.

Full details: [SECURITY.md](../../SECURITY.md).

## Known limitations

- Only locally executed workspaces can be planned and applied; remote/cloud execution backends are read-only.
- Workspaces opened from the Workspace Hub can be planned and applied by default: plans, replacements, imports (through a reviewed plan), applies, drift scans, dependency graphs and AWS cloud discovery. This was run with real engines on the local backend only (rows HUB-02 to HUB-17); remote backends, other engine pairs and the remaining risks in the qualification record are not covered. `--no-hub-execution` keeps Hub workspaces read-only, so these run only for the project you start Hclaro in.
- With Hub execution on, known limitations (M26.08): cost estimates are available only for the project Hclaro was started in (a Hub plan's Cost estimate tab says so); a Hub workspace that keeps a reviewed plan is not closed to make room, so with four such workspaces another one cannot plan until a plan is applied, expires (1 hour) or its workspace is read again; and there is no UI button yet to close a workspace view (`DELETE /api/v1/contexts/{id}` exists).
- Views of the project opened from the terminal are not deep-linkable; Hub workspace views are.
- AWS cloud discovery (AWS CLI v2) covers the resource types listed in [Cloud discovery](../guides/cloud-discovery.md). A scan covers at most 6 regions, 1,000 objects per type and region and 5,000 objects in total; a scan that hits a limit is shown as incomplete. The account is shown only after an explicit AWS STS check, never inferred from a profile name; expired SSO sessions are reported as such. Other AWS types and organization-wide scans are not supported.
- Google Cloud discovery (`gcloud`, Google Cloud SDK 400 or newer) lists seven types in the project your gcloud configuration resolves to: `google_compute_network`, `google_compute_subnetwork`, `google_compute_firewall`, `google_compute_instance`, `google_compute_disk` (zonal), `google_storage_bucket` and `google_sql_database_instance`. It is offered only when `gcloud` is on `PATH`. The project is shown only after an explicit verification; the verification and the scan use gcloud's existing login (Hclaro has no Google Cloud sign-in), never request or read an access token, and list each type for the whole project in one read-only call, so the regions you select filter what is kept rather than limiting the calls. A limit cut marks the scan incomplete. Regional disks, other Google Cloud types, folders and organizations are not covered.
- Azure discovery (`az`, Azure CLI 2.50 or newer) lists eight types in the subscription your az login resolves to: `azurerm_resource_group`, `azurerm_virtual_network`, `azurerm_subnet`, `azurerm_network_security_group`, `azurerm_linux_virtual_machine`, `azurerm_windows_virtual_machine`, `azurerm_managed_disk` and `azurerm_storage_account`. It is offered only when `az` is on `PATH`. The subscription and tenant are shown only after an explicit verification that uses az's existing login (Hclaro has no Azure sign-in) and never requests or reads an access token. Each type is listed for the whole subscription in one read-only call, so the locations you select filter what is kept. Only the public Azure cloud is supported; sovereign clouds (China, US Government) are refused. A limit cut marks the scan incomplete. Subnets are read from the virtual network list. Other Azure types, management groups and tenants are not covered.
- Cost estimates are off by default and use the Infracost CLI v0.10, which sends price lookups (provider, region, resource type and pricing attributes such as instance type or size) to Infracost's external pricing API. Hclaro sends it a minimized plan without state, outputs, variables, secrets, tags or free text, with AWS account IDs in attribute values replaced by zeros (resource addresses are kept as they are), but cannot see what the CLI adds itself. `--strict-local` turns cost estimation off completely.
- Binaries are not code-signed or notarized, so Gatekeeper and SmartScreen warn on first launch ([what to do](../GETTING_STARTED.md#2-install)). Checksums show that a download is intact; build-provenance attestations tie it to hclaro's release workflow. Neither is a code signature.
- A plan that contains an action combination Hclaro does not recognize can be reviewed but never applied from Hclaro; see [plan-actions.md](../plan-actions.md).
- Releases are distributed through [GitHub Releases](https://github.com/botopsy/hclaro/releases) and a Homebrew tap that installs the release archives (`brew install botopsy/tap/hclaro`): the install script (`curl -fsSL https://get.hclaro.dev | sh`), no other package managers, native installers or auto-update, and Hclaro does not tell you when a new version exists. Upgrading is manual ([GETTING_STARTED.md](../GETTING_STARTED.md#upgrade-go-back-and-back-up)); later channels and their conditions are in [RELEASING.md](../RELEASING.md#distribution-roadmap).

## Giving feedback

Use [GitHub Issues](https://github.com/botopsy/hclaro/issues) for bugs and feature requests (the forms ask for the version, OS, engine and steps), and [GitHub Discussions](https://github.com/botopsy/hclaro/discussions) for questions and ideas.

**Never post** AWS or other cloud credentials, state files, plan files or plan output, `.tfvars` contents, private configuration, the private session URL, or real account IDs, ARNs, hostnames and bucket names. Replace them with placeholders such as `123456789012` or `example-bucket`. If a problem needs sensitive data to reproduce, describe its shape instead.

Security problems go through private reporting, never public issues: see [SECURITY.md](../../SECURITY.md#reporting-a-vulnerability).
