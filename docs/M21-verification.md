# M21 implementation and verification

**Status: draft.** The sidebar is regrouped around the user journey (understand a project → plan and analyze → manage), with a project switcher, a separate Terraform/OpenTofu workspace selector, an Import & discovery parent entry, an icon-only desktop mode and a narrow-screen drawer. It is a navigation refactor only: no Go code, API route, client route, CLI argument, confirmation or redaction changed.

M21 builds on M01–M17 and M19. M18 (state refactoring studio) and M20 (AWS session manager) are not on `master`; nothing here depends on them.

## Audit (FR-01)

### Routes before and after

Client routes are unchanged (`web/src/router.ts`); the Go server still serves the app shell only for these paths (`internal/api/api.go`, `clientRoute`).

| URL | Route kind | Scope | Workbench |
| --- | --- | --- | --- |
| `/` | `launch` | Project opened from the terminal; its views are in memory, not in the URL | `launch` |
| `/projects` | `hub` | Global: the Hub project list | `launch` |
| `/projects/<id>` | `project` | One Hub project: workspaces, init, workspace lifecycle, profiles | `launch` |
| `/projects/<id>/workspaces/<ws>/<view>` | `context` | One Hub project workspace, read-only; `<view>` ∈ resources, overview, modules, providers, tools, git; `?resource=` and `?profile=` deep-link parameters | `<id>/<ws>` |
| `/compare` | `compare` | Global | `launch` |
| `/history` | `history` | Global | `launch` |

### Sidebar destinations: old → new

| Before (label · group) | Implementation | After (label · group) | Change |
| --- | --- | --- | --- |
| Projects · top | `navigate('/projects')`, `HubView` | **Project switcher → All projects** | Moved into the switcher, with the terminal project, up to 5 recent Hub projects and the current project's workspace page |
| project tile (static) | text only | **Project switcher** (button) | Now a control; shows status (ready, loading, missing, none) |
| Workspace `<select>` · context bar (Hub workspaces) | `navigate(contextPath(...))` | **Workspace** selector under the switcher | Moved; context bar keeps a read-only workspace label and the production-like badge. In compact mode it is inside the switcher panel |
| Overview, Resources (count), Modules, Providers · WORKSPACE | explorer views in `App.tsx` | same · project group (no heading) | Unchanged behaviour; count badge kept |
| Plans · WORKFLOWS | `PlansView` (launch only) | Plans · **Plan & analyze** | — |
| Drift · WORKFLOWS | `DriftView` | **Drift detection** · Plan & analyze | Renamed |
| Graph · WORKFLOWS | `GraphView` | **Dependency graph** · Plan & analyze | Renamed |
| Compare · top | `/compare`, `CompareView` | Compare · Plan & analyze | Same URL; accessible name is now the visible label |
| Import, Discovery · WORKFLOWS | `ImportView`, `DiscoveryView` | **Import & discovery** · Manage, with **Import existing** / **Discover cloud resources** sub-navigation | One entry, two unchanged workflows; *Prepare import* still hands the prefill from Discovery to Import |
| Operations (running indicator) · WORKFLOWS | `OperationsView` | Operations · Manage | Running indicator kept |
| History · top | `/history`, `HistoryView` | History · Manage | Same URL |
| Git · WORKSPACE | `GitView` (launch and Hub workspaces) | Git · bottom | Kept as a page: it is a full repository/changes/diff view. The context-bar Git badge still opens it |
| Tools · WORKSPACE | `ToolsView` (Hub workspaces; launch → jumped straight to its Hub entry or *Save to Hub*) | Tools · bottom | Kept as a page: four read-only commands with results and per-context history. For the terminal-opened project it now opens an explanation page (`hub/LaunchTools.tsx`) with **Open tools for workspace …**, **Open in new tab** or **Go to Save to Hub**, instead of switching the tab's project without warning. Also on the project page as **Project tools** |
| Settings · WORKSPACE | `SettingsPanel` (app display preferences) | Settings · bottom | Moved; labelled *Settings*, not *Project settings*, because it holds application preferences only |
| Local by design / Alpha / v0.1 | footer | one-line footer | Compact |

### Ambiguous scope and how it is handled

- **Global pages and the project nav.** On `/projects`, `/compare` and `/history` the sidebar shows the project opened from the terminal (as before, its items open it at `/`). No project item is marked current on a global page.
- **Hub project page (`/projects/<id>`).** Previously project items silently opened the *terminal* project. They now open the Hub project's last-used (or CLI-default) workspace; without one they are disabled with *Choose a workspace of this project first.* Launch-only items stay disabled there.
- **Launch-only workflows.** Plans, Drift detection, Dependency graph, Import & discovery and Operations stay disabled in Hub workspaces with the existing read-only explanation.
- **Settings in a Hub workspace** is not in the URL (as before). Back/forward now leaves it, because Hub explorer view changes push history entries instead of replacing them.

## Implementation

| File | Change |
| --- | --- |
| `web/src/navigation/config.ts` (new) | Typed items: id, label, icon, group, scope, target (view with child views, or global route), badge, description. `navState`/`isActive` derive availability and active state from the parsed route and view; `viewTrail` names breadcrumbs. |
| `web/src/navigation/Sidebar.tsx` (new) | Brand row with collapse/close buttons, project switcher (disclosure panel: Escape and outside click close, focus returns), workspace control slot, grouped `<nav aria-label="Main">`, scrolling middle, fixed footer, compact-mode tooltips (`position: fixed`, on hover *and* focus, Escape dismisses), Import & discovery sub-navigation. |
| `web/src/navigation/ConfirmSwitch.tsx` (new) | Modal (shared `useModal`) listing what a switch would discard; *Stay here* is focused. |
| `web/src/router.ts` | `workbenchKey(route)` (was inline in `App`). |
| `web/src/App.tsx` | Uses the config and `Sidebar`; `sidebar` display preference; narrow drawer state; guarded `requestSwitch`; Hub project page targets; breadcrumbs from `viewTrail`; Hub view changes push history; registry read on every route for the switcher (a local file read, no engine command). |
| `web/src/styles.css` | `--sidebar-width` drives the column and content offset; denser items; compact rail; drawer (`visibility: hidden` when closed, so it leaves the tab order); switcher, tooltip, sub-navigation and confirm styles. The old ≤ 900 px icon rail is replaced by the drawer. |
| `scripts/browser-smoke.mjs` | Opens Import/Discovery through the new entry; new navigation checks and screenshots. |
| Tests | `web/src/navigation/Navigation.test.tsx` (new); three assertions in `App.test.tsx`/`Hub.test.tsx` updated for renamed items. |
| Docs | README *Navigation* section and renamed entries, `docs/architecture.md`, `CHANGELOG.md`, this file. |

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.3, Node 24.2, Playwright Chromium. No network, credential or real engine was used: the browser suite runs the real Go handler on synthetic fixtures with every non-loopback request aborted; manual screenshots came from `make demo-hub` (synthetic projects and fake engines).

| Check | Result |
| --- | --- |
| `make lint` (`go vet`, `tsc -b`, ESLint `--max-warnings 0`) | Passed |
| `make test` (`go test -race`, Vitest) | Passed; Vitest 160/160 in 18 files (146 before M21) |
| New UI tests (`Navigation.test.tsx`, 14) | Every implemented view reachable exactly once; Hub read-only, no-project and Hub-project availability with reasons; active state for nested Import/Discovery, global routes and Hub URLs; breadcrumbs and workbench keys; grouped render with exactly one `aria-current`; terminal workspace shown but not switchable; project switcher (recent list, Escape returns focus, switch opens only `POST /contexts`); Hub workspace switching from the sidebar, separate from the project; confirmation before discarding a replacement selection (*Stay here* keeps it, global pages do not ask, *Switch anyway* proceeds); Import & discovery tabs, breadcrumb and last-used tab with **no mutating request**; Git/Tools/Settings in the footer with URL updates and back navigation leaving Settings; Hub project page items open the last workspace; compact mode persistence, names, `aria-current`, focus tooltip, Escape dismissal, keyboard activation, compact switcher with the workspace selector; narrow drawer focus, Escape, close on navigation. |
| `make build`, `make browser-test` | Passed, including the new checks: all 14 items and both group headings present; switcher Escape/focus; compact width 64 px, focus tooltip and Escape, Enter activation; preference survives reload; at 1512 × 430 the groups scroll and Settings stays in view; at 760 px the closed drawer is hidden with no horizontal overflow, opens from the menu button, closes with Escape (focus returns) and after choosing a page; all existing plan/apply/import/Git/discovery/cost checks; no external requests. |
| Screenshots | `docs/screenshots/navigation-expanded-dark.png`, `navigation-switcher-dark.png`, `navigation-compact-dark.png`, `navigation-drawer-dark.png` (browser suite). Light and dark Hub, project page and Hub workspace switcher were inspected manually from `make demo-hub`. |
| Behaviour unchanged | No Go file changed. Terraform/OpenTofu commands, confirmations (plan preflight, typed apply confirmation, import consent, discovery scan consent, workspace lifecycle), redaction and security headers are untouched. Navigation code calls only `navigate`, and the switcher's only server effect is the existing context open when a workspace page is shown. |

## Intentional deviations

- **Labels are sentence case** (*Drift detection*, *Dependency graph*, *Import & discovery*), matching the rest of the UI rather than the task's title case. Group headings are styled uppercase with CSS, so screen readers do not spell them.
- **Settings stays *Settings*.** It contains theme, density, grouping and editor preferences for the whole app; calling it *Project settings* would misdescribe it.
- **No *Open project* item in the switcher.** Adding a project is a dialog on the Hub page; **All projects** reaches it in one click. Adding a second entry point would duplicate the dialog's validation flow.
- **Launch-project views are still not in the URL.** Making them deep-linkable needs new server shell routes, which this milestone does not change. Back/forward work for Hub workspace views, Compare, History and the Hub.

## Known limitations

- The switch confirmation covers work the workbench holds (replacement selection, a replacement plan not started, a discovery-prepared import). Text typed into the Import wizard or a dialog is local to that component and is not detected; browser back/forward does not ask.
- In compact mode, tooltips for disabled items depend on the browser delivering pointer events to disabled buttons; the reason is always available as the item's `title` in expanded mode.
- Recent projects come from the registry's `lastOpenedAt`; projects never opened in Hclaro are listed only under **All projects**.

## Manual verification

1. `make demo-hub` and open the printed URL. The sidebar shows the switcher (*No project open*), project items disabled with a reason, and Compare, History and Settings enabled.
2. Open **network**: the switcher shows *network · Choose a workspace* or opens the last workspace from **Overview**. Open a workspace: the **Workspace** selector appears under the switcher; switch to `prod` and see the production-like badge in the context bar.
3. Use **Collapse sidebar**, Tab through the rail (labels appear on focus, Escape hides them), reload (still compact), then **Expand sidebar**.
4. Narrow the window below 900 px: the menu button opens the drawer; Escape, the close button, the backdrop or choosing a page closes it.
5. `make demo`: select a resource for replacement, open the switcher and choose another project; confirm that *Leave this project?* appears. Open **Import & discovery** and switch between **Import existing** and **Discover cloud resources**; nothing is scanned or written.
