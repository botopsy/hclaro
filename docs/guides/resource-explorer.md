# Resource Explorer and navigation

Browse the recorded state of a workspace.

## Explorer

- Resource table with 50-row pages, address sorting, and composable provider/type/mode/module filters.
- Hierarchical modules with direct resource counts; data sources remain distinct from managed resources.
- Inspector with canonical address, provider source, module, instance key, nested JSON, and copy actions.
- Overview with resource, data source, child-module and provider counts and distribution by type.
- Light/dark/system themes, compact density, and flat/module grouping. Cmd/Ctrl+K focuses resource search; arrow keys navigate resource buttons; Escape closes details. Tab and native disclosure controls navigate modules and JSON.
- **Redacted values.** Values the server redacts show as hidden in the inspector. **Reveal** (the eye icon) fetches exactly one hidden value on your explicit click: the server reads state again, returns that value only (never cached, logged, stored, exported or sent to AI), limits reveals to 10 per minute, and the browser drops the value on **Hide**, closing the inspector, navigation or after the page was hidden for five minutes. A copied value is on your clipboard. Workspaces opened through the Hub support reveal; the project opened from the terminal does not. See [SECURITY.md](../../SECURITY.md).

## Navigation

The sidebar follows the usual order of work: the **project switcher** and the Terraform/OpenTofu **workspace** on top, then the project's **Overview**, **Resources**, **Modules** and **Providers**; **Plan & analyze** (Plans, Drift detection, Dependency graph, Compare); **Manage** (Import & discovery, Operations, History); and **Git**, **Tools** and **Settings** at the bottom. Project, workspace and Git branch are shown separately and never stand in for each other; the workspace of the project opened from the terminal is shown but not switchable, because Hclaro never selects a CLI workspace for you.

- **Import & discovery** opens two separate workflows, **Import existing** and **Discover cloud resources**, one click apart, and reopens the one you used last in the tab.
- Items that do not apply are disabled with the reason as their tooltip, for example the workflows of a read-only Hub workspace.
- Switching project never runs a command. If the switch would discard a replacement selection or an import prepared from discovery in this tab, Hclaro asks first.
- **Collapse sidebar** keeps an icon-only rail with labels on hover and keyboard focus (Escape hides them); the choice is saved with the display preferences. Below 900 px the sidebar is a drawer opened from the menu button and closed with Escape, the close button or a click outside.
