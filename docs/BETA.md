# hclaro public beta

hclaro, published by [BotOpsy](https://github.com/botopsy), is in public beta. It is free and open source under the [Apache License 2.0](../LICENSE), and the beta is a chance to try it on real projects and tell us what breaks. Please read this page first.

## What "beta" means here

- Features may change, move or be removed between beta releases. Configuration files Hclaro writes (`projects.json`, `profiles.json`, `history.json`, `cost-estimates.json`) are versioned: Hclaro never overwrites a file written by a newer version and keeps a copy before any future format upgrade, but there is no automatic migration promise yet. See [config-compatibility.md](config-compatibility.md).
- The software is provided "as is", without warranty, as stated in the license.
- There is no support agreement or response-time commitment.

## What has been tested

| Area | Evidence |
| --- | --- |
| Every feature | Go and web unit tests, plus a real-browser suite against the real Go server, all on **synthetic** state, plans and fake Terraform/OpenTofu/AWS/Infracost CLIs. Run on Linux, macOS and Windows in CI. |
| Real Terraform | Terraform 1.5.5 with the local backend and built-in resources only, against a disposable project. |
| Not yet certified | Any cloud provider, any remote backend, Terraform Cloud/HCP, and every OpenTofu release. Apply and import have not been run against a real engine. Row by row: [support-matrix.md](support-matrix.md). |
| Platforms | Every release archive (macOS arm64/amd64, Linux amd64/arm64, Windows amd64) is checksum-verified and run with `--version` natively. The full test suite runs on Linux amd64, macOS arm64 and Windows amd64 only. |

**Start with non-production projects.** Plans, drift scans and graphs contact your providers and backend with your credentials and can take a state lock. An apply changes real infrastructure and cannot be undone by Hclaro.

## Data, privacy and security

- Hclaro runs only on your machine and listens only on `127.0.0.1` with a per-launch credential. It has no telemetry, accounts or update checks.
- It reads state through your Terraform/OpenTofu CLI, which may contact your backend and load provider plugins. It reaches AWS only through your `aws` CLI when you start a discovery, and Infracost only when you opt in to cost estimates (`--strict-local` disables that).
- Secrets are redacted on the server before anything reaches the browser, using sensitivity markers and heuristics. **Heuristics can miss secrets**, for example a short password stored under an ordinary attribute name, and they hide some ordinary values to be safe. Treat what you see as potentially sensitive. Saved plan files are plaintext and can contain secrets; they live only in a private temp folder and are deleted after apply, on expiry, on cancel and on shutdown (see [SECURITY.md](../SECURITY.md#limits)).
- Saved plans can contain plaintext secrets. Hclaro keeps them in a private temporary folder, deletes them after use, and never writes them to your project.

Full details: [SECURITY.md](../SECURITY.md).

## Known limitations

- Only locally executed workspaces can be planned and applied; remote/cloud execution backends are read-only.
- Workspaces opened from the Workspace Hub are read-only by default; plans, drift, graphs, discovery, imports and applies run for the project you start Hclaro in. The hidden, experimental `--experimental-hub-execution` flag enables plans, replacements, imports (through a reviewed plan), applies, drift scans, dependency graphs and AWS cloud discovery (only) in Hub workspaces; it is not yet qualified with real engines.
- Views of the project opened from the terminal are not deep-linkable; Hub workspace views are.
- Cloud discovery supports AWS only (AWS CLI v2) and five resource types: `aws_vpc`, `aws_subnet`, `aws_security_group`, `aws_s3_bucket`, `aws_db_instance`. A scan covers at most 6 regions, 1,000 objects per type and region and 5,000 objects in total; a scan that hits a limit is shown as incomplete. The account is shown only after an explicit AWS STS check, never inferred from a profile name; expired SSO sessions are reported as such. Other AWS types, Azure and Google Cloud are not planned for this beta.
- Cost estimates are off by default and use the Infracost CLI v0.10, which sends price lookups (provider, region, resource type and pricing attributes such as instance type or size) to Infracost's external pricing API. Hclaro sends it a minimized plan without state, outputs, variables, secrets, tags or free text, with AWS account IDs in attribute values replaced by zeros (resource addresses are kept as they are), but cannot see what the CLI adds itself. `--strict-local` turns cost estimation off completely.
- Binaries are not code-signed or notarized.
- A plan that contains an action combination Hclaro does not recognize can be reviewed but never applied from Hclaro; see [plan-actions.md](plan-actions.md).
- Releases are distributed through [GitHub Releases](https://github.com/botopsy/hclaro/releases) only: no package managers, installers or auto-update, and Hclaro does not tell you when a new version exists. Upgrading is manual ([GETTING_STARTED.md](GETTING_STARTED.md#upgrade-go-back-and-back-up)); later channels and their conditions are in [RELEASING.md](RELEASING.md#distribution-roadmap).

## Giving feedback

Use [GitHub Issues](https://github.com/botopsy/hclaro/issues) for bugs and feature requests (the forms ask for the version, OS, engine and steps), and [GitHub Discussions](https://github.com/botopsy/hclaro/discussions) for questions and ideas.

**Never post** AWS or other cloud credentials, state files, plan files or plan output, `.tfvars` contents, private configuration, the private session URL, or real account IDs, ARNs, hostnames and bucket names. Replace them with placeholders such as `123456789012` or `example-bucket`. If a problem needs sensitive data to reproduce, describe its shape instead.

Security problems go through private reporting, never public issues: see [SECURITY.md](../SECURITY.md#reporting-a-vulnerability).
