import { cleanup, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import planGolden from '../../../fixtures/plan/all-actions.golden.json';
import { PlanReview } from './PlanReview';
import type { Plan, ScanSummary } from '../types';

const review = Object.fromEntries(Object.entries(planGolden).filter(([key]) => key !== 'details'));
const plan = {
  ...review, id: 'a'.repeat(32), operationId: 'b'.repeat(32), createdAt: '2026-11-09T09:00:00Z', engine: 'terraform', currentEngineVersion: '1.9.8',
  workspace: 'demo', varFiles: [], kind: 'plan', replace: [], destructive: false, artifactSha256: 'c'.repeat(64), fingerprint: 'd'.repeat(64),
  contextChanged: false, apply: { available: true, artifactRetained: true, expiresAt: '2099-01-01T10:00:00Z' }
} as unknown as Plan;
const cid = 'f'.repeat(32);

function show(scans: ScanSummary[] | 'error', scope: Parameters<typeof PlanReview>[0]['scope']) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith(`contexts/${cid}/scans`) && scans !== 'error' ? Response.json({ scans }) : Response.json({ error: 'route_not_found' }, { status: 404 })));
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlanReview plan={plan} copy={async () => {}} onApply={() => {}} scope={scope} /></QueryClientProvider>);
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Security scan hint in Plan Review', () => {
  it('summarizes a current scan and says it never blocks', async () => {
    show([{ scanner: 'trivy', counts: { high: 2 }, finishedAt: '2026-11-09T09:05:00Z' }], { kind: 'context', contextId: cid });
    const hint = await screen.findByText(/Security scan:/);
    expect(hint).toHaveTextContent('2 high (trivy,');
    expect(hint).toHaveTextContent('never blocks an apply');
  });
  it('is absent for a clean scan, an unavailable route and the launch project', async () => {
    show([{ scanner: 'trivy', counts: {}, finishedAt: '2026-11-09T09:05:00Z' }], { kind: 'context', contextId: cid });
    await screen.findByText(/Planned|Changes|changes/i, {}, { timeout: 2000 }).catch(() => undefined);
    expect(screen.queryByText(/Security scan:/)).not.toBeInTheDocument();
    cleanup();
    show('error', { kind: 'context', contextId: cid });
    await new Promise(r => setTimeout(r, 50));
    expect(screen.queryByText(/Security scan:/)).not.toBeInTheDocument();
    cleanup();
    show([{ scanner: 'trivy', counts: { high: 2 }, finishedAt: '2026-11-09T09:05:00Z' }], undefined);
    await new Promise(r => setTimeout(r, 50));
    expect(screen.queryByText(/Security scan:/)).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.filter(([url]) => /scans/.test(String(url)))).toHaveLength(0);
  });
});
