import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ImportView } from './ImportView';
import { PlanReview } from '../plans/PlanReview';
import type { ImportOptions, ImportPreview, Plan } from '../types';

const options: ImportOptions = { canImport: true, canApply: true, supportsImportBlocks: true, supportsGeneratedConfig: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', varFiles: [] };
const content = '# Import block prepared with Hclaro.\n\nimport {\n  to = aws_s3_bucket.legacy_logs\n  id = "hclaro-legacy-logs"\n}\n';
const ready: ImportPreview = {
  address: 'aws_s3_bucket.legacy_logs', file: 'imports.tf', content, sha256: 'a'.repeat(64), config: 'found', configFile: 'main.tf', canGenerate: false, issues: [], blocked: false,
  recipe: { block: content, commands: ['# Save the import block in imports.tf in the root module, then:', 'terraform plan -out=import.tfplan'], legacy: "terraform import 'aws_s3_bucket.legacy_logs' 'hclaro-legacy-logs'" }
};

let posts: { url: string; body: Record<string, unknown> }[] = [];
function serve(opts: ImportOptions, preview: ImportPreview) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/imports/options')) return Response.json(opts);
    if (url.endsWith('/imports/preview')) return Response.json(preview);
    if (url.endsWith('/imports')) return Response.json({ id: 'b'.repeat(32), kind: 'plan', status: 'running' }, { status: 202 });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view() {
  const started = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><ImportView workspaceStatus="ready" onStarted={started} copy={async () => {}} /></QueryClientProvider>);
  return started;
}
async function describeObject() {
  await userEvent.type(screen.getByLabelText('Resource instance address'), 'aws_s3_bucket.legacy_logs');
  await userEvent.type(screen.getByLabelText('Import ID'), 'hclaro-legacy-logs');
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Import wizard', () => {
  it('shows the new file before writing and writes only after explicit consent', async () => {
    serve(options, ready);
    const started = view();
    await describeObject();
    await userEvent.click(screen.getByRole('button', { name: 'Check and preview' }));
    const file = await screen.findByLabelText('Contents of imports.tf');
    expect(file).toHaveTextContent('to = aws_s3_bucket.legacy_logs');
    expect(screen.getByText(/Resource block found in/)).toBeInTheDocument();
    expect(posts.map(p => p.url)).toEqual(['/api/v1/imports/preview']);
    const write = screen.getByRole('button', { name: 'Write file and plan import' });
    expect(write).toBeDisabled();
    await userEvent.click(screen.getByLabelText(/Write imports.tf to this project/));
    await userEvent.click(write);
    expect(posts.at(-1)).toEqual({ url: '/api/v1/imports', body: { address: 'aws_s3_bucket.legacy_logs', id: 'hclaro-legacy-logs', provider: '', file: 'imports.tf', generateConfig: false, varFiles: [], previewSha256: 'a'.repeat(64), acknowledgeWrite: true, acknowledgeBackendAccess: true } });
    expect(started).toHaveBeenCalledOnce();
  });

  it('discards the preview when the request changes', async () => {
    serve(options, ready);
    view();
    await describeObject();
    await userEvent.click(screen.getByRole('button', { name: 'Check and preview' }));
    await screen.findByLabelText('Contents of imports.tf');
    await userEvent.type(screen.getByLabelText('Import ID'), 'x');
    expect(screen.queryByLabelText('Contents of imports.tf')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Write file and plan import' })).not.toBeInTheDocument();
  });

  it('blocks a collision and offers nothing to write', async () => {
    serve(options, { ...ready, content: '', sha256: '', blocked: true, issues: [{ code: 'address_in_state', severity: 'error', message: 'aws_s3_bucket.legacy_logs is already in state.' }] });
    view();
    await describeObject();
    await userEvent.click(screen.getByRole('button', { name: 'Check and preview' }));
    const problems = await screen.findByRole('alert', { name: 'Problems to fix' });
    expect(within(problems).getByText(/already in state/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Write file and plan import' })).not.toBeInTheDocument();
  });

  it('explains why importing is unavailable and shows the manual recipe', async () => {
    serve({ ...options, canImport: false, reason: 'Remote execution backends run plans remotely.' }, ready);
    view();
    expect(await screen.findByText(/Remote execution backends run plans remotely/)).toBeInTheDocument();
    await describeObject();
    await userEvent.click(screen.getByRole('button', { name: 'Check and preview' }));
    expect(await screen.findByText('terraform plan -out=import.tfplan', { exact: false })).toBeVisible();
    expect(screen.getByText(/Hclaro never runs it/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Write file and plan import' })).not.toBeInTheDocument();
  });
});

describe('Import plan review', () => {
  it('shows the requested import next to every other change and the redacted generated configuration', () => {
    const plan = {
      id: 'c'.repeat(32), operationId: 'b'.repeat(32), createdAt: '2026-11-05T09:00:00Z', engine: 'terraform', engineVersion: '1.9.8', currentEngineVersion: '1.9.8', workspace: 'demo', varFiles: [], formatVersion: '1.2', errored: false,
      summary: { 'no-op': 1, update: 1 }, imports: 1, drift: [], outputs: [], warnings: [], kind: 'import', replace: [], destructive: false, artifactSha256: 'd'.repeat(64), fingerprint: 'e'.repeat(64), contextChanged: false,
      apply: { available: true, artifactRetained: true },
      import: { address: 'aws_s3_bucket.legacy_logs', file: 'legacy.tf', generatedFile: 'legacy_generated.tf' },
      generatedConfig: 'resource "aws_s3_bucket" "legacy_logs" {\n  replication_token = "[redacted]"\n}',
      changes: [
        { id: '1'.repeat(32), address: 'aws_s3_bucket.legacy_logs', module: '', mode: 'managed', type: 'aws_s3_bucket', name: 'legacy_logs', provider: 'aws', actions: ['no-op'], action: 'no-op', importing: true, attributeChanges: 0 },
        { id: '2'.repeat(32), address: 'aws_s3_bucket.artifacts', module: '', mode: 'managed', type: 'aws_s3_bucket', name: 'artifacts', provider: 'aws', actions: ['update'], action: 'update', attributeChanges: 1 }
      ]
    } as Plan;
    render(<QueryClientProvider client={new QueryClient()}><PlanReview plan={plan} copy={async () => {}} onApply={() => {}} /></QueryClientProvider>);
    const summary = screen.getByRole('region', { name: 'Requested import' });
    expect(within(summary).getByText('aws_s3_bucket.legacy_logs')).toBeInTheDocument();
    expect(within(summary).getByText(/1 other change is part of this plan/)).toBeInTheDocument();
    expect(within(summary).getByText(/replication_token = "\[redacted\]"/)).toBeInTheDocument();
    expect(screen.getByText(/1 import, 1 resource change/)).toBeInTheDocument();
  });
});
