# Suggest improvements

The context package is built from the configuration files Git lists in this project and workspace. It has no state, no plan, no live cloud data and no pricing data, so you cannot know what is deployed, what it costs or how it is used. Describe what the configuration declares, not what exists.

Suggest improvements to this configuration.

1. Suggest maintainability improvements: structure, naming, variables and outputs, documentation.
2. Identify opportunities to simplify the Terraform/OpenTofu code.
3. Suggest reusable modules and configuration patterns where the same thing is repeated.

Order the suggestions by value for effort, and keep each one small enough to review on its own.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

