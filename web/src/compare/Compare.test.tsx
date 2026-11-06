import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden output keeps the frontend honest about the contract.
import golden from '../../../fixtures/compare/dev-prod.golden.json';
import { CompareView } from './CompareView';
import type { Comparison, ComparisonDetail, ComparisonMatch, Project } from '../types';

const comparison = golden.comparison as unknown as Comparison;
const page = golden.page as unknown as { matches: ComparisonMatch[]; total: number };
const details: Record<string, ComparisonDetail> = Object.fromEntries([golden.bucket, golden.database, golden.web].map(d => [d.id, d as unknown as ComparisonDetail]));
const projectId = 'a'.repeat(32);
const project: Project = { id: projectId, alias: 'Infra', rootPath: '/synthetic/network', enginePreference: '', pinned: false, addedAt: '2026-11-06T09:00:00Z', status: 'ready', initialized: true, backend: 'local', executionMode: 'local-cli', providerLock: false, launch: false, inspection: { engine: 'terraform', version: '1.9.8', workspaces: ['default', 'dev', 'prod'], cliDefault: 'dev', hiddenWorkspaces: 0, inspectedAt: '2026-11-06T09:00:00Z' }, workspaceActions: { change: true, delete: true } };
const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';

type Route = (url: string, init?: RequestInit) => Response | undefined;
let posts: { url: string; body: unknown }[] = [];
let gets: string[] = [];
function serve(...routes: Route[]) {
  posts = [];
  gets = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    else gets.push(url);
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/api/v1/projects')) return Response.json({ projects: [project], registry: { status: 'ready', notice: '', path: '' }, environment: { dataDir: '.terraform', dataDirShared: false, workspaceInherited: false, cliArgsInherited: false } });
    if (url.includes('/execution-profiles')) return Response.json({ profiles: [], defaultId: '', status: 'ready' });
    if (url.includes('/comparisons/target')) return Response.json({ project, open: false });
    if (url.endsWith('/api/v1/comparisons') && init?.method === 'POST') return Response.json(comparison, { status: 202 });
    if (url.endsWith('/api/v1/comparisons')) return Response.json({ comparisons: [comparison] });
    if (url.includes(`/comparisons/${comparison.id}/resources/`)) {
      const id = url.split('/').at(-1)!;
      return details[id] ? Response.json(details[id]) : Response.json({ error: 'match_not_found' }, { status: 404 });
    }
    if (url.includes(`/comparisons/${comparison.id}/resources?`)) {
      const status = new URL(url, 'http://x').searchParams.get('status');
      const matches = status ? page.matches.filter(m => status.split(',').includes(m.status)) : page.matches;
      return Response.json({ matches, total: matches.length });
    }
    if (url.endsWith(`/comparisons/${comparison.id}`)) return Response.json(comparison);
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view() {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><CompareView copy={async () => {}} /></QueryClientProvider>);
}
function seed(withComparison = true) {
  sessionStorage.setItem('hclaro.compare.draft', JSON.stringify({ left: { projectId, workspace: 'dev' }, right: { projectId, workspace: 'prod' }, rules: [] }));
  if (withComparison) sessionStorage.setItem('hclaro.compare.id', comparison.id);
}
beforeEach(() => {
  sessionStorage.clear();
  serve();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe('Compare workspaces', () => {
  it('reads nothing until both targets are chosen and the preflight is confirmed', async () => {
    const user = userEvent.setup();
    view();
    const compare = screen.getByRole('button', { name: /Compare…/ });
    expect(compare).toBeDisabled();
    await screen.findAllByRole('option', { name: 'Infra' });
    await user.selectOptions(screen.getByRole('combobox', { name: 'Left project' }), projectId);
    await user.type(screen.getByRole('combobox', { name: 'Left workspace' }), 'dev');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Right project' }), projectId);
    await user.type(screen.getByRole('combobox', { name: 'Right workspace' }), 'dev');
    expect(compare).toBeDisabled();
    expect(screen.getByText(/Choose a different workspace, project or profile/)).toBeInTheDocument();
    await user.clear(screen.getByRole('combobox', { name: 'Right workspace' }));
    await user.type(screen.getByRole('combobox', { name: 'Right workspace' }), 'prod');
    expect(await screen.findAllByText('Not read in this session')).toHaveLength(2);
    expect(screen.getAllByText('Production-like name')).toHaveLength(1);
    expect(posts).toHaveLength(0);
    await user.click(compare);
    const dialog = screen.getByRole('dialog', { name: 'Compare workspaces' });
    expect(dialog).toHaveTextContent('Infra / dev (left) with Infra / prod (right)');
    expect(dialog).toHaveTextContent('Does not plan, refresh, select a workspace or write state.');
    expect(posts).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: 'Compare' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({ left: { projectId, workspace: 'dev' }, right: { projectId, workspace: 'prod' }, rules: [], reload: false, acknowledgeStateRead: true });
    expect(await screen.findByRole('group', { name: 'Filter by comparison status' })).toBeInTheDocument();
  });

  it('shows target identity, counts, the drift caveat and redacted differences', async () => {
    const user = userEvent.setup();
    seed();
    view();
    const cards = await screen.findByRole('group', { name: 'Filter by comparison status' });
    expect(within(cards).getByRole('button', { name: 'Attribute differences: 4' })).toBeInTheDocument();
    expect(within(cards).getByRole('button', { name: 'Only in right: 2' })).toBeInTheDocument();
    expect(within(cards).getByRole('button', { name: 'Unknown: 1' })).toBeInTheDocument();
    expect(screen.getByText('Different is not necessarily drift')).toBeInTheDocument();
    expect(screen.getByText(/Not live cloud inventory/)).toBeInTheDocument();
    expect(screen.getByText(/matched no left address/)).toBeInTheDocument();
    expect(screen.getByLabelText('Rule matches')).toHaveTextContent('Ignore region1 value');
    const table = await screen.findByRole('table');
    expect(within(table).getAllByRole('row')).toHaveLength(page.matches.length + 1);
    await user.click(within(table).getByRole('button', { name: /aws_db_instance\.main/ }));
    const drawer = await screen.findByRole('complementary', { name: 'Comparison details' });
    expect(await within(drawer).findAllByText('redacted · not compared')).toHaveLength(3);
    expect(drawer).toHaveTextContent('Equality cannot be confirmed');
    expect(document.body.textContent).not.toContain(canary);
  });

  it('filters by status, shows ignored and normalized rows and deep-links to each side', async () => {
    const user = userEvent.setup();
    const open = vi.fn();
    vi.stubGlobal('open', open);
    seed();
    view();
    const cards = await screen.findByRole('group', { name: 'Filter by comparison status' });
    await user.click(within(cards).getByRole('button', { name: 'Same: 4' }));
    await waitFor(() => expect(gets.some(g => g.includes('status=same'))).toBe(true));
    const table = await screen.findByRole('table');
    await waitFor(() => expect(within(table).getAllByRole('row')).toHaveLength(5));
    await user.click(within(table).getByRole('button', { name: /aws_s3_bucket\.logs/ }));
    const drawer = await screen.findByRole('complementary', { name: 'Comparison details' });
    expect(await within(drawer).findByText('ignored by rule 2')).toBeInTheDocument();
    expect(within(drawer).getAllByText('equal after rule 1')).toHaveLength(3);
    expect(within(within(drawer).getByRole('table')).queryByText('force_destroy')).not.toBeInTheDocument();
    await user.click(within(drawer).getByRole('checkbox', { name: /Show 1 equal/ }));
    expect(within(within(drawer).getByRole('table')).getByText('force_destroy')).toBeInTheDocument();
    await user.click(within(drawer).getByRole('button', { name: 'Open in right workspace' }));
    expect(open).toHaveBeenCalledWith(expect.stringMatching(new RegExp(`^/projects/${projectId}/workspaces/prod/resources\\?resource=[0-9a-f]{32}&profile=none#session=`)), '_blank', 'noopener');
  });

  it('never presents an unavailable side as empty', async () => {
    seed();
    const partial: Comparison = { ...comparison, status: 'partial', withheld: true, summary: undefined, modules: [], providers: [], right: { ...comparison.right, status: 'error', error: { code: 'workspace_missing', message: 'This workspace does not exist in the project.' } } };
    serve(url => url.endsWith(`/comparisons/${comparison.id}`) ? Response.json(partial) : undefined);
    view();
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Only one side could be read');
    expect(alert).toHaveTextContent('Right: This workspace does not exist in the project.');
    expect(alert).toHaveTextContent('never treated as an empty one');
    expect(screen.queryByRole('group', { name: 'Filter by comparison status' })).not.toBeInTheDocument();
    expect(gets.some(g => g.includes('/resources'))).toBe(false);
  });

  it('withholds a stale comparison and offers to compare again', async () => {
    seed();
    const stale: Comparison = { ...comparison, status: 'stale', current: false, withheld: true, staleReason: 'The prod workspace of Infra was read again.', summary: undefined };
    serve(url => url.endsWith(`/comparisons/${comparison.id}`) ? Response.json(stale) : undefined);
    view();
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('This comparison no longer matches. The prod workspace of Infra was read again.');
    expect(within(alert).getByRole('button', { name: /Compare again/ })).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('sends explicit rules and shows the rule the server refused', async () => {
    const user = userEvent.setup();
    seed(false);
    serve((url, init) => url.endsWith('/api/v1/comparisons') && init?.method === 'POST' ? Response.json({ error: 'invalid_rule', index: 1, reason: 'Rule 2: This value looks like a credential. Rules may not contain secrets.' }, { status: 400 }) : undefined);
    view();
    await user.click(await screen.findByText('Matching and ignore rules'));
    await user.click(screen.getByRole('button', { name: /Map renamed address/ }));
    await user.type(screen.getByRole('textbox', { name: 'Rule 1 left address' }), 'module.app');
    await user.type(screen.getByRole('textbox', { name: 'Rule 1 right address' }), 'module.application');
    await user.click(screen.getByRole('button', { name: /Ignore attribute/ }));
    await user.type(screen.getByRole('textbox', { name: 'Rule 2 attribute path' }), 'tags.Owner');
    expect(screen.getByText(/can hide meaningful divergence/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Compare…/ }));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Compare' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect((posts[0].body as { rules: unknown }).rules).toEqual([{ kind: 'map', left: 'module.app', right: 'module.application' }, { kind: 'ignore', path: 'tags.Owner' }]);
    expect(await within(screen.getByRole('dialog')).findByRole('alert')).toHaveTextContent('Rule 2: This value looks like a credential');
    expect(screen.getAllByRole('listitem').find(li => li.classList.contains('rule-invalid'))).toHaveTextContent('2');
  });

  it('keeps each comparison separate when switching', async () => {
    const user = userEvent.setup();
    seed();
    const otherId = 'd'.repeat(32);
    const other: Comparison = { ...comparison, id: otherId, left: { ...comparison.left, target: { ...comparison.left.target, workspace: 'default' } } };
    serve(url => {
      if (url.endsWith('/api/v1/comparisons')) return Response.json({ comparisons: [other, comparison] });
      if (url.endsWith(`/comparisons/${otherId}`)) return Response.json(other);
      if (url.includes(`/comparisons/${otherId}/resources?`)) return Response.json({ matches: [], total: 0 });
      return undefined;
    });
    view();
    const table = await screen.findByRole('table');
    await user.click(within(table).getByRole('button', { name: /aws_db_instance\.main/ }));
    expect(await screen.findByRole('complementary', { name: 'Comparison details' })).toBeInTheDocument();
    await user.selectOptions(screen.getByRole('combobox', { name: 'Comparison history' }), otherId);
    expect(await screen.findByText('Neither side records resources')).toBeInTheDocument();
    expect(screen.queryByRole('complementary', { name: 'Comparison details' })).not.toBeInTheDocument();
    expect(gets.some(g => g.includes(`/comparisons/${otherId}/resources/`))).toBe(false);
  });
});
