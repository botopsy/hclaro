# Changelog

## Unreleased

### Open-source public beta preparation (M22, draft)

- hclaro is published by **BotOpsy** and licensed under the **Apache License 2.0** (`LICENSE`, official text): free for personal and commercial use, modification and redistribution. Release archives now include `LICENSE`.
- New public documentation: README introduction (capabilities, install, quick start, license and feedback), `docs/GETTING_STARTED.md`, `docs/BETA.md`, `docs/RELEASING.md` (draft), a contribution policy (feedback via Issues; no external pull requests during the beta), a private vulnerability-reporting section in `SECURITY.md`, issue forms that ask for redacted reports, and a Discussions template.
- Release builds no longer restore CI caches, and CI uses Go 1.27.3. `.gitignore` covers Terraform/OpenTofu plans, variable files, crash logs, CLI configuration, credentials and editor files.
- Removed two broken legacy scripts (`scripts/build-offline-preview.mjs`, `scripts/offline-render-test.mjs`); milestone verification notes moved to `docs/archive/verification/`.
- `TRADEMARKS.md` describes how to refer to the hclaro and BotOpsy names and logos; it does not limit Apache-2.0 rights. Questions and ideas go to GitHub Discussions.
- Internal milestone task files, coding-agent instructions and the third-party design mockup are no longer part of the repository contents.
- Audit reports in `docs/audits/`. No product behaviour changed.

### Navigation and information architecture (M21, draft)

- The sidebar is grouped by task: project views (Overview, Resources, Modules, Providers), **Plan & analyze** (Plans, Drift detection, Dependency graph, Compare), **Manage** (Import & discovery, Operations, History) and Git, Tools and Settings at the bottom, with a compact *Local by design · Alpha* footer. Groups scroll on their own, so project context and Settings stay reachable in short windows.
- A **project switcher** replaces the Projects item: current project and how it was opened, the terminal-opened project, recent Hub projects, the project's workspace page and **All projects**. The Terraform/OpenTofu **workspace** selector moved under it, separate from the project; the context bar still shows the workspace and the production-like hint.
- **Import & discovery** is one entry with two separate workflows (**Import existing**, **Discover cloud resources**); the wizards, APIs and confirmations are unchanged.
- **Collapsible sidebar**: an icon-only mode with keyboard-accessible tooltips, saved with the display preferences. Below 900 px the sidebar is a dismissible drawer instead of an icon rail.
- **Tools** for the project opened from the terminal now explains that the tools run in the folder's Hub entry and offers **Open tools for workspace …** or **Open in new tab**, instead of switching the tab without warning.
- Switching project asks first when it would discard a replacement selection or a discovery-prepared import. In Hub workspaces, back and forward now move between views. Typed navigation configuration in `web/src/navigation/`. No API, route, CLI, Terraform/OpenTofu or security behaviour changed.

### Brand identity: hollow cursor (M19, draft)

- New **hollow-cursor identity**: a terminal-style chevron, lowercase `hclaro` in JetBrains Mono Bold and an unfilled cursor, in light, dark and lime (inverse) variants, plus a compact `h` + cursor app mark. Reusable `HclaroLogo` and `HclaroMark` components with one accessible name and decorative parts hidden.
- The sidebar shows the wordmark, and the compact mark when collapsed; a static mark is shown while the bundle loads; the Workspace Hub's first-run state and a new **Settings → About Hclaro** row use the brand. New favicon (SVG, 16/32 px PNG) and Apple touch icon; `theme-color` follows the theme.
- **Centralized theme tokens**: the five reference colours (`#0B1220`, `#F4F4F0`, `#E8E8E1`, `#C6F432`, `#6A8F00`) are raw tokens; every semantic token is defined once for both themes with `light-dark()`. The system theme applies before any script runs and a saved theme before React renders, so the palette no longer flashes. Primary buttons are navy (light) or lime (dark); focus rings, form-control borders and plan statuses were re-measured for WCAG 2.2 AA in both themes and statuses stay distinct from the brand accent.
- The logo typeface is self-hosted from `@fontsource/jetbrains-mono` (OFL-1.1, Latin 700 only) and embedded in the binary; no network font. `make brand-assets` regenerates the icon set. Documentation in `docs/design/BRAND.md`. No API, CLI, Terraform/OpenTofu or security behaviour changed.

### Cost impact estimation (M17, draft)

- **Cost estimate** tab in the plan review: projected recurring monthly change with currency, new/removed/changed split, priced configuration totals, coverage of cost-bearing changes, caveats (unsupported types, usage-based charges, free resources, forgotten resources, exchange rates, not a bill), a breakdown by resource type and per-resource line items with price components. Unpriced resources are labelled, never shown as zero.
- Optional **Infracost CLI** v0.10 adapter behind a `cost.Runner` provider abstraction (`CanEstimate`, `Disclosure`, `Estimate`, cancellation). Off by default; an explicit data disclosure and opt-in per session are required, and nothing runs or is sent before it. Hclaro never installs Infracost or handles its API key.
- The adapter receives a minimized plan in a private temporary file and an allowlisted environment without cloud credentials. Estimates are bound to the plan and its context fingerprint and marked out of date after a change; failures and cancellation never affect plans or applies.
- New `--strict-local` flag: cost estimation is unavailable (*Not available in strict local-only mode*).
- Sanitized aggregate totals are saved locally in `cost-estimates.json` and can be cleared.
- New routes: `GET /api/v1/cost/provider`, `POST /api/v1/cost/opt-in`, `POST /api/v1/cost/opt-out`, `POST /api/v1/plans/{id}/cost-estimates`, `GET /api/v1/plans/{id}/cost-estimate`, `GET /api/v1/cost-estimates/{id}`, `POST /api/v1/cost-estimates/{id}/cancel`, `GET /api/v1/cost/history`, `POST /api/v1/cost/history/clear`. New package `internal/cost`; synthetic report `fixtures/cost`, a synthetic `infracost` CLI for `make demo` (`scripts/fake-infracost`).

### AWS cloud discovery (M16, draft)

- **Discovery** view for the launch project: verify the AWS context (CLI v2 and STS caller identity; account, principal type, region; never inferred from a profile name), preview a scan (types, up to 6 regions, limits, concurrency, request estimate, possible API charges) and confirm it, follow progress, cancel, and browse results labelled **Possibly untracked**, **Already tracked**, **Unknown** or **Unresolved** against the workspace's recorded state, with filters by label, service and text.
- Supported types: `aws_vpc`, `aws_subnet`, `aws_security_group`, `aws_s3_bucket`, `aws_db_instance`. Pagination, throttling retries with backoff, per-service partial results (access denied is "missing, not empty"), expired SSO detection, multi-region de-duplication, limits and cancellation. Tags and names are sanitized; rules, policies, endpoints and DB configuration are never read.
- **Prepare import** opens the Import wizard with the import ID and a suggested address after checking the account again; the import route re-checks the link (`discoveryToken`). No bulk import.
- New routes under `/api/v1/discovery/…`; `POST /api/v1/imports` accepts `discoveryToken`. New package `internal/discovery` with an allowlisted read-only AWS CLI runner; synthetic fixtures in `fixtures/aws`, a synthetic `aws` CLI for `make demo` (`scripts/fake-aws`).

### Git-aware infrastructure (M15, draft)

- **Git** view for the launch project and Hub workspaces: repository name, project path inside the repository, branch, HEAD, linked worktree, and the changed `.tf`, `.tf.json`, `.tofu`, `.tfvars` and lock files of the project root and its local modules, with warnings for a detached HEAD, a branch without commits, untracked IaC files and IaC changes elsewhere in a monorepo. A Git badge in the context bar shows branch and commit (read from `.git` files) and opens the view.
- Redacted line diffs against HEAD for an explicitly selected `.tf`/`.tf.json` file (secret-named literals, credential-shaped values and secret heredocs replaced on the server). Variable and lock files are listed by name only. Binary, oversized and malformed output is refused or truncated.
- **Open in VS Code / Cursor** from diffs and from the resource inspector's new best-effort **Configuration source** link (`main.tf:12`), through the editor's URL handler with a validated, percent-encoded path. The editor is chosen in Settings.
- Plans record a **source fingerprint** (commit, branch, uncommitted IaC files, SHA-256 of the IaC files of the root and local modules). The review shows the source and warns *Plan generated from a different source revision*; the new preflight check `git.source` blocks applies after IaC changes (including local modules outside the project folder) and asks for an acknowledgement when only HEAD moved.
- New routes: `GET /api/v1/git/head`, `POST /api/v1/git/status`, `POST /api/v1/git/diff`, `POST /api/v1/editor/open` (and the same under `/api/v1/projects/{id}/`), `GET /api/v1/resources/{id}/source`, `GET /api/v1/contexts/{cid}/resources/{rid}/source`; plan detail adds `source` and `sourceStatus`. New package `internal/gitctx`; client route `/projects/<id>/workspaces/<name>/git`. Hclaro never runs Git commands that change a repository.

### Safe apply and preflight checks (M14, draft)

- **Apply preflight** in the apply dialog: context identity at the top (project, workspace, engine and version, backend and execution mode, profile, plan digest, fingerprint), blocking checks with remediation, warnings with one acknowledgement each, passed checks, and the impact of the whole saved plan (counts, high-risk resources, every planned change with requested ones marked). Apply stays disabled while anything blocks.
- Checks run on the server and again when the apply starts: context, profile and fingerprint, plan eligibility, digest and expiry, engine support, backend execution mode, local operation conflicts and policy validity block; a dirty Git tree and production-like names warn by default; replace/import plans that change other resources warn; destructive plans still need the typed phrase.
- Optional **Verify AWS identity** (STS GetCallerIdentity) only on request; unverified identity is shown as unknown, never inferred from a profile name.
- Project policy `.hclaro/policy.json` (version 1): `gitDirty`, `productionLabels`, `destructiveLimit`, `awsAccounts`. Invalid files block; the policy cannot relax built-in checks.
- New route `POST /api/v1/plans/{id}/preflight`; `POST /api/v1/plans/{id}/approval` now requires `{preflightBinding, acknowledged}`; new refusals `preflight_required`, `preflight_expired`, `acknowledgement_required`, `preflight_blocked`, `preflight_changed`. Apply operations record `preflight` (policy and acknowledged check IDs). New package `internal/preflight`.

### Plan and execution history (M13, draft)

- **History** in the sidebar: a persistent local record of plans (normal, replace, import), applies, drift scans, graphs, initializations, workspace commands and project tools, for the launch project and Hub projects. Each entry has its times, duration, exit code, status, engine and version, project and workspace, profile, context fingerprint, redacted command, diagnostic, Git commit and branch, a sanitized log excerpt and, for plans, the counts and addresses/actions of up to 300 changes.
- Planned changes and the execution outcome are separate: a plan shows whether an apply used it; an apply reports *Engine reported applied* (never verified), *Partial* or *Nothing applied*; cancelled and failed plans keep no plan; operations interrupted by a stop are shown as unknown at the next start.
- Filters (project, workspace, operation, status), search, a details drawer with *Planned* / *Executed* stages, comparison of two compatible plan summaries, and redacted Markdown/JSON export (without log lines). `hclaro --export-history FILE.md|FILE.json` writes the history to a new file and exits.
- Retention by count, age and size (default 500 operations, 90 days, 4 MiB), deleting one entry and clearing all or one project's history. Plan files, state, values, environment and raw logs are never stored, and a plan in history cannot be applied.
- `history.json` (schema 1) next to `projects.json`: atomic `0600` writes, recovery from corrupt files and interrupted writes, migrations for later schemas, read-only for newer ones.
- New routes: `GET /api/v1/history`, `GET /api/v1/history/{id}`, `GET /api/v1/history/compare`, `GET /api/v1/history/export`, `PATCH /api/v1/history/retention`, `DELETE /api/v1/history/{id}`, `DELETE /api/v1/history`. New package `internal/history`; `operations.Recorder`; client route `/history`.

### Workspace comparison (M12, draft)

- **Compare** in the sidebar compares the recorded state of two Hub workspaces: `{project, workspace, execution profile}` per side, in one project or across project roots, Terraform and OpenTofu mixed. Each side shows its root, engine, backend and last read time. Nothing is read until **Compare…** is confirmed; each side is read like a Hub tab (`show -json` with a per-command `TF_WORKSPACE`), and a side already read in this session is reused.
- Instances match by exact canonical address; renamed modules, resources or instances pair only through explicit mapping rules, and a mapping conflict is reported as uncomparable. Statuses: attribute differences, only left, only right, same, unknown (redacted values not compared) and uncomparable (type, mode or provider differs). Data sources are counted separately. The page states that a difference is not drift.
- Ignore rules (attribute paths, optionally per resource type) and substitution rules (`dev` → `prod`) for expected differences, with per-rule match counts and a warning that they can hide divergence. Credential-shaped rule text is refused; rules are not saved to disk.
- Status filters, search, mode/type filters, paging, a side-by-side redacted attribute diff, **Open in left/right workspace** deep links (with the same profile), Modules and Providers tabs, and Markdown/JSON export of a redacted summary (paths and counts, no values, no folder paths).
- A side that cannot be read makes the comparison partial, never empty; reading a side again, releasing it, or changing its project or profile makes the comparison stale and withholds its results.
- New routes: `GET /api/v1/comparisons/target`, `POST /api/v1/comparisons`, `GET /api/v1/comparisons`, `GET /api/v1/comparisons/{id}`, `…/resources`, `…/resources/{match}`, `…/export`. New package `internal/compare`; client route `/compare`. `make demo-hub` adds two comparison projects.

### Dependency graph and blast radius (M11, draft)

- A **Graph** view for the project opened from the terminal. **Generate graph** runs, after a preflight dialog, exactly `terraform graph` / `tofu graph` (no flags) and keeps a redacted model in memory. It never plans, refreshes, applies or reads a saved plan.
- Reads both the Terraform 1.7+ resource-only format and the full walk graph of earlier Terraform and OpenTofu; drops engine bookkeeping nodes, hides internal nodes by default and labels resource links through them as derived; detects cycles; withholds secret-shaped node names; refuses malformed or oversized output.
- Canvas with zoom, pan, fit, module collapse, provider/type/data/internal filters and search, plus an accessible virtualized list. The inspector shows dependencies, direct dependents and **Potential impact** (reachability with hop counts), maps resources to recorded instances only by address grammar, and links to **Resources**.
- **Compare with plan** overlays the **confirmed planned changes** of a complete, current plan and splits them, for the selected node, into those in its potential impact and the rest. Replace plans offer an optional **Inspect impact** step. A dependency is never presented as the cause of a change.
- New routes: `GET /api/v1/graphs/options`, `POST /api/v1/graphs`, `GET /api/v1/graphs`, `GET /api/v1/graphs/{id}`, `GET /api/v1/graphs/{id}/nodes/{node}`. New package `internal/graph`; engine `Grapher` interface.

### Drift detection (M10, draft)

- A **Drift** view for the project opened from the terminal. **Check drift** runs, after a preflight dialog, `plan -input=false -json -out=<private temp file> -refresh-only` with the selected variable files and execution profile, reads it back with `show -json` and deletes it. It never runs `refresh`, applies the refresh-only plan, writes state or starts a normal plan.
- Results classify drifted objects as changed, missing remotely or cannot verify, with a redacted **Recorded in state** / **Observed during scan** diff, grouping by module or provider, search, inspector links and a separate **Run a normal plan…** action. "No drift" is shown only for a complete, error-free scan; lock conflicts, expired credentials, cancellation and partial provider failures are reported as failed or incomplete.
- A scan stops matching (and its resource list is withheld) when the workspace, engine, configuration, variable files, data directory, environment or profile change. Up to ten redacted scans are kept in memory for 12 hours.
- Unsupported for remote/cloud execution, when planning is unavailable and when no state is recorded, with the reason shown.
- New routes: `GET /api/v1/drift-scans/options`, `POST /api/v1/drift-scans`, `GET /api/v1/drift-scans`, `GET /api/v1/drift-scans/{id}`, `GET /api/v1/drift-scans/{id}/resources/{change}`; progress and cancellation use the operation routes. New package `internal/drift`.

### Execution profiles (M9, draft)

- Hub projects can have named **execution profiles**: engine preference, workspace selector, approved variable files (canonical, symlink-safe, inside the project), `-parallelism`/`-lock-timeout` for plans, an AWS profile alias and region, and the names of expected environment variables. Profiles never store credentials, tokens, variable values or file contents; they are saved in `profiles.json` next to `projects.json` with revisions.
- A **Profile** selector in the context bar and on the project page chooses the profile for the current browser tab only; **Manage** creates, edits, clones, removes and sets the default.
- Plan, replace and import dialogs show the full execution context (engine, workspace, root, backend, variable files, AWS names, expected variables, notices) labeled **Identity not verified**, and bind its fingerprint to the operation. Init and workspace commands, Hub read contexts and project tools use the selected profile too.
- Each command gets the inherited environment with `TF_WORKSPACE`, `TF_LOG*` and `TF_CLI_ARGS*` removed (unsafe `TF_CLI_ARGS*` refuse, in any letter case), inherited static AWS keys removed when an AWS profile is chosen, and only `AWS_PROFILE`/`AWS_REGION`/`AWS_DEFAULT_REGION` added.
- Editing or deleting a profile makes plans created with it unapplicable and invalidates approvals; running operations keep the context they started with. Without a profile, behavior is unchanged.
- New routes under `/api/v1/projects/{id}/execution-profiles`, `/var-files` and `/execution-context`; `profileId` (and `contextFingerprint` for plans and imports) on the plan, import, context, init and workspace routes.
- Variable file paths passed with `-var-file` may now be relative paths in subfolders; names with spaces are refused.

## 0.1.0-alpha.6 — 2026-11-06

### Read the launch project again

- The project opened from the terminal has a **Read again** button (context bar and its error screen). It inspects the folder and rereads recorded state as at startup; no init, plan or refresh runs. Refused while a plan, apply, import or Hub operation runs in that folder.
- Initializing that same folder from the Hub rereads it automatically once init ends, so Plans, Import and Operations become available without restarting Hclaro. Previously the view stayed "uninitialized" until a restart.
- **Tools** in the sidebar now works for the project opened from the terminal: it opens that folder's Hub Tools view in the workspace the launch view reads, or, if the folder is not saved in the Hub, goes to the Hub's **Save to Hub** prompt. Previously it was disabled.
- New route: `POST /api/v1/workspace/reload` (`{}`) → 202; 409 `operation_running`, `workspace_loading`, `reload_unavailable`.

## 0.1.0-alpha.5 — 2026-11-06

The Workspace Hub, guided init, CLI workspace lifecycle and project tools are new in this release. They have been tested only against a synthetic engine, not a real Terraform/OpenTofu engine or remote backend; treat them as drafts and use them only on disposable projects.

### Project tools (M8, draft)

- Each Hub workspace tab has a **Tools** view (and the project page a **Project tools** button) with four read-only commands that run only on click: **Validate** (`validate -json`, human-output fallback), **Format check** (`fmt -check -no-color`, optional `-recursive`), **Providers** (`providers`) and **Outputs** (`output -json`, metadata only).
- Commands are fixed server-side; the request names only an open workspace context (and `recursive` for format check). No plain `fmt`, `-diff`, `-write`, `-chdir`, paths, executables or extra flags. Inherited `TF_CLI_ARGS`/`TF_CLI_ARGS_<command>` other than `-no-color` refuse.
- Validate distinguishes **valid**, **not valid** and **not validated** (init needed, providers missing, unreadable output), with sanitized diagnostics and `file:line:column`; snippets are never decoded. A not-validated result offers to review Init without running it.
- Format check enumerates the files it reads first, shows only those paths, never follows linked folders (a linked folder refuses a recursive check; a linked file outside the project refuses any check) and never writes.
- Providers and outputs run with the context's workspace as child `TF_WORKSPACE`; validate and format check run without one. Output values are skipped by the decoder and never stored, logged or returned; names that look like secrets are counted only.
- Tools take a shared claim on the project root: they run alongside each other and in parallel across projects, never alongside init, workspace commands or plans in the same folder. The same tool cannot run twice at once in one context. Cancel interrupts the process group and discards partial output.
- Operation center: **Operations in this project** lists init, workspace and tool operations for the project (this workspace or all) with status, duration, result and sanitized logs. In memory only; running operations are never evicted from the history.
- New routes: `POST /api/v1/projects/{id}/operations/{validate|fmt-check|providers|outputs-metadata}` (`{contextId, recursive?}`), `GET /api/v1/projects/{id}/operations`; operations use `kind: "tool"` and the shared `/api/v1/operations/{id}` routes. Client route `/projects/<id>/workspaces/<name>/tools`.
- The synthetic CLI emulates validate, fmt -check, providers and output -json with failure modes (`HCLARO_FAKE_TOOL`).

### CLI workspace lifecycle (M7, draft)

- A Hub project page offers **New workspace…** and, under **Advanced actions**, **Set CLI default…** and **Delete** per workspace. Each lists workspaces with the CLI first, shows the exact command, effects and inherited-setting notices, and runs only after a checkbox (create, set default) or the exact typed name (delete).
- Commands are fixed: `workspace new NAME`, `workspace select NAME`, `workspace delete NAME`. Names cannot start with `-`; no `-force`, `-or-create`, `-lock=false` or `-state`. `TF_WORKSPACE` is not passed; `TF_CLI_ARGS`/`TF_CLI_ARGS_workspace` with other flags make the actions unavailable.
- Creating warns that the engine also changes the CLI default; setting the CLI default warns that terminal commands in the folder now target that state. Choosing a workspace in a tab still never runs `workspace select`.
- Delete only for an empty (no resources or outputs), non-default, non-CLI-default workspace on the local backend, checked at review and again before running. Remote, unknown and non-workspace backends disable the actions with a reason.
- Reviews are single-use and expire after 10 minutes; a changed workspace list, CLI default, root, engine, configuration or `TF_*` environment refuses the start. One command per project root, shared with init and plans; cancel interrupts the process group.
- Results show exit code and CLI default before/after; success requires a listing that shows the effect, otherwise `unknown` with advice to list again. Nothing is retried or undone automatically. A deleted workspace's open views close.
- Hub cards show the CLI default, the workspace last opened in Hclaro and which workspace changes the backend allows.
- New routes: `POST /api/v1/projects/{id}/workspaces/review`, `POST /api/v1/projects/{id}/operations/workspace`; operations use `kind: "workspace"` and the shared `/api/v1/operations/{id}` routes.

### Guided init (M6, draft)

- A Hub project page offers **Initialize…** / **Run init again…**. A read-only review shows the folder, engine, backend, workspace context, data directory, lock file, the exact command and its effects; init runs only after an explicit acknowledgement and click.
- The command is fixed: `init -input=false -no-color`, or `init -input=false -json` for Terraform 1.9+. No upgrade, migration, force-copy, reconfigure or backend values; inherited `TF_CLI_ARGS`/`TF_CLI_ARGS_init` with other flags make init unavailable; `TF_WORKSPACE` is not passed.
- The reviewed context (root, engine executable, configuration and lock file fingerprint, `TF_*` environment) must be unchanged at request time and again just before the engine starts.
- Sanitized streaming log with cancel (interrupts the engine's process group), honest status (`succeeded` only with exit 0 and a data directory; `unknown` otherwise), exit code, lock-file change notice and classified remediation for backend migration, interactive input, lock-file upgrade, credentials, network, registry, backend settings, state lock and version mismatch. Never retried automatically.
- One operation per project root: init, and the launch project's plan/apply/import, exclude each other for the same folder; listing or reading that project's workspaces waits. Other projects initialize independently.
- New routes: `GET /api/v1/projects/{id}/init-status`, `POST /api/v1/projects/{id}/operations/init`; init operations use the existing `/api/v1/operations/{id}`, `/events` and `/cancel`. Operation statuses add `queued`, `cancelling` and `unknown`.

### Workspace Hub (M5, draft)

- New **Projects** view, reachable from every screen: register Terraform/OpenTofu root folders by typed path or with a one-folder-at-a-time folder chooser; rename, pin, reorder and remove them; see status, initialization, backend type, engine version, workspace count and last-opened time. Removing forgets the entry only. Moved or deleted folders stay listed with **Repair path**.
- The registry is a versioned `projects.json` in the per-user configuration directory (`HCLARO_CONFIG_DIR` overrides it), written atomically. Corrupt files are moved aside; newer or unreadable files are never overwritten.
- Opening a project lists its CLI workspaces with `workspace list` and marks the CLI default. Opening a workspace reads its recorded state read-only with a per-command `TF_WORKSPACE`; `workspace select` never runs and the terminal's workspace is unchanged. Each browser tab holds its own project and workspace (`/projects/<id>/workspaces/<name>/resources`), with a workspace switcher, **New tab** and **Read again**.
- Hub workspaces are read-only. Plans, applies and imports remain for the project Hclaro was launched with.
- `hclaro --hub` opens the Hub; `hclaro` outside a project opens the Hub instead of failing. `--path` keeps its behavior, including failing for an explicit path that is not a project.
- An absolute `TF_DATA_DIR` disables Hub reads (it would be shared by every project); an inherited `TF_WORKSPACE` is reported and replaced only in Hub children.
- New routes: `GET/POST /api/v1/projects`, `PATCH/DELETE /api/v1/projects/{id}`, `POST /api/v1/projects/order`, `POST /api/v1/projects/{id}/inspect`, `POST /api/v1/projects/browse`, `POST /api/v1/contexts`, `GET /api/v1/contexts/{id}[/resources[/{id}]]`.
- `make demo-hub` runs the Hub with synthetic projects and a throwaway registry.

## 0.1.0-alpha.4 — 2026-11-05

Replace, saved-plan apply and import are new in this release. They have been tested only against synthetic engine output, not a real Terraform/OpenTofu engine; treat them as drafts and use them only on disposable workspaces.

### Import

- New **Import** view: enter an address, import ID, optional provider and a new file name; **Check and preview** validates without writing or running anything and shows the exact import block.
- Detects an address already in state, an object with the same `id` already managed, a missing resource block, count/for_each key mismatches, an existing import/moved block for the address, existing destination files and credential-shaped IDs.
- After explicit consent, writes the block to a new root-module file (never overwrites; symlinks refused) and runs a full plan. Optional `-generate-config-out` for root, unindexed addresses writes `<name>_generated.tf`; the review shows a redacted copy.
- Import plans count imports separately, are applicable even when the import is the only (no-op) change, and always require the typed `apply <workspace>` phrase.
- Unavailable workspaces (for example remote execution) get a manual recipe; `terraform import` is never run.
- Capabilities gain `supportsImportBlocks`, `supportsGeneratedConfig`, `canImport` and `importReason`. New routes: `GET /api/v1/imports/options`, `POST /api/v1/imports/preview`, `POST /api/v1/imports`.

### Replace

- Select managed instances in the resource explorer (up to 50; data sources cannot be selected) and plan their replacement: a full plan with one `-replace=<address>` per instance, never `-target`.
- Replacement review lists the requested instances, flags any the plan does not replace, and counts every other change, including deletions and replacements you did not select.

### Saved-plan apply

- Apply a reviewed plan for locally executed workspaces. Remote/cloud execution stays plan-only.
- Only the most recent eligible plan keeps its saved file (private `0700` temp directory, one hour). It is deleted after one apply attempt, on expiry, when superseded and on shutdown.
- Apply needs a one-time, five-minute approval bound to the plan, and typing `apply <workspace>` for plans that delete, replace or forget objects. Context, engine version, workspace and the file's SHA-256 are re-checked; the engine's stale-plan refusal is reported, never retried.
- Apply logs stream like plan logs. State is re-read afterwards, and the outcome (applied, not applied, uncertain) is reported separately from the exit status.
- New routes: `POST /api/v1/plans/{id}/approval`, `POST /api/v1/plans/{id}/apply`. `POST /api/v1/plans` accepts `replace` (resource IDs).
- After a failed apply request, the dialog requests a new approval and asks for the phrase again instead of having to be closed and reopened.
- On startup, saved-plan directories older than three hours (left only by a Hclaro process that was killed) are deleted from the temp directory.

### Details drawer

- Resource and change details open in a non-modal drawer docked to the right edge: 720px by default, resizable by dragging or with arrow keys, width remembered per browser. Esc or × closes it.

### Fixes

- A leading icon in information lines (plan, replacement and import summaries) no longer sits on its own line.

### Development

- Pull requests and `master` are verified on CircleCI instead of GitHub Actions; tagged releases still build on GitHub Actions.

## 0.1.0-alpha.3 — 2026-11-05

### Readability

- Larger type: text that was 7–11px is now 10–14px; main UI text is 13–14px.
- Attribute names in the JSON tree no longer shrink when values are long (`arn` no longer wraps to "ar / n").

### Redaction policy

- Redaction now hides likely secrets instead of everything outside a 25-name allowlist. Hidden: values marked sensitive (at any depth), secret-like attribute names, and credential-shaped or random-looking values (PEM, JWT, bearer, AWS access key IDs, URLs with passwords, vendor token prefixes, long mixed-case or hex strings). Tags, enums, booleans, numbers, IDs, ARNs and regions are now shown in the inspector and plan review.
- Output values follow the same policy instead of always being hidden.
- Fixed: a plan diff row whose value is an object ignored sensitivity markers nested inside it.
- `asia`/`akia` substrings and plain URLs no longer hide values or reject state metadata; only full AWS access key IDs and URLs with credentials do.
- Plan diffs say "not set" / "removed" instead of "—".

## 0.1.0-alpha.2 — 2026-11-05

First release published by the release workflow. Covers everything since 0.1.0-alpha.1.

### Release packaging

- Tag-triggered release workflow: full CI, embed drift check, cross-compiled archives for macOS/Linux (amd64, arm64) and Windows amd64 with checksums, native `--version` smoke tests, and a GitHub release (pre-release for suffixed tags).
- CI no longer runs twice on tag pushes; the release workflow calls it instead.

### M2 plan visualizer

- Explicit plan runs from the UI with a preflight dialog: `plan -input=false -json -out=<private temp>`, then `show -json`; optional variable files from the project root.
- Plan review: per-action counts and filters, complete change list (including collateral deletes, reads, moves, imports and deposed objects), attribute diffs with sensitive / known-after-apply / forces-replacement markers, outputs, drift, plan SHA-256 and configuration-change warning.
- Live sanitized progress over SSE with bounded replay, cancellation (interrupt, then kill after 15 s), one-hour timeout and shutdown cleanup. Saved plans are deleted as soon as review data is extracted.
- Operations view with session history.
- Two CSRF-protected POST routes (start plan, cancel). No apply endpoint.
- Windows cancellation sends `CTRL_BREAK` to the engine's own process group, so it can release a state lock before the 15 s kill.
- Diagnostics and log messages redact known input values (`TF_VAR_*` and loaded variable files) before the line heuristics.
- The context fingerprint now covers the data directory (backend metadata, workspace, modules) and planning environment variables, and is taken before the engine runs.
- Verified against real Terraform 1.5.5 on a disposable local project. Fixes found there: the default local backend (no `backend` block) is now recognized for planning; partly unknown outputs show "known after apply" per element; a blank first line no longer hides the interrupt message.


### M1 verification closed locally

- Embedded the production Vite build in place of the offline preview.
- Verified locally: Go race tests, Vitest, typecheck, ESLint, production and cross builds, live loopback demo with Host/Origin/session checks, full-API canary sweep, Ctrl+C shutdown, and Playwright browser checks.
- Fixed third-party notice generation for packages that declare a license without shipping a license file.
- Fixed an ambiguous browser-test locator.
- Scoped Go test/vet to project packages so `web/node_modules` is never compiled; CI and `make install` now use `npm ci` only.
- Updated frontend dependencies within their current majors, fixing critical/high Vitest and Vite dev-server advisories. Playwright held at 1.58 for macOS 13.
- Raised the Go minimum to 1.26. Pinned CI actions to v7 release commits, disabled credential persistence and dependency caches, and added an embedded-asset drift check.
- Upgraded majors: Vite 8, @vitejs/plugin-react 6, Vitest 5, ESLint 10, lucide-react 1.x, jsdom 29, jest-dom 7. TypeScript stays on 5.9 until typescript-eslint supports 7.
- CI passes on Ubuntu 24.04, macOS and Windows; Ubuntu is pinned for Playwright 1.58.
- Open: Linux/Windows packaged-binary runtime checks, real engine certification.

## 0.1.0-alpha.1 — M1 implementation, verification incomplete

- Added Go CLI, official read-only engine adapter, discovery, bounded state parsing/redaction and secure local HTTP API.
- Added React resource explorer, module tree, inspector, overview, provider/module views and appearance settings.
- Added synthetic fixtures, fake executables, API/security/parser tests, and build/development tooling.
- Included an explicitly labeled offline UI preview; npm/production frontend checks and browser/live-loopback verification remain blocked by the development sandbox. No verified release is declared.
