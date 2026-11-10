// Typed navigation model (M21). The sidebar renders these items; availability and the active
// state are derived from the parsed route and the current view, never from URL substrings.
import { FolderTree, GitBranch, GitCompareArrows, History, Import, Layers3, LayoutDashboard, Network, ScanSearch, Settings2, Terminal, Waypoints, Workflow, Wrench, type LucideIcon } from 'lucide-react';
import type { Route } from '../router';

// Every page the workbench can show for a project. Explorer views also live in Hub
// workspace URLs (see router.ts); the others are kept in memory for the launch project.
export type View = 'Overview' | 'Resources' | 'Modules' | 'Providers' | 'Tools' | 'Git' | 'Settings' | 'Plans' | 'Drift' | 'Graph' | 'Discovery' | 'Import' | 'Operations';

export type NavGroup = 'project' | 'analyze' | 'manage' | 'utility';

export const groupLabels: Record<NavGroup, string | undefined> = {
  project: undefined,
  analyze: 'Plan & analyze',
  manage: 'Manage',
  utility: undefined
};

// Where an item may be used:
// - project: any open project workspace (terminal-opened or Hub workspace);
// - launch:  only the project opened from the terminal (Hub workspaces are read-only);
// - execution: the project opened from the terminal, and Hub workspaces when the server
//              enables scoped execution (M24.07–M24.08; plans, imports, replacements and
//              applies run in that workspace only);
// - global:  a page of its own, independent of the open project;
// - app:     application preferences, always available.
export type NavScope = 'project' | 'launch' | 'execution' | 'global' | 'app';

export type NavTarget = {
  kind: 'view';
  view: View;
  // Child views that keep this item active (for example the Import & discovery tabs).
  children?: View[];
} | {
  kind: 'route';
  route: 'compare' | 'history';
};

export type NavBadge = 'resources' | 'operations';

export type NavItem = {
  id: string;
  label: string;
  icon: LucideIcon;
  group: NavGroup;
  scope: NavScope;
  target: NavTarget;
  // Extra text for assistive technology; the visible label is always part of the name.
  description?: string;
  badge?: NavBadge;
};

export const navItems: readonly NavItem[] = [
  { id: 'overview', label: 'Overview', icon: LayoutDashboard, group: 'project', scope: 'project', target: { kind: 'view', view: 'Overview' } },
  { id: 'resources', label: 'Resources', icon: Layers3, group: 'project', scope: 'project', target: { kind: 'view', view: 'Resources' }, badge: 'resources' },
  { id: 'modules', label: 'Modules', icon: FolderTree, group: 'project', scope: 'project', target: { kind: 'view', view: 'Modules' } },
  { id: 'providers', label: 'Providers', icon: Network, group: 'project', scope: 'project', target: { kind: 'view', view: 'Providers' } },
  { id: 'plans', label: 'Plans', icon: Workflow, group: 'analyze', scope: 'execution', target: { kind: 'view', view: 'Plans' } },
  { id: 'drift', label: 'Drift detection', icon: ScanSearch, group: 'analyze', scope: 'launch', target: { kind: 'view', view: 'Drift' } },
  { id: 'graph', label: 'Dependency graph', icon: Waypoints, group: 'analyze', scope: 'launch', target: { kind: 'view', view: 'Graph' } },
  { id: 'compare', label: 'Compare', icon: GitCompareArrows, group: 'analyze', scope: 'global', target: { kind: 'route', route: 'compare' }, description: 'Compare workspaces' },
  { id: 'import-discovery', label: 'Import & discovery', icon: Import, group: 'manage', scope: 'execution', target: { kind: 'view', view: 'Import', children: ['Discovery'] }, description: 'Import existing objects or discover cloud resources' },
  { id: 'operations', label: 'Operations', icon: Terminal, group: 'manage', scope: 'execution', target: { kind: 'view', view: 'Operations' }, badge: 'operations' },
  { id: 'history', label: 'History', icon: History, group: 'manage', scope: 'global', target: { kind: 'route', route: 'history' }, description: 'Operation history on this machine' },
  { id: 'git', label: 'Git', icon: GitBranch, group: 'utility', scope: 'project', target: { kind: 'view', view: 'Git' }, description: 'Branch, commit and changed IaC files' },
  { id: 'tools', label: 'Tools', icon: Wrench, group: 'utility', scope: 'project', target: { kind: 'view', view: 'Tools' }, description: 'Validate, format check, providers and outputs' },
  { id: 'settings', label: 'Settings', icon: Settings2, group: 'utility', scope: 'app', target: { kind: 'view', view: 'Settings' } }
];

// The two workflows behind the Import & discovery entry. They stay separate pages.
export const importDiscoveryTabs: readonly { view: View; label: string }[] = [
  { view: 'Import', label: 'Import existing' },
  { view: 'Discovery', label: 'Discover cloud resources' }
];

// What the sidebar is showing a project for:
// - launch:        the project Hclaro was started with (all workflows);
// - hub-workspace: a Hub project's CLI workspace, read-only (/projects/<id>/workspaces/<ws>);
// - hub-project:   a Hub project page with no workspace open yet (/projects/<id>);
// - none:          started without a project; choose one in the Hub.
export type NavMode = 'launch' | 'hub-workspace' | 'hub-project' | 'none';

export type NavContext = {
  route: Route;
  mode: NavMode;
  view: View;
  // For hub-project: the workspace project items open, if one is known.
  projectWorkspace?: string;
  // The server runs plans and applies in Hub workspaces (session capability).
  scopedExecution?: boolean;
};

export type NavState = {
  enabled: boolean;
  active: boolean;
  // Why an item is unavailable, or what it does when that is not obvious.
  reason?: string;
};

const hubPlansOnly = 'Hub workspaces run plans, imports, replacements and applies only. Drift scans, dependency graphs and cloud discovery run only for the project opened from the terminal.';
const hubReadOnly = 'Hub workspaces are read-only. Plans, drift scans, dependency graphs, cloud discovery, applies and imports run only for the project opened from the terminal.';

// Pages that show the current project. Global pages (Projects, Compare, History) do not.
export function onProjectPage(route: Route) {
  return route.kind === 'launch' || route.kind === 'context';
}

export function isActive(item: NavItem, route: Route, view: View) {
  if (item.target.kind === 'route') return route.kind === item.target.route;
  if (!onProjectPage(route)) return false;
  return view === item.target.view || !!item.target.children?.includes(view);
}

export function navState(item: NavItem, context: NavContext): NavState {
  const active = isActive(item, context.route, context.view);
  if (item.scope === 'global' || item.scope === 'app') return { enabled: true, active };
  if (context.mode === 'none') return { enabled: false, active, reason: item.scope === 'launch' ? 'Available for a project opened from the terminal.' : 'Open a project from Projects first.' };
  if (item.scope === 'launch' && context.mode !== 'launch') return { enabled: false, active, reason: context.scopedExecution ? hubPlansOnly : hubReadOnly };
  if (item.scope === 'execution' && context.mode !== 'launch' && !context.scopedExecution) return { enabled: false, active, reason: hubReadOnly };
  if (context.mode === 'hub-project' && !context.projectWorkspace) return { enabled: false, active, reason: 'Choose a workspace of this project first.' };
  return { enabled: true, active };
}

export function itemsIn(group: NavGroup) {
  return navItems.filter(item => item.group === group);
}

export function navItem(id: string) {
  const item = navItems.find(i => i.id === id);
  if (!item) throw new Error(`Unknown navigation item ${id}`);
  return item;
}

// Page labels for breadcrumbs: the navigation label, with the Import & discovery parent.
export function viewTrail(view: View): string[] {
  const tab = importDiscoveryTabs.find(t => t.view === view);
  if (tab) return ['Import & discovery', tab.label];
  const item = navItems.find(i => i.target.kind === 'view' && i.target.view === view);
  return [item?.label || view];
}
