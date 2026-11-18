# Describe this project

The context package is built from the configuration files Git lists in this project and workspace. It has no state, no plan, no live cloud data and no pricing data, so you cannot know what is deployed, what it costs or how it is used. Describe what the configuration declares, not what exists.

Explain this Terraform/OpenTofu project to someone new to it.

1. Summarize what the project appears to build and for what purpose, as far as the configuration shows it.
2. Describe the infrastructure architecture: the main layers, environments and how they fit together.
3. Identify the major components: modules, key resources, providers and their versions.
4. Explain the dependencies between components and on remote modules.

End with a short list of what the package does not show that a newcomer should ask about.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

