import { useEffect, useState } from 'react';

// Client routes. Segments are routing hints only: the server resolves project IDs and
// workspace names itself and never reads files from them.
export type ExplorerView = 'Resources' | 'Overview' | 'Modules' | 'Providers' | 'Tools';
export type Route = {
  kind: 'launch';
} | {
  kind: 'hub';
} | {
  kind: 'project';
  projectId: string;
} | {
  kind: 'context';
  projectId: string;
  workspace: string;
  view: ExplorerView;
};
const views: Record<string, ExplorerView> = {
  resources: 'Resources',
  overview: 'Overview',
  modules: 'Modules',
  providers: 'Providers',
  tools: 'Tools'
};
const projectId = /^[0-9a-f]{32}$/;
const workspaceName = /^[A-Za-z0-9_-]{1,90}$/;
export function parseRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean);
  if (parts[0] !== 'projects') return { kind: 'launch' };
  if (parts.length === 1) return { kind: 'hub' };
  if (!projectId.test(parts[1])) return { kind: 'hub' };
  if (parts.length === 2) return { kind: 'project', projectId: parts[1] };
  let workspace = '';
  try {
    workspace = decodeURIComponent(parts[3] || '');
  } catch {/* Malformed escapes fall back to the project page. */}
  const view = views[parts[4] || 'resources'];
  if (parts[2] !== 'workspaces' || !workspaceName.test(workspace) || !view || parts.length > 5) return { kind: 'project', projectId: parts[1] };
  return { kind: 'context', projectId: parts[1], workspace, view };
}
export function hubPath() {
  return '/projects';
}
export function projectPath(id: string) {
  return `/projects/${encodeURIComponent(id)}`;
}
export function contextPath(id: string, workspace: string, view: ExplorerView = 'Resources') {
  return `/projects/${encodeURIComponent(id)}/workspaces/${encodeURIComponent(workspace)}/${view.toLowerCase()}`;
}
export function isExplorerView(view: string): view is ExplorerView {
  return Object.values(views).includes(view as ExplorerView);
}
const listeners = new Set<() => void>();
export function navigate(path: string, replace = false) {
  if (path === window.location.pathname) return;
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
