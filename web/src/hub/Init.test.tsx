import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import type { InitStatus, Operation, Project, ProjectList } from '../types';

const id = 'a'.repeat(32);
const contextId = 'f'.repeat(64);
const legacy: Project = {
  id, alias: 'legacy', rootPath: '/infra/legacy', enginePreference: 'terraform', pinned: false, addedAt: '2026-11-05T10:00:00Z',
  status: 'ready', initialized: false, backend: 'unknown', executionMode: 'unsupported', providerLock: false, launch: false,
  workspaceActions: { change: false, reason: 'Initialize the project first.', delete: false, deleteReason: 'Initialize the project first.' }
};
const list: ProjectList = {
  projects: [legacy],
  registry: { status: 'ready', notice: '', path: '/config/projects.json' },
  environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: true, inheritedWorkspace: 'prod', cliArgsInherited: false }
};
const status: InitStatus = {
  projectId: id, alias: 'legacy', rootPath: '/infra/legacy', status: 'ready', initialized: false, backend: 'unknown', executionMode: 'unsupported',
  backendDeclared: true, engine: 'terraform', dataDir: '.terraform', lockfile: false, command: ['terraform', 'init', '-input=false', '-no-color'],
  notices: ['Inherited TF_WORKSPACE is not passed to init; init uses this project\'s CLI-default workspace.'], canInit: true, contextId
};
const op = (extra: Partial<Operation>): Operation => ({
  id: 'e'.repeat(32), kind: 'init', status: 'queued', stage: 'Queued', startedAt: '2026-11-05T12:00:00Z', durationMs: 0,
  command: ['terraform', 'init', '-input=false', '-json'], varFiles: [], replace: [], stateReloaded: false, engineDiagnostics: [],
  cancelRequested: false, logDropped: 0, logRetained: true, projectId: id, ...extra
});

type Call = { path: string; method: string; body?: unknown; headers: Record<string, string> };
let calls: Call[];
function stub(initStatus: InitStatus, final: Operation) {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    const path = url.replace('/api/v1/', '').split('?')[0];
    calls.push({ path, method, body: init.body ? JSON.parse(init.body as string) : undefined, headers: init.headers as Record<string, string> });
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'projects') return Response.json(list);
    if (path === 'workspace') return Response.json({ status: 'error', info: { warnings: [], capabilities: {} }, diagnostic: { code: 'no_launch_project', message: 'No project.' } });
    if (path.endsWith('/inspect')) return Response.json({ ...legacy, inspection: { workspaces: [], hiddenWorkspaces: 0, inspectedAt: '2026-11-05T12:00:00Z', diagnostic: { code: 'uninitialized', message: 'Backend initialization was not detected.' } } });
    if (path.endsWith('/init-status')) return Response.json(initStatus);
    if (path.endsWith('/operations/init')) return Response.json(op({}), { status: 202 });
    if (path.endsWith('/events')) {
      const body = [
        'id: 1\nevent: log\ndata: {"seq":1,"time":"2026-11-05T12:00:01Z","level":"info","text":"Initializing the backend..."}\n\n',
        'event: status\ndata: ' + JSON.stringify(final) + '\n\nevent: end\ndata: {"done":true}\n\n'
      ].join('');
      return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } });
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

async function openWizard() {
  const user = userEvent.setup();
  app();
  await user.click(await screen.findByRole('button', { name: 'Review and initialize…' }));
  const dialog = await screen.findByRole('dialog', { name: /init for legacy/ });
  return { user, dialog };
}

describe('Init wizard', () => {
  it('never initializes without an explicit confirmation', async () => {
    stub(status, op({ status: 'succeeded' }));
    const { user, dialog } = await openWizard();
    // Reviewing reads the folder only: no init request, and the button waits for consent.
    expect(await within(dialog).findByText('terraform init -input=false -no-color')).toBeInTheDocument();
    expect(dialog).toHaveTextContent('/infra/legacy');
    expect(dialog).toHaveTextContent('Declared in configuration; not initialized yet');
    expect(dialog).toHaveTextContent('TF_WORKSPACE is not passed');
    expect(dialog).toHaveTextContent('Never upgrades or migrates');
    const initialize = within(dialog).getByRole('button', { name: 'Initialize' });
    expect(initialize).toBeDisabled();
    expect(calls.filter(c => c.path.endsWith('/operations/init'))).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(calls.filter(c => c.method !== 'GET' && !c.path.endsWith('/inspect'))).toHaveLength(0);
  });

  it('starts a standard init with the reviewed context and shows the result', async () => {
    stub(status, op({ status: 'succeeded', stage: 'Finished', exitCode: 0, lockfileChanged: true }));
    const { user, dialog } = await openWizard();
    await user.click(await within(dialog).findByRole('checkbox'));
    await user.click(within(dialog).getByRole('button', { name: 'Initialize' }));
    const start = calls.filter(c => c.path.endsWith('/operations/init'));
    expect(start).toHaveLength(1);
    expect(start[0].method).toBe('POST');
    expect(start[0].body).toEqual({ mode: 'standard', expectedContextId: contextId });
    expect(start[0].headers['X-Hclaro-CSRF']).toBe('c'.repeat(64));
    expect(await within(dialog).findByText('Initializing the backend...')).toBeInTheDocument();
    const result = await within(dialog).findByRole('status', { name: 'Init result' });
    expect(result).toHaveTextContent('Initialized');
    expect(result).toHaveTextContent('exit code 0');
    expect(result).toHaveTextContent('.terraform.lock.hcl was created or changed');
    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    // The project is read again after init; it is never retried.
    await waitFor(() => expect(calls.filter(c => c.path.endsWith('/inspect')).length).toBeGreaterThan(1));
    expect(calls.filter(c => c.path.endsWith('/operations/init'))).toHaveLength(1);
  });

  it('explains a failure with remediation and does not retry', async () => {
    stub(status, op({
      status: 'failed', stage: 'Finished', exitCode: 1,
      diagnostic: { code: 'backend_migration', message: 'Hclaro never runs init with -migrate-state, -force-copy or -reconfigure.' },
      engineDiagnostics: [{ severity: 'error', summary: 'Backend configuration changed' }]
    }));
    const { user, dialog } = await openWizard();
    await user.click(await within(dialog).findByRole('checkbox'));
    await user.click(within(dialog).getByRole('button', { name: 'Initialize' }));
    const result = await within(dialog).findByRole('alert', { name: 'Init result' });
    expect(result).toHaveTextContent('Initialization failed');
    expect(result).toHaveTextContent('exit code 1');
    expect(result).toHaveTextContent('never runs init with -migrate-state');
    expect(result).toHaveTextContent('Backend configuration changed');
    expect(result).toHaveTextContent('Nothing was retried automatically.');
    await user.click(within(dialog).getByRole('button', { name: 'Review again' }));
    // Reviewing again needs a fresh confirmation.
    expect(await within(dialog).findByRole('button', { name: 'Initialize' })).toBeDisabled();
    expect(calls.filter(c => c.path.endsWith('/operations/init'))).toHaveLength(1);
  });

  it('shows why init is unavailable', async () => {
    stub({ ...status, canInit: false, contextId: undefined, reasonCode: 'unsafe_cli_args', reason: 'Inherited TF_CLI_ARGS_init adds flags other than -no-color.' }, op({}));
    const { dialog } = await openWizard();
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('TF_CLI_ARGS_init adds flags');
    expect(within(dialog).queryByRole('checkbox')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Initialize' })).toBeDisabled();
  });
});
