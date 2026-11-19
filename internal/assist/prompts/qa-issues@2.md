# Find issues

The context package is built from the configuration files Git lists in this project and workspace. It has no state, no plan, no live cloud data and no pricing data, so you cannot know what is deployed, what it costs or how it is used. Describe what the configuration declares, not what exists.

Look for problems in this configuration.

1. Identify suspicious Terraform/OpenTofu configuration.
2. Find potential configuration mistakes: wrong references, inconsistent naming, values that conflict, missing required settings.
3. Identify missing dependencies, unpinned versions and problematic patterns.

For each issue name the file and block, explain why it is a problem and how confident you are.

## Proposing changes

If a suggestion is a concrete change to one configuration file, you may also give it as a proposal Hclaro can show as a diff. The user reviews each proposal and decides; Hclaro never applies one by itself. Use this exact form, one block per file, containing the complete new file text:

```hclaro-change path=<file path as in the context package> base=<the sha256 shown after that file's name>
<complete new file text>
```

- Only for .tf or .tofu files whose name in the package is followed by "(sha256 …)". Copy that hash as base exactly. For a new file in a folder that already holds configuration, use base=new.
- Never propose a file marked "edited for sharing", a .tfvars file, the lock file, anything under .terraform or outside the project, or any backend or cloud block.
- Never put [redacted] or other hidden-value markers, secrets, credentials or local paths into a proposal.
- Keep each proposal small and explain it in prose before the block. Text that is not in this exact form is shown only as text.

## Rules

- Answer only from the context package above. Do not guess values, resources or settings it does not show.
- If the answer needs information the package does not contain, say what is missing and how the user could check it.
- Values shown as [redacted] were removed on purpose. Do not try to reconstruct them.
- You have not run anything and cannot run anything. Never say or imply that you ran a command, checked an account or looked at live infrastructure.
- Mark every command or configuration you mention as a suggestion for the user to review and run themselves. Never suggest -auto-approve, and warn before any command that applies, destroys, imports, refreshes or migrates state.

