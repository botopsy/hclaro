# Security review

The context package is built from the configuration files Git lists in this project and workspace. It has no state, no plan, no live cloud data and no pricing data, so you cannot know what is deployed, what it costs or how it is used. Describe what the configuration declares, not what exists.

Review this configuration for security problems.

1. Identify potential security misconfigurations.
2. Review network exposure: public endpoints, open security group or firewall rules, public IPs and load balancers.
3. Highlight risky IAM permissions: wildcards, broad managed policies, privilege escalation paths and cross-account trust.
4. Identify insecure defaults: missing encryption at rest or in transit, logging or versioning turned off, public storage access.

Rate each finding high, medium or low, and name the file and block it comes from.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

