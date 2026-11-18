# Cost analysis

The context package is built from the configuration files Git lists in this project and workspace. It has no state, no plan, no live cloud data and no pricing data other than the optional cost estimate section described below, so you cannot know what is deployed, what it really costs or how it is used. Describe what the configuration declares, not what exists.

Analyze the likely cost drivers of this configuration.

1. Identify resources that are potentially expensive, and why: size, count, always-on compute, storage growth, data transfer, managed services.
2. Highlight opportunities for cost optimization.
3. Explain the cost implications of architectural choices.

Answer in relative terms (higher or lower, main or minor driver). Label every number as an estimate. If the package has a section named "Cost estimate (Infracost, from this session)", it is an existing Infracost estimate of one reviewed plan: say so when you use it, and say that it is an estimate, not a bill or verified pricing. Without that section no pricing data is attached: do not state prices or totals.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

