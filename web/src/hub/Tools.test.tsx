import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import type { Operation, Project, ProjectList, ToolName, ToolResult } from '../types';

const id = 'a'.repeat(32);
const cid = 'c'.repeat(32);
const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const network: Project = {
  id, alias: 'network', rootPath: '/infra/network', enginePreference: 'terraform', pinned: false, addedAt: '2026-11-05T10:00:00Z',
  status: 'ready', initialized: true, backend: 'local', executionMode: 'local-cli', providerLock: true, launch: false, lastWorkspace: 'dev',
  workspaceActions: { change: true, delete: true },
  inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev', 'prod'], cliDefault: 'dev', hiddenWorkspaces: 0, inspectedAt: '2026-11-05T12:00:00Z' }
};
const list: ProjectList = {
  projects: [network],
  registry: { status: 'ready', notice: '', path: '/config/projects.json' },
  environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: false, cliArgsInherited: false }
};
const commands: Record<ToolName, string[]> = {
  validate: ['terraform', 'validate', '-json'],
  'fmt-check': ['terraform', 'fmt', '-check', '-no-color'],
  providers: ['terraform', 'providers'],
  'outputs-metadata': ['terraform', 'output', '-json']
};
let seq = 0;
function op(tool: ToolName, result: Partial<ToolResult>, extra: Partial<Operation> = {}): Operation {
  seq++;
  return {
    id: seq.toString(16).padStart(32, '0'), kind: 'tool', status: 'succeeded', stage: 'Finished', startedAt: '2026-11-05T12:00:00Z', durationMs: 420, exitCode: 0,
    command: commands[tool], varFiles: [], replace: [], stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true, projectId: id,
    tool: { tool, contextId: cid, workspace: 'dev', usesWorkspace: tool === 'providers' || tool === 'outputs-metadata', engineVersion: '1.9.8', outcome: 'passed', errors: 0, warnings: 0, findings: [], ...result },
    ...extra
  };
}

type Call = { path: string; method: string; body?: unknown; headers: Record<string, string> };
let calls: Call[];
let history: Operation[];
// results maps a tool to the finished operation the server reports for it.
function stub(results: Partial<Record<ToolName, Operation>>, initial: Operation[] = []) {
  calls = [];
  history = [...initial];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    const path = url.replace('/api/v1/', '').split('?')[0];
    const body = init.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ path, method, body, headers: init.headers as Record<string, string> });
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'projects') return Response.json(list);
    if (path === 'operations') return Response.json({ operations: [] });
    if (path === 'workspace') return Response.json({ status: 'error', info: { warnings: [], capabilities: {} }, diagnostic: { code: 'no_launch_project', message: 'No project.' } });
    if (path === 'contexts' && method === 'POST') return Response.json({ id: cid, projectId: id, alias: 'network', rootPath: '/infra/network', engine: 'terraform', workspace: body.workspace, dataDir: '.terraform', createdAt: '2026-11-05T12:00:00Z', reused: false });
    if (path === `contexts/${cid}`) return Response.json({ status: 'error', info: { project: 'network', engine: 'terraform', workspace: 'dev', backend: 'local', warnings: [], capabilities: {} }, diagnostic: { code: 'uninitialized', message: 'Initialize this workspace first.' }, managed: 0, data: 0, modules: [], providers: {}, types: {} });
    if (path === `projects/${id}/operations` && method === 'GET') return Response.json({ operations: history });
    const tool = path.match(/^projects\/[0-9a-f]{32}\/operations\/(validate|fmt-check|providers|outputs-metadata)$/)?.[1] as ToolName | undefined;
    if (tool && method === 'POST') {
      const done = results[tool]!;
      history = [done, ...history];
      return Response.json({ ...done, status: 'queued', stage: 'Queued', tool: { ...done.tool!, outcome: 'not_checked' } }, { status: 202 });
    }
    const events = path.match(/^operations\/([0-9a-f]{32})\/events$/);
    if (events) {
      const final = history.find(o => o.id === events[1])!;
      return new Response('id: 1\nevent: log\ndata: {"seq":1,"time":"2026-11-05T12:00:01Z","level":"info","text":"Checking engine version…"}\n\nevent: status\ndata: ' + JSON.stringify(final) + '\n\nevent: end\ndata: {"done":true}\n\n', { headers: { 'Content-Type': 'text/event-stream' } });
    }
    return Response.json({}, { status: 404 });
  }));
}
function app(path = `/projects/${id}/workspaces/dev/tools`) {
  window.history.pushState(null, '', path);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.pushState(null, '', '/');
});
const started = () => calls.filter(c => c.method === 'POST' && /\/operations\/(validate|fmt-check|providers|outputs-metadata)$/.test(c.path));
const card = (name: string) => screen.getByRole('region', { name });

describe('Project tools', () => {
  it('runs nothing until asked and explains what each tool reads', async () => {
    stub({});
    app();
    expect(await screen.findByRole('heading', { name: 'Project tools' })).toBeInTheDocument();
    await screen.findByRole('region', { name: 'Validate' });
    expect(card('Validate')).toHaveTextContent('terraform validate -json');
    expect(card('Validate')).toHaveTextContent('Configuration only');
    expect(card('Validate')).toHaveTextContent('not a plan');
    expect(card('Format check')).toHaveTextContent('never rewrites files');
    expect(card('Providers')).toHaveTextContent('Reads dev state');
    expect(card('Providers')).toHaveTextContent('do not show which cloud account or identity');
    expect(card('Outputs')).toHaveTextContent('never shown, including values that are not marked sensitive');
    expect(await screen.findByText('No operations yet')).toBeInTheDocument();
    expect(started()).toHaveLength(0);
  });

  it('validates once with only the context ID and shows sanitized diagnostics with locations', async () => {
    stub({
      validate: op('validate', {
        outcome: 'findings', valid: false, errors: 2, warnings: 1, output: 'json', findings: [
          { severity: 'error', summary: 'Unsupported argument', detail: 'An argument named "bucket_name" is not expected here.', file: 'main.tf', line: 3, column: 3 },
          { severity: 'error', summary: 'Invalid reference', detail: '[line withheld: references a sensitive attribute]', file: 'modules/db/main.tf', line: 12, column: 9 },
          { severity: 'warning', summary: 'Deprecated attribute', file: 'main.tf', line: 4 }
        ]
      }, { status: 'failed', exitCode: 1, diagnostic: { code: 'validation_errors', message: 'The configuration is not valid.' } })
    });
    const user = userEvent.setup();
    app();
    await user.click(within(await screen.findByRole('region', { name: 'Validate' })).getByRole('button', { name: /Run/ }));
    expect(started()).toHaveLength(1);
    expect(started()[0].body).toEqual({ contextId: cid });
    expect(started()[0].headers['X-Hclaro-CSRF']).toBe('c'.repeat(64));
    const result = await screen.findByRole('alert', { name: 'Validate result' });
    expect(result).toHaveTextContent('Not valid · 2 errors, 1 warning');
    expect(result).toHaveTextContent('exit code 1');
    expect(result).toHaveTextContent('main.tf:3:3');
    expect(result).toHaveTextContent('modules/db/main.tf:12:9');
    expect(result).toHaveTextContent('Read-only: nothing was written');
    expect(document.body.textContent).not.toContain(canary);
  });

  it('says a validate that could not run did not validate, and offers Init without running it', async () => {
    stub({
      validate: op('validate', { outcome: 'not_checked', errors: 1, findings: [{ severity: 'error', summary: 'Missing required provider' }] }, {
        status: 'failed', exitCode: 1, diagnostic: { code: 'plugins_missing', message: 'Required providers or modules are not installed. Nothing was checked.' }
      })
    });
    const user = userEvent.setup();
    app();
    await user.click(within(await screen.findByRole('region', { name: 'Validate' })).getByRole('button', { name: /Run/ }));
    const result = await screen.findByRole('alert', { name: 'Validate result' });
    expect(result).toHaveTextContent('Not validated');
    expect(result).not.toHaveTextContent('Not valid ·');
    expect(result).toHaveTextContent('Nothing was initialized automatically');
    await user.click(within(result).getByRole('button', { name: 'Review Init on the project page' }));
    expect(window.location.pathname).toBe(`/projects/${id}`);
    expect(calls.some(c => c.path.endsWith('/operations/init'))).toBe(false);
  });

  it('runs a recursive format check only when toggled and lists the files', async () => {
    stub({
      'fmt-check': op('fmt-check', { outcome: 'findings', format: { recursive: true, checked: 7, folders: 3, unformatted: ['main.tf', 'modules/db/main.tf'], hidden: 1, skipped: ['linked'] } }, {
        status: 'failed', exitCode: 3, command: ['terraform', 'fmt', '-check', '-no-color', '-recursive'], diagnostic: { code: 'unformatted', message: 'Some files are not in canonical format. Hclaro checks only; it never rewrites files.' }
      })
    });
    const user = userEvent.setup();
    app();
    const fmt = await screen.findByRole('region', { name: 'Format check' });
    expect(fmt).toHaveTextContent('terraform fmt -check -no-color');
    expect(fmt).not.toHaveTextContent('-recursive terraform');
    await user.click(within(fmt).getByRole('checkbox'));
    expect(fmt).toHaveTextContent('terraform fmt -check -no-color -recursive');
    await user.click(within(fmt).getByRole('button', { name: /Run/ }));
    expect(started()[0].body).toEqual({ contextId: cid, recursive: true });
    const result = await screen.findByRole('alert', { name: 'Format check result' });
    expect(result).toHaveTextContent('Needs formatting');
    expect(result).toHaveTextContent('Checked 7 configuration files in 3 folders, including subfolders');
    expect(within(result).getByText('modules/db/main.tf')).toBeInTheDocument();
    expect(result).toHaveTextContent('Not followed (symbolic links): linked');
  });

  it('shows provider requirements and output metadata without values', async () => {
    stub({
      providers: op('providers', { providers: { configuration: [{ source: 'registry.terraform.io/hashicorp/aws', constraint: '~> 5.0' }, { source: 'registry.terraform.io/hashicorp/aws', module: 'module.network' }], state: [{ source: 'registry.terraform.io/hashicorp/aws' }], hidden: 0 } }),
      'outputs-metadata': op('outputs-metadata', { outputs: { items: [{ name: 'connection_string', type: 'string', sensitive: false }, { name: 'db_password', type: 'string', sensitive: true }], hidden: 1 } })
    });
    const user = userEvent.setup();
    app();
    await user.click(within(await screen.findByRole('region', { name: 'Providers' })).getByRole('button', { name: /Run/ }));
    const providers = await screen.findByRole('status', { name: 'Providers result' });
    expect(providers).toHaveTextContent('workspace dev');
    expect(providers).toHaveTextContent('module.network');
    expect(providers).toHaveTextContent('Recorded state');
    expect(providers).toHaveTextContent('do not prove which cloud account or identity');
    await user.click(within(card('Outputs')).getByRole('button', { name: /Run/ }));
    const outputs = await screen.findByRole('status', { name: 'Outputs result' });
    const rows = within(outputs).getAllByRole('row');
    expect(rows[1]).toHaveTextContent('connection_stringstringNot markedNot shown');
    expect(rows[2]).toHaveTextContent('db_passwordstringMarked sensitiveNot shown');
    expect(outputs).toHaveTextContent('1 output name that looks like a secret');
    expect(outputs).toHaveTextContent('even for outputs not marked sensitive');
    expect(document.body.textContent).not.toContain(canary);
  });

  it('lists project operations by workspace with links back to the project', async () => {
    const init: Operation = { ...op('validate', {}), kind: 'init', tool: undefined, command: ['terraform', 'init', '-input=false', '-no-color'] };
    const prod = op('outputs-metadata', { workspace: 'prod', contextId: 'd'.repeat(32) });
    const dev = op('providers', {});
    stub({}, [dev, prod, init]);
    const user = userEvent.setup();
    app();
    const center = await screen.findByRole('region', { name: 'Operations in this project' });
    await waitFor(() => expect(within(center).getAllByRole('row')).toHaveLength(3));
    expect(center).toHaveTextContent('Providers');
    expect(center).toHaveTextContent('Init');
    expect(center).not.toHaveTextContent('Outputs');
    await user.click(within(center).getByRole('button', { name: 'All workspaces' }));
    expect(within(center).getAllByRole('row')).toHaveLength(4);
    expect(within(center).getByText('prod')).toBeInTheDocument();
    // The last result of a tool in this context is one click away.
    await user.click(within(card('Providers')).getByRole('button', { name: 'Show the last Providers result' }));
    expect(await screen.findByRole('status', { name: 'Providers result' })).toBeInTheDocument();
    await user.click(within(center).getByRole('button', { name: /Project page/ }));
    expect(window.location.pathname).toBe(`/projects/${id}`);
    expect(started()).toHaveLength(0);
  });

  it('opens the Hub tools of the project opened from the terminal, or asks to save it first', async () => {
    const launchSnapshot = { status: 'ready', capturedAt: '2026-11-05T12:00:00Z', info: { project: 'network', engine: 'terraform', version: '1.9.8', workspace: 'dev', backend: 'local', executionMode: 'local-cli', initialized: true, providerLock: true, capabilities: {}, warnings: [] }, managed: 0, data: 0, modules: [], providers: {}, types: {} };
    for (const registered of [true, false]) {
      stub({});
      const hub = fetch as unknown as (url: string, init?: RequestInit) => Promise<Response>;
      vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        const path = url.replace('/api/v1/', '').split('?')[0];
        if (path === 'workspace') return Response.json(launchSnapshot);
        if (path === 'projects') return Response.json({ ...list, projects: registered ? [{ ...network, launch: true }] : [], launchRoot: '/infra/network' });
        if (path === 'resources') return Response.json({ resources: [], total: 0 });
        return hub(url, init);
      }));
      const user = userEvent.setup();
      app('/');
      const open = vi.spyOn(window, 'open').mockImplementation(() => null);
      const tools = await screen.findByRole('button', { name: 'Tools' });
      expect(tools).toBeEnabled();
      // Tools first explains where the tools run; the tab stays on this project.
      await user.click(tools);
      expect(window.location.pathname).toBe('/');
      expect(tools).toHaveAttribute('aria-current', 'page');
      expect(await screen.findByRole('list', { name: 'Available tools' })).toHaveTextContent('Validate');
      if (registered) {
        expect(screen.getByRole('heading', { name: 'Tools run in this folder’s Hub entry' })).toBeInTheDocument();
        await user.click(screen.getByRole('button', { name: 'Open in new tab' }));
        expect(open).toHaveBeenCalledWith(expect.stringMatching(new RegExp(`^/projects/${id}/workspaces/dev/tools#session=`)), '_blank', 'noopener');
        expect(window.location.pathname).toBe('/');
        await user.click(screen.getByRole('button', { name: 'Open tools for workspace dev' }));
        expect(window.location.pathname).toBe(`/projects/${id}/workspaces/dev/tools`);
        expect(await screen.findByRole('heading', { name: 'Project tools' })).toBeInTheDocument();
      } else {
        expect(screen.getByRole('heading', { name: 'Save this project to the Hub first' })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /Open tools/ })).not.toBeInTheDocument();
        await user.click(screen.getByRole('button', { name: 'Go to Save to Hub' }));
        expect(window.location.pathname).toBe('/projects');
        expect(await screen.findByText(/Save this project to the Hub to run project tools/)).toBeInTheDocument();
      }
      expect(started()).toHaveLength(0);
      open.mockRestore();
      cleanup();
      vi.unstubAllGlobals();
    }
  });
});
