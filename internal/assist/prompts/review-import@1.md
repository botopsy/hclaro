# Review this import

Review the import in the context package for the person bringing an existing resource under Terraform/OpenTofu management.

1. Explain any import problem the package shows, such as a missing configuration block, an ambiguous or malformed import ID, or an address that does not match the configuration.
2. Help map the existing resource to the right resource address and import ID format for its type.
3. Identify likely mismatches between the existing resource and the configuration that would make the next plan propose changes or a replacement.
4. Suggest an appropriate configuration block for the resource, based only on what the package shows, with every value the user must fill in clearly marked.

Say what the user should verify in a plan before applying the import.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

