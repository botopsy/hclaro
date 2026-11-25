# Hclaro documentation

Hclaro is a local-first Terraform/OpenTofu manager with a CLI and a browser-based UI. Start with [Getting started](GETTING_STARTED.md); every other page is listed here by task.

## Get started

| Page | What it covers |
| --- | --- |
| [Getting started](GETTING_STARTED.md) | Requirements, installation (script, Homebrew, archive, source), the synthetic demo, opening your first project, upgrade and uninstall. |
| [Troubleshooting](reference/troubleshooting.md) | Common problems and what to do about them. |

## User guides

| Guide | What it covers |
| --- | --- |
| [Workspace Hub](guides/workspace-hub.md) | Registering project roots, guided `init`, CLI workspace lifecycle and read-only project tools. |
| [Resource Explorer and navigation](guides/resource-explorer.md) | Browsing recorded state, the inspector, revealing one hidden value, and the sidebar. |
| [Plans and apply](guides/plans-and-apply.md) | Running and reviewing plans, the apply preflight and the confirmation to apply a reviewed saved plan. |
| [Import and replace](guides/import-and-replace.md) | Replacing selected instances and importing existing objects through reviewed plans. |
| [Drift detection](guides/drift-detection.md) | Refresh-only scans that find changes made outside Terraform/OpenTofu. |
| [Dependency graph](guides/dependency-graph.md) | Resource dependencies, potential impact and comparison with a plan. |
| [Workspace comparison](guides/workspace-comparison.md) | Comparing the recorded state of two workspaces. |
| [Execution profiles](guides/execution-profiles.md) | Engine, workspace, variable files and cloud identity bound to each command. |
| [AWS authentication](guides/aws-authentication.md) | AWS CLI and aws-vault bindings, SSO sign-in, identity checks. |
| [Cloud discovery](guides/cloud-discovery.md) | Read-only AWS, Google Cloud and Azure discovery that feeds Import. |
| [Cost estimates](guides/cost-estimation.md) | Optional Infracost estimates of a plan's monthly cost change. |
| [History and Git](guides/history-and-git.md) | Operation history, retention and export; Git status, diffs, editor links and branch switching. |
| [AI assistance](guides/ai-assistance.md) | Optional review of a plan, drift scan, import or resource with an assistant you already use. |

## Technical reference

| Page | What it covers |
| --- | --- |
| [Command line](reference/cli.md) | Flags, exit codes and what launching a project does. |
| [Compatibility](reference/compatibility.md) | Supported Terraform and OpenTofu versions and backend behavior. |
| [Support matrix](support-matrix.md) | What has been run against real engines, backends and platforms, with evidence. |
| [Configuration files](config-compatibility.md) | Files Hclaro writes, their schema versions, upgrade and going back. |
| [Plan actions](plan-actions.md) | How Hclaro classifies plan actions and why unknown combinations cannot be applied. |
| [Status, testing and limitations](reference/limitations.md) | What is qualified, what is not, what is disabled by design, and known limitations. |

## Architecture and development

| Page | What it covers |
| --- | --- |
| [Architecture overview](architecture.md) | Runtime and trust boundaries, packages, execution lifecycle, apply preflight and the HTTP contract. |
| [Security model](../SECURITY.md) | Threat model, protections, residual risks and how to report a vulnerability. |
| [Architecture decision records](architecture/) | [ADR M24: scoped execution for Hub workspaces](architecture/adr-m24-scoped-execution.md). |
| [Building and testing](development/building-and-testing.md) | Build prerequisites, `make` targets, browser tests, cross-compilation and CI. |
| [Release process](RELEASING.md) | Versioning, tagging, the release workflow, Homebrew and the distribution roadmap. |
| [Brand](design/BRAND.md) | Colors, logo geometry and tokens. |
| [Real-engine qualification](qualification/README.md) | The maintainer harness and the recorded evidence behind the support matrix. |
| [Product specification](PRD.md) | The original product requirements, kept for maintainers; not a user guide. |
