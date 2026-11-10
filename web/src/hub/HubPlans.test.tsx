import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import golden from '../../../fixtures/plan/all-actions.golden.json';
import driftGolden from '../../../fixtures/drift/mixed.golden.json';
import graphGolden from '../../../fixtures/graph/terraform.golden.json';
import discoveryGolden from '../../../fixtures/discovery/scan.golden.json';
import App from '../App';
import { contextPath, parseRoute } from '../router';
import type { DependencyGraph, DiscoveryOptions, DiscoveryPrefill, DiscoveryScan, DriftScan, Operation, Plan, PreflightReport, Project, ProjectList, ScopedPlanOptions, Workspace } from '../types';

// M24.07: plans and applies of a Hub workspace go only to that workspace's own context.
const network = 'a'.repeat(32);
const planId = 'b'.repeat(32);
const opId = 'e'.repeat(32);
// One open context per workspace (and per profile); "Open again" yields a new one.
const ctx: Record<string, string> = { dev: '1'.repeat(32), prod: '2'.repeat(32), reopened: '3'.repeat(32) };
const project: Project = {
  id: network, alias: 'network', rootPath: '/infra/network', enginePreference: '', pinned: false, addedAt: '2026-11-05T10:00:00Z', status: 'ready', initialized: true, backend: 'local', executionMode: 'local-cli', providerLock: false, launch: false, workspaceActions: { change: true, delete: true },
  lastWorkspace: 'dev', inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev', 'prod'], cliDefault: 'default', hiddenWorkspaces: 0, inspectedAt: '2026-11-05T11:00:00Z' }
};
const listing: ProjectList = { projects: [project], registry: { status: 'ready', notice: '', path: '/config/hclaro/projects.json' }, environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: false, cliArgsInherited: false } };
const wsOf = (cid: string) => Object.keys(ctx).find(k => ctx[k] === cid) === 'prod' ? 'prod' : 'dev';
function snapshot(cid: string): Workspace {
  const ws = wsOf(cid);
  return {
    status: 'ready', capturedAt: '2026-11-05T12:00:00Z',
    context: { id: cid, projectId: network, alias: 'network', rootPath: '/infra/network', engine: 'terraform', workspace: ws, dataDir: '.terraform', createdAt: '2026-11-05T12:00:00Z' },
    info: { project: 'network', engine: 'terraform', version: '1.9.8', workspace: ws, backend: 'local', executionMode: 'local-cli', initialized: true, providerLock: true, warnings: [], capabilities: { canReadState: true, canPlan: true, canApplySavedPlan: true, supportsImportBlocks: true, supportsGeneratedConfig: true, canImport: false } },
    managed: 0, data: 0, modules: [{ address: '', parent: '', count: 0 }], providers: {}, types: {}
  };
}
const review: Record<string, unknown> = { ...golden };
delete review.details;
const plan = (ws: string) => ({ ...review, id: planId, operationId: opId, createdAt: '2026-11-05T12:05:00Z', engine: 'terraform', currentEngineVersion: '1.9.8', workspace: ws, varFiles: [], kind: 'plan', replace: [], destructive: true, artifactSha256: 'c'.repeat(64), fingerprint: 'd'.repeat(64), contextChanged: false, apply: { available: true, artifactRetained: true, expiresAt: '2026-11-05T13:00:00Z' } }) as unknown as Plan;
const options = (cid: string): ScopedPlanOptions => ({ canPlan: true, scope: { contextId: cid, projectId: network, alias: 'network', rootPath: '/infra/network', engine: 'terraform', version: '1.9.8', workspace: wsOf(cid), profile: null, backend: 'local', executionMode: 'local-cli' }, backendAccessRequired: true, varFiles: ['dev.tfvars'], executionContext: null, contextFingerprint: '' });
const finished: Operation = { id: opId, kind: 'plan', status: 'succeeded', stage: 'Finished', startedAt: '2026-11-05T12:04:00Z', durationMs: 900, exitCode: 0, command: ['terraform', 'plan'], varFiles: [], replace: [], planId, stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true };
const report = (ws: string): PreflightReport => ({
  planId, generatedAt: '2026-11-05T12:06:00Z', expiresAt: '2026-11-05T12:16:00Z',
  context: { project: 'network', engine: 'terraform', engineVersion: '1.9.8', workspace: ws, backend: 'local', executionMode: 'local-cli', planKind: 'plan', planCreatedAt: '2026-11-05T12:05:00Z', artifactSha256: 'c'.repeat(64), fingerprint: 'd'.repeat(16), varFiles: [] },
  checks: [{ id: 'context.production', title: 'Not a production-like context', severity: 'warn', status: 'fail', evidence: `Production-like label: workspace ${ws}.`, source: 'policy' }],
  blocked: false, blockers: 0, acknowledge: ['context.production'],
  impact: { summary: { delete: 1 }, total: 1, destructive: 1, imports: 0, requested: [], extra: 0, highRisk: [], changes: [], truncated: false },
  policy: { source: 'default', version: 1, valid: true }, identity: { state: 'not_requested' },
  binding: 'f'.repeat(64), phrase: `apply ${ws}`, command: ['terraform', 'apply', '<reviewed saved plan>']
});

type Call = { url: string; method: string; body?: Record<string, unknown> };
let calls: Call[];
let gate: boolean;
let override: ((path: string, method: string) => Response | undefined) | undefined;
function stub() {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    const body = init.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ url, method, body });
    const path = url.replace('/api/v1/', '').split('?')[0];
    const special = override?.(path, method);
    if (special) return special;
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64), capabilities: { scopedExecution: gate } });
    if (path === 'projects') return Response.json(listing);
    if (path === 'contexts') return Response.json(snapshot(body.reload ? ctx.reopened : ctx[body.workspace]).context);
    if (path === 'workspace') return Response.json({ ...snapshot(ctx.dev), context: undefined, status: 'error', diagnostic: { code: 'no_launch_project', message: 'No project.' } });
    const m = path.match(/^contexts\/([0-9a-f]{32})(?:\/(.*))?$/);
    if (!m) return Response.json({ error: 'route_not_found' }, { status: 404 });
    const [, cid, rest = ''] = m;
    const ws = wsOf(cid);
    if (rest === '') return Response.json(snapshot(cid));
    if (rest === 'plans/options') return Response.json(options(cid));
    if (rest === 'plans' && method === 'GET') return Response.json({ plans: ws === 'dev' ? [{ id: planId, createdAt: '2026-11-05T12:05:00Z', changes: 11 }] : [], scope: null });
    if (rest === 'plans' && method === 'POST') return Response.json({ ...finished, status: 'running' }, { status: 202 });
    if (rest === `plans/${planId}` && ws === 'dev') return Response.json(plan(ws));
    if (rest === 'operations') return Response.json({ operations: ws === 'dev' ? [finished] : [] });
    if (rest === `operations/${opId}/events`) return new Response('event: log\ndata: {"seq":1,"time":"2026-11-05T12:04:00Z","level":"info","text":"Planning in dev"}\n\nevent: end\ndata: {}\n\n', { headers: { 'Content-Type': 'text/event-stream' } });
    if (rest === `plans/${planId}/preflight`) return Response.json(report(ws));
    if (rest === `plans/${planId}/approval`) return Response.json({ confirmationToken: '9'.repeat(64) });
    if (rest === `plans/${planId}/apply`) return Response.json({ ...finished, kind: 'apply', status: 'running' }, { status: 202 });
    return Response.json({ error: 'plan_not_found' }, { status: 404 });
  }));
}
function app(path: string) {
  window.history.pushState(null, '', path);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
// No Hub request may reach the launch project's plan, operation or apply routes.
const launchCalls = () => calls.filter(c => /^\/api\/v1\/(plans|operations)(\/|$|\?)/.test(c.url));
beforeEach(() => {
  gate = true;
  override = undefined;
  localStorage.clear();
  sessionStorage.clear();
  stub();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.history.pushState(null, '', '/');
});

// The full flows click and type through several dialogs; parallel suites slow them down.
describe('Hub plan flow (M24.07)', { timeout: 20000 }, () => {
  it('plans, reviews and applies in A/dev through its own context only', async () => {
    app(contextPath(network, 'dev', 'Plans'));
    const user = userEvent.setup();
    const run = await screen.findByRole('button', { name: 'Run plan' });
    await waitFor(() => expect(run).toBeEnabled());
    await user.click(run);
    const preflight = await screen.findByRole('dialog', { name: 'Run a plan' });
    const uses = within(preflight).getByLabelText('This plan will use');
    expect(uses).toHaveTextContent('network');
    expect(uses).toHaveTextContent('/infra/network');
    expect(uses).toHaveTextContent('dev');
    expect(uses).toHaveTextContent('the CLI default is not changed');
    await user.click(within(preflight).getByRole('button', { name: 'Run plan' }));
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/plans`)).toBe(true));
    expect(calls.find(c => c.url === `/api/v1/contexts/${ctx.dev}/plans` && c.method === 'POST')!.body).toEqual({ acknowledgeBackendAccess: true, varFiles: [], replace: [], contextFingerprint: '' });

    // Review: choosing a plan makes it a deep link.
    await user.selectOptions(await screen.findByLabelText('Plan history'), planId);
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/plans/${planId}`);
    await user.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Apply to network / dev' });
    await within(dialog).findByText(/Production-like label/);
    await user.click(within(dialog).getByRole('checkbox'));
    await user.type(within(dialog).getByLabelText('Confirmation phrase'), 'apply dev');
    await user.click(within(dialog).getByRole('button', { name: 'Apply plan' }));
    await waitFor(() => expect(calls.some(c => c.url.endsWith('/apply'))).toBe(true));
    expect(calls.filter(c => c.method === 'POST' && c.url !== '/api/v1/contexts').map(c => c.url)).toEqual([
      `/api/v1/contexts/${ctx.dev}/plans`,
      `/api/v1/contexts/${ctx.dev}/plans/${planId}/preflight`,
      `/api/v1/contexts/${ctx.dev}/plans/${planId}/approval`,
      `/api/v1/contexts/${ctx.dev}/plans/${planId}/apply`
    ]);
    expect(launchCalls()).toEqual([]);
    // Nothing asks the CLI to select a workspace.
    expect(calls.some(c => JSON.stringify(c.body || {}).includes('select'))).toBe(false);
  });

  it('restores a deep-linked review on load and clears all A/dev state when switching to prod', async () => {
    app(contextPath(network, 'dev', 'Plans', planId));
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    expect(await screen.findByRole('dialog', { name: 'Apply to network / dev' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.selectOptions(screen.getByLabelText('Workspace'), 'prod');
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/prod/plans`);
    expect(await screen.findByText('No plans yet')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Review and apply…' })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(calls.some(c => c.url.startsWith(`/api/v1/contexts/${ctx.prod}/plans`))).toBe(true);
    // Back returns to the dev review, read again from dev's context.
    window.history.back();
    await screen.findByRole('button', { name: 'Review and apply…' });
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/plans/${planId}`);
    expect(launchCalls()).toEqual([]);
  });

  it('lists a workspace\'s operations and streams their logs from its context', async () => {
    app(contextPath(network, 'dev', 'Operations'));
    const user = userEvent.setup();
    await user.click(within(await screen.findByRole('table')).getByRole('button', { name: /^Plan/ }));
    expect(await screen.findByText('Planning in dev')).toBeInTheDocument();
    expect(calls.some(c => c.url.startsWith(`/api/v1/contexts/${ctx.dev}/operations/${opId}/events`))).toBe(true);
    await user.click(screen.getByRole('button', { name: /Review plan/ }));
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/plans/${planId}`);
    expect(launchCalls()).toEqual([]);
  });

  it('keeps Hub workspaces read-only when the server does not enable scoped execution', async () => {
    gate = false;
    app(contextPath(network, 'dev', 'Plans'));
    expect(await screen.findByText('Hub workspaces are read-only in this session')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Plans' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Run plan' })).not.toBeInTheDocument();
    expect(calls.some(c => /contexts\/[0-9a-f]{32}\/(plans|operations)/.test(c.url))).toBe(false);
    expect(launchCalls()).toEqual([]);
  });

  it('asks to open a closed workspace again instead of acting on another context', async () => {
    override = (path, method) => method === 'GET' && path === `contexts/${ctx.dev}/plans/options` ? Response.json({ error: 'context_closed', reason: 'The project, engine, execution profile, environment or backend of this workspace changed, so it was closed. Open the workspace again.' }, { status: 404 }) : undefined;
    app(contextPath(network, 'dev', 'Plans'));
    expect(await screen.findByRole('heading', { name: 'Open this workspace again' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run plan' })).not.toBeInTheDocument();
    override = undefined;
    await userEvent.setup().click(screen.getByRole('button', { name: 'Open again' }));
    await waitFor(() => expect(calls.some(c => c.url === '/api/v1/contexts' && c.body?.reload === true)).toBe(true));
    expect(await screen.findByRole('button', { name: 'Run plan' })).toBeInTheDocument();
    expect(calls.some(c => c.url.startsWith(`/api/v1/contexts/${ctx.reopened}/plans`))).toBe(true);
    expect(launchCalls()).toEqual([]);
  });

  it('explains a stale plan link and never looks it up elsewhere', async () => {
    const stale = 'd'.repeat(32);
    app(contextPath(network, 'dev', 'Plans', stale));
    expect(await screen.findByText(/This plan is not kept here/)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Show the latest plan' }));
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/plans`);
    expect(calls.filter(c => c.url.includes(stale)).map(c => c.url)).toEqual([`/api/v1/contexts/${ctx.dev}/plans/${stale}`]);
    expect(launchCalls()).toEqual([]);
  });

  it('explains why planning is unavailable for a remote or uninitialized workspace', async () => {
    override = (path) => path === `contexts/${ctx.dev}/plans/options` ? Response.json({ canPlan: false, code: 'execution_unsupported', reason: 'This workspace uses remote execution.', scope: null, backendAccessRequired: true, varFiles: [], executionContext: null, contextFingerprint: '' }) : undefined;
    app(contextPath(network, 'dev', 'Plans'));
    expect(await screen.findByText(/This workspace uses remote execution\..*remote and cloud execution stay read-only/)).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Run plan' })[0]).toBeDisabled();
  });

  it('parses plan and operation deep links strictly', () => {
    expect(parseRoute(`/projects/${network}/workspaces/dev/plans`)).toEqual({ kind: 'context', projectId: network, workspace: 'dev', view: 'Plans' });
    expect(parseRoute(`/projects/${network}/workspaces/dev/plans/${planId}`)).toEqual({ kind: 'context', projectId: network, workspace: 'dev', view: 'Plans', planId });
    expect(parseRoute(`/projects/${network}/workspaces/dev/operations`)).toEqual({ kind: 'context', projectId: network, workspace: 'dev', view: 'Operations' });
    for (const bad of [`plans/${planId}/apply`, 'plans/..', 'plans/XYZ', `operations/${planId}`, `resources/${planId}`]) {
      expect(parseRoute(`/projects/${network}/workspaces/dev/${bad}`)).toEqual({ kind: 'project', projectId: network });
    }
    expect(contextPath(network, 'dev', 'Plans', '../x')).toBe(`/projects/${network}/workspaces/dev/plans`);
    expect(contextPath(network, 'dev', 'Resources', planId)).toBe(`/projects/${network}/workspaces/dev/resources`);
  });
});

// M24.08: import and replace in a Hub workspace go only to that workspace's own context.
describe('Hub import and replace (M24.08)', { timeout: 20000 }, () => {
  const importOptions = (cid: string) => ({ canImport: true, canApply: true, supportsImportBlocks: true, supportsGeneratedConfig: true, engine: 'terraform', version: '1.9.8', workspace: wsOf(cid), backend: 'local', varFiles: [], scope: options(cid).scope, executionContext: null, contextFingerprint: '' });
  const preview = { address: 'aws_s3_bucket.logs', file: 'imports.tf', content: 'import {\n  to = aws_s3_bucket.logs\n  id = "synthetic-logs"\n}\n', sha256: '7'.repeat(64), config: 'found', configFile: 'main.tf', canGenerate: false, issues: [], blocked: false };
  const resource = (n: number) => ({ id: String(n).repeat(32).slice(0, 32), address: `aws_s3_bucket.b${n}`, mode: 'managed', type: 'aws_s3_bucket', name: `b${n}`, provider: 'registry.terraform.io/hashicorp/aws', module: '', attributes: {} });

  it('previews, writes and plans an import in A/dev through its own context only', async () => {
    override = (path, method) => {
      const m = path.match(/^contexts\/([0-9a-f]{32})\/(imports.*)$/);
      if (!m) return undefined;
      if (m[2] === 'imports/options') return Response.json(importOptions(m[1]));
      if (m[2] === 'imports/preview') return Response.json({ ...preview, scope: options(m[1]).scope });
      if (m[2] === 'imports' && method === 'POST') return Response.json({ ...finished, status: 'running', import: { address: preview.address, file: preview.file } }, { status: 202 });
      return undefined;
    };
    app(contextPath(network, 'dev', 'Import'));
    expect(parseRoute(window.location.pathname)).toMatchObject({ kind: 'context', view: 'Import' });
    const user = userEvent.setup();
    // Import & discovery tabs: both run in this workspace's context (M24.09).
    await screen.findByRole('form', { name: 'Existing object' });
    expect(screen.getByRole('navigation', { name: 'Import and discovery' })).toBeInTheDocument();
    await user.type(screen.getByLabelText('Resource instance address'), 'aws_s3_bucket.logs');
    await user.type(screen.getByLabelText('Import ID'), 'synthetic-logs');
    await user.click(screen.getByRole('button', { name: /Check and preview/ }));
    const confirm = await screen.findByRole('region', { name: 'Write the file and plan' });
    const uses = within(confirm).getByLabelText('This import plan will use');
    expect(uses).toHaveTextContent('/infra/network');
    expect(uses).toHaveTextContent('dev');
    expect(confirm).toHaveTextContent('Every workspace of this project plans this import block');
    await user.click(within(confirm).getByRole('checkbox', { name: /Write imports.tf/ }));
    await user.click(within(confirm).getByRole('button', { name: /Write file/ }));
    await waitFor(() => expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/plans`));
    const posts = calls.filter(c => c.method === 'POST' && c.url !== '/api/v1/contexts');
    expect(posts.map(c => c.url)).toEqual([`/api/v1/contexts/${ctx.dev}/imports/preview`, `/api/v1/contexts/${ctx.dev}/imports`]);
    expect(posts[1].body).toEqual({ address: 'aws_s3_bucket.logs', id: 'synthetic-logs', provider: '', file: 'imports.tf', generateConfig: false, varFiles: [], previewSha256: preview.sha256, acknowledgeWrite: true, acknowledgeBackendAccess: true, contextFingerprint: '' });
    expect(calls.some(c => /^\/api\/v1\/(imports|plans\/options)/.test(c.url))).toBe(false);
    expect(launchCalls()).toEqual([]);
  });

  it('a closed context asks to open the workspace again and writes nothing', async () => {
    override = path => path.endsWith('/imports/options') ? Response.json({ error: 'context_closed', reason: 'This workspace view is no longer open.' }, { status: 404 }) : undefined;
    app(contextPath(network, 'dev', 'Import'));
    expect(await screen.findByRole('heading', { name: 'Open this workspace again' })).toBeInTheDocument();
    expect(calls.some(c => c.method === 'POST' && c.url.includes('/imports'))).toBe(false);
  });

  it('plans a replacement of instances chosen in A/dev with their IDs, as a full plan', async () => {
    const page = { resources: [resource(1), resource(2)], total: 2 };
    override = path => /^contexts\/[0-9a-f]{32}\/resources$/.test(path) ? Response.json(page) : undefined;
    app(contextPath(network, 'dev', 'Resources'));
    const user = userEvent.setup();
    await user.click(await screen.findByRole('checkbox', { name: 'Select aws_s3_bucket.b1 for replacement' }));
    await user.click(screen.getByRole('button', { name: 'Plan replacement…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Plan a replacement' });
    expect(dialog).toHaveTextContent('This is a full plan, not a targeted one');
    expect(within(dialog).getByLabelText('This plan will use')).toHaveTextContent('dev');
    await user.click(within(dialog).getByRole('button', { name: 'Plan replacement' }));
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/plans`)).toBe(true));
    expect(calls.find(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/plans`)!.body).toEqual({ acknowledgeBackendAccess: true, varFiles: [], replace: [resource(1).id], contextFingerprint: '' });
    expect(launchCalls()).toEqual([]);
  });

  it('offers no replacement selection while Hub execution is off', async () => {
    gate = false;
    override = path => /^contexts\/[0-9a-f]{32}\/resources$/.test(path) ? Response.json({ resources: [resource(1)], total: 1 }) : undefined;
    app(contextPath(network, 'dev', 'Resources'));
    await screen.findByText('aws_s3_bucket.b1');
    expect(screen.queryByRole('checkbox', { name: /for replacement/ })).toBeNull();
  });
});

describe('Hub drift scans and graphs (M24.09)', { timeout: 20000 }, () => {
  const scan = { ...(driftGolden.scan as unknown as DriftScan), workspace: 'dev' };
  const graph = { ...(graphGolden.graph as unknown as DependencyGraph), workspace: 'dev', plan: undefined };
  const analysisOptions = (cid: string) => ({ engine: 'terraform', version: '1.9.8', workspace: wsOf(cid), backend: 'local', executionMode: 'local-cli', scope: options(cid).scope, executionContext: null, contextFingerprint: '' });
  // A/dev has one scan and one graph; prod has none. Identical resource addresses in both.
  function serveAnalysis() {
    override = (path, method) => {
      const m = path.match(/^contexts\/([0-9a-f]{32})\/((drift-scans|graphs).*)$/);
      if (!m) return undefined;
      const [, cid, rest] = m;
      const dev = wsOf(cid) === 'dev';
      if (rest === 'drift-scans/options') return Response.json({ ...analysisOptions(cid), canScan: true, varFiles: [] });
      if (rest === 'graphs/options') return Response.json({ ...analysisOptions(cid), canGenerate: true, command: ['terraform', 'graph'] });
      if (rest === 'drift-scans' && method === 'GET') return Response.json({ scans: dev ? [scan] : [] });
      if (rest === 'graphs' && method === 'GET') return Response.json({ graphs: dev ? [graph] : [] });
      if (rest === 'drift-scans' && method === 'POST') return Response.json({ operation: { ...finished, kind: 'drift', status: 'running' }, scan }, { status: 202 });
      if (rest === 'graphs' && method === 'POST') return Response.json({ operation: { ...finished, kind: 'graph', status: 'running' }, graph }, { status: 202 });
      if (dev && rest === `drift-scans/${scan.id}`) return Response.json(scan);
      if (dev && rest === `graphs/${graph.id}`) return Response.json(graph);
      return Response.json({ error: rest.startsWith('graphs') ? 'graph_not_found' : 'scan_not_found' }, { status: 404 });
    };
  }
  const launchAnalysis = () => calls.filter(c => /^\/api\/v1\/(drift-scans|graphs|plans|operations)(\/|$|\?)/.test(c.url));

  it('scans drift in A/dev through its own context only and shows nothing of it in prod', async () => {
    serveAnalysis();
    app(contextPath(network, 'dev', 'Drift'));
    const user = userEvent.setup();
    expect(await screen.findByLabelText('Scan history')).toBeInTheDocument();
    // Opening the view runs nothing.
    expect(calls.filter(c => c.method === 'POST' && c.url !== '/api/v1/contexts')).toEqual([]);
    await user.click(screen.getAllByRole('button', { name: 'Check drift' })[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Check drift' });
    const uses = within(dialog).getByLabelText('This scan will use');
    expect(uses).toHaveTextContent('/infra/network');
    expect(uses).toHaveTextContent('dev');
    await user.click(within(dialog).getByRole('button', { name: 'Check drift' }));
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/drift-scans`)).toBe(true));
    expect(calls.find(c => c.method === 'POST' && c.url.endsWith('/drift-scans'))!.body).toEqual({ acknowledgeProviderAccess: true, varFiles: [], contextFingerprint: '' });

    await user.selectOptions(screen.getByLabelText('Workspace'), 'prod');
    expect(window.location.pathname).toBe(`/projects/${network}/workspaces/prod/drift`);
    expect(await screen.findByText('No drift scans yet')).toBeInTheDocument();
    expect(screen.queryByLabelText('Scan history')).not.toBeInTheDocument();
    expect(calls.some(c => c.url.includes(`contexts/${ctx.prod}/drift-scans/${scan.id}`))).toBe(false);
    expect(launchAnalysis()).toEqual([]);
  });

  it('generates and shows a graph of A/dev from its own context, with its own plans only', async () => {
    serveAnalysis();
    vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
    app(contextPath(network, 'dev', 'Graph'));
    const user = userEvent.setup();
    expect(await screen.findByLabelText('Graph history')).toBeInTheDocument();
    await waitFor(() => expect(calls.some(c => c.url === `/api/v1/contexts/${ctx.dev}/graphs/${graph.id}`)).toBe(true));
    expect(calls.some(c => c.url === `/api/v1/contexts/${ctx.dev}/plans`)).toBe(true);
    await user.click(screen.getAllByRole('button', { name: 'Generate graph' })[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Generate dependency graph' });
    expect(within(dialog).getByLabelText('The graph will use')).toHaveTextContent('/infra/network');
    await user.click(within(dialog).getByRole('button', { name: 'Generate graph' }));
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/graphs`)).toBe(true));
    expect(calls.find(c => c.method === 'POST' && c.url.endsWith('/graphs'))!.body).toEqual({ acknowledgeEngineRun: true, contextFingerprint: '' });

    await user.selectOptions(screen.getByLabelText('Workspace'), 'prod');
    expect(await screen.findByText('No dependency graph yet')).toBeInTheDocument();
    expect(launchAnalysis()).toEqual([]);
  });

  it('asks to open a closed workspace again before scanning', async () => {
    serveAnalysis();
    const inner = override!;
    override = (path, method) => path === `contexts/${ctx.dev}/drift-scans/options` ? Response.json({ error: 'context_closed', reason: 'This workspace view is no longer open. Open the workspace again.' }, { status: 404 }) : inner(path, method);
    app(contextPath(network, 'dev', 'Drift'));
    expect(await screen.findByRole('heading', { name: 'Open this workspace again' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Check drift' })).not.toBeInTheDocument();
    expect(launchAnalysis()).toEqual([]);
  });

  it('keeps drift and graph closed in Hub workspaces while scoped execution is off', async () => {
    gate = false;
    serveAnalysis();
    app(contextPath(network, 'dev', 'Drift'));
    expect(await screen.findByText('Hub workspaces are read-only in this session')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Drift detection' })).toBeDisabled();
    expect(calls.some(c => /drift-scans|graphs/.test(c.url))).toBe(false);
  });

  it('parses drift and graph deep links', () => {
    expect(parseRoute(`/projects/${network}/workspaces/dev/drift`)).toEqual({ kind: 'context', projectId: network, workspace: 'dev', view: 'Drift' });
    expect(parseRoute(`/projects/${network}/workspaces/dev/graph`)).toEqual({ kind: 'context', projectId: network, workspace: 'dev', view: 'Graph' });
    expect(parseRoute(`/projects/${network}/workspaces/dev/graph/${planId}`)).toEqual({ kind: 'project', projectId: network });
  });
});

describe('Hub cloud discovery (M24.09)', { timeout: 20000 }, () => {
  const scan = discoveryGolden.scan as unknown as DiscoveryScan;
  const prefill = discoveryGolden.prefill as unknown as DiscoveryPrefill;
  const verified = discoveryGolden.options as unknown as DiscoveryOptions;
  // A/dev was verified and scanned; prod was not. Both are one project and root.
  function serveDiscovery() {
    override = (path, method) => {
      const m = path.match(/^contexts\/([0-9a-f]{32})\/(discovery\/.*|imports.*)$/);
      if (!m) return undefined;
      const [, cid, rest] = m;
      const dev = wsOf(cid) === 'dev';
      if (rest === 'discovery/options') return Response.json({ ...verified, context: undefined, identity: dev ? verified.identity : undefined, workspace: wsOf(cid), scope: options(cid).scope, executionContext: null, contextFingerprint: '' });
      if (rest === 'discovery/scans' && method === 'GET') return Response.json({ scans: dev ? [scan] : [] });
      if (rest === 'discovery/identity') return Response.json(verified.identity);
      if (dev && rest === `discovery/scans/${scan.id}`) return Response.json(scan);
      if (dev && rest.startsWith(`discovery/scans/${scan.id}/candidates/`)) return Response.json(prefill);
      if (rest === 'imports/options') return Response.json({ canImport: true, canApply: true, supportsImportBlocks: true, supportsGeneratedConfig: true, engine: 'terraform', version: '1.9.8', workspace: wsOf(cid), backend: 'local', varFiles: [], scope: options(cid).scope, executionContext: null, contextFingerprint: '' });
      return Response.json({ error: 'scan_not_found' }, { status: 404 });
    };
  }
  const launchDiscovery = () => calls.filter(c => /^\/api\/v1\/(discovery|imports)(\/|$|\?)/.test(c.url));

  it('shows, verifies and prepares in A/dev through its own context only, and nothing of it in prod', async () => {
    serveDiscovery();
    app(contextPath(network, 'dev', 'Discovery'));
    const user = userEvent.setup();
    expect(await screen.findByText('Scan complete')).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Import and discovery' })).toBeInTheDocument();
    // Opening the page contacts nothing.
    expect(calls.filter(c => c.method === 'POST' && c.url !== '/api/v1/contexts')).toEqual([]);
    await user.click(screen.getByText('Execution context'));
    expect(screen.getByLabelText('Discovery uses')).toHaveTextContent('/infra/network');

    await user.click(screen.getByRole('button', { name: 'Verify again' }));
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/discovery/identity`)).toBe(true));
    expect(calls.find(c => c.url.endsWith('/discovery/identity'))!.body).toEqual({ contextFingerprint: '' });

    const row = screen.getByText('hclaro-legacy-logs').closest('tr')!;
    await user.click(within(row).getByRole('button', { name: /Prepare import/ }));
    await waitFor(() => expect(window.location.pathname).toBe(`/projects/${network}/workspaces/dev/import`));
    const prepare = calls.find(c => c.url.includes('/prepare'))!;
    expect(prepare.url).toMatch(new RegExp(`^/api/v1/contexts/${ctx.dev}/discovery/scans/${scan.id}/candidates/[0-9a-f]{32}/prepare$`));
    expect(prepare.body).toEqual({ contextFingerprint: '', acknowledgeUnknownMapping: false });
    expect(await screen.findByText(/From Cloud discovery/)).toBeInTheDocument();
    expect(screen.getByLabelText('Import ID')).toHaveValue(prefill.importId);

    await user.click(screen.getByRole('button', { name: 'Discover cloud resources' }));
    await user.selectOptions(screen.getByLabelText('Workspace'), 'prod');
    // The prepared import is unsaved work of dev's workbench: switching asks first.
    const leave = await screen.findByRole('dialog', { name: 'Leave this project?' });
    expect(leave).toHaveTextContent('An import prepared from cloud discovery');
    await user.click(within(leave).getByRole('button', { name: 'Switch anyway' }));
    await waitFor(() => expect(window.location.pathname).toBe(`/projects/${network}/workspaces/prod/discovery`));
    expect(await screen.findByRole('heading', { name: 'AWS context not verified' })).toBeInTheDocument();
    expect(screen.queryByText('Scan complete')).not.toBeInTheDocument();
    expect(calls.some(c => c.url.includes(`contexts/${ctx.prod}/discovery/scans/${scan.id}`))).toBe(false);
    expect(launchDiscovery()).toEqual([]);
  });

  it('sends the discovery link with a Hub import', async () => {
    serveDiscovery();
    const inner = override!;
    const importPreview = { address: prefill.suggestedAddress, file: 'imports.tf', content: 'import {}\n', sha256: '7'.repeat(64), config: 'found', configFile: 'main.tf', canGenerate: false, issues: [], blocked: false };
    override = (path, method) => path === `contexts/${ctx.dev}/imports/preview` ? Response.json({ ...importPreview, scope: options(ctx.dev).scope })
      : path === `contexts/${ctx.dev}/imports` && method === 'POST' ? Response.json({ ...finished, status: 'running' }, { status: 202 }) : inner(path, method);
    app(contextPath(network, 'dev', 'Discovery'));
    const user = userEvent.setup();
    await screen.findByText('Scan complete');
    await user.click(within(screen.getByText('hclaro-legacy-logs').closest('tr')!).getByRole('button', { name: /Prepare import/ }));
    await user.click(await screen.findByRole('button', { name: /Check and preview/ }));
    const confirm = await screen.findByRole('region', { name: 'Write the file and plan' });
    await user.click(within(confirm).getByRole('checkbox', { name: /Write imports.tf/ }));
    await user.click(within(confirm).getByRole('button', { name: /Write file/ }));
    await waitFor(() => expect(calls.some(c => c.method === 'POST' && c.url === `/api/v1/contexts/${ctx.dev}/imports`)).toBe(true));
    expect(calls.find(c => c.method === 'POST' && c.url.endsWith('/imports'))!.body).toMatchObject({ id: prefill.importId, discoveryToken: prefill.token, contextFingerprint: '' });
    expect(calls.find(c => c.method === 'POST' && c.url.endsWith('/imports'))!.body).not.toHaveProperty('profileId');
    expect(launchDiscovery()).toEqual([]);
  });

  it('parses discovery deep links', () => {
    expect(parseRoute(`/projects/${network}/workspaces/dev/discovery`)).toEqual({ kind: 'context', projectId: network, workspace: 'dev', view: 'Discovery' });
  });
});
