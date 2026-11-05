import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import type { Operation, Project, ProjectList, WorkspaceReview } from '../types';

const id = 'a'.repeat(32);
const contextId = 'f'.repeat(64);
const network: Project = {
  id, alias: 'network', rootPath: '/infra/network', enginePreference: 'terraform', pinned: false, addedAt: '2026-11-05T10:00:00Z',
  status: 'ready', initialized: true, backend: 'local', executionMode: 'local-cli', providerLock: true, launch: false, lastWorkspace: 'prod',
  workspaceActions: { change: true, delete: true }
};
const inspected = { ...network, inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev', 'prod', 'qa'], cliDefault: 'dev', hiddenWorkspaces: 0, inspectedAt: '2026-11-05T12:00:00Z' } };
const list = (p: Project): ProjectList => ({
  projects: [p],
  registry: { status: 'ready', notice: '', path: '/config/projects.json' },
  environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: true, inheritedWorkspace: 'dev', cliArgsInherited: false }
});
const review = (extra: Partial<WorkspaceReview>): WorkspaceReview => ({
  projectId: id, alias: 'network', rootPath: '/infra/network', action: 'new', name: 'staging', engine: 'terraform', engineVersion: '1.9.8',
  backend: 'local', executionMode: 'local-cli', dataDir: '.terraform', workspaces: ['default', 'dev', 'prod', 'qa'], cliDefault: 'dev',
  command: ['terraform', 'workspace', 'new', 'staging'], notices: ['Inherited TF_WORKSPACE is not passed: the engines refuse to create or select a workspace while it overrides the selection.'],
  support: { change: true, delete: true }, launch: false, canRun: true, contextId, ...extra
});
const op = (extra: Partial<Operation>): Operation => ({
  id: 'e'.repeat(32), kind: 'workspace', status: 'queued', stage: 'Queued', startedAt: '2026-11-05T12:00:00Z', durationMs: 0,
  command: ['terraform', 'workspace', 'new', 'staging'], varFiles: [], replace: [], stateReloaded: false, engineDiagnostics: [],
  cancelRequested: false, logDropped: 0, logRetained: true, projectId: id, ...extra
});

type Call = { path: string; method: string; body?: unknown; headers: Record<string, string> };
let calls: Call[];
function stub(reviews: Record<string, WorkspaceReview>, final: Operation, project: Project = network) {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    const path = url.replace('/api/v1/', '').split('?')[0];
    const body = init.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ path, method, body, headers: init.headers as Record<string, string> });
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'projects') return Response.json(list(project));
    if (path === 'workspace') return Response.json({ status: 'error', info: { warnings: [], capabilities: {} }, diagnostic: { code: 'no_launch_project', message: 'No project.' } });
    if (path.endsWith('/inspect')) return Response.json({ ...inspected, workspaceActions: project.workspaceActions });
    if (path.endsWith('/workspaces/review')) return Response.json(reviews[`${body.action} ${body.name}`]);
    if (path.endsWith('/operations/workspace')) return Response.json(op({ command: ['terraform', 'workspace', body.action, body.name] }), { status: 202 });
    if (path.endsWith('/events')) {
      const events = [
        'id: 1\nevent: log\ndata: {"seq":1,"time":"2026-11-05T12:00:01Z","level":"info","text":"Created and switched to workspace \\"staging\\"!"}\n\n',
        'event: status\ndata: ' + JSON.stringify(final) + '\n\nevent: end\ndata: {"done":true}\n\n'
      ].join('');
      return new Response(events, { headers: { 'Content-Type': 'text/event-stream' } });
    }
    return Response.json({}, { status: 404 });
  }));
}
function app() {
  window.history.pushState(null, '', `/projects/${id}`);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.pushState(null, '', '/');
});
const mutations = () => calls.filter(c => c.path.endsWith('/operations/workspace'));
const reviews = () => calls.filter(c => c.path.endsWith('/workspaces/review'));

describe('Workspace lifecycle', () => {
  it('opening a workspace only changes this tab and never sends a workspace command', async () => {
    stub({}, op({}));
    const user = userEvent.setup();
    app();
    const row = await screen.findByRole('button', { name: /dev\s*CLI default/ });
    expect(screen.getByRole('button', { name: /^prod/ })).toHaveTextContent('Last opened here');
    // Set-default and delete are hidden until advanced actions are shown.
    expect(screen.queryByRole('button', { name: 'Set prod as CLI default' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /^prod/ }));
    expect(window.location.pathname).toBe(`/projects/${id}/workspaces/prod/resources`);
    expect(row).toBeDefined();
    expect(reviews()).toHaveLength(0);
    expect(mutations()).toHaveLength(0);
  });

  it('creates a workspace only after review and acknowledgement, and reports the changed CLI default', async () => {
    stub({ 'new staging': review({}) }, op({
      status: 'succeeded', stage: 'Finished', exitCode: 0,
      workspace: { action: 'new', name: 'staging', cliDefaultBefore: 'dev', cliDefaultAfter: 'staging', workspaces: ['default', 'dev', 'prod', 'qa', 'staging'], ran: true, verified: true }
    }));
    const user = userEvent.setup();
    app();
    await user.click(await screen.findByRole('button', { name: 'New workspace…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create a workspace' });
    const input = within(dialog).getByRole('textbox', { name: 'Workspace name' });
    await user.type(input, '-force');
    expect(within(dialog).getByRole('button', { name: /Review/ })).toBeDisabled();
    await user.clear(input);
    await user.type(input, 'staging');
    await user.click(within(dialog).getByRole('button', { name: /Review/ }));
    expect(await within(dialog).findByText('terraform workspace new staging')).toBeInTheDocument();
    expect(reviews()[0].body).toEqual({ action: 'new', name: 'staging' });
    expect(dialog).toHaveTextContent('Also changes the CLI default from dev to staging');
    expect(dialog).toHaveTextContent('Inherited TF_WORKSPACE is not passed');
    const create = within(dialog).getByRole('button', { name: 'Create workspace' });
    expect(create).toBeDisabled();
    expect(mutations()).toHaveLength(0);
    await user.click(within(dialog).getByRole('checkbox'));
    await user.click(create);
    expect(mutations()).toHaveLength(1);
    expect(mutations()[0].body).toEqual({ action: 'new', name: 'staging', expectedContextId: contextId });
    expect(mutations()[0].headers['X-Hclaro-CSRF']).toBe('c'.repeat(64));
    const result = await within(dialog).findByRole('status', { name: 'Workspace command result' });
    expect(result).toHaveTextContent('exit code 0');
    expect(result).toHaveTextContent('CLI default for this folder: staging (was dev)');
    await user.click(within(dialog).getByRole('button', { name: 'Open staging in this tab' }));
    expect(window.location.pathname).toBe(`/projects/${id}/workspaces/staging/resources`);
    expect(mutations()).toHaveLength(1);
  });

  it('sets the CLI default as a separate advanced action with its own warning', async () => {
    stub({ 'select prod': review({ action: 'select', name: 'prod', command: ['terraform', 'workspace', 'select', 'prod'] }) }, op({
      status: 'succeeded', stage: 'Finished', exitCode: 0,
      workspace: { action: 'select', name: 'prod', cliDefaultBefore: 'dev', cliDefaultAfter: 'prod', ran: true, verified: true }
    }));
    const user = userEvent.setup();
    app();
    await user.click(await screen.findByRole('checkbox', { name: 'Advanced actions' }));
    expect(screen.getByRole('button', { name: 'Set dev as CLI default' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Set prod as CLI default' }));
    const dialog = await screen.findByRole('dialog', { name: 'Set prod as CLI default' });
    expect(await within(dialog).findByText('terraform workspace select prod')).toBeInTheDocument();
    expect(dialog).toHaveTextContent("Commands you run in other terminals here will read and change prod's state");
    const confirm = within(dialog).getByRole('button', { name: 'Set as CLI default' });
    expect(confirm).toBeDisabled();
    await user.click(within(dialog).getByRole('checkbox'));
    await user.click(confirm);
    expect(mutations()[0].body).toEqual({ action: 'select', name: 'prod', expectedContextId: contextId });
    expect(await within(dialog).findByRole('status', { name: 'Workspace command result' })).toHaveTextContent('CLI default for this folder: prod (was dev)');
  });

  it('deletes only after typing the exact name, and blocks default and the CLI default', async () => {
    stub({ 'delete qa': review({ action: 'delete', name: 'qa', command: ['terraform', 'workspace', 'delete', 'qa'], empty: true }) }, op({
      status: 'succeeded', stage: 'Finished', exitCode: 0,
      workspace: { action: 'delete', name: 'qa', cliDefaultBefore: 'dev', cliDefaultAfter: 'dev', ran: true, verified: true }
    }));
    const user = userEvent.setup();
    app();
    await user.click(await screen.findByRole('checkbox', { name: 'Advanced actions' }));
    expect(screen.getByRole('button', { name: 'Delete workspace default' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Delete workspace dev' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Delete workspace qa' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete workspace qa' });
    expect(await within(dialog).findByText('terraform workspace delete qa')).toBeInTheDocument();
    expect(dialog).toHaveTextContent('never -force');
    expect(dialog).toHaveTextContent('Checked empty');
    const confirm = within(dialog).getByRole('button', { name: 'Delete workspace' });
    const typed = within(dialog).getByRole('textbox', { name: 'Type qa to confirm' });
    await user.type(typed, 'QA');
    expect(confirm).toBeDisabled();
    await user.clear(typed);
    await user.type(typed, 'qa');
    await user.click(confirm);
    expect(mutations()[0].body).toEqual({ action: 'delete', name: 'qa', expectedContextId: contextId });
    const result = await within(dialog).findByRole('status', { name: 'Workspace command result' });
    expect(result).toHaveTextContent('(unchanged)');
    expect(within(dialog).queryByRole('button', { name: /Open qa/ })).not.toBeInTheDocument();
  });

  it('shows a refusal without any way to confirm', async () => {
    stub({ 'delete prod': review({ action: 'delete', name: 'prod', canRun: false, contextId: undefined, empty: false, reasonCode: 'workspace_not_empty', reason: "This workspace's recorded state still has resources or outputs. Hclaro deletes only empty workspaces and never uses -force." }) }, op({}));
    const user = userEvent.setup();
    app();
    await user.click(await screen.findByRole('checkbox', { name: 'Advanced actions' }));
    await user.click(screen.getByRole('button', { name: 'Delete workspace prod' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete workspace prod' });
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('deletes only empty workspaces');
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Delete workspace' })).toBeDisabled();
    expect(mutations()).toHaveLength(0);
  });

  it('reports an unconfirmed result honestly and never retries', async () => {
    stub({ 'new staging': review({}) }, op({
      status: 'unknown', stage: 'Finished', exitCode: 0,
      diagnostic: { code: 'unverified', message: 'The engine reported success, but Hclaro could not list workspaces afterwards to confirm it.' },
      workspace: { action: 'new', name: 'staging', cliDefaultBefore: 'dev', ran: true, verified: false }
    }));
    const user = userEvent.setup();
    app();
    await user.click(await screen.findByRole('button', { name: 'New workspace…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create a workspace' });
    await user.type(within(dialog).getByRole('textbox', { name: 'Workspace name' }), 'staging');
    await user.click(within(dialog).getByRole('button', { name: /Review/ }));
    await user.click(await within(dialog).findByRole('checkbox'));
    await user.click(within(dialog).getByRole('button', { name: 'Create workspace' }));
    const result = await within(dialog).findByRole('alert', { name: 'Workspace command result' });
    expect(result).toHaveTextContent('Result not confirmed');
    expect(result).toHaveTextContent('The CLI default could not be confirmed');
    expect(result).toHaveTextContent('Nothing was retried or undone automatically.');
    await user.click(within(dialog).getAllByRole('button', { name: 'Close' }).at(-1)!);
    await waitFor(() => expect(calls.filter(c => c.path.endsWith('/inspect')).length).toBeGreaterThan(1));
    expect(mutations()).toHaveLength(1);
  });

  it('disables workspace changes for an unsupported backend and says why', async () => {
    stub({}, op({}), { ...network, backend: 'remote', executionMode: 'remote-exec', workspaceActions: { change: false, reason: 'Workspaces of the remote backend are managed by the remote service.', delete: false, deleteReason: 'Workspaces of the remote backend are managed by the remote service.' } });
    app();
    expect(await screen.findByRole('button', { name: 'New workspace…' })).toBeDisabled();
    expect(await screen.findByText(/managed by the remote service/)).toBeInTheDocument();
  });
});
