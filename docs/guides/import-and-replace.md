# Import and replace

Both flows go through a reviewed plan and the normal apply dialog; see [Plans and apply](plans-and-apply.md).

## Replace

In **Resources**, tick the checkbox of one or more managed instances (up to 50; data sources cannot be selected) and choose **Plan replacement…**. Hclaro runs a full plan with one `-replace=<address>` argument per instance, using the exact address from state. It never adds `-target`. The review lists the requested instances, flags any the plan does not actually replace, and counts every other change in the plan, including deletions and replacements you did not select. Replacing destroys and recreates objects; Hclaro does not promise zero downtime.

## Import

**Import & discovery → Import existing** brings an object that already exists outside Terraform/OpenTofu into state. Enter the resource instance address (managed resources only, with its count index or for_each key if the block uses one), the provider's import ID, an optional provider reference such as `aws.west`, and the name of a **new** file in the root module. **Check and preview** writes nothing and runs nothing. It reports:

- the address is already in state, or an object of the same type with that `id` (or, for the discovery types, the same identifier, such as an RDS DB instance identifier) is already managed elsewhere (both block the import);
- a missing resource block (blocks the import unless the engine generates one), or a block that could not be verified;
- an instance key that does not match the block's `count`/`for_each` (warning; the check is textual, the plan is authoritative);
- an existing import or moved block for the same address (in `.tf`, `.tofu` or their JSON forms), a root configuration file that cannot be read or decoded, an existing destination file, override file names, and import IDs that look like credentials (all block the import).

The preview shows the exact new file. **Write file and plan import** needs a separate consent checkbox. Hclaro then writes the file without overwriting anything (temporary file, then a hard link that fails if the name exists; symlinks and a swapped project directory are refused) and runs a full plan, exactly like **Run plan**: you review the import together with every other change the plan contains. Applying uses the normal apply dialog. Because an import writes state, it always requires typing `apply <workspace>`.

The engine can generate the resource block (`-generate-config-out`) for root-module addresses without an instance key when no block exists. It writes a second new file, `<name>_generated.tf`, during the plan. The review shows a redacted copy of it before anything can be applied; the file on disk contains the values the engine read from the object, so review it before committing it.

Hclaro never deletes files you approved. After a failed plan or apply the review says how to retry (fix the file, then run any plan: plans include import blocks in your configuration) or back out (delete the file). When importing through Hclaro is unavailable, for example for remote execution backends, the wizard still checks the request and shows a manual recipe: the import block plus `plan -out`, `show` and `apply` of the saved plan. The legacy `import` command is shown only as a labeled alternative because it writes state without a reviewable plan.
