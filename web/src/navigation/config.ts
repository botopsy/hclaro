// Typed navigation model (M21). The sidebar renders these items; availability and the active
// state are derived from the parsed route and the current view, never from URL substrings.
import { FolderTree, GitBranch, GitCompareArrows, History, Import, Layers3, LayoutDashboard, Network, ScanSearch, Settings2, Sparkles, Terminal, Waypoints, Workflow, Wrench, type LucideIcon } from 'lucide-react';
import type { Route } from '../router';

// Every page the workbench can show for a project. Views with a segment also live in Hub
// workspace URLs (see router.ts); Settings is kept in memory only.
export type View = 'Overview' | 'Resources' | 'Modules' | 'Providers' | 'Tools' | 'Git' | 'Settings' | 'Plans' | 'Drift' | 'Graph' | 'Discovery' | 'Import' | 'Operations' | 'Assistant';
export type ExplorerView = Exclude<View, 'Settings'>;

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
//              enables scoped execution (M24.07–M24.09; plans, imports, replacements,
//              applies, drift scans and graphs run in that workspace only);
// - global:  a page of its own, independent of the open project;
// - app:     application preferences, always available.
type NavScope = 'project' | 'launch' | 'execution' | 'global' | 'app';

// The page table: one row per view. Adding a page adds a row here and its component in App.
// segment: the view's URL segment in a Hub workspace; badge: the safety note in its heading.
type Page = { segment?: string; title: string; description: string; badge: string; scope: NavScope };
const confirm = 'Confirm before apply';
export const pages: Record<View, Page> = {
  Overview: { segment: 'overview', title: 'Workspace overview', description: 'The shape of your infrastructure, at a glance.', badge: 'Read-only', scope: 'project' },
  Resources: { segment: 'resources', title: 'Resource explorer', description: 'A clear view of everything recorded in your state.', badge: 'Read-only', scope: 'project' },
  Modules: { segment: 'modules', title: 'Modules', description: 'Explore the structure behind your resources.', badge: 'Read-only', scope: 'project' },
  Providers: { segment: 'providers', title: 'Providers', description: 'Provider sources referenced by this state snapshot.', badge: 'Read-only', scope: 'project' },
  Plans: { segment: 'plans', title: 'Plans', description: 'See every change a plan proposes before anything happens.', badge: confirm, scope: 'execution' },
  Drift: { segment: 'drift', title: 'Drift detection', description: 'Compare what state records with what providers report, only when you ask.', badge: 'Never writes state', scope: 'execution' },
  Graph: { segment: 'graph', title: 'Dependency graph', description: 'See what depends on what, and what a change could reach. Only a plan confirms what will change.', badge: 'Read-only', scope: 'execution' },
  Import: { segment: 'import', title: 'Import an existing object', description: 'Bring an object that already exists into state through a reviewed import block and plan.', badge: confirm, scope: 'execution' },
  Discovery: { segment: 'discovery', title: 'Cloud discovery', description: 'Find existing AWS objects this workspace does not record yet, and prepare a reviewed import. Only when you ask.', badge: 'Read-only cloud reads', scope: 'execution' },
  Operations: { segment: 'operations', title: 'Operations', description: 'Plans and applies run during this session, with sanitized output.', badge: confirm, scope: 'execution' },
  Git: { segment: 'git', title: 'Git changes', description: 'Branch, commit and the IaC files you changed, with redacted diffs. Read-only: Hclaro never runs checkout, commit or push.', badge: 'Read-only', scope: 'project' },
  Tools: { segment: 'tools', title: 'Project tools', description: 'Validate, check formatting, and list provider requirements and output names. Read-only, and only when you ask.', badge: 'Read-only', scope: 'project' },
  Assistant: { segment: 'assistant', title: 'AI Assistant', description: 'Ask a detected AI assistant about Terraform or OpenTofu, with no project data or with project context you preview first.', badge: 'You choose what is shared', scope: 'project' },
  Settings: { title: 'Settings', description: 'Make this workspace feel like yours.', badge: 'Read-only', scope: 'app' }
};
export function pageOf(view: View): Page {
  return pages[view];
}

// Pages outside a project workspace, by route kind. The project page's path holds its ID.
export const globalPages = {
  hub: { path: '/projects', eyebrow: 'WORKSPACE HUB', title: 'Projects', description: 'Every Terraform and OpenTofu root you work with, in one place.', badge: 'Read-only' },
  project: { eyebrow: 'WORKSPACE HUB', title: 'Project workspaces', description: 'Open a CLI workspace to read its recorded state in this tab.', badge: 'Changes ask first' },
  compare: { path: '/compare', eyebrow: 'WORKSPACE HUB', title: 'Compare workspaces', description: 'Line up the recorded state of two workspaces: dev and prod, or one stack in two roots. Only when you ask.', badge: 'Read-only' },
  history: { path: '/history', eyebrow: 'ON THIS MACHINE', title: 'Operation history', description: 'What ran, when, against which workspace, and what each reviewed plan proposed. Redacted metadata only.', badge: 'Never re-runs' }
} as const;

type NavTarget = {
  kind: 'view';
  view: View;
  // Child views that keep this item active (for example the Import & discovery tabs).
  children?: View[];
} | {
  kind: 'route';
  route: 'compare' | 'history';
};

type NavBadge = 'resources' | 'operations';

export type NavItem = {
  id: string;
  label: string;
  icon: LucideIcon;
  group: NavGroup;
  target: NavTarget;
  // Extra text for assistive technology; the visible label is always part of the name.
  description?: string;
  badge?: NavBadge;
};

export const navItems: readonly NavItem[] = [
  { id: 'overview', label: 'Overview', icon: LayoutDashboard, group: 'project', target: { kind: 'view', view: 'Overview' } },
  { id: 'resources', label: 'Resources', icon: Layers3, group: 'project', target: { kind: 'view', view: 'Resources' }, badge: 'resources' },
  { id: 'modules', label: 'Modules', icon: FolderTree, group: 'project', target: { kind: 'view', view: 'Modules' } },
  { id: 'providers', label: 'Providers', icon: Network, group: 'project', target: { kind: 'view', view: 'Providers' } },
  { id: 'plans', label: 'Plans', icon: Workflow, group: 'analyze', target: { kind: 'view', view: 'Plans' } },
  { id: 'drift', label: 'Drift detection', icon: ScanSearch, group: 'analyze', target: { kind: 'view', view: 'Drift' } },
  { id: 'graph', label: 'Dependency graph', icon: Waypoints, group: 'analyze', target: { kind: 'view', view: 'Graph' } },
  { id: 'compare', label: 'Compare', icon: GitCompareArrows, group: 'analyze', target: { kind: 'route', route: 'compare' }, description: 'Compare workspaces' },
  { id: 'import-discovery', label: 'Import & discovery', icon: Import, group: 'manage', target: { kind: 'view', view: 'Import', children: ['Discovery'] }, description: 'Import existing objects or discover cloud resources' },
  { id: 'operations', label: 'Operations', icon: Terminal, group: 'manage', target: { kind: 'view', view: 'Operations' }, badge: 'operations' },
  { id: 'history', label: 'History', icon: History, group: 'manage', target: { kind: 'route', route: 'history' }, description: 'Operation history on this machine' },
  { id: 'git', label: 'Git', icon: GitBranch, group: 'utility', target: { kind: 'view', view: 'Git' }, description: 'Branch, commit and changed IaC files' },
  { id: 'tools', label: 'Tools', icon: Wrench, group: 'utility', target: { kind: 'view', view: 'Tools' }, description: 'Validate, format check, providers and outputs' },
  { id: 'settings', label: 'Settings', icon: Settings2, group: 'utility', target: { kind: 'view', view: 'Settings' } }
];

// Items shown only while AI assistance is on for this session (AI-01.09): hidden, not disabled.
const aiNavItems: readonly NavItem[] = [
  { id: 'assistant', label: 'AI Assistant', icon: Sparkles, group: 'analyze', target: { kind: 'view', view: 'Assistant' }, description: 'Ask an AI assistant, with or without previewed project context' }
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
  // AI assistance is on for this session: aiNavItems are shown.
  ai?: boolean;
};

type NavState = {
  enabled: boolean;
  active: boolean;
  // Why an item is unavailable, or what it does when that is not obvious.
  reason?: string;
};

const hubPlansOnly = 'Available only for the project opened from the terminal.';
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

// A view item has its page's scope; route items open global pages.
function scopeOf(item: NavItem): NavScope {
  return item.target.kind === 'view' ? pageOf(item.target.view).scope : 'global';
}

export function navState(item: NavItem, context: NavContext): NavState {
  const active = isActive(item, context.route, context.view);
  const scope = scopeOf(item);
  if (scope === 'global' || scope === 'app') return { enabled: true, active };
  if (context.mode === 'none') return { enabled: false, active, reason: scope === 'launch' ? 'Available for a project opened from the terminal.' : 'Open a project from Projects first.' };
  if (scope === 'launch' && context.mode !== 'launch') return { enabled: false, active, reason: context.scopedExecution ? hubPlansOnly : hubReadOnly };
  if (scope === 'execution' && context.mode !== 'launch' && !context.scopedExecution) return { enabled: false, active, reason: hubReadOnly };
  if (context.mode === 'hub-project' && !context.projectWorkspace) return { enabled: false, active, reason: 'Choose a workspace of this project first.' };
  return { enabled: true, active };
}

export function itemsIn(group: NavGroup, ai = false) {
  return (ai ? [...navItems, ...aiNavItems] : navItems).filter(item => item.group === group);
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
  const item = [...navItems, ...aiNavItems].find(i => i.target.kind === 'view' && i.target.view === view);
  return [item?.label || view];
}
