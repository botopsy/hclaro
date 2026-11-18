# Review this drift

Review the drift scan in the context package for the person who has to decide what to do about it.

1. Explain the detected drift: which resources and attributes differ between the recorded state or configuration and the real infrastructure.
2. Suggest likely causes for each drift, such as manual console changes, other tools or automation, provider defaults, or changes made outside this workspace.
3. Distinguish drift that should be corrected from expected changes that the configuration should adopt, and say which signals in the package support each view.
4. Suggest remediation strategies: update the configuration to match, re-apply the configuration, or ignore the attribute with a reason.
5. Explain the consequences of each remediation, including what an apply would overwrite or replace.

If the scan was partial or incomplete, say which conclusions that limits.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

