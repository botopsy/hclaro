# Security

Hclaro M1 is read-only. No plan/apply/import/replace/destroy/state-edit API or generic executor exists. Development and CI use only synthetic fixtures and fake engines.

## Protected boundaries

- Bind exclusively to 127.0.0.1; exact Host and Origin validation, cross-site fetch rejection, no wildcard CORS.
- Random per-launch bearer authorization for every API, including health. The private URL uses a fragment, not an HTTP query credential.
- GET/HEAD only; bodies rejected; bounded queries, headers, request duration, engine output and parsing depth.
- Same-origin CSP, no framing or referrers, no external runtime assets, no raw-state endpoint.
- Sensitivity markers plus key/value heuristics and conservative scalar allowlisting before snapshot publication. Unknown values stay redacted, including arbitrary tags and policy contents.
- No raw diagnostics or state persisted by Hclaro. Child TF_LOG output and checkpoint checks are disabled; unsafe injected CLI flags fail explicitly.

## Limits

A local process that can inspect memory, modify executables, or read terminal/browser session credentials is outside the threat model. Existing IaC engines, provider plugins, selected directories and inherited credential configuration are trusted local inputs; state inspection can load plugins and contact the configured backend. This does not turn existing provider behavior into a security sandbox.

Sensitive values should not appear in resource addresses/instance keys, module/provider identifiers or allowlisted identity metadata. No heuristic can identify every secret under a misleading key. The default masking policy is deliberately restrictive; it is not a promise that arbitrary identity metadata is safe to share publicly. Do not share the private session URL.

Backend execution modes and unknown initialization metadata are reported conservatively. No real cloud, backend, engine version or operating-system runtime matrix has been certified yet. Browser/live-listener verification and the full production frontend build are blocked in the current development environment; see [M01 evidence](docs/M01-verification.md).

## Reporting

Do not attach real state, plans, credentials, private session URLs, or unredacted CLI logs to a report. Supply the affected version, a description, and a minimal synthetic reproduction. A private reporting address has not been established; contact the repository owner privately before disclosing exploit details. No security-support SLA is offered for this alpha.
