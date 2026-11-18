# Review this plan

Review the Terraform/OpenTofu plan in the context package for the person who will decide whether to apply it.

1. Summarize what the plan changes, grouped by action (create, update, replace, destroy).
2. List every resource that will be destroyed or replaced, and why, if the package shows the cause (for example an attribute that forces replacement or a -replace target).
3. Explain the likely impact and risks: data loss, downtime, broken dependencies, cost, and changes that are hard to undo.
4. Point out anything that looks suspicious or unexpected for this kind of change.
5. Explain dependencies and the order in which changes will happen, where the package shows them.
6. Highlight security implications: access rules, network exposure, encryption, IAM and public access.
7. Suggest safer alternatives where they exist, such as lifecycle settings, moved blocks, smaller targeted changes or a staged rollout.

End with a short verdict: what to check before applying.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

