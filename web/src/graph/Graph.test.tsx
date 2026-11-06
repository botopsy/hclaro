import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden output keeps the frontend honest about the contract.
import golden from '../../../fixtures/graph/terraform.golden.json';
import { GraphView } from './GraphView';
import type { DependencyGraph, GraphImpact, GraphNode, GraphOptions, Operation, PlanSummary } from '../types';

const withPlan = golden.graph as unknown as DependencyGraph;
const planId = golden.planId;
// The same graph without a plan: no overlay and no planned changes.
const plain: DependencyGraph = { ...withPlan, plan: undefined, nodes: withPlan.nodes.map(n => ({ ...n, planned: undefined })) };
const impactWithPlan = golden.impact as unknown as GraphImpact;
const { plan: _plan, ...impactPlain } = impactWithPlan;
void _plan;
const options: GraphOptions = { canGenerate: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 's3', executionMode: 'local-cli', command: ['terraform', 'graph'] };
const plans: PlanSummary[] = [{ id: planId, operationId: 'b'.repeat(32), createdAt: '2026-11-06T09:59:00Z', engine: 'terraform', workspace: 'demo', varFiles: [], kind: 'plan', replace: [], summary: {}, errored: false, changes: 11, outputs: 0, drift: 0 }];
const finished: Operation = { id: withPlan.operationId, kind: 'graph', status: 'succeeded', stage: 'Finished', startedAt: withPlan.startedAt, durationMs: 900, exitCode: 0, command: ['terraform', 'graph'], varFiles: [], replace: [], stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true, graphId: withPlan.id };
const sg = withPlan.nodes.find(n => n.displayName === 'aws_security_group.web')!;

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
    if (url.endsWith('/graphs/options')) return Response.json(options);
    if (url.endsWith('/graphs')) return Response.json({ graphs: [{ ...plain, nodes: [], edges: [], resourceEdges: [] }] });
    if (url.endsWith('/plans')) return Response.json({ plans });
    if (url.endsWith('/operations')) return Response.json({ operations: [finished] });
    if (url.endsWith(`/graphs/${withPlan.id}?planId=${planId}`)) return Response.json(withPlan);
    if (url.endsWith(`/graphs/${withPlan.id}`)) return Response.json(plain);
    if (url.endsWith(`/nodes/${sg.id}?planId=${planId}`)) return Response.json(impactWithPlan);
    if (url.includes('/nodes/')) return Response.json(impactPlain);
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view(focus?: { planId: string; address: string }) {
  const openResource = vi.fn();
  const openPlan = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><GraphView project="platform-infra" focus={focus} openResource={openResource} openPlan={openPlan} copy={async () => {}} /></QueryClientProvider>);
  return { openResource, openPlan };
}
beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  serve();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function node(name: string) {
  return screen.getByRole('button', { name: new RegExp(`^(Resource|Data source) ${name.replace(/[.[\]"]/g, '\\$&')}(,|$)`) });
}

describe('Graph', () => {
  it('runs nothing on load and draws the latest graph without claiming planned changes', async () => {
    view();
    expect(await screen.findByRole('group', { name: 'Graph summary' })).toHaveTextContent('17 resources');
    expect(posts).toHaveLength(0);
    expect(node('aws_security_group.web')).toBeInTheDocument();
    expect(screen.getByText(/No plan selected: the graph shows dependencies only/)).toBeInTheDocument();
    expect(document.querySelectorAll('.graph-node.planned')).toHaveLength(0);
    // Edges are drawn with an arrow toward the dependency; the legend names both link kinds.
    expect(document.querySelectorAll('path.graph-edge').length).toBe(withPlan.resourceEdges.length);
    const legend = screen.getByLabelText('Legend');
    expect(legend).toHaveTextContent('Depends on (declared by the engine)');
    expect(legend).toHaveTextContent('Derived through internal nodes');
  });

  it('shows potential impact as reachability only until a plan is chosen', async () => {
    view();
    const user = userEvent.setup();
    await screen.findByRole('group', { name: 'Graph summary' });
    await user.click(node('aws_security_group.web'));
    const inspector = await screen.findByRole('complementary', { name: 'Node details' });
    const impact = await within(inspector).findByRole('region', { name: 'Potential impact' });
    expect(impact).toHaveTextContent('Potential impact 6 resources');
    expect(impact).toHaveTextContent('it does not mean they will change');
    expect(within(impact).getByRole('button', { name: 'module.compute.module.workers.aws_instance.node' }).closest('li')).toHaveTextContent('2 hops');
    expect(within(inspector).queryByRole('region', { name: 'Confirmed planned changes' })).not.toBeInTheDocument();
    expect(inspector).toHaveTextContent('Choose a plan to compare');
    expect(inspector).toHaveTextContent('A resource in configuration with no instance recorded');
    // Highlighting is marked with text, not only color.
    expect(document.querySelector('.graph-node.impact')).toHaveTextContent('◆ impact');
    expect(document.querySelector('.graph-node.upstream')).toHaveTextContent('▲ dependency');

    await user.selectOptions(screen.getByLabelText('Compare with plan'), planId);
    expect(await screen.findByText(/Showing/)).toHaveTextContent('Showing 11 confirmed planned changes');
    await user.click(node('aws_security_group.web'));
    const confirmed = await screen.findByRole('region', { name: 'Confirmed planned changes' });
    expect(confirmed).toHaveTextContent('a dependency link does not show that one change causes another');
    expect(confirmed).toHaveTextContent('Here and in its potential impact 6');
    expect(confirmed).toHaveTextContent('aws_s3_bucket.logs');
    expect(posts).toHaveLength(0);
  });

  it('links instances to Resources and plans to Plans', async () => {
    const { openResource, openPlan } = view();
    const user = userEvent.setup();
    await screen.findByRole('group', { name: 'Graph summary' });
    await user.click(node('module.network.aws_subnet.main'));
    const inspector = await screen.findByRole('complementary', { name: 'Node details' });
    expect(inspector).toHaveTextContent('and 27 more');
    await user.click(within(inspector).getByRole('button', { name: 'module.network.aws_subnet.main[0]' }));
    const subnet = withPlan.nodes.find(n => n.displayName === 'module.network.aws_subnet.main')!;
    expect(openResource).toHaveBeenCalledWith(subnet.instances![0].resourceId);
    await user.selectOptions(screen.getByLabelText('Compare with plan'), planId);
    await user.click(await screen.findByRole('button', { name: /View in Plans/ }));
    expect(openPlan).toHaveBeenCalledWith(planId);
  });

  it('filters, searches, collapses modules and offers an accessible list', async () => {
    view();
    const user = userEvent.setup();
    await screen.findByRole('group', { name: 'Graph summary' });
    await user.click(screen.getByLabelText('Data sources'));
    expect(screen.queryByRole('button', { name: /^Data source/ })).not.toBeInTheDocument();
    await user.click(screen.getByLabelText('Data sources'));
    await user.selectOptions(screen.getByLabelText(/Type/), 'aws_instance');
    expect(screen.getAllByRole('button', { name: /^Resource / })).toHaveLength(3);
    await user.selectOptions(screen.getByLabelText(/Type/), '');
    await user.click(screen.getByRole('checkbox', { name: 'compute' }));
    expect(screen.getByRole('button', { name: /Collapsed module module\.compute \(2\)/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Resource module\.compute\.aws_instance\.main/ })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText('Find a node'), 'iam{Enter}');
    expect(screen.getByText(/2 matches/)).toBeInTheDocument();
    expect(await screen.findByRole('complementary', { name: 'Node details' })).toHaveTextContent('aws_iam_role');
    await user.click(screen.getByRole('tab', { name: 'List' }));
    const list = screen.getByRole('list', { name: /graph nodes/ });
    expect(within(list).getAllByRole('listitem').length).toBe(2);
  });

  it('generates only after the preflight and posts nothing else', async () => {
    serve((url, init) => init?.method === 'POST' && url.endsWith('/graphs') ? Response.json({ operation: { ...finished, status: 'running' }, graph: { ...plain, status: 'running' } }, { status: 202 }) : undefined);
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Generate graph' }));
    const dialog = await screen.findByRole('dialog', { name: 'Generate dependency graph' });
    expect(dialog).toHaveTextContent('terraform graph');
    expect(dialog).toHaveTextContent('Does not plan, refresh, apply or write state');
    expect(posts).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: 'Generate graph' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({ acknowledgeEngineRun: true });
  });

  it('explains unsupported, stale and failed graphs without showing nodes', async () => {
    serve(url => url.endsWith('/graphs/options') ? Response.json({ ...options, canGenerate: false, reason: 'This workspace runs operations remotely.' }) : url.endsWith(`/graphs/${withPlan.id}`) ? Response.json({ ...plain, current: false, withheld: true, staleReason: 'This graph was generated for workspace demo; the project now reads workspace other.', nodes: [], edges: [], resourceEdges: [] }) : undefined);
    view();
    expect(await screen.findByText(/runs operations remotely/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Generate graph' })).toBeDisabled();
    expect(await screen.findByText('This graph no longer matches.')).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Graph summary' })).not.toBeInTheDocument();
    cleanup();
    serve(url => url.endsWith(`/graphs/${withPlan.id}`) ? Response.json({ ...plain, status: 'failed', nodes: [], edges: [], resourceEdges: [], diagnostic: { code: 'init_required', message: 'This working directory needs initialization.' } }) : undefined);
    view();
    expect(await screen.findByText('Graph not available')).toBeInTheDocument();
    expect(screen.getByText(/never treated as “no dependencies”/)).toBeInTheDocument();
  });

  it('opens from Inspect impact with the plan overlaid and the replaced resource selected', async () => {
    view({ planId, address: 'aws_security_group.web' });
    const inspector = await screen.findByRole('complementary', { name: 'Node details' });
    expect(inspector.querySelector('.address-box code')).toHaveTextContent('aws_security_group.web');
    expect(await screen.findByRole('region', { name: 'Confirmed planned changes' })).toBeInTheDocument();
    expect(gets.some(u => u.endsWith(`/graphs/${withPlan.id}?planId=${planId}`))).toBe(true);
    expect(posts).toHaveLength(0);
  });

  it('keeps a 1,000-node graph usable: list first, virtualized, modules collapsed on the canvas', async () => {
    const nodes: GraphNode[] = Array.from({ length: 1000 }, (_, i) => ({ id: `n${i}`, displayName: `module.m${String(Math.floor(i / 50)).padStart(2, '0')}.aws_instance.r${i}`, resourceAddress: `module.m${String(Math.floor(i / 50)).padStart(2, '0')}.aws_instance.r${i}`, modulePath: `module.m${String(Math.floor(i / 50)).padStart(2, '0')}`, nodeKind: 'resource', type: 'aws_instance', confidence: 'configuration', instanceCount: 0 }));
    const edges = nodes.slice(1).map((n, i) => ({ from: n.id, to: `n${i}`, edgeKind: 'depends_on' as const, confidence: 'declared' as const }));
    const big: DependencyGraph = { ...plain, nodes, edges, resourceEdges: edges, summary: { ...plain.summary, nodes: 1000, resources: 1000, dataSources: 0, edges: 999 } };
    serve(url => url.endsWith(`/graphs/${withPlan.id}`) ? Response.json(big) : undefined);
    const start = performance.now();
    view();
    // Modules start collapsed: 20 groups instead of 1,000 nodes.
    expect(await screen.findByRole('list', { name: '20 graph nodes' })).toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Expand all' }));
    const list = screen.getByRole('list', { name: '1000 graph nodes' });
    expect(within(list).getAllByRole('listitem').length).toBeLessThan(40);
    await user.click(screen.getByRole('tab', { name: 'Canvas' }));
    expect(document.querySelectorAll('.graph-node')).toHaveLength(1000);
    await user.click(screen.getByRole('button', { name: 'Collapse all' }));
    expect(screen.getAllByRole('button', { name: /^Collapsed module/ })).toHaveLength(20);
    expect(performance.now() - start).toBeLessThan(15000);
  });
});
