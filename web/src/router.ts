import { useEffect, useState } from 'react';

// Client routes. Segments are routing hints only: the server resolves project IDs and
// workspace names itself and never reads files from them.
// Views a Hub workspace URL can hold. Plans (with an optional plan ID, the review) and
// Operations are deep-linkable since M24.07, Import since M24.08, Drift, Graph and Discovery
// since M24.09;
// they act only on that
// workspace's own context.
export type ExplorerView = 'Resources' | 'Overview' | 'Modules' | 'Providers' | 'Tools' | 'Git' | 'Plans' | 'Operations' | 'Import' | 'Discovery' | 'Drift' | 'Graph';
export type Route = {
  kind: 'launch';
} | {
  kind: 'hub';
} | {
  kind: 'compare';
} | {
  kind: 'history';
} | {
  kind: 'project';
  projectId: string;
} | {
  kind: 'context';
  projectId: string;
  workspace: string;
  view: ExplorerView;
  planId?: string;
};
const views: Record<string, ExplorerView> = {
  resources: 'Resources',
  overview: 'Overview',
  modules: 'Modules',
  providers: 'Providers',
  tools: 'Tools',
  git: 'Git',
  plans: 'Plans',
  operations: 'Operations',
  import: 'Import',
  discovery: 'Discovery',
  drift: 'Drift',
  graph: 'Graph'
};
const projectId = /^[0-9a-f]{32}$/;
const workspaceName = /^[A-Za-z0-9_-]{1,90}$/;
export function parseRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean);
  if (parts[0] === 'compare' && parts.length === 1) return { kind: 'compare' };
  if (parts[0] === 'history' && parts.length === 1) return { kind: 'history' };
  if (parts[0] !== 'projects') return { kind: 'launch' };
  if (parts.length === 1) return { kind: 'hub' };
  if (!projectId.test(parts[1])) return { kind: 'hub' };
  if (parts.length === 2) return { kind: 'project', projectId: parts[1] };
  let workspace = '';
  try {
    workspace = decodeURIComponent(parts[3] || '');
  } catch {/* Malformed escapes fall back to the project page. */}
  const view = views[parts[4] || 'resources'];
  const planId = view === 'Plans' && parts.length === 6 && projectId.test(parts[5]) ? parts[5] : undefined;
  if (parts[2] !== 'workspaces' || !workspaceName.test(workspace) || !view || parts.length > (planId ? 6 : 5)) return { kind: 'project', projectId: parts[1] };
  return { kind: 'context', projectId: parts[1], workspace, view, ...(planId ? { planId } : {}) };
}
// Each project workspace gets its own workbench; every other route shares the launch one.
// Moving between two different keys discards the in-memory state of the tab's workbench.
export function workbenchKey(route: Route) {
  return route.kind === 'context' ? `${route.projectId}/${route.workspace}` : 'launch';
}
export function comparePath() {
  return '/compare';
}
export function historyPath() {
  return '/history';
}
// Deep links into a Hub workspace may name a resource to inspect and the execution profile
// the link was made with (a new tab has no profile choice of its own). The parameters are
// read once at startup and removed from the address bar.
let linkedResource: { path: string; id: string } | undefined;
export function consumeDeepLink() {
  const route = parseRoute(window.location.pathname);
  const params = new URLSearchParams(window.location.search);
  if (route.kind === 'context') {
    const resource = params.get('resource') || '';
    if (/^[0-9a-f]{32}$/.test(resource)) linkedResource = { path: window.location.pathname, id: resource };
    const profile = params.get('profile') || '';
    if (profile === 'none' || projectId.test(profile)) {
      try {
        sessionStorage.setItem(`hclaro.profile.${route.projectId}`, profile);
      } catch {/* Without storage the project's default profile applies. */}
    }
  }
  if (window.location.search) window.history.replaceState(null, '', window.location.pathname);
}
// takeLinkedResource returns the deep-linked resource while the linked page is shown. It is
// idempotent (StrictMode may run state initializers twice); navigating away forgets it.
export function takeLinkedResource() {
  return linkedResource && linkedResource.path === window.location.pathname ? linkedResource.id : undefined;
}
export function hubPath() {
  return '/projects';
}
export function projectPath(id: string) {
  return `/projects/${encodeURIComponent(id)}`;
}
export function contextPath(id: string, workspace: string, view: ExplorerView = 'Resources', planId?: string) {
  const plan = view === 'Plans' && planId && projectId.test(planId) ? `/${planId}` : '';
  return `/projects/${encodeURIComponent(id)}/workspaces/${encodeURIComponent(workspace)}/${view.toLowerCase()}${plan}`;
}
export function isExplorerView(view: string): view is ExplorerView {
  return Object.values(views).includes(view as ExplorerView);
}
const listeners = new Set<() => void>();
export function navigate(path: string, replace = false) {
  if (path === window.location.pathname) return;
  linkedResource = undefined;
  if (replace) window.history.replaceState(null, '', path);
  else window.history.pushState(null, '', path);
  listeners.forEach(l => l());
}
export function useRoute(): Route {
  const [pathname, setPathname] = useState(() => window.location.pathname);
  useEffect(() => {
    const update = () => setPathname(window.location.pathname);
    listeners.add(update);
    window.addEventListener('popstate', update);
    return () => {
      listeners.delete(update);
      window.removeEventListener('popstate', update);
    };
  }, []);
  return parseRoute(pathname);
}
// Names are labels only. A production-like word in a name is a hint, never a guarantee.
export function productionLike(...labels: (string | undefined)[]) {
  return labels.some(l => !!l && /(^|[^a-z])(prod|production|prd|live)([^a-z]|$)/i.test(l));
}
