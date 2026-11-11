import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden responses keep the frontend honest about the cost contract.
import golden from '../../../fixtures/cost/estimate.golden.json';
import planGolden from '../../../fixtures/plan/all-actions.golden.json';
import { PlanReview } from './PlanReview';
import { signed } from './CostTab';
import type { PlanScope } from './routes';
import type { CostEstimate, CostProviderCapabilities, Plan } from '../types';

const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const off = golden.providerOff as unknown as CostProviderCapabilities;
const ready = golden.providerReady as unknown as CostProviderCapabilities;
const estimate = golden.estimate as unknown as CostEstimate;
const planId = estimate.planId;
const review = Object.fromEntries(Object.entries(planGolden).filter(([key]) => key !== 'details'));
const plan = {
  ...review, id: planId, operationId: 'b'.repeat(32), createdAt: '2026-11-09T09:00:00Z', engine: 'terraform', currentEngineVersion: '1.9.8',
  workspace: 'demo', varFiles: [], kind: 'plan', replace: [], destructive: true, artifactSha256: 'c'.repeat(64), fingerprint: 'd'.repeat(64),
  contextChanged: false, apply: { available: true, artifactRetained: true, expiresAt: '2026-11-09T10:00:00Z' }
} as unknown as Plan;

type Route = (url: string, init?: RequestInit) => Response | undefined;
let posts: { url: string; body: unknown }[] = [];
function serve(provider: CostProviderCapabilities, latest: CostEstimate | null, ...routes: Route[]) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/cost/provider')) return Response.json(provider);
    if (url.endsWith(`/plans/${planId}/cost-estimate`)) return latest ? Response.json(latest) : Response.json({ error: 'estimate_not_found', reason: 'Not estimated.' }, { status: 404 });
    if (url.endsWith('/cost/history')) return Response.json({ records: [], persisted: true });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
async function openCost(p: Plan = plan, scope?: PlanScope) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlanReview plan={p} copy={async () => {}} onApply={() => {}} scope={scope} /></QueryClientProvider>);
  await userEvent.click(screen.getByRole('tab', { name: /Cost estimate/ }));
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Cost estimate tab', () => {
  it('in a Hub workspace explains estimates are launch-only and calls no cost route', async () => {
    serve(ready, estimate);
    await openCost(plan, { kind: 'context', contextId: 'f'.repeat(32) });
    expect(await screen.findByText('Cost estimation is not available')).toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.filter(([url]) => /cost/.test(String(url)))).toHaveLength(0);
  });
  it('in a Hub workspace never shows a launch-project estimate already in the cache', async () => {
    serve(ready, estimate);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(['cost-provider'], ready);
    client.setQueryData(['cost-estimate', planId], estimate);
    render(<QueryClientProvider client={client}><PlanReview plan={plan} copy={async () => {}} onApply={() => {}} scope={{ kind: 'context', contextId: 'f'.repeat(32) }} /></QueryClientProvider>);
    await userEvent.click(screen.getByRole('tab', { name: /Cost estimate/ }));
    expect(await screen.findByText('Cost estimation is not available')).toBeInTheDocument();
    expect(screen.queryByText(/USD/)).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.filter(([url]) => /cost/.test(String(url)))).toHaveLength(0);
  });
  it('is off by default: nothing is posted until the disclosure is accepted', async () => {
    serve(off, null, url => url.endsWith('/cost/opt-in') ? Response.json(ready) : undefined);
    await openCost();
    expect(await screen.findByText('Cost estimation is off')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Estimate cost/ })).not.toBeInTheDocument();
    // Plan changes stay reviewable; the estimate never gates the plan.
    expect(posts).toHaveLength(0);
    await userEvent.click(screen.getByRole('button', { name: /Review data disclosure/ }));
    const dialog = screen.getByRole('dialog', { name: /Infracost CLI/ });
    expect(within(dialog).getByText(/This calls an external pricing API/)).toBeInTheDocument();
    expect(within(dialog).getAllByText('pricing.api.infracost.io').length).toBeGreaterThan(0);
    expect(within(dialog).getByText(/Cloud credentials: the CLI gets no AWS/)).toBeInTheDocument();
    expect(within(dialog).getByText(/never asks for, shows or stores it/)).toBeInTheDocument();
    const enable = within(dialog).getByRole('button', { name: /Turn on for this session/ });
    expect(enable).toBeDisabled();
    expect(posts).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole('checkbox'));
    await userEvent.click(enable);
    expect(posts).toEqual([{ url: '/api/v1/cost/opt-in', body: { acknowledged: true, disclosureVersion: '1' } }]);
    expect(await screen.findByRole('button', { name: /Estimate cost/ })).toBeInTheDocument();
    expect(posts.some(p => p.url.includes('cost-estimates'))).toBe(false);
  });

  it('estimates only on request and shows currency, coverage and unpriced resources without amounts', async () => {
    let latest: CostEstimate | null = null;
    serve(ready, null, (url, init) => {
      if (url.endsWith(`/plans/${planId}/cost-estimates`) && init?.method === 'POST') {
        latest = estimate;
        return Response.json({ ...estimate, status: 'running', breakdown: undefined }, { status: 202 });
      }
      if (url.endsWith(`/plans/${planId}/cost-estimate`)) return latest ? Response.json(latest) : Response.json({ error: 'estimate_not_found' }, { status: 404 });
      return undefined;
    });
    await openCost();
    const start = await screen.findByRole('button', { name: /Estimate cost/ });
    expect(posts).toHaveLength(0);
    await userEvent.click(start);
    expect(posts).toEqual([{ url: `/api/v1/plans/${planId}/cost-estimates`, body: {} }]);
    expect(await screen.findByText(/Estimating with Infracost/)).toBeInTheDocument();
    const delta = await screen.findByText((_, el) => el?.classList.contains('cost-delta') ?? false, {}, { timeout: 3000 });
    expect(delta).toHaveTextContent('+9.89 USD / month');
    expect(screen.getByText(/Estimated recurring monthly change \(partial\)/)).toBeInTheDocument();
    expect(screen.getByText(/Covers 3 of 5 cost-bearing changes \(60%\)\. 3 without a direct price\./)).toBeInTheDocument();
    expect(screen.getAllByText(/not your cloud bill/).length).toBeGreaterThan(0);
    const items = screen.getByRole('table', { name: 'Changed resources' });
    const row = (address: string) => within(items).getByText(address).closest('tr')!;
    expect(row('module.compute.aws_instance.main[0]')).toHaveTextContent('+7.59 USD');
    expect(row('aws_db_instance.primary')).toHaveTextContent(/Partly estimated.*14\.71 USD.*17\.01 USD.*\+2\.30 USD/);
    // Unsupported, usage-based, free and forgotten resources carry labels, never amounts.
    for (const [address, label] of [['aws_s3_bucket.logs', 'Usage-based'], ['aws_iam_role.legacy', 'No direct price'], ['random_password.unused', 'Not estimated']]) {
      expect(row(address)).toHaveTextContent(label);
      expect(row(address)).not.toHaveTextContent('USD');
    }
    const types = screen.getByRole('table', { name: 'By resource type' });
    expect(within(types).getByText('aws_s3_bucket').closest('tr')).toHaveTextContent('Not estimated');
    expect(within(types).getByText('aws_iam_role').closest('tr')).toHaveTextContent('No direct price');
    expect(screen.getByText(/Infracost 0\.10\.39/, { selector: '.cost-provenance' })).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(canary);
  });

  it('marks a stale estimate and refuses to estimate a plan whose context changed', async () => {
    serve(ready, { ...estimate, stale: true, staleReason: 'The project context changed since this plan was made.' });
    await openCost({ ...plan, contextChanged: true });
    expect(await screen.findByText(/Out of date: The project context changed/)).toBeInTheDocument();
    expect(screen.getByText(/Run a new plan to estimate current changes/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Estimate/ })).not.toBeInTheDocument();
  });

  it('shows a failed estimate without affecting the plan', async () => {
    serve(ready, { ...estimate, status: 'failed', breakdown: undefined, error: { code: 'pricing_unavailable', message: 'The Infracost pricing API could not be reached.' } });
    await openCost();
    expect(await screen.findByText('Estimate failed')).toBeInTheDocument();
    expect(screen.getByText(/pricing API could not be reached/)).toBeInTheDocument();
    expect(screen.getByText(/The plan is unaffected and can still be applied/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Review and apply/ })).toBeInTheDocument();
  });

  it('explains strict local-only mode and a missing CLI', async () => {
    serve({ ...off, state: 'strict_local', strictLocal: true, installed: false, reason: 'Cost estimation is not available in strict local-only mode.' }, null);
    await openCost();
    expect(await screen.findByText('Not available in strict local-only mode')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /disclosure|Estimate/ })).not.toBeInTheDocument();
    cleanup();
    serve({ ...off, state: 'cli_missing', installed: false, reason: 'The Infracost CLI (infracost) was not found on PATH. Hclaro never installs it.' }, null);
    await openCost();
    expect(await screen.findByText('Infracost CLI not found')).toBeInTheDocument();
    expect(screen.getByText(/never installs it/)).toBeInTheDocument();
    expect(posts).toHaveLength(0);
  });

  it('labels other currencies and negative changes explicitly', async () => {
    const eur = { ...estimate, breakdown: { ...estimate.breakdown!, currency: 'EUR', monthlyDelta: '-12.40' } };
    serve(ready, eur);
    await openCost();
    const delta = await screen.findByText((_, el) => el?.classList.contains('cost-delta') ?? false);
    expect(delta).toHaveTextContent('−12.40 EUR / month');
    expect(delta).toHaveClass('down');
    expect(signed('0.00')).toBe('0.00');
    expect(signed('3.10')).toBe('+3.10');
  });
});
