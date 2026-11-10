import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden output keeps the frontend honest about the contract.
import golden from '../../../fixtures/discovery/scan.golden.json';
import { DiscoveryView } from './DiscoveryView';
import { ImportView } from '../imports/ImportView';
import type { DiscoveryOptions, DiscoveryPrefill, DiscoveryScan } from '../types';

const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const verified = golden.options as unknown as DiscoveryOptions;
const unverified: DiscoveryOptions = { ...verified, identity: undefined };
const scan = golden.scan as unknown as DiscoveryScan;
const prefill = golden.prefill as unknown as DiscoveryPrefill;

type Route = (url: string, init?: RequestInit) => Response | undefined;
let posts: { url: string; body: Record<string, unknown> }[] = [];
let gets: string[] = [];
function serve(state: { options: DiscoveryOptions; scans: DiscoveryScan[] }, ...routes: Route[]) {
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
    if (url.includes('/discovery/options')) return Response.json(state.options);
    if (url.endsWith('/discovery/identity')) {
      state.options = verified;
      return Response.json(verified.identity);
    }
    if (url.endsWith('/discovery/scans') && init?.method === 'POST') {
      state.scans = [scan];
      return Response.json(scan, { status: 202 });
    }
    if (url.endsWith('/discovery/scans')) return Response.json({ scans: state.scans });
    if (url.includes(`/discovery/scans/${scan.id}/candidates/`)) return Response.json(prefill);
    if (url.includes(`/discovery/scans/${scan.id}`)) return Response.json(scan);
    if (url.includes('/imports/options')) return Response.json({ canImport: true, canApply: true, supportsImportBlocks: true, supportsGeneratedConfig: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] });
    if (url.includes('/plans/options')) return Response.json({ canPlan: true, canApply: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] });
    if (url.endsWith('/imports/preview')) return Response.json({ address: 'aws_s3_bucket.legacy_logs', file: 'imports.tf', content: 'import {\n  to = aws_s3_bucket.legacy_logs\n  id = "hclaro-legacy-logs"\n}\n', sha256: 'f'.repeat(64), config: 'found', configFile: 'main.tf', canGenerate: false, issues: [], blocked: false });
    if (url.endsWith('/imports')) return Response.json({ id: '9'.repeat(32), kind: 'plan', status: 'running' }, { status: 202 });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function client() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}
function view() {
  const onPrepared = vi.fn();
  const openResource = vi.fn();
  render(<QueryClientProvider client={client()}><DiscoveryView onPrepared={onPrepared} openResource={openResource} copy={async () => {}} /></QueryClientProvider>);
  return { onPrepared, openResource };
}
beforeEach(() => serve({ options: unverified, scans: [] }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Cloud discovery', () => {
  it('contacts nothing on load and verifies the AWS context only when asked', async () => {
    view();
    expect(await screen.findByRole('heading', { name: 'AWS context not verified' })).toBeInTheDocument();
    expect(screen.getByText(/never infers the account from a profile name/)).toBeInTheDocument();
    expect(posts).toHaveLength(0);
    expect(gets.every(u => u.includes('/discovery/options') || u.endsWith('/discovery/scans'))).toBe(true);
    expect(screen.queryByRole('button', { name: /Scan…/ })).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Verify AWS context' }));
    expect(await screen.findByRole('heading', { name: 'AWS account 111122223333' })).toBeInTheDocument();
    expect(posts.map(p => p.url)).toEqual(['/api/v1/discovery/identity']);
    expect(screen.getByText(/assumed-role\/HclaroDemoReadOnly/)).toBeInTheDocument();
  });

  it('previews scope, requests and charges before a scan, and requires consent', async () => {
    serve({ options: verified, scans: [] });
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Scan…/ }));
    const dialog = screen.getByRole('dialog', { name: /Scan AWS account 111122223333/ });
    expect(within(dialog).getByText(/can incur charges/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Only Describe and List operations run/)).toBeInTheDocument();
    const start = within(dialog).getByRole('button', { name: 'Start scan' });
    expect(start).toBeDisabled();
    // Default: every type in the default region (one per regional type plus S3 once), plus STS.
    expect(within(dialog).getByText(/Calls AWS list APIs 6–11 times/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('checkbox', { name: 'eu-west-1' }));
    expect(within(dialog).getByText(/Calls AWS list APIs 10–19 times/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('checkbox', { name: /Call the AWS APIs listed above/ }));
    await user.click(start);
    expect(await screen.findByText('Scan complete')).toBeInTheDocument();
    const body = posts.find(p => p.url.endsWith('/discovery/scans'))!.body;
    expect(body).toMatchObject({ identityId: verified.identity!.id, acknowledgeApiCalls: true, scope: { regions: ['us-east-1', 'eu-west-1'], maxPerType: 200, concurrency: 2 } });
  });

  it('labels candidates conservatively, filters them and never shows secrets', async () => {
    serve({ options: verified, scans: [scan] });
    const { openResource } = view();
    const user = userEvent.setup();
    expect(await screen.findByText('Scan complete')).toBeInTheDocument();
    const counts = screen.getByRole('group', { name: 'Filter by state mapping' });
    for (const [label, count] of [['Possibly untracked', '7'], ['Unknown', '1'], ['Already tracked', '3'], ['Unresolved', '1']]) {
      expect(within(counts).getByRole('button', { name: new RegExp(`${label}\\s*${count}`) })).toBeInTheDocument();
    }
    expect(screen.getByText(/Other workspaces, projects or tools may manage the object/)).toBeInTheDocument();
    // Credential-shaped tag values are replaced on the server.
    expect(screen.getAllByText('redacted').length).toBeGreaterThan(0);
    // Tracked objects link to their state address and offer no import.
    const artifacts = screen.getByText('hclaro-demo-artifacts').closest('tr')!;
    expect(within(artifacts).queryByRole('button', { name: /Prepare import/ })).not.toBeInTheDocument();
    await user.click(within(artifacts).getByRole('button', { name: /aws_s3_bucket.artifacts/ }));
    expect(openResource).toHaveBeenCalled();
    // A subnet shared from another account has no import ID.
    const shared = screen.getByText('subnet-0f1e2d3c4b5a60002').closest('tr')!;
    expect(within(shared).getByText('Unresolved')).toBeInTheDocument();
    expect(within(shared).queryByRole('button', { name: /Prepare import/ })).not.toBeInTheDocument();
    await user.click(within(counts).getByRole('button', { name: /Unknown/ }));
    expect(screen.getAllByRole('row')).toHaveLength(2);
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    await user.selectOptions(screen.getByLabelText('Service filter'), 'aws_db_instance');
    expect(screen.getAllByRole('row')).toHaveLength(3);
    expect(document.body).not.toHaveTextContent(canary);
    expect(document.body).not.toHaveTextContent('hunter2');
    expect(document.body).not.toHaveTextContent('demo.user@example.com');
  });

  it('asks before preparing an import with an unknown mapping', async () => {
    serve({ options: verified, scans: [scan] });
    const { onPrepared } = view();
    const user = userEvent.setup();
    await screen.findByText('Scan complete');
    await user.click(within(screen.getByRole('group', { name: 'Filter by state mapping' })).getByRole('button', { name: /Unknown/ }));
    await user.click(screen.getByRole('button', { name: /Prepare import/ }));
    const dialog = screen.getByRole('alertdialog', { name: 'State mapping unknown' });
    expect(posts.filter(p => p.url.includes('/prepare'))).toHaveLength(0);
    await user.click(within(dialog).getByRole('button', { name: /I reviewed it/ }));
    expect(onPrepared).toHaveBeenCalledWith(prefill);
    expect(posts.find(p => p.url.includes('/prepare'))!.body).toMatchObject({ acknowledgeUnknownMapping: true });
  });

  it('shows a scan of another context as withheld', async () => {
    serve({ options: verified, scans: [scan] }, url => url.includes(`/discovery/scans/${scan.id}`) ? Response.json({ ...scan, current: false, withheld: true, results: [], staleReason: 'The launch workspace is now prod, not demo. Results are withheld; scan again in this context.' }) : undefined);
    view();
    expect(await screen.findByText(/This scan belongs to another context/)).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'Filter by state mapping' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Prepare import/ })).not.toBeInTheDocument();
  });

  it('reports denied services as missing, not empty', async () => {
    const partial: DiscoveryScan = { ...scan, status: 'partial', tasks: scan.tasks.map(t => t.type === 'aws_db_instance' ? { ...t, status: 'failed', error: 'access_denied', message: 'AWS denied this read (missing IAM permission). Results for this service and region are missing, not empty.' } : t) };
    serve({ options: verified, scans: [partial] }, url => url.includes(`/discovery/scans/${scan.id}`) ? Response.json(partial) : undefined);
    view();
    expect(await screen.findByText('Scan incomplete')).toBeInTheDocument();
    expect(screen.getAllByText(/RDS DB instances · eu-west-1/).length).toBeGreaterThan(0);
    expect(screen.getByText(/missing is not the same as none/)).toBeInTheDocument();
  });

  it('names the supported types and limits, and reports the per-scan cap', async () => {
    const capped: DiscoveryScan = { ...scan, status: 'partial', truncated: true, tasks: scan.tasks.map(t => t.type === 'aws_db_instance' ? { ...t, status: 'cancelled', message: 'Not listed: the scan reached its object limit.' } : t) };
    serve({ options: verified, scans: [capped] }, url => url.includes(`/discovery/scans/${scan.id}`) ? Response.json(capped) : undefined);
    view();
    expect(await screen.findByText('Scan incomplete')).toBeInTheDocument();
    const explainer = screen.getByText(/supported types only/);
    for (const t of ['aws_vpc', 'aws_subnet', 'aws_security_group', 'aws_s3_bucket', 'aws_db_instance']) expect(within(explainer).getByText(t)).toBeInTheDocument();
    // toLocaleString groups digits by the machine's locale (1,000 / 1.000 / 1 000).
    expect(explainer).toHaveTextContent(/at most 6 regions, 1\D?000 objects per type and region and 5\D?000 objects per scan\. Other AWS types, Azure and Google Cloud are not discovered/);
    expect(screen.getByText(/stopped at 5\D?000 objects per scan; more exist/)).toBeInTheDocument();
    expect(screen.getAllByText(/Not listed: the scan reached its object limit/)).toHaveLength(2);
  });
});

describe('Import wizard from discovery', () => {
  it('prefills the ID, keeps the address editable and sends the discovery link', async () => {
    serve({ options: verified, scans: [scan] });
    render(<QueryClientProvider client={client()}><ImportView prefill={prefill} onStarted={() => {}} copy={async () => {}} /></QueryClientProvider>);
    const user = userEvent.setup();
    expect(await screen.findByText(/From Cloud discovery/)).toBeInTheDocument();
    expect(screen.getByLabelText('Import ID')).toHaveValue('hclaro-legacy-logs');
    const address = screen.getByLabelText('Resource instance address');
    expect(address).toHaveValue('aws_s3_bucket.hclaro_legacy_logs');
    await user.clear(address);
    await user.type(address, 'aws_s3_bucket.legacy_logs');
    await user.click(screen.getByRole('button', { name: /Check and preview/ }));
    await user.click(await screen.findByRole('checkbox', { name: /Write/ }));
    await user.click(screen.getByRole('button', { name: /Write file and plan import/ }));
    const body = posts.find(p => p.url.endsWith('/imports'))!.body;
    expect(body).toMatchObject({ address: 'aws_s3_bucket.legacy_logs', id: 'hclaro-legacy-logs', discoveryToken: prefill.token });
  });

  it('unlinks when the import ID or type changes', async () => {
    serve({ options: verified, scans: [scan] });
    render(<QueryClientProvider client={client()}><ImportView prefill={prefill} onStarted={() => {}} copy={async () => {}} /></QueryClientProvider>);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Import ID'), '-other');
    expect(screen.getByText(/No longer linked to the discovered object/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Check and preview/ }));
    await user.click(await screen.findByRole('checkbox', { name: /Write/ }));
    await user.click(screen.getByRole('button', { name: /Write file and plan import/ }));
    expect(posts.find(p => p.url.endsWith('/imports'))!.body).not.toHaveProperty('discoveryToken');
  });
});
