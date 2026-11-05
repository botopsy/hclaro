# Security

Hclaro never changes infrastructure. Launch is read-only. Since M2, a user can explicitly start a **plan**, which contacts providers and the backend and may lock the state, but never writes state or infrastructure. No apply/import/replace/destroy/state-edit API or generic executor exists. Development and CI use only synthetic fixtures and fake engines.

## Protected boundaries

- Bind exclusively to 127.0.0.1; exact Host and Origin validation, cross-site fetch rejection, no wildcard CORS.
- Random per-launch bearer authorization for every API, including health. The private URL uses a fragment, not an HTTP query credential.
- GET/HEAD for every read route; bodies rejected; bounded queries, headers, request duration, engine output and parsing depth.
- Exactly two POST routes (start plan, cancel operation). Each requires the exact Origin, same-origin fetch metadata when present, JSON content type, a per-launch CSRF header, a body of at most 16 KiB, strict decoding, and an explicit backend-access acknowledgement for plans. No apply route exists.
- Plans: fixed argv (`plan -input=false -json -out=…`), no user-supplied flags; variable files limited to a server-side list of root `*.tfvars` names; unsafe `TF_CLI_ARGS_plan` refused. One operation at a time.
- Saved plans are written only to a private `0700` OS temp directory, set to `0600`, hashed, read back and deleted immediately — and on failure, cancellation, timeout and shutdown. They are never written to the project.
- Plan values and outputs are redacted with the state policy after diffing. Progress logs are rebuilt from typed engine events; free text is sanitized line by line; stderr is never returned.
- Same-origin CSP, no framing or referrers, no external runtime assets, no raw-state endpoint.
- Redaction before snapshot publication: engine sensitivity markers at any depth, secret-like attribute names, and credential-shaped or random-looking values are hidden. Other values are shown, as `terraform plan` would show them.
- No raw diagnostics or state persisted by Hclaro. Child TF_LOG output and checkpoint checks are disabled; unsafe injected CLI flags fail explicitly.

## Limits

A local process that can inspect memory, modify executables, or read terminal/browser session credentials is outside the threat model. Existing IaC engines, provider plugins, selected directories and inherited credential configuration are trusted local inputs; state inspection can load plugins and contact the configured backend. This does not turn existing provider behavior into a security sandbox.

Sensitive values should not appear in resource addresses/instance keys or module/provider identifiers. No heuristic can identify every secret: a secret the provider does not mark sensitive, stored under an ordinary attribute name and not looking random (such as a short human-chosen password), is shown. Engine and provider diagnostics are free text. Known input values (`TF_VAR_*` and values from loaded variable files) are replaced and credential-shaped lines are withheld, but a secret that is neither, such as one returned by a provider API inside an error message, can still be shown. A forcefully stopped plan (after the 15-second grace period, or immediately on Windows when Hclaro has no console) can leave a state lock; Hclaro never unlocks state. Shown values are not a promise that the data is safe to share publicly. Do not share the private session URL.

Backend execution modes and unknown initialization metadata are reported conservatively. Only Terraform 1.5.5 with the local backend and the built-in `terraform_data` resource has been run for real, on macOS. No cloud provider, remote backend, OpenTofu release or other operating-system runtime has been certified. See [M01 evidence](docs/M01-verification.md) and [M02 evidence](docs/M02-verification.md).

## Reporting

Do not attach real state, plans, credentials, private session URLs, or unredacted CLI logs to a report. Supply the affected version, a description, and a minimal synthetic reproduction. A private reporting address has not been established; contact the repository owner privately before disclosing exploit details. No security-support SLA is offered for this alpha.
