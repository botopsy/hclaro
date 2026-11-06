import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden output keeps the frontend honest about the contract.
import golden from '../../../fixtures/drift/mixed.golden.json';
import { DriftView } from './DriftView';
import type { DriftDetail, DriftOptions, DriftScan, Operation } from '../types';

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
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
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
    const list = screen.getByRole('region', { name: 'Drifted resources' });
    expect(within(list).getByText('aws_security_group.web')).toBeInTheDocument();
    expect(within(list).getByText('Not found during scan')).toBeInTheDocument();
    expect(screen.getByText(/data source record/)).toBeInTheDocument();
    expect(screen.queryByText('No drift found')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(canary);
  });

  it('filters, groups by provider and module, and searches', async () => {
    view();
    const user = userEvent.setup();
    const list = await screen.findByRole('region', { name: 'Drifted resources' });
    expect(within(list).getByText('module.compute')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Missing remotely/ }));
    expect(within(list).getAllByRole('row')).toHaveLength(2);
    expect(within(list).getByText('module.compute.aws_instance.main[3]')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    await user.selectOptions(screen.getByLabelText('Group by'), 'provider');
    expect(within(list).getByText('aws')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Filter drifted resources'), 'security');
    expect(within(list).getAllByRole('row')).toHaveLength(2);
  });

  it('shows recorded and observed values with redaction and deep-links the inspector', async () => {
    const { openResource, runPlan } = view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /aws_security_group\.web/ }));
    const drawer = await screen.findByRole('complementary', { name: 'Drift details' });
    await within(drawer).findByText('Recorded in state');
    expect(within(drawer).getByRole('columnheader', { name: 'Recorded in state' })).toBeInTheDocument();
    expect(within(drawer).getByRole('columnheader', { name: 'Observed during scan' })).toBeInTheDocument();
    expect(within(drawer).getAllByText('0.0.0.0/0').length).toBeGreaterThan(0);
    expect(within(drawer).getAllByText('redacted').length).toBeGreaterThan(0);
    expect(within(drawer).getByText('Not in the current snapshot')).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('AKIAABCDEFGHIJKLMNOP');
    await user.click(screen.getByRole('button', { name: 'Open aws_s3_bucket.artifacts in the resource inspector' }));
    expect(openResource).toHaveBeenCalledWith(scan.resources.find(r => r.address === 'aws_s3_bucket.artifacts')!.resourceId);
    await user.click(screen.getByRole('button', { name: /^module\.network\.aws_subnet\.main\[0\]/ }));
    expect(await screen.findByText('cannot verify')).toBeInTheDocument();
    // A normal plan is a separate, explicit action.
    await user.click(screen.getByRole('button', { name: /Run a normal plan/ }));
    expect(runPlan).toHaveBeenCalled();
    expect(posts).toHaveLength(0);
  });

  it('asks before scanning and sends only the reviewed request', async () => {
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [] }) : undefined, (url, init) => init?.method === 'POST' && url.endsWith('/drift-scans') ? Response.json({ operation: { ...finished, status: 'running' }, scan: { ...scan, status: 'running' } }, { status: 202 }) : undefined);
    view();
    const user = userEvent.setup();
    expect(await screen.findByText('No drift scans yet')).toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: /Check drift/ })[0]);
    const dialog = screen.getByRole('dialog', { name: 'Check drift' });
    expect(dialog).toHaveTextContent('plan -refresh-only');
    expect(dialog).toHaveTextContent('Queries provider APIs');
    expect(dialog).toHaveTextContent('May lock the state');
    expect(dialog).toHaveTextContent('Writes no state');
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
    cleanup();
    // The same counts from a scan that failed on permissions are never "no drift".
    const denied: DriftScan = { ...clean, status: 'failed', outcome: undefined, exhaustive: false, diagnostic: { code: 'credentials', message: 'The engine could not authenticate with the backend or a provider.' }, engineDiagnostics: [{ severity: 'error', summary: 'No valid credential sources found', detail: '[redacted]' }] };
    serve(url => url.endsWith('/drift-scans') ? Response.json({ scans: [denied] }) : url.endsWith(`/drift-scans/${scan.id}`) ? Response.json(denied) : undefined);
    view();
    expect(await screen.findByText('Scan failed')).toBeInTheDocument();
    expect(screen.getByText(/not the same as no drift/)).toBeInTheDocument();
    expect(screen.queryByText('No drift found')).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Filter by classification' })).not.toBeInTheDocument();
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
    expect(screen.queryByRole('region', { name: 'Drifted resources' })).not.toBeInTheDocument();
  });

  it('explains an unsupported workspace and disables the scan', async () => {
    serve(url => url.endsWith('/drift-scans/options') ? Response.json({ ...options, canScan: false, executionMode: 'remote-exec', reason: 'This workspace uses a remote or cloud backend that runs plans remotely.' }) : url.endsWith('/drift-scans') ? Response.json({ scans: [] }) : undefined);
    view();
    expect(await screen.findByText(/Drift scans are unavailable here/)).toBeInTheDocument();
    expect(screen.getByText(/runs plans remotely/)).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /Check drift/ })[0]).toBeDisabled();
  });
});
