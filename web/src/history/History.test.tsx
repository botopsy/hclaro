import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go API's golden output keeps the frontend honest about the contract.
import golden from '../../../fixtures/history/history.golden.json';
import { HistoryView } from './HistoryView';
import type { HistoryEntry, HistoryPage, PlanComparison } from '../types';

const page = golden.page as unknown as HistoryPage;
const plan = golden.plan as unknown as HistoryEntry;
const apply = golden.apply as unknown as HistoryEntry;
const failed = golden.failed as unknown as HistoryEntry;
const comparison = golden.compare as unknown as PlanComparison;
const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const second = page.entries.find(e => e.kind === 'plan' && e.id !== plan.id && e.status === 'succeeded')!;

type Route = (url: string, init?: RequestInit) => Response | undefined;
let requests: { method: string; url: string; body?: unknown }[] = [];
function serve(...routes: Route[]) {
  requests = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ method: init?.method ?? 'GET', url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.includes('/history/compare?')) return Response.json(comparison);
    if (url.includes('/history/export?')) return new Response('# Hclaro operation history', { headers: { 'Content-Type': 'text/markdown' } });
    if (url.includes('/api/v1/history?')) return Response.json(page);
    for (const e of [plan, apply, failed]) {
      if (url.endsWith(`/history/${e.id}`)) return Response.json({ ...e, live: false });
    }
    if (url.includes('/history/')) return Response.json({ ...page.entries.find(e => url.endsWith(e.id)), live: false });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view() {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><HistoryView copy={async () => {}} /></QueryClientProvider>);
}
function mutations() {
  return requests.filter(r => r.method !== 'GET');
}
beforeEach(() => serve());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Operation history', () => {
  it('lists recorded operations with status, change chips and honest results', async () => {
    view();
    const table = await screen.findByRole('table');
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(page.entries.length);
    expect(within(rows[0]).getByText('Failed')).toBeInTheDocument();
    expect(within(rows[0]).getByText('No plan kept')).toBeInTheDocument();
    const applyRow = rows.find(r => within(r).queryByText('Apply'))!;
    expect(within(applyRow).getByText('Engine reported applied')).toBeInTheDocument();
    const planRow = rows.find(r => r.textContent?.includes('Planned · not applied') && r.textContent.includes('Engine reported applied'))!;
    expect(planRow).toBeTruthy();
    expect(within(planRow).getByTitle(/to create/)).toBeInTheDocument();
    expect(screen.getByText(`${page.total} of ${page.stored}`)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(canary);
    // Nothing is written while browsing.
    expect(mutations()).toHaveLength(0);
  });

  it('sends filters to the server and resets them', async () => {
    const user = userEvent.setup();
    view();
    await screen.findByRole('table');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by project' }), page.projects[0].id);
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by workspace' }), 'demo');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by status' }), 'failed');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by operation' }), 'apply');
    await user.type(screen.getByRole('textbox', { name: 'Search history' }), 'aws_instance');
    await waitFor(() => {
      const last = new URL(requests.filter(r => r.url.includes('/api/v1/history?')).at(-1)!.url, 'http://x');
      expect(last.searchParams.get('projectId')).toBe(page.projects[0].id);
      expect(last.searchParams.get('workspace')).toBe('demo');
      expect(last.searchParams.get('status')).toBe('failed');
      expect(last.searchParams.get('kind')).toBe('apply');
      expect(last.searchParams.get('query')).toBe('aws_instance');
      expect(last.searchParams.get('offset')).toBe('0');
    });
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    await waitFor(() => expect(new URL(requests.filter(r => r.url.includes('/api/v1/history?')).at(-1)!.url, 'http://x').searchParams.has('projectId')).toBe(false));
  });

  it('separates the planned stage from the executed stage and never offers to apply', async () => {
    const user = userEvent.setup();
    view();
    const table = await screen.findByRole('table');
    const planRow = within(table).getAllByRole('row').find(r => r.textContent?.includes('Planned · not applied') && r.textContent.includes('Engine reported applied'))!;
    await user.click(within(planRow).getByRole('button', { name: /^Plan/ }));
    const drawer = await screen.findByRole('complementary', { name: 'Operation details' });
    const planned = await within(drawer).findByRole('region', { name: /Planned/ });
    expect(within(planned).getByText(plan.plan!.changes[0].address)).toBeInTheDocument();
    expect(within(planned).getByText(/cannot be applied from history/)).toBeInTheDocument();
    const executed = within(drawer).getByRole('region', { name: /Executed/ });
    expect(within(executed).getByText(/Applied by a later operation/)).toBeInTheDocument();
    expect(within(executed).getByText(/not proof that resources are healthy/)).toBeInTheDocument();
    expect(within(drawer).queryByRole('button', { name: /apply/i })).toBeNull();
    expect(within(drawer).getByRole('list', { name: 'Sanitized log excerpt' })).toBeInTheDocument();
  });

  it('shows an apply as not verified and a failed plan with its diagnostic', async () => {
    const user = userEvent.setup();
    view();
    const table = await screen.findByRole('table');
    await user.click(within(table).getAllByRole('button', { name: /^Apply/ })[0]);
    const outcome = await screen.findByRole('region', { name: /Outcome/ });
    expect(within(outcome).getByText(/Not verified against your provider/)).toBeInTheDocument();
    expect(within(outcome).getByText('Engine reported applied')).toBeInTheDocument();
    await user.click(within(table).getAllByRole('row')[1].querySelector('button')!);
    const failedOutcome = await screen.findByText('plan failed');
    expect(failedOutcome).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(canary);
  });

  it('compares two compatible plan summaries only', async () => {
    const user = userEvent.setup();
    view();
    await screen.findByRole('table');
    const compare = screen.getByRole('button', { name: /Compare summaries/ });
    expect(compare).toBeDisabled();
    // Applies and failed plans have no checkbox; only plans with a summary do.
    const boxes = screen.getAllByRole('checkbox');
    expect(boxes).toHaveLength(page.entries.filter(e => e.plan && e.kind !== 'apply').length);
    for (const box of boxes) await user.click(box);
    expect(compare).toBeEnabled();
    await user.click(compare);
    const dialog = await screen.findByRole('dialog', { name: 'Compare plan summaries' });
    await within(dialog).findByText(`${comparison.added.length} newly planned`);
    expect(requests.some(r => r.url.includes(`/history/compare?left=`) && r.url.includes(plan.id) && r.url.includes(second.id))).toBe(true);
    for (const n of comparison.notices) expect(within(dialog).getByText(n)).toBeInTheDocument();
  });

  it('deletes one entry only after confirmation', async () => {
    const user = userEvent.setup();
    serve((_url, init) => init?.method === 'DELETE' ? Response.json({ deleted: true }) : undefined);
    view();
    const table = await screen.findByRole('table');
    await user.click(within(table).getAllByRole('row')[1].querySelector('button')!);
    await user.click(await screen.findByRole('button', { name: 'Delete from history' }));
    expect(screen.getByText(/does not change infrastructure, state or files/)).toBeInTheDocument();
    expect(mutations()).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Delete entry' }));
    await waitFor(() => expect(mutations()).toEqual([{ method: 'DELETE', url: `/api/v1/history/${failed.id}`, body: {} }]));
  });

  it('clears the filtered project or everything, and saves retention', async () => {
    const user = userEvent.setup();
    serve((_url, init) => init?.method === 'DELETE' ? Response.json({ removed: 4 }) : init?.method === 'PATCH' ? Response.json(JSON.parse(String(init.body))) : undefined);
    view();
    await screen.findByRole('table');
    await user.selectOptions(screen.getByRole('combobox', { name: 'Filter by project' }), page.projects[0].id);
    await user.click(screen.getByRole('button', { name: /Clear…/ }));
    const dialog = screen.getByRole('dialog', { name: 'Clear operation history' });
    expect(within(dialog).getByText(/does not change infrastructure, state, plans or project files/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: /Clear history/ }));
    await waitFor(() => expect(mutations().at(-1)).toEqual({ method: 'DELETE', url: '/api/v1/history', body: { confirm: true, projectId: page.projects[0].id } }));

    await user.click(screen.getByRole('button', { name: /Retention/ }));
    const retention = screen.getByRole('dialog', { name: 'History retention' });
    await user.clear(within(retention).getByLabelText('Operations to keep'));
    await user.type(within(retention).getByLabelText('Operations to keep'), '100');
    await user.clear(within(retention).getByLabelText('Days to keep'));
    await user.type(within(retention).getByLabelText('Days to keep'), '30');
    await user.click(within(retention).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(mutations().at(-1)).toEqual({ method: 'PATCH', url: '/api/v1/history/retention', body: { maxEntries: 100, maxAgeDays: 30, maxBytes: page.retention.maxBytes } }));
  });

  it('shows a refusal reason, an empty history and a read-only notice', async () => {
    serve(url => url.includes('/api/v1/history?') ? Response.json({ ...page, status: 'read_only', notice: 'Operation history was written by a newer Hclaro (schema 2).', total: 0, stored: 0, entries: [], projects: [] }) : undefined);
    view();
    expect(await screen.findByText(/written by a newer Hclaro/)).toBeInTheDocument();
    expect(screen.getByText('No operations recorded yet')).toBeInTheDocument();
    expect(screen.getByText(/not being recorded in this session/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Retention/ })).toBeDisabled();
    expect(screen.getByRole('button', { name: /Clear…/ })).toBeDisabled();
  });

  it('exports through an authenticated download, never a server path', async () => {
    const user = userEvent.setup();
    const create = vi.fn(() => 'blob:x');
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: create, revokeObjectURL: () => {} }));
    view();
    await screen.findByRole('table');
    await user.click(screen.getByRole('button', { name: /Markdown/ }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    const exported = requests.find(r => r.url.includes('/history/export?'))!;
    expect(exported.method).toBe('GET');
    expect(new URL(exported.url, 'http://x').searchParams.get('format')).toBe('markdown');
    expect(exported.url).not.toContain('path');
  });
});
