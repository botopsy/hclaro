# hclaro public beta

hclaro, published by [BotOpsy](https://github.com/botopsy), is in public beta. It is free and open source under the [Apache License 2.0](../LICENSE), and the beta is a chance to try it on real projects and tell us what breaks. Please read this page first.

## What "beta" means here

- Features may change, move or be removed between beta releases. Configuration files Hclaro writes (`projects.json`, `profiles.json`, `history.json`) are versioned and Hclaro refuses files it does not understand rather than overwriting them, but there is no migration promise yet.
- The software is provided "as is", without warranty, as stated in the license.
- There is no support agreement or response-time commitment.

## What has been tested

| Area | Evidence |
| --- | --- |
| Every feature | Go and web unit tests, plus a real-browser suite against the real Go server, all on **synthetic** state, plans and fake Terraform/OpenTofu/AWS/Infracost CLIs. Run on Linux, macOS and Windows in CI. |
| Real Terraform | Terraform 1.5.5 with the local backend and built-in resources only, against a disposable project. |
| Not yet certified | Any cloud provider, any remote backend, Terraform Cloud/HCP, and every OpenTofu release. Apply and import have not been run against a real engine. |
| Platforms | Release archives are smoke-tested natively on macOS arm64, Linux amd64 and Windows amd64. Linux arm64 and macOS amd64 are cross-compiled only. |

**Start with non-production projects.** Plans, drift scans and graphs contact your providers and backend with your credentials and can take a state lock. An apply changes real infrastructure and cannot be undone by Hclaro.

## Data, privacy and security

- Hclaro runs only on your machine and listens only on `127.0.0.1` with a per-launch credential. It has no telemetry, accounts or update checks.
- It reads state through your Terraform/OpenTofu CLI, which may contact your backend and load provider plugins. It reaches AWS only through your `aws` CLI when you start a discovery, and Infracost only when you opt in to cost estimates (`--strict-local` disables that).
- Secrets are redacted on the server before anything reaches the browser, using sensitivity markers and heuristics. **Heuristics can miss secrets**, for example a short password stored under an ordinary attribute name. Treat what you see as potentially sensitive.
- Saved plans can contain plaintext secrets. Hclaro keeps them in a private temporary folder, deletes them after use, and never writes them to your project.

Full details: [SECURITY.md](../SECURITY.md).

## Known limitations

- Only locally executed workspaces can be planned and applied; remote/cloud execution backends are read-only.
- Workspaces opened from the Workspace Hub are read-only; plans, drift, graphs, discovery, imports and applies run for the project you start Hclaro in.
- Views of the project opened from the terminal are not deep-linkable; Hub workspace views are.
- Cloud discovery supports AWS only, five resource types.
- Binaries are not code-signed or notarized.

## Giving feedback

Use [GitHub Issues](https://github.com/botopsy/hclaro/issues) for bugs and feature requests (the forms ask for the version, OS, engine and steps), and [GitHub Discussions](https://github.com/botopsy/hclaro/discussions) for questions and ideas.

**Never post** AWS or other cloud credentials, state files, plan files or plan output, `.tfvars` contents, private configuration, the private session URL, or real account IDs, ARNs, hostnames and bucket names. Replace them with placeholders such as `123456789012` or `example-bucket`. If a problem needs sensitive data to reproduce, describe its shape instead.

Security problems go through private reporting, never public issues: see [SECURITY.md](../SECURITY.md#reporting-a-vulnerability).
