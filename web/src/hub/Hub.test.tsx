import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import { consumeDeepLink, contextPath, parseRoute, productionLike } from '../router';
import type { Project, ProjectList, Resource, Workspace } from '../types';

// Opens the top bar workspace switcher and chooses a workspace (this tab only).
async function pickWorkspace(user: ReturnType<typeof userEvent.setup>, ws: string) {
  await user.click(screen.getByRole('button', { name: /Switch workspace/ }));
  await user.click(within(screen.getByRole('list', { name: 'Workspaces' })).getByRole('button', { name: new RegExp(`^${ws}`) }));
}

const network = 'a'.repeat(32);
const platform = 'b'.repeat(32);
const project = (id: string, alias: string, extra: Partial<Project> = {}): Project => ({
  id,
  alias,
  rootPath: `/infra/${alias}`,
  enginePreference: '',
  pinned: false,
  addedAt: '2026-11-05T10:00:00Z',
  status: 'ready',
  initialized: true,
  backend: 'local',
  executionMode: 'local-cli',
  providerLock: false,
  launch: false,
  workspaceActions: { change: true, delete: true },
  ...extra
});
function listing(projects: Project[]): ProjectList {
  return {
    projects,
    registry: { status: 'ready', notice: '', path: '/config/hclaro/projects.json' },
    environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: false, cliArgsInherited: false }
  };
}
const inspected = project(network, 'network', {
  lastOpenedAt: '2026-11-05T11:00:00Z',
  lastWorkspace: 'dev',
  inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev', 'prod'], cliDefault: 'dev', hiddenWorkspaces: 0, inspectedAt: '2026-11-05T11:00:00Z' }
});
// Context IDs have the server's 32-hex form.
const ctx: Record<string, string> = { default: '1'.repeat(32), dev: '2'.repeat(32), prod: '3'.repeat(32), demo: '4'.repeat(32) };
const projects = [inspected, project(platform, 'prod-platform', { status: 'missing', problem: 'The folder no longer exists at this path.' })];
function snapshot(ws: string): Workspace {
  return {
    status: 'ready',
    capturedAt: '2026-11-05T12:00:00Z',
    context: { id: ctx[ws], projectId: network, alias: 'network', rootPath: '/infra/network', engine: 'terraform', workspace: ws, dataDir: '.terraform', createdAt: '2026-11-05T12:00:00Z' },
    info: {
      project: 'network', engine: 'terraform', version: '1.9.8', workspace: ws, backend: 'local', executionMode: 'local-cli', initialized: true, providerLock: false, warnings: [],
      capabilities: { canReadState: true, canPlan: false, planReason: 'Hub workspaces are read-only.', canApplySavedPlan: false, supportsImportBlocks: true, supportsGeneratedConfig: true, canImport: false }
    },
    managed: 1, data: 0, modules: [{ address: '', parent: '', count: 1 }], providers: { 'registry.terraform.io/hashicorp/aws': 1 }, types: { aws_s3_bucket: 1 }
  };
}
const bucket = (ws: string): Resource => ({ id: `id-${ws}`, address: `aws_s3_bucket.${ws}_logs`, mode: 'managed', type: 'aws_s3_bucket', name: `${ws}_logs`, provider: 'registry.terraform.io/hashicorp/aws', module: '', attributes: { password: '[redacted]' } });

type Call = { url: string; method: string; body?: unknown; headers: Record<string, string> };
let calls: Call[];
let hold: Promise<void> | undefined;
let inspectReply: Project | undefined;
function stub(list: ProjectList) {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ url, method, body, headers: init.headers as Record<string, string> });
    const path = url.replace('/api/v1/', '').split('?')[0];
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'projects' && method === 'GET') return Response.json(list);
    if (path === 'projects' && method === 'POST') return Response.json(project('c'.repeat(32), 'added'), { status: 201 });
    if (path === 'projects/browse') return Response.json({ path: '/infra', parent: '/', project: false, entries: ['network'], truncated: false });
    if (path.endsWith('/inspect')) return Response.json(inspectReply ?? inspected);
    if (path.endsWith('/workspaces/review')) return Response.json({ error: { code: 'busy', message: 'Review refused in this test.' } }, { status: 409 });
    if (path === 'contexts') return Response.json({ ...snapshot(body.workspace).context });
    const match = path.match(/^contexts\/([0-9a-f]{32})(\/resources)?/);
    const ws = match && Object.keys(ctx).find(k => ctx[k] === match[1]);
    if (match && ws) {
      if (hold && ws === 'prod') await hold;
      return Response.json(match[2] ? { resources: [bucket(ws)], total: 1 } : snapshot(ws));
    }
    if (path === 'workspace') return Response.json({ ...snapshot('demo'), context: undefined, status: 'error', diagnostic: { code: 'no_launch_project', message: 'No project.' } });
    if (path === 'operations') return Response.json({ operations: [] });
    if (method !== 'GET') return Response.json({});
    return Response.json({}, { status: 404 });
  }));
}
function app(path: string) {
  window.history.pushState(null, '', path);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
beforeEach(() => {
  localStorage.clear();
  hold = undefined;
  inspectReply = undefined;
  vi.spyOn(window, 'open').mockImplementation(() => null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.history.pushState(null, '', '/');
});

describe('Workspace Hub', () => {
  it('lists projects in a table with honest status and production hints', async () => {
    stub(listing(projects));
    app('/projects');
    expect(await screen.findByRole('heading', { level: 1, name: 'Projects' })).toBeInTheDocument();
    const table = await screen.findByRole('table', { name: 'Projects' });
    const rows = () => within(table).getAllByRole('row').slice(1);
    expect(rows()).toHaveLength(2);
    expect(table).toHaveTextContent('Folder missing');
    expect(table).toHaveTextContent('Terraform 1.9.8');
    expect(within(table).getByRole('button', { name: 'Repair path' })).toBeEnabled();
    expect(within(table).getByRole('button', { name: 'Open network' })).toBeEnabled();
    expect(table).toHaveTextContent('Production-like name');
    // GET only: listing projects never runs engine commands.
    expect(calls.filter(c => c.method !== 'GET')).toHaveLength(0);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Search projects'), 'prod');
    // Workspace names are searchable once listed: "network" has a prod workspace.
    expect(rows()).toHaveLength(2);
    await user.clear(screen.getByLabelText('Search projects'));
    await user.type(screen.getByLabelText('Search projects'), 'platform');
    expect(rows()).toHaveLength(1);
  });
  it('pins and reorders projects through the registry endpoints', async () => {
    stub(listing(projects));
    app('/projects');
    const user = userEvent.setup();
    expect(await screen.findByRole('button', { name: 'Move network up' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Move network down' }));
    await waitFor(() => expect(calls.find(c => c.url.endsWith('projects/order'))?.body).toEqual({ ids: [platform, network] }));
    await user.click(screen.getByRole('button', { name: 'Pin network' }));
    await waitFor(() => expect(calls.find(c => c.method === 'PATCH')?.body).toEqual({ pinned: true }));
  });
  it('shows project details and renames, lists workspaces and opens only on request', async () => {
    stub(listing(projects));
    app('/projects');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Details for network' }));
    const drawer = screen.getByRole('complementary', { name: 'Project details' });
    expect(drawer).toHaveTextContent('/infra/network');
    expect(drawer).toHaveTextContent('Not read yet');
    // Workspaces listed earlier in this session show without running the CLI again.
    expect(within(drawer).getByText('CLI default')).toBeInTheDocument();
    expect(within(drawer).queryByRole('button', { name: 'Set dev as CLI default' })).not.toBeInTheDocument();
    expect(within(drawer).getByRole('button', { name: 'Delete workspace dev' })).toBeDisabled();
    expect(within(drawer).getByRole('button', { name: 'Delete workspace default' })).toBeDisabled();
    expect(calls.filter(c => c.method !== 'GET')).toHaveLength(0);
    await user.click(within(drawer).getByRole('button', { name: /List again/ }));
    await waitFor(() => expect(calls.some(c => c.url.endsWith(`projects/${network}/inspect`))).toBe(true));
    await user.clear(within(drawer).getByLabelText('Name'));
    await user.type(within(drawer).getByLabelText('Name'), 'core-network');
    await user.click(within(drawer).getByRole('button', { name: 'Rename' }));
    await waitFor(() => expect(calls.find(c => c.method === 'PATCH')?.body).toEqual({ alias: 'core-network' }));
    // Set as CLI default opens the reviewed, confirmed workspace dialog; nothing runs yet.
    await user.click(within(drawer).getByRole('button', { name: 'Set prod as CLI default' }));
    expect(await screen.findByRole('dialog', { name: 'Set prod as CLI default' })).toBeInTheDocument();
    await waitFor(() => expect(calls.find(c => c.url.endsWith('/workspaces/review'))?.body).toEqual({ action: 'select', name: 'prod' }));
    expect(calls.some(c => c.url.includes('operations/workspace'))).toBe(false);
    await user.keyboard('{Escape}');
    await user.click(within(drawer).getByRole('button', { name: /Open project/ }));
    expect(window.location.pathname).toBe(contextPath(network, 'dev'));
  });
  it('adds a project with CSRF protection and a folder chooser', async () => {
    stub(listing([]));
    app('/projects');
    const user = userEvent.setup();
    expect(await screen.findByText('No projects yet')).toBeInTheDocument();
    // With no projects only the empty state offers the action.
    expect(screen.getAllByRole('button', { name: 'Add project' })).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Add project' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add a project' });
    await user.click(within(dialog).getByRole('button', { name: /Browse/ }));
    await user.click(await within(dialog).findByRole('button', { name: 'network' }));
    await waitFor(() => expect(calls.filter(c => c.url.endsWith('projects/browse')).at(-1)?.body).toEqual({ path: '/infra/network' }));
    await user.clear(within(dialog).getByLabelText('Root folder'));
    await user.type(within(dialog).getByLabelText('Root folder'), '/infra/network');
    await user.click(within(dialog).getByRole('button', { name: 'Add project' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const add = calls.find(c => c.method === 'POST' && c.url.endsWith('/projects'))!;
    expect(add.body).toEqual({ path: '/infra/network', alias: '', engine: '' });
    expect(add.headers['X-Hclaro-CSRF']).toBe('c'.repeat(64));
    expect(await screen.findByRole('status')).toHaveTextContent('added');
  });
  it('removes only the registry entry after confirmation', async () => {
    stub(listing(projects));
    app('/projects');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Details for network' }));
    await user.click(screen.getByRole('button', { name: 'Remove from Hclaro' }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveTextContent('Nothing on disk or in your backend is deleted');
    await user.click(within(dialog).getByRole('button', { name: 'Remove from Hclaro' }));
    await waitFor(() => expect(calls.some(c => c.method === 'DELETE' && c.url.endsWith(`projects/${network}`))).toBe(true));
  });
  it('lists workspaces explicitly and opens one in this tab only', async () => {
    stub(listing(projects));
    app(`/projects/${network}`);
    expect(await screen.findByRole('button', { name: /prod.*Read state/ })).toBeInTheDocument();
    expect(calls.filter(c => c.method === 'POST').map(c => c.url)).toEqual([`/api/v1/projects/${network}/inspect`]);
    expect(screen.getByText('CLI default')).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Open prod in a new tab' }));
    expect(window.open).toHaveBeenCalledWith(expect.stringMatching(new RegExp(`^/projects/${network}/workspaces/prod/resources#session=`)), '_blank', 'noopener');
    await user.click(screen.getByRole('button', { name: /dev.*Read state/ }));
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/resources`);
    expect(await screen.findByRole('button', { name: /dev_logs/ })).toBeInTheDocument();
  });
  it('offers a separate session only as an optional, copyable command', async () => {
    stub(listing(projects));
    app(`/projects/${network}`);
    const summary = await screen.findByText('Open in a separate session (optional)');
    expect(summary.closest('details')).not.toHaveAttribute('open');
    expect(screen.getByText("hclaro --path '/infra/network'")).toBeInTheDocument();
    expect(screen.getByText(/do not coordinate/)).toBeInTheDocument();
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    await user.click(screen.getByRole('button', { name: 'Copy command' }));
    expect(writeText).toHaveBeenCalledWith("hclaro --path '/infra/network'");
    expect(await screen.findByText('Command copied')).toBeInTheDocument();
    expect(calls.filter(c => c.method === 'POST').map(c => c.url)).toEqual([`/api/v1/projects/${network}/inspect`]);
  });
  // A repository can name its folders; PowerShell must not expand $(...), $var, ` or % in one.
  it('quotes a Windows folder as an inert PowerShell literal', async () => {
    const rootPath = "C:\\src\\it's\u2019 stack$(Start-Process calc)`%USERNAME%";
    inspectReply = { ...inspected, rootPath };
    stub(listing([inspectReply]));
    app(`/projects/${network}`);
    await screen.findByText('Open in a separate session (optional)');
    const command = "hclaro --path 'C:\\src\\it''s\u2019\u2019 stack$(Start-Process calc)`%USERNAME%'";
    expect(screen.getByText(command)).toBeInTheDocument();
    expect(screen.getByText('Run in PowerShell.')).toBeInTheDocument();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Copy command' }));
    expect(writeText).toHaveBeenCalledWith(command);
  });
  it('scopes a workspace view to its own context and never shows the previous one', async () => {
    stub(listing(projects));
    app(`/projects/${network}/workspaces/dev/resources`);
    expect(await screen.findByRole('button', { name: /dev_logs/ })).toBeInTheDocument();
    const open = calls.find(c => c.url.endsWith('/contexts'))!;
    expect(open.body).toEqual({ projectId: network, workspace: 'dev', reload: false });
    expect(calls.some(c => c.url.includes(`contexts/${ctx.dev}/resources`))).toBe(true);
    expect(calls.some(c => c.url === '/api/v1/resources' || c.url.startsWith('/api/v1/resources?'))).toBe(false);
    // Hub contexts are read-only: no replacement selection and no workflows.
    expect(screen.queryByLabelText(/for replacement/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Plans' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Import & discovery' })).toBeDisabled();
    let release!: () => void;
    hold = new Promise(r => {
      release = r;
    });
    const user = userEvent.setup();
    await pickWorkspace(user, 'prod');
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/prod/resources`);
    // While prod loads, nothing from dev remains on screen.
    expect(await screen.findByText('Reading your workspace')).toBeInTheDocument();
    expect(screen.queryByText('dev_logs')).not.toBeInTheDocument();
    expect(screen.getByText('Production-like name')).toBeInTheDocument();
    release();
    expect(await screen.findByRole('button', { name: /prod_logs/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /dev_logs/ })).not.toBeInTheDocument();
    expect(calls.filter(c => c.url.endsWith('/contexts')).map(c => (c.body as { workspace: string }).workspace)).toEqual(['dev', 'prod']);
    // No call ever asks the server to select a workspace.
    expect(calls.some(c => JSON.stringify(c.body || {}).includes('select'))).toBe(false);
  });
  it('keeps the latest workspace when switching back before a slow one answers', async () => {
    stub(listing(projects));
    app(`/projects/${network}/workspaces/dev/resources`);
    expect(await screen.findByRole('button', { name: /dev_logs/ })).toBeInTheDocument();
    let release!: () => void;
    hold = new Promise(r => {
      release = r;
    });
    const user = userEvent.setup();
    await pickWorkspace(user, 'prod');
    expect(await screen.findByText('Reading your workspace')).toBeInTheDocument();
    await pickWorkspace(user, 'dev');
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/resources`);
    expect(await screen.findByRole('button', { name: /dev_logs/ })).toBeInTheDocument();
    // The slow prod answer arrives after the switch back and is never shown here.
    release();
    await new Promise(r => setTimeout(r, 50));
    expect(screen.getByRole('button', { name: /dev_logs/ })).toBeInTheDocument();
    expect(screen.queryByText('prod_logs')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Switch workspace/ })).toHaveTextContent('dev');
  });
  it('opens Compare workspaces from the sidebar without reading anything', async () => {
    stub(listing(projects));
    app('/projects');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Compare' }));
    expect(window.location.pathname).toBe('/compare');
    expect(await screen.findByRole('heading', { name: 'Compare workspaces' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Compare' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('button', { name: /Compare…/ })).toBeDisabled();
    expect(calls.filter(c => c.method !== 'GET')).toHaveLength(0);
  });
  it('opens a deep-linked resource with the linked profile and removes the parameters', async () => {
    stub(listing(projects));
    const rid = 'f'.repeat(32);
    window.history.pushState(null, '', `/projects/${network}/workspaces/prod/resources?resource=${rid}&profile=none`);
    consumeDeepLink();
    expect(window.location.search).toBe('');
    expect(sessionStorage.getItem(`hclaro.profile.${network}`)).toBe('none');
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
    await waitFor(() => expect(calls.some(c => c.url.endsWith(`contexts/${ctx.prod}/resources/${rid}`))).toBe(true));
    sessionStorage.clear();
  });
  it('ignores malformed deep-link parameters', () => {
    window.history.pushState(null, '', `/projects/${network}/workspaces/prod/resources?resource=../x&profile=${encodeURIComponent('-lock=false')}`);
    consumeDeepLink();
    expect(window.location.search).toBe('');
    expect(sessionStorage.getItem(`hclaro.profile.${network}`)).toBeNull();
  });
  it('explains expired credentials when workspaces cannot be listed', async () => {
    const advice = 'Unable to list workspaces. The CLI reported rejected or expired credentials. Sign in again in a terminal (for example aws sso login), then try again. Nothing was changed.';
    inspectReply = { ...inspected, inspection: { workspaces: [], hiddenWorkspaces: 0, inspectedAt: '2026-11-05T12:00:00Z', diagnostic: { code: 'workspace_list', message: advice } } };
    stub(listing(projects));
    app(`/projects/${network}`);
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Workspaces unavailable');
    expect(alert).toHaveTextContent('expired credentials');
    expect(alert).toHaveTextContent('Nothing was initialized or changed.');
    expect(screen.queryByRole('button', { name: /Read state/ })).not.toBeInTheDocument();
  });
  it('opens the Hub when Hclaro was started without a project', async () => {
    stub(listing([]));
    app('/');
    expect(await screen.findByText('No projects yet')).toBeInTheDocument();
    expect(window.location.pathname).toBe('/projects');
    expect(screen.getByRole('button', { name: 'Resources' })).toBeDisabled();
  });
  it('shows a read-only registry and inherited settings honestly', async () => {
    const list = listing(projects);
    list.registry = { status: 'unavailable', notice: 'The project registry was written by a newer Hclaro.', path: '/c' };
    list.environment = { dataDir: '/shared', dataDirShared: true, workspaceInherited: true, inheritedWorkspace: 'staging', cliArgsInherited: false };
    stub(list);
    app('/projects');
    expect(await screen.findByText('The project registry was written by a newer Hclaro.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add project' })).toBeDisabled();
    // A read-only registry blocks every change: pin, order, rename and remove.
    expect(screen.getByRole('button', { name: 'Pin network' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move network down' })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Details for network' }));
    expect(screen.getByRole('button', { name: 'Rename' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Remove from Hclaro' })).toBeDisabled();
    expect(screen.getByText(/TF_DATA_DIR is an absolute path/)).toBeInTheDocument();
    expect(document.body).toHaveTextContent('set to staging');
  });
});

describe('Routes', () => {
  it('parses only well-formed segments', () => {
    expect(parseRoute('/')).toEqual({ kind: 'launch' });
    expect(parseRoute('/projects')).toEqual({ kind: 'hub' });
    expect(parseRoute('/compare')).toEqual({ kind: 'compare' });
    expect(parseRoute('/compare/extra')).toEqual({ kind: 'launch' });
    expect(parseRoute('/history')).toEqual({ kind: 'history' });
    expect(parseRoute('/history/' + 'a'.repeat(32))).toEqual({ kind: 'launch' });
    expect(parseRoute(`/projects/${network}/workspaces/prod`)).toEqual({ kind: 'context', projectId: network, workspace: 'prod', view: 'Resources' });
    expect(parseRoute(`/projects/${network}/workspaces/prod/modules`)).toEqual({ kind: 'context', projectId: network, workspace: 'prod', view: 'Modules' });
    for (const bad of ['..%2Fprod', '%E0%A4%A', 'a%20b', 'x'.repeat(91)]) {
      expect(parseRoute(`/projects/${network}/workspaces/${bad}/resources`)).toEqual({ kind: 'project', projectId: network });
    }
    expect(parseRoute('/projects/../../etc')).toEqual({ kind: 'hub' });
    expect(parseRoute(`/projects/${network}/workspaces/prod/state`)).toEqual({ kind: 'project', projectId: network });
  });
  it('flags production-like names without inferring criticality', () => {
    expect(productionLike('prod')).toBe(true);
    expect(productionLike('infra-production')).toBe(true);
    expect(productionLike('product-catalog')).toBe(false);
    expect(productionLike('dev', undefined)).toBe(false);
  });
});
