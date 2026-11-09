import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import { PlanReview } from '../plans/PlanReview';
import type { GitContext, GitDiff, Plan, Project, ProjectList, Resource, SourceReference } from '../types';

const id = 'a'.repeat(32);
const cid = 'c'.repeat(32);
const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const commit = '0123456789abcdef0123456789abcdef01234567';
const network: Project = {
  id, alias: 'network', rootPath: '/infra/network', enginePreference: 'terraform', pinned: false, addedAt: '2026-11-05T10:00:00Z',
  status: 'ready', initialized: true, backend: 'local', executionMode: 'local-cli', providerLock: true, launch: false, lastWorkspace: 'dev',
  workspaceActions: { change: true, delete: true },
  inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev'], cliDefault: 'dev', hiddenWorkspaces: 0, inspectedAt: '2026-11-05T12:00:00Z' }
};
const list: ProjectList = {
  projects: [network],
  registry: { status: 'ready', notice: '', path: '/config/projects.json' },
  environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: false, cliArgsInherited: false }
};
const mainID = '1'.repeat(32);
const varsID = '2'.repeat(32);
const status: GitContext = {
  state: 'repository',
  repository: { name: 'infra', projectPath: 'network', worktree: false, localModules: 1 },
  head: { commit, branch: 'feature/vpc', detached: false, unborn: false, worktree: false },
  clean: false,
  files: [
    { id: mainID, path: 'network/main.tf', place: 'root', kind: 'hcl', status: 'modified', staged: false, unstaged: true, diffable: true },
    { id: varsID, path: 'network/prod.tfvars', place: 'root', kind: 'vars', status: 'modified', staged: false, unstaged: true, diffable: false, reason: 'Variable files can hold secrets; their contents are never shown.' }
  ],
  counts: { changed: 2, untracked: 0, outside: 1, other: 3 },
  warnings: [{ code: 'outside_changes', message: 'IaC files changed elsewhere in the repository.' }],
  incomplete: false,
  checkedAt: '2026-11-09T10:00:00Z'
};
const diff: GitDiff = {
  file: status.files[0], base: 'HEAD', added: 2, removed: 1, redacted: 1, truncated: false, binary: false,
  hunks: [{ oldStart: 3, oldLines: 2, newStart: 3, newLines: 3, section: 'resource "aws_vpc" "main" {', lines: [
    { kind: 'context', old: 3, new: 3, text: '  cidr_block = "10.0.0.0/16"' },
    { kind: 'delete', old: 4, text: '  enable_dns = false' },
    { kind: 'add', new: 4, text: '  enable_dns = true' },
    { kind: 'add', new: 5, text: '  password = "[redacted]"', redacted: true }
  ] }]
};

type Call = { path: string; method: string; body?: Record<string, unknown>; headers: Record<string, string> };
let calls: Call[];
function stub(git: Partial<GitContext> = {}, head?: GitContext) {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const method = init.method || 'GET';
    const path = url.replace('/api/v1/', '').split('?')[0];
    const body = init.body ? JSON.parse(init.body as string) : undefined;
    calls.push({ path, method, body, headers: (init.headers || {}) as Record<string, string> });
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'projects') return Response.json(list);
    if (path === 'operations') return Response.json({ operations: [] });
    if (path === `projects/${id}/execution-profiles`) return Response.json({ profiles: [], status: 'ready' });
    if (path === 'contexts' && method === 'POST') return Response.json({ id: cid, projectId: id, alias: 'network', rootPath: '/infra/network', engine: 'terraform', workspace: 'dev', dataDir: '.terraform', createdAt: '2026-11-05T12:00:00Z', reused: false });
    if (path === `contexts/${cid}`) return Response.json({ status: 'ready', info: { project: 'network', engine: 'terraform', version: '1.9.8', workspace: 'dev', backend: 'local', warnings: [], capabilities: {} }, managed: 0, data: 0, modules: [], providers: {}, types: {} });
    if (path === `projects/${id}/git/head`) return Response.json(head || { ...status, files: [], clean: false });
    if (path === `projects/${id}/git/status` && method === 'POST') return Response.json({ ...status, ...git });
    if (path === `projects/${id}/git/diff` && method === 'POST') return Response.json(diff);
    if (path === `projects/${id}/editor/open` && method === 'POST') return Response.json({ editor: body.editor, path: 'network/main.tf', line: body.line });
    return Response.json({}, { status: 404 });
  }));
}
function app(path = `/projects/${id}/workspaces/dev/git`) {
  window.history.pushState(null, '', path);
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
  window.history.pushState(null, '', '/');
});

describe('Git changes', () => {
  it('shows branch, HEAD and changed IaC files, and diffs only an explicitly selected .tf file', async () => {
    stub();
    app();
    expect(await screen.findByRole('heading', { name: 'Git changes' })).toBeInTheDocument();
    const summary = await screen.findByRole('region', { name: 'Repository' });
    expect(summary).toHaveTextContent('infra');
    expect(summary).toHaveTextContent('feature/vpc');
    expect(summary).toHaveTextContent('0123456');
    expect(summary).toHaveTextContent('2 changed files');
    expect(screen.getByText(/IaC files changed elsewhere/)).toBeInTheDocument();
    // Status is one POST with an empty body; nothing is diffed until a file is chosen.
    const statusCalls = calls.filter(c => c.path.endsWith('git/status'));
    expect(statusCalls).toHaveLength(1);
    expect(statusCalls[0].body).toEqual({});
    expect(statusCalls[0].headers['X-Hclaro-CSRF']).toBe('c'.repeat(64));
    expect(calls.some(c => c.path.endsWith('git/diff'))).toBe(false);
    const user = userEvent.setup();
    // A variable file shows why it is never displayed and is not requested.
    await user.click(screen.getByRole('button', { name: /network\/prod\.tfvars/ }));
    const drawer = await screen.findByLabelText('Diff inspector');
    expect(drawer).toHaveTextContent('never shown');
    expect(calls.some(c => c.path.endsWith('git/diff'))).toBe(false);
    await user.click(screen.getByRole('button', { name: /network\/main\.tf/ }));
    await waitFor(() => expect(calls.filter(c => c.path.endsWith('git/diff'))).toHaveLength(1));
    const diffCall = calls.find(c => c.path.endsWith('git/diff'))!;
    expect(diffCall.body).toEqual({ fileId: mainID });
    const inspector = screen.getByLabelText('Diff inspector');
    await waitFor(() => expect(inspector).toHaveTextContent('Added: enable_dns = true'));
    expect(inspector).toHaveTextContent('password = "[redacted]"');
    expect(within(inspector).getByText(/1 line redacted/)).toBeInTheDocument();
    expect(within(inspector).getByText(/Only a plan shows which resources actually change/)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(canary);
    // Open in editor sends only the opaque ID, a line and an editor name.
    await user.click(within(inspector).getByRole('button', { name: 'Open in VS Code' }));
    await waitFor(() => expect(calls.some(c => c.path.endsWith('editor/open'))).toBe(true));
    const open = calls.find(c => c.path.endsWith('editor/open'))!;
    expect(open.body).toEqual({ fileId: mainID, line: 3, editor: 'vscode' });
    expect(await screen.findByRole('status')).toHaveTextContent('Asked VS Code to open network/main.tf:3');
    // No request ever carries a path or a Git argument.
    for (const c of calls) expect(JSON.stringify(c.body || {})).not.toMatch(/infra\/|\/etc|checkout|commit|push/);
  }, 15000);

  it('shows the Git badge in the context bar and opens the view from it', async () => {
    stub();
    app(`/projects/${id}/workspaces/dev/resources`);
    const badge = await screen.findByRole('button', { name: /Git feature\/vpc @ 0123456/ });
    expect(calls.some(c => c.path.endsWith('git/status'))).toBe(false);
    await userEvent.setup().click(badge);
    expect(await screen.findByRole('heading', { name: 'Git changes' })).toBeInTheDocument();
    expect(window.location.pathname).toBe(`/projects/${id}/workspaces/dev/git`);
  });

  it('warns about a detached HEAD and untracked IaC files, and explains a folder outside Git', async () => {
    stub({ head: { commit, detached: true, unborn: false, worktree: true }, warnings: [{ code: 'detached_head', message: 'HEAD is detached: plans are made from a commit that is not on a branch.' }, { code: 'untracked_iac', message: 'Untracked IaC files are part of the configuration the engine reads, but they are not in Git.' }] });
    app();
    expect(await screen.findByText(/HEAD is detached/)).toBeInTheDocument();
    expect(screen.getByText(/Untracked IaC files/)).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Repository' })).toHaveTextContent('Detached HEAD');
    cleanup();
    stub({ state: 'not_repository', message: 'This project is not inside a Git repository.', head: undefined, repository: undefined, files: [] }, { state: 'not_repository', message: 'This project is not inside a Git repository.', clean: false, files: [], counts: { changed: 0, untracked: 0, outside: 0, other: 0 }, warnings: [], incomplete: false, checkedAt: '' });
    app();
    expect(await screen.findByRole('heading', { name: 'Not a Git repository' })).toBeInTheDocument();
    expect(screen.getByText('No Git')).toBeInTheDocument();
  });

  it('uses the editor chosen in Settings', async () => {
    localStorage.setItem('hclaro.preferences', JSON.stringify({ editor: 'cursor' }));
    stub();
    app();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /network\/main\.tf/ }));
    await user.click(await screen.findByRole('button', { name: 'Open in Cursor' }));
    await waitFor(() => expect(calls.find(c => c.path.endsWith('editor/open'))?.body).toMatchObject({ editor: 'cursor' }));
  });
});

describe('Resource source links', () => {
  it('locates a resource best-effort and opens it in the editor', async () => {
    const resource: Resource = { id: 'r'.repeat(32), address: 'aws_s3_bucket.logs', mode: 'managed', type: 'aws_s3_bucket', name: 'logs', provider: 'registry.terraform.io/hashicorp/aws', module: '', attributes: { bucket: 'logs' } };
    const ref: SourceReference = { state: 'found', confidence: 'block', fileId: mainID, path: 'network/main.tf', line: 12, blockType: 'resource', reason: 'Best effort: matched the block header textually.' };
    calls = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
      const method = init.method || 'GET';
      const path = url.replace('/api/v1/', '').split('?')[0];
      const body = init.body ? JSON.parse(init.body as string) : undefined;
      calls.push({ path, method, body, headers: (init.headers || {}) as Record<string, string> });
      if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
      if (path === 'projects') return Response.json(list);
      if (path === 'operations') return Response.json({ operations: [] });
      if (path === `projects/${id}/execution-profiles`) return Response.json({ profiles: [], status: 'ready' });
      if (path === 'contexts') return Response.json({ id: cid, projectId: id, alias: 'network', rootPath: '/infra/network', engine: 'terraform', workspace: 'dev', dataDir: '.terraform', createdAt: '2026-11-05T12:00:00Z', reused: false });
      if (path === `contexts/${cid}`) return Response.json({ status: 'ready', capturedAt: '2026-11-05T12:00:00Z', info: { project: 'network', engine: 'terraform', version: '1.9.8', workspace: 'dev', backend: 'local', warnings: [], capabilities: {} }, managed: 1, data: 0, modules: [{ address: '', parent: '', count: 1 }], providers: { [resource.provider]: 1 }, types: { aws_s3_bucket: 1 } });
      if (path === `contexts/${cid}/resources`) return Response.json({ resources: [resource], total: 1 });
      if (path === `contexts/${cid}/resources/${resource.id}`) return Response.json(resource);
      if (path === `contexts/${cid}/resources/${resource.id}/source`) return Response.json(ref);
      if (path === `projects/${id}/git/head`) return Response.json({ ...status, files: [] });
      if (path === `projects/${id}/editor/open`) return Response.json({ editor: 'vscode', path: 'network/main.tf', line: 12 });
      return Response.json({}, { status: 404 });
    }));
    app(`/projects/${id}/workspaces/dev/resources`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /logs.*aws_s3_bucket/ }));
    const inspector = await screen.findByLabelText('Resource inspector');
    expect(await within(inspector).findByText('network/main.tf:12')).toBeInTheDocument();
    expect(within(inspector).getByText('Best effort')).toBeInTheDocument();
    await user.click(within(inspector).getByRole('button', { name: 'Open in VS Code' }));
    await waitFor(() => expect(calls.find(c => c.path === `projects/${id}/editor/open`)?.body).toEqual({ fileId: mainID, line: 12, editor: 'vscode' }));
  });
});

describe('Plan source revision', () => {
  it('warns when a plan was generated from a different source revision', () => {
    const plan = {
      id: 'p'.repeat(32), operationId: 'o'.repeat(32), createdAt: '2026-11-09T09:00:00Z', engine: 'terraform', engineVersion: '1.9.8', currentEngineVersion: '1.9.8', workspace: 'demo', varFiles: [], formatVersion: '1.2', errored: false,
      summary: { update: 1 }, changes: [], drift: [], outputs: [], warnings: [], kind: 'plan', replace: [], imports: 0, destructive: false, artifactSha256: 'a'.repeat(64), fingerprint: 'f'.repeat(64), contextChanged: true,
      apply: { available: false, artifactRetained: true, reason: 'Plan generated from different source files. Run a new plan.' },
      source: { repository: true, commit, branch: 'main', detached: false, dirty: true, dirtyKnown: true, dirtyFiles: 2, contentSha256: 'c'.repeat(64), files: 4, localModules: 1, recordedAt: '2026-11-09T09:00:00Z' },
      sourceStatus: { state: 'changed', headChanged: false, filesChanged: true, message: 'Plan generated from different source files: IaC files in the project or its local modules changed since the plan.' }
    } as unknown as Plan;
    render(<QueryClientProvider client={new QueryClient()}><PlanReview plan={plan} copy={async () => {}} /></QueryClientProvider>);
    expect(screen.getByText(/main @ 0123456 · 2 uncommitted IaC files/)).toBeInTheDocument();
    const alerts = screen.getAllByRole('alert').map(a => a.textContent).join(' ');
    expect(alerts).toMatch(/different source files.*Run a new plan before applying/);
    expect(screen.queryByRole('button', { name: /Review and apply/ })).not.toBeInTheDocument();
  });
});
