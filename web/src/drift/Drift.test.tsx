import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden output keeps the frontend honest about the contract.
import golden from '../../../fixtures/drift/mixed.golden.json';
import { DriftView } from './DriftView';
import type { DriftDetail, DriftOptions, DriftScan, Operation } from '../types';
import { testLimits } from '../testLimits';

const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const scan = golden.scan as unknown as DriftScan;
const details = golden.details as unknown as Record<string, DriftDetail>;
const options: DriftOptions = { canScan: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 's3', executionMode: 'local-cli', varFiles: ['prod.tfvars'] };
const finished: Operation = { id: scan.operationId, kind: 'drift', status: 'succeeded', stage: 'Finished', startedAt: scan.startedAt, durationMs: 4000, exitCode: 0, command: ['terraform', 'plan', '-input=false', '-json', '-out=<private temporary file>', '-refresh-only'], varFiles: [], replace: [], stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true, driftScanId: scan.id };

type Route = (url: string, init?: RequestInit) => Response | undefined;
let posts: { url: string; body: unknown }[] = [];
function serve(...routes: Route[]) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64), limits: testLimits });
    if (url.endsWith('/drift-scans/options')) return Response.json(options);
    if (url.endsWith('/drift-scans')) return Response.json({ scans: [scan] });
    if (url.endsWith('/operations')) return Response.json({ operations: [finished] });
    if (url.endsWith(`/drift-scans/${scan.id}`)) return Response.json(scan);
    const detail = url.match(/resources\/([0-9a-f]{32})$/);
    if (detail) return Response.json(details[detail[1]]);
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view() {
  const openResource = vi.fn();
  const runPlan = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><DriftView project="platform-infra" openResource={openResource} runPlan={runPlan} copy={async () => {}} /></QueryClientProvider>);
  return { openResource, runPlan };
}
beforeEach(() => serve());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Drift', () => {
  it('runs nothing on load and shows the latest scan with classifications', async () => {
    view();
    expect(await screen.findByText('3 objects changed outside Terraform')).toBeInTheDocument();
    expect(posts).toHaveLength(0);
    const counts = screen.getByRole('group', { name: 'Filter by classification' });
    for (const [label, count] of [['Changed', '2'], ['Missing remotely', '1'], ['Cannot verify', '1']]) {
      expect(within(counts).getByRole('button', { name: new RegExp(`${label}\\s*${count}`) })).toBeInTheDocument();
    }
    const list = screen.getByRole('table', { name: 'Drifted resources' });
    expect(within(list).getByRole('button', { name: /aws_security_group\.web/ })).toBeInTheDocument();
    expect(within(list).getByText('Not found during scan')).toBeInTheDocument();
    // Drift chips carry a symbol and a word.
    expect(within(list).getAllByText('changed').length).toBeGreaterThan(0);
    expect(within(list).getByText('missing')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Check again/ })).toBeEnabled();
    expect(screen.getByText(/data source record/)).toBeInTheDocument();
    expect(screen.queryByText('No drift found')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(canary);
  });

  it('filters, groups by module or none, and searches', async () => {
    view();
    const user = userEvent.setup();
    const list = await screen.findByRole('table', { name: 'Drifted resources' });
    expect(within(list).getByRole('button', { name: /module\.compute/, expanded: true })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Missing remotely/ }));
    // Header, one group row and the missing instance.
    expect(within(list).getAllByRole('row')).toHaveLength(3);
    expect(within(list).getByRole('button', { name: /module\.compute\.aws_instance\.main\[3\]/ })).toBeInTheDocument();
    expect(screen.getByText('1 shown')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    expect([...(screen.getByLabelText('Group by') as HTMLSelectElement).options].map(o => o.text)).toEqual(['Module', 'None']);
    await user.selectOptions(screen.getByLabelText('Group by'), 'none');
    await user.type(screen.getByLabelText('Filter drifted resources'), 'security');
    expect(within(list).getAllByRole('row')).toHaveLength(2);
    await user.type(screen.getByLabelText('Filter drifted resources'), 'zzz');
    expect(screen.getByText('No drift matches “securityzzz”.')).toBeInTheDocument();
  });

  it('opens a drawer with recorded and reported values, redaction and next steps', async () => {
    const { openResource, runPlan } = view();
    const user = userEvent.setup();
    const row = await screen.findByRole('button', { name: /aws_security_group\.web/ });
    await user.click(row);
    let drawer = await screen.findByRole('complementary', { name: 'Drift details' });
    await within(drawer).findByText('What changed');
    expect(row.closest('tr')).toHaveClass('active');
    expect(within(drawer).getByRole('heading', { name: 'aws_security_group.web' })).toBeInTheDocument();
    expect(within(drawer).getByRole('columnheader', { name: 'Recorded in state' })).toBeInTheDocument();
    expect(within(drawer).getByRole('columnheader', { name: 'Provider reports now' })).toBeInTheDocument();
    expect(within(drawer).getAllByText('0.0.0.0/0').length).toBeGreaterThan(0);
    expect(within(drawer).getAllByText('redacted').length).toBeGreaterThan(0);
    expect(within(drawer).getByText('Nothing here changes state or infrastructure.')).toBeInTheDocument();
    // Not in the current snapshot: no resource details to open.
    expect(within(drawer).getByRole('button', { name: 'Resource details' })).toBeDisabled();
    expect(document.body).not.toHaveTextContent('AKIAABCDEFGHIJKLMNOP');
    expect(document.body).not.toHaveTextContent(canary);
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: /^aws_s3_bucket\.artifacts/ }));
    drawer = await screen.findByRole('complementary', { name: 'Drift details' });
    await user.click(await within(drawer).findByRole('button', { name: 'Resource details' }));
    expect(openResource).toHaveBeenCalledWith(scan.resources.find(r => r.address === 'aws_s3_bucket.artifacts')!.resourceId);
    await user.click(within(drawer).getByRole('button', { name: /Run plan/ }));
    expect(runPlan).toHaveBeenCalledTimes(1);
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: /^module\.compute\.aws_instance\.main\[3\]/ }));
    drawer = await screen.findByRole('complementary', { name: 'Drift details' });
    expect((await within(drawer).findByText('Not found by the provider')).parentElement).toHaveTextContent('with ID i-0demo3');
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: /^module\.network\.aws_subnet\.main\[0\]/ }));
    drawer = await screen.findByRole('complementary', { name: 'Drift details' });
    expect((await within(drawer).findAllByText('cannot verify')).length).toBeGreaterThan(0);
    await user.keyboard('{Escape}');
    // A normal plan is a separate, explicit action.
    await user.click(screen.getByRole('button', { name: /Run a normal plan/ }));
    expect(runPlan).toHaveBeenCalledTimes(2);
    expect(posts).toHaveLength(0);
  }, 15000);

  it('asks before scanning and sends only the reviewed request', async () => {
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [] }) : undefined, (url, init) => init?.method === 'POST' && url.endsWith('/drift-scans') ? Response.json({ operation: { ...finished, status: 'running' }, scan: { ...scan, status: 'running' } }, { status: 202 }) : undefined);
    view();
    const user = userEvent.setup();
    expect(await screen.findByText('No drift scans yet')).toBeInTheDocument();
    expect(await screen.findByText(/The last 10 scan results are kept in memory for up to 12 hours/)).toBeInTheDocument();
    // One primary button: the empty state's.
    expect(screen.getAllByRole('button', { name: /Check drift/ })).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: /Check drift/ }));
    const dialog = screen.getByRole('dialog', { name: 'Check drift' });
    expect(dialog).toHaveTextContent('plan -refresh-only');
    expect(dialog).toHaveTextContent('Queries provider APIs');
    expect(dialog).toHaveTextContent('May lock the state');
    expect(dialog).toHaveTextContent('Writes no state');
    expect(within(dialog).getByRole('region', { name: 'This scan will use' })).toHaveTextContent('platform-infra');
    expect(posts).toHaveLength(0);
    await user.click(within(dialog).getByLabelText('prod.tfvars'));
    await user.click(within(dialog).getByRole('button', { name: 'Check drift' }));
    expect(posts).toEqual([{ url: '/api/v1/drift-scans', body: { acknowledgeProviderAccess: true, varFiles: ['prod.tfvars'] } }]);
  });

  it('shows no-drift only for an exhaustive completed scan', async () => {
    const clean: DriftScan = { ...scan, outcome: 'no_drift', resources: [], warnings: [], summary: { changed: 0, missing: 0, cannotVerify: 0, unrecognized: 0, total: 0, outputChanges: 0 } };
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [clean] }) : url.endsWith(`/drift-scans/${scan.id}`) ? Response.json(clean) : undefined);
    view();
    expect(await screen.findByText('No drift found')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Check again/ })).toBeInTheDocument();
    cleanup();
    // The same counts from a scan that failed on permissions are never "no drift".
    const denied: DriftScan = { ...clean, status: 'failed', outcome: undefined, exhaustive: false, diagnostic: { code: 'credentials', message: 'The engine could not authenticate with the backend or a provider.' }, engineDiagnostics: [{ severity: 'error', summary: 'No valid credential sources found', detail: '[redacted]' }] };
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [denied] }) : url.endsWith(`/drift-scans/${scan.id}`) ? Response.json(denied) : undefined);
    view();
    expect(await screen.findByText('Scan failed')).toBeInTheDocument();
    expect(screen.getByText(/not the same as no drift/)).toBeInTheDocument();
    expect(screen.queryByText('No drift found')).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Filter by classification' })).not.toBeInTheDocument();
    cleanup();
    // With its operation still listed, the shared failed state names the cause and offers the log.
    const failedOp: Operation = { ...finished, status: 'failed', exitCode: 1, diagnostic: { code: 'credentials', message: 'm' } };
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [denied] }) : url.endsWith(`/drift-scans/${scan.id}`) ? Response.json(denied) : url.endsWith('/operations') ? Response.json({ operations: [failedOp] }) : url.includes('/events') ? new Response(`event: status\ndata: ${JSON.stringify(failedOp)}\n\nevent: end\ndata: {}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } }) : undefined);
    view();
    expect(await screen.findByRole('heading', { name: 'Credentials missing or expired' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'View log' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Try again/ })).toBeInTheDocument();
    expect(screen.getByText(/not the same as no drift/)).toBeInTheDocument();
  });

  it('labels a partial scan and a stale scan truthfully', async () => {
    const partial: DriftScan = { ...scan, status: 'partial', outcome: undefined, exhaustive: false, diagnostic: { code: 'credentials', message: 'Access denied.' } };
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [partial] }) : url.endsWith(`/drift-scans/${scan.id}`) ? Response.json(partial) : undefined);
    view();
    expect(await screen.findByText('Scan incomplete')).toBeInTheDocument();
    expect(screen.getByText(/not necessarily checked/)).toBeInTheDocument();
    cleanup();
    const stale: DriftScan = { ...scan, current: false, withheld: true, resources: [], staleReason: 'This scan was for workspace demo; the project now reads workspace prod.' };
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [stale] }) : url.endsWith(`/drift-scans/${scan.id}`) ? Response.json(stale) : undefined);
    view();
    expect(await screen.findByText(/This scan no longer matches/)).toBeInTheDocument();
    expect(screen.getByText(/now reads workspace prod/)).toBeInTheDocument();
    expect(screen.queryByRole('table', { name: 'Drifted resources' })).not.toBeInTheDocument();
  });

  it('explains an unsupported workspace and disables the scan', async () => {
    serve(url => url.endsWith('/drift-scans/options') ? Response.json({ ...options, canScan: false, executionMode: 'remote-exec', reason: 'This workspace uses a remote or cloud backend that runs plans remotely.' }) : url.endsWith('/drift-scans') ? Response.json({ scans: [] }) : undefined);
    view();
    expect(await screen.findByText(/Drift scans are unavailable here/)).toBeInTheDocument();
    expect(screen.getByText(/runs plans remotely/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Check drift/ })).toBeDisabled();
  });
});
