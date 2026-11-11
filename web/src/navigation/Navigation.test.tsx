import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import golden from '../../../fixtures/discovery/scan.golden.json';
import App from '../App';
import { parseRoute, workbenchKey } from '../router';
import type { DiscoveryOptions, Project, ProjectList, Resource, Workspace } from '../types';
import { importDiscoveryTabs, isActive, navItem, navItems, navState, viewTrail, type NavContext } from './config';

const hubId = 'a'.repeat(32);
const otherId = 'b'.repeat(32);
const project = (id: string, alias: string, extra: Partial<Project> = {}): Project => ({
  id, alias, rootPath: `/infra/${alias}`, enginePreference: '', pinned: false, addedAt: '2026-11-05T10:00:00Z', status: 'ready', initialized: true, backend: 'local', executionMode: 'local-cli', providerLock: false, launch: false, workspaceActions: { change: true, delete: true }, ...extra
});
const network = project(hubId, 'network', { lastOpenedAt: '2026-11-05T11:00:00Z', lastWorkspace: 'dev', inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev', 'prod'], cliDefault: 'dev', hiddenWorkspaces: 0, inspectedAt: '2026-11-05T11:00:00Z' } });
const platform = project(otherId, 'platform', { lastOpenedAt: '2026-11-05T10:30:00Z' });
const listing: ProjectList = {
  projects: [network, platform],
  registry: { status: 'ready', notice: '', path: '/config/hclaro/projects.json' },
  environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: false, cliArgsInherited: false },
  launchRoot: '/infra/launch-stack'
};
const resource: Resource = { id: 'r1', address: 'aws_s3_bucket.logs', mode: 'managed', type: 'aws_s3_bucket', name: 'logs', provider: 'registry.terraform.io/hashicorp/aws', module: '', attributes: {} };
// Context IDs have the server's 32-hex form; this one encodes the workspace name.
const ctxId = (ws: string) => Array.from(ws, c => c.charCodeAt(0).toString(16).padStart(2, '0')).join('').padEnd(32, '0');
const wsOf = (cid: string) => cid.replace(/(00)+$/, '').match(/../g)!.map(h => String.fromCharCode(parseInt(h, 16))).join('');
function snapshot(ws: string, hub: boolean): Workspace {
  return {
    status: 'ready',
    capturedAt: '2026-11-05T12:00:00Z',
    ...(hub ? { context: { id: ctxId(ws), projectId: hubId, alias: 'network', rootPath: '/infra/network', engine: 'terraform', workspace: ws, dataDir: '.terraform', createdAt: '2026-11-05T12:00:00Z' } } : {}),
    info: { project: hub ? 'network' : 'launch-stack', engine: 'terraform', version: '1.9.8', workspace: ws, backend: 'local', executionMode: 'local-cli', initialized: true, providerLock: true, warnings: [], capabilities: { canReadState: true, canPlan: !hub, canApplySavedPlan: !hub, supportsImportBlocks: true, supportsGeneratedConfig: true, canImport: !hub } },
    managed: 1, data: 0, modules: [{ address: '', parent: '', count: 1 }], providers: { [resource.provider]: 1 }, types: { aws_s3_bucket: 1 }
  };
}
const discovery = { ...(golden.options as unknown as DiscoveryOptions), identity: undefined };

type Call = { url: string; method: string };
let calls: Call[];
function stub() {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    calls.push({ url, method });
    const path = url.replace('/api/v1/', '').split('?')[0];
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'projects') return Response.json(listing);
    if (path === 'contexts') return Response.json(snapshot(JSON.parse(String(init.body)).workspace, true).context);
    const hub = path.match(/^contexts\/([0-9a-f]{32})(\/resources)?/);
    if (hub) return Response.json(hub[2] ? { resources: [resource], total: 1 } : snapshot(wsOf(hub[1]), true));
    if (path === 'workspace') return Response.json(snapshot('demo', false));
    if (path === 'resources') return Response.json({ resources: [resource], total: 1 });
    if (path === 'operations') return Response.json({ operations: [] });
    if (path === 'imports/options') return Response.json({ canImport: true, canApply: true, supportsImportBlocks: true, supportsGeneratedConfig: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] });
    if (path === 'plans/options') return Response.json({ canPlan: true, canApply: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] });
    if (path === 'plans') return Response.json({ plans: [] });
    if (path === 'discovery/options') return Response.json(discovery);
    if (path.startsWith('history')) return Response.json({ entries: [], total: 0 });
    return Response.json({}, { status: 404 });
  }));
}
// test-setup.ts makes every media query match, which puts the sidebar in its narrow drawer
// mode. Desktop tests opt out of the narrow query.
function desktop() {
  vi.stubGlobal('matchMedia', vi.fn().mockImplementation((query: string) => ({ matches: !query.includes('width'), media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
}
function app(path = '/') {
  window.history.pushState(null, '', path);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
const nav = () => screen.getByRole('navigation', { name: 'Main' });
const navButton = (name: string) => within(nav()).getByRole('button', { name });
const mutations = () => calls.filter(c => c.method !== 'GET' && !c.url.endsWith('/session'));

beforeEach(() => {
  localStorage.clear();
  stub();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.pushState(null, '', '/');
});

describe('Navigation model', () => {
  const route = parseRoute('/');
  const context = (extra: Partial<NavContext>): NavContext => ({ route, mode: 'launch', view: 'Resources', ...extra });
  it('groups every implemented destination exactly once', () => {
    expect(navItems.map(i => i.label)).toEqual(['Overview', 'Resources', 'Modules', 'Providers', 'Plans', 'Drift detection', 'Dependency graph', 'Compare', 'Import & discovery', 'Operations', 'History', 'Git', 'Tools', 'Settings']);
    expect(new Set(navItems.map(i => i.id)).size).toBe(navItems.length);
    const views = navItems.flatMap(i => i.target.kind === 'view' ? [i.target.view, ...(i.target.children || [])] : []);
    for (const view of ['Overview', 'Resources', 'Modules', 'Providers', 'Tools', 'Git', 'Settings', 'Plans', 'Drift', 'Graph', 'Discovery', 'Import', 'Operations']) expect(views).toContain(view);
  });
  it('keeps Hub workspaces read-only and explains disabled items', () => {
    const hub = context({ route: parseRoute(`/projects/${hubId}/workspaces/dev/resources`), mode: 'hub-workspace' });
    expect(navState(navItem('plans'), hub)).toMatchObject({ enabled: false, reason: expect.stringContaining('read-only') });
    expect(navState(navItem('import-discovery'), hub).enabled).toBe(false);
    expect(navState(navItem('resources'), hub)).toEqual({ enabled: true, active: true });
    expect(navState(navItem('compare'), hub).enabled).toBe(true);
    const none = context({ mode: 'none' });
    expect(navState(navItem('overview'), none)).toMatchObject({ enabled: false, reason: 'Open a project from Projects first.' });
    expect(navState(navItem('settings'), none).enabled).toBe(true);
    expect(navState(navItem('history'), none).enabled).toBe(true);
    const page = context({ route: parseRoute(`/projects/${hubId}`), mode: 'hub-project' });
    expect(navState(navItem('overview'), page)).toMatchObject({ enabled: false, reason: 'Choose a workspace of this project first.' });
    expect(navState(navItem('overview'), { ...page, projectWorkspace: 'dev' }).enabled).toBe(true);
  });
  it('enables Hub plans and operations only when the server enables scoped execution', () => {
    const hub = context({ route: parseRoute(`/projects/${hubId}/workspaces/dev/plans`), mode: 'hub-workspace', view: 'Plans', scopedExecution: true });
    expect(navState(navItem('plans'), hub)).toEqual({ enabled: true, active: true });
    expect(navState(navItem('operations'), hub).enabled).toBe(true);
    // M24.08: Import runs in the Hub workspace too; M24.09: drift scans and graphs as well.
    for (const id of ['import-discovery', 'drift', 'graph']) {
      expect(navState(navItem(id), hub).enabled).toBe(true);
      expect(navState(navItem(id), { ...hub, scopedExecution: false }).enabled).toBe(false);
    }
    expect(navState(navItem('plans'), { ...hub, scopedExecution: false }).enabled).toBe(false);
    const page = context({ route: parseRoute(`/projects/${hubId}`), mode: 'hub-project', scopedExecution: true });
    expect(navState(navItem('plans'), page).enabled).toBe(false);
    expect(navState(navItem('plans'), { ...page, projectWorkspace: 'dev' }).enabled).toBe(true);
    expect(navState(navItem('plans'), context({ mode: 'none', scopedExecution: true })).enabled).toBe(false);
  });
  it('derives active state from the route and view, including nested workflows', () => {
    const item = navItem('import-discovery');
    expect(isActive(item, route, 'Import')).toBe(true);
    expect(isActive(item, route, 'Discovery')).toBe(true);
    expect(isActive(item, route, 'Plans')).toBe(false);
    // Global pages are never shown as a project view.
    expect(isActive(navItem('resources'), parseRoute('/compare'), 'Resources')).toBe(false);
    expect(isActive(navItem('compare'), parseRoute('/compare'), 'Resources')).toBe(true);
    expect(isActive(navItem('history'), parseRoute('/history'), 'Resources')).toBe(true);
    expect(isActive(navItem('resources'), parseRoute(`/projects/${hubId}/workspaces/dev/resources`), 'Resources')).toBe(true);
  });
  it('names pages for breadcrumbs and keys workbenches per context', () => {
    expect(viewTrail('Discovery')).toEqual(['Import & discovery', 'Discover cloud resources']);
    expect(viewTrail('Drift')).toEqual(['Drift detection']);
    expect(importDiscoveryTabs.map(t => t.view)).toEqual(['Import', 'Discovery']);
    expect(workbenchKey(parseRoute('/compare'))).toBe(workbenchKey(parseRoute('/')));
    expect(workbenchKey(parseRoute(`/projects/${hubId}/workspaces/dev/git`))).toBe(`${hubId}/dev`);
  });
});

describe('Sidebar', () => {
  it('renders grouped navigation with one current page and the project context on top', async () => {
    app();
    expect(await within(nav()).findByRole('group', { name: 'Plan & analyze' })).toBeInTheDocument();
    expect(within(nav()).getByRole('group', { name: 'Manage' })).toBeInTheDocument();
    expect(within(within(nav()).getByRole('group', { name: 'Project' })).getAllByRole('button').map(b => b.getAttribute('aria-label'))).toEqual(['Overview', 'Resources', 'Modules', 'Providers']);
    await waitFor(() => expect(navButton('Resources')).toHaveTextContent('1'));
    expect(navButton('Resources')).toHaveAttribute('aria-current', 'page');
    expect(within(nav()).getAllByRole('button').filter(b => b.getAttribute('aria-current') === 'page')).toHaveLength(1);
    const sidebar = screen.getByRole('complementary', { name: 'Application navigation' });
    expect(await within(sidebar).findByText('launch-stack')).toBeInTheDocument();
    expect(within(sidebar).getByText('Opened from terminal')).toBeInTheDocument();
    // The terminal project's workspace is shown, not switchable: Hclaro never selects one.
    expect(within(sidebar).getByText('demo')).toBeInTheDocument();
    expect(within(sidebar).queryByRole('combobox')).not.toBeInTheDocument();
    expect(mutations()).toEqual([]);
  });
  it('switches projects from the switcher without running anything', async () => {
    app();
    const user = userEvent.setup();
    const trigger = await screen.findByRole('button', { name: /Switch project/ });
    await user.click(trigger);
    const panel = screen.getByRole('group', { name: 'Switch project' });
    expect(within(panel).getByRole('button', { name: /All projects/ })).toBeInTheDocument();
    const recent = await within(panel).findAllByRole('button', { name: /network|platform/ });
    expect(recent.map(b => b.textContent)).toEqual(['networkWorkspace dev', 'platformChoose a workspace']);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('group', { name: 'Switch project' })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    await user.click(trigger);
    await user.click(within(screen.getByRole('group', { name: 'Switch project' })).getByRole('button', { name: /network/ }));
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/dev/resources`);
    expect(await screen.findByRole('button', { name: /logs.*aws_s3_bucket/ })).toBeInTheDocument();
    // Only the workspace read is opened; no plan, init, select or apply is requested.
    expect(mutations().map(c => c.url)).toEqual(['/api/v1/contexts']);
  });
  it('switches the Terraform workspace of a Hub project from the sidebar, separate from the project', async () => {
    app(`/projects/${hubId}/workspaces/dev/overview`);
    const user = userEvent.setup();
    const select = await screen.findByLabelText('Workspace');
    expect(screen.getByRole('complementary', { name: 'Application navigation' })).toContainElement(select);
    expect(await screen.findByText('Hub project')).toBeInTheDocument();
    await user.selectOptions(select, 'prod');
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/prod/overview`);
    expect(await screen.findByText('Production-like name')).toBeInTheDocument();
    // The switcher offers this project's workspace page and the terminal project.
    await user.click(screen.getByRole('button', { name: /Switch project/ }));
    const panel = screen.getByRole('group', { name: 'Switch project' });
    expect(within(panel).getByRole('button', { name: /Workspaces of this project/ })).toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: /launch-stack/ }));
    expect(window.location.pathname).toBe('/');
  });
  it('asks before a switch would discard a replacement selection', async () => {
    app();
    const user = userEvent.setup();
    await user.click(await screen.findByLabelText('Select aws_s3_bucket.logs for replacement'));
    await user.click(screen.getByRole('button', { name: /Switch project/ }));
    await user.click(within(screen.getByRole('group', { name: 'Switch project' })).getByRole('button', { name: /network/ }));
    const dialog = screen.getByRole('dialog', { name: 'Leave this project?' });
    expect(dialog).toHaveTextContent('1 instance selected for replacement');
    expect(window.location.pathname).toBe('/');
    await user.click(within(dialog).getByRole('button', { name: 'Stay here' }));
    expect(screen.getByRole('region', { name: 'Replacement selection' })).toBeInTheDocument();
    // Global pages share this workbench, so going there keeps the selection without asking.
    await user.click(navButton('Compare'));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(window.location.pathname).toBe('/compare');
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe('/'));
    await user.click(screen.getByRole('button', { name: /Switch project/ }));
    await user.click(within(screen.getByRole('group', { name: 'Switch project' })).getByRole('button', { name: /network/ }));
    await user.click(screen.getByRole('button', { name: 'Switch anyway' }));
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/dev/resources`);
    expect(mutations().map(c => c.url)).toEqual(['/api/v1/contexts']);
  });
  it('opens Import & discovery as two separate workflows, one click apart, without running anything', async () => {
    app();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Import & discovery' }));
    expect(await screen.findByRole('heading', { name: 'Import an existing object' })).toBeInTheDocument();
    const tabs = screen.getByRole('navigation', { name: 'Import and discovery' });
    expect(within(tabs).getByRole('button', { name: 'Import existing' })).toHaveAttribute('aria-current', 'page');
    expect(navButton('Import & discovery')).toHaveAttribute('aria-current', 'page');
    await user.click(within(tabs).getByRole('button', { name: 'Discover cloud resources' }));
    expect(await screen.findByRole('heading', { name: 'Cloud discovery' })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: /AWS context not verified/ })).toBeInTheDocument();
    expect(navButton('Import & discovery')).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('navigation', { name: 'Breadcrumb' })).toHaveTextContent('Import & discoveryDiscover cloud resources');
    // The entry reopens the workflow last used in this tab.
    await user.click(navButton('Plans'));
    await user.click(navButton('Import & discovery'));
    expect(await screen.findByRole('heading', { name: 'Cloud discovery' })).toBeInTheDocument();
    expect(mutations()).toEqual([]);
  });
  it('keeps Git, Tools and Settings reachable from the sidebar footer', async () => {
    app(`/projects/${hubId}/workspaces/dev/resources`);
    const user = userEvent.setup();
    const utilities = within(nav()).getByRole('group', { name: 'Project utilities' });
    expect(within(utilities).getAllByRole('button').map(b => b.getAttribute('aria-label'))).toEqual(['Git', 'Tools', 'Settings']);
    await user.click(within(utilities).getByRole('button', { name: 'Tools' }));
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/dev/tools`);
    expect(await screen.findByRole('heading', { name: 'Project tools' })).toBeInTheDocument();
    await user.click(within(utilities).getByRole('button', { name: 'Git' }));
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/dev/git`);
    expect(await screen.findByRole('heading', { name: 'Git changes' })).toBeInTheDocument();
    await user.click(within(utilities).getByRole('button', { name: 'Settings' }));
    expect(await screen.findByRole('heading', { name: 'Appearance' })).toBeInTheDocument();
    expect(within(utilities).getByRole('button', { name: 'Settings' })).toHaveAttribute('aria-current', 'page');
    // Back leaves Settings for the previous explorer view in the URL.
    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/dev/tools`));
    expect(await screen.findByRole('heading', { name: 'Project tools' })).toBeInTheDocument();
    expect(within(utilities).getByRole('button', { name: 'Tools' })).toHaveAttribute('aria-current', 'page');
  });
  it('opens a Hub project page item in the workspace last used there', async () => {
    app(`/projects/${hubId}`);
    const user = userEvent.setup();
    await waitFor(() => expect(navButton('Overview')).toBeEnabled());
    expect(navButton('Plans')).toBeDisabled();
    await user.click(navButton('Overview'));
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/dev/overview`);
  });
  it('collapses to an icon-only sidebar with keyboard tooltips and remembers the choice', async () => {
    desktop();
    const view = app();
    const user = userEvent.setup();
    const collapse = await screen.findByRole('button', { name: 'Collapse sidebar' });
    expect(collapse).toHaveAttribute('aria-expanded', 'true');
    await user.click(collapse);
    expect(document.querySelector('.app')).toHaveClass('sidebar-compact');
    expect(JSON.parse(localStorage.getItem('hclaro.preferences')!).sidebar).toBe('compact');
    // Icon-only items keep their names and current state.
    expect(navButton('Resources')).toHaveAttribute('aria-current', 'page');
    act(() => navButton('Drift detection').focus());
    expect(screen.getByRole('tooltip')).toHaveTextContent('Drift detection');
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
    await user.keyboard('{Enter}');
    expect(await screen.findByRole('heading', { name: 'Drift detection' })).toBeInTheDocument();
    // The project context stays reachable from the compact rail.
    expect(screen.getByRole('button', { name: /^Project launch-stack\. Switch project/ })).toBeInTheDocument();
    view.unmount();
    app();
    const expand = await screen.findByRole('button', { name: 'Expand sidebar' });
    await user.click(expand);
    expect(JSON.parse(localStorage.getItem('hclaro.preferences')!).sidebar).toBe('expanded');
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
  });
  it('shows the workspace selector inside the compact switcher of a Hub workspace', async () => {
    desktop();
    localStorage.setItem('hclaro.preferences', JSON.stringify({ sidebar: 'compact' }));
    app(`/projects/${hubId}/workspaces/dev/resources`);
    const user = userEvent.setup();
    expect(screen.queryByLabelText('Workspace')).not.toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: /Switch project/ }));
    await user.selectOptions(within(screen.getByRole('group', { name: 'Switch project' })).getByLabelText('Workspace'), 'prod');
    expect(window.location.pathname).toBe(`/projects/${hubId}/workspaces/prod/resources`);
  });
  it('uses a dismissible drawer on narrow screens', async () => {
    app();
    const user = userEvent.setup();
    const menu = await screen.findByRole('button', { name: 'Open navigation' });
    expect(menu).toHaveAttribute('aria-expanded', 'false');
    // The compact preference does not apply to the drawer.
    expect(document.querySelector('.app')).not.toHaveClass('sidebar-compact');
    await user.click(menu);
    expect(menu).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('button', { name: 'Close navigation' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(menu).toHaveAttribute('aria-expanded', 'false');
    expect(menu).toHaveFocus();
    await user.click(menu);
    await user.click(navButton('Modules'));
    expect(menu).toHaveAttribute('aria-expanded', 'false');
    expect(await screen.findByRole('heading', { name: 'Modules' })).toBeInTheDocument();
  });
});
