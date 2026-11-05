import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go parser's golden output keeps the frontend honest about the API contract.
import golden from '../../../fixtures/plan/all-actions.golden.json';
import { PlansView } from './PlansView';
import type { Operation, Plan, PlanOptions } from '../types';

const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const planId = 'a'.repeat(32);
const { details, ...review } = golden as typeof golden & { details: Record<string, unknown> };
const plan = {
  ...review,
  id: planId,
  operationId: 'b'.repeat(32),
  createdAt: '2026-11-05T09:00:00Z',
  engine: 'terraform',
  currentEngineVersion: '1.9.8',
  workspace: 'demo',
  varFiles: [],
  artifactSha256: 'c'.repeat(64),
  artifactRetained: false,
  fingerprint: 'd'.repeat(64),
  contextChanged: false,
  apply: { available: false, reason: 'Applying plans is not available in this version of Hclaro.' }
} as unknown as Plan;
const options: PlanOptions = { canPlan: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 's3', varFiles: ['prod.tfvars'] };
const operation: Operation = { id: 'b'.repeat(32), kind: 'plan', status: 'succeeded', stage: 'Finished', startedAt: '2026-11-05T09:00:00Z', durationMs: 4200, exitCode: 0, command: ['terraform', 'plan', '-input=false', '-json', '-out=<private temporary file>'], varFiles: [], planId, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true };

type Route = (url: string, init?: RequestInit) => Response | undefined;
let posts: { url: string; init: RequestInit }[] = [];
function serve(...routes: Route[]) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, init });
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/plans/options')) return Response.json(options);
    if (url.endsWith('/plans')) return Response.json({ plans: [{ id: planId, createdAt: plan.createdAt, changes: 11 }] });
    if (url.endsWith('/operations')) return Response.json({ operations: [operation] });
    if (url.endsWith(`/plans/${planId}`)) return Response.json(plan);
    const change = url.match(/changes\/([0-9a-f]{32})$/);
    if (change) return Response.json(details[change[1]]);
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view(planChoice?: string) {
  const choose = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlansView project="platform-infra" planId={planChoice} choosePlan={choose} copy={async () => {}} /></QueryClientProvider>);
  return choose;
}
beforeEach(() => serve());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Plans', () => {
  it('shows every planned change, including collateral, and never offers apply', async () => {
    view();
    expect(await screen.findByText(/11 resource changes and 4 output changes/)).toBeInTheDocument();
    const counts = screen.getByRole('group', { name: 'Filter by action' });
    for (const [label, count] of [['Create', '1'], ['Update', '3'], ['Replace', '2'], ['Delete', '2'], ['Read', '1'], ['Forget', '1'], ['Unknown', '1'], ['No change', '2']]) {
      expect(within(counts).getByRole('button', { name: new RegExp(`${label}\\s*${count}`) })).toBeInTheDocument();
    }
    const table = screen.getByRole('region', { name: 'Planned resource changes' });
    expect(within(table).getAllByRole('row')).toHaveLength(12);
    expect(within(table).getByText('aws_iam_role.legacy')).toBeInTheDocument();
    expect(within(table).getByText('The resource was removed from configuration')).toBeInTheDocument();
    expect(within(table).queryByText('module.network.aws_subnet.main[1]')).not.toBeInTheDocument();
    await userEvent.click(screen.getByLabelText('Show unchanged'));
    expect(within(table).getByText('module.network.aws_subnet.main[1]')).toBeInTheDocument();
    expect(screen.getByRole('note')).toHaveTextContent('Review only');
    expect(screen.queryByRole('button', { name: /apply/i })).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('action combination Hclaro does not recognize');
    expect(document.body).not.toHaveTextContent(canary);
  });

  it('filters by action and explains a replacement attribute by attribute', async () => {
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Replace\s*2/ }));
    const table = screen.getByRole('region', { name: 'Planned resource changes' });
    expect(within(table).getAllByRole('row')).toHaveLength(3);
    await user.click(within(table).getByRole('button', { name: /module\.compute\.aws_instance\.main\[0\]/ }));
    const inspector = await screen.findByRole('complementary', { name: 'Change details' });
    expect(await within(inspector).findByText('Destroy the existing object, then create a replacement.')).toBeInTheDocument();
    const ami = within(inspector).getByRole('rowheader', { name: /ami/ });
    expect(ami).toHaveTextContent('forces replacement');
    const id = within(inspector).getByRole('rowheader', { name: /^id/ }).closest('tr')!;
    expect(id).toHaveTextContent('i-0aa');
    expect(id).toHaveTextContent('known after apply');
    expect(inspector).not.toHaveTextContent(canary);
    await user.click(within(inspector).getByRole('button', { name: 'Close change details' }));
    await user.click(screen.getByRole('tab', { name: /Outputs/ }));
    expect(screen.getByText('db_password').closest('tr')).toHaveTextContent('sensitive');
    await user.click(screen.getByRole('tab', { name: /Changed outside/ }));
    expect(screen.getByText('aws_s3_bucket.artifacts')).toBeInTheDocument();
  });

  it('requires the preflight before starting a plan and sends the CSRF token', async () => {
    serve((_url, init) => init?.method === 'POST' ? Response.json({ ...operation, status: 'running', planId: undefined }, { status: 202 }) : undefined);
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Run plan' }));
    const dialog = screen.getByRole('dialog', { name: 'Run a plan' });
    expect(dialog).toHaveTextContent('Contacts providers and the s3 backend');
    expect(dialog).toHaveTextContent('May lock the state');
    expect(dialog).toHaveTextContent('Does not change infrastructure');
    expect(posts).toHaveLength(0);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(posts).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Run plan' }));
    await user.click(screen.getByLabelText('prod.tfvars'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Run plan' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(JSON.parse(posts[0].init.body as string)).toEqual({ acknowledgeBackendAccess: true, varFiles: ['prod.tfvars'] });
    expect((posts[0].init.headers as Record<string, string>)['X-Hclaro-CSRF']).toBe('e'.repeat(64));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('enables planning once the workspace finishes loading', async () => {
    let calls = 0;
    serve(url => url.endsWith('/plans/options') ? Response.json({ ...options, canPlan: calls++ > 0 }) : undefined);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const tree = (status: string) => <QueryClientProvider client={client}><PlansView project="p" workspaceStatus={status} choosePlan={() => {}} copy={async () => {}} /></QueryClientProvider>;
    const { rerender } = render(tree('loading'));
    expect(await screen.findByText(/Reading the workspace/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    rerender(tree('ready'));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Run plan' })).toBeEnabled());
  });

  it('explains when planning is unavailable', async () => {
    serve(url => url.endsWith('/plans/options') ? Response.json({ ...options, canPlan: false, reason: 'Remote execution backends run plans remotely.' }) : url.endsWith('/plans') ? Response.json({ plans: [] }) : url.endsWith('/operations') ? Response.json({ operations: [] }) : undefined);
    view();
    expect(await screen.findByText('Remote execution backends run plans remotely.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    expect(screen.getByText('No plans yet')).toBeInTheDocument();
  });

  it('reports a failed plan without offering a plan to review', async () => {
    const failed: Operation = { ...operation, status: 'failed', planId: undefined, exitCode: 1, diagnostic: { code: 'state_locked', message: 'The state is locked by another operation. Hclaro never force-unlocks state.' }, engineDiagnostics: [{ severity: 'error', summary: 'Error acquiring the state lock', detail: '[line withheld: looks like a secret]' }] };
    serve(url => url.endsWith('/plans') ? Response.json({ plans: [] }) : url.endsWith('/operations') ? Response.json({ operations: [failed] }) : undefined);
    view();
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Plan failed');
    expect(alert).toHaveTextContent('never force-unlocks');
    expect(alert).toHaveTextContent('No plan was kept');
    expect(alert).toHaveTextContent('Error acquiring the state lock');
    expect(screen.queryByText('No plans yet')).not.toBeInTheDocument();
  });

  it('labels an older plan shown below a failed run', async () => {
    const failed: Operation = { ...operation, id: 'f'.repeat(32), status: 'cancelled', planId: undefined, diagnostic: { code: 'cancelled', message: 'The plan was cancelled and no plan was kept.' } };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [failed, operation] }) : undefined);
    view();
    expect(await screen.findByText(/Below is the previous successful plan/)).toBeInTheDocument();
    expect(screen.getAllByRole('alert')[0]).toHaveTextContent('Plan cancelled');
  });

  it('streams a running plan and cancels it', async () => {
    const running: Operation = { ...operation, status: 'running', stage: 'Planning', planId: undefined, exitCode: undefined };
    const stream = [
      'id: 1\nevent: log\ndata: {"seq":1,"time":"2026-11-05T09:00:00Z","level":"info","text":"aws_s3_bucket.logs: plan to create"}\n\n',
      'event: status\ndata: ' + JSON.stringify(running) + '\n\n'
    ].join('');
    let cancelled = false;
    serve((url, init) => {
      if (url.endsWith('/operations')) return Response.json({ operations: [cancelled ? { ...running, status: 'cancelled' } : running] });
      if (url.includes('/events')) return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } });
      if (url.endsWith('/cancel') && init?.method === 'POST') {
        cancelled = true;
        return Response.json({ ...running, cancelRequested: true }, { status: 202 });
      }
      if (url.endsWith('/plans')) return Response.json({ plans: [] });
      return undefined;
    });
    view();
    const log = await screen.findByRole('log', { name: 'Plan output' });
    expect(await within(log).findByText('aws_s3_bucket.logs: plan to create')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    await userEvent.click(screen.getByRole('button', { name: 'Cancel plan' }));
    await waitFor(() => expect(posts.some(p => p.url.endsWith(`/operations/${running.id}/cancel`))).toBe(true));
    expect(await screen.findByText('Cancelling')).toBeInTheDocument();
  });
});
