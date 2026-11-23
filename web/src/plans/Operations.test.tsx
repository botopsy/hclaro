import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { OperationsView } from './OperationsView';
import type { Operation } from '../types';

const operation: Operation = { id: 'b'.repeat(32), kind: 'plan', status: 'succeeded', stage: 'Finished', startedAt: '2026-11-05T09:00:00Z', durationMs: 4200, exitCode: 0, command: ['terraform', 'plan', '-input=false', '-json', '-out=<private temporary file>'], varFiles: [], replace: [], planId: 'c'.repeat(32), stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: false };

function view(operations: Operation[]) {
  const fetch = vi.fn(async () => Response.json({ operations }));
  vi.stubGlobal('fetch', fetch);
  const openPlan = vi.fn();
  const openPlans = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><OperationsView openPlan={openPlan} openPlans={openPlans} /></QueryClientProvider>);
  return { fetch, openPlan, openPlans };
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Operations', () => {
  it('shows an empty state that only navigates to Plans', async () => {
    const user = userEvent.setup();
    const { fetch, openPlans } = view([]);
    expect(await screen.findByText('No operations in this session')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Go to Plans' }));
    expect(openPlans).toHaveBeenCalledOnce();
    expect(fetch).toHaveBeenCalledOnce();
    expect(screen.getByText('How this list is kept')).toBeInTheDocument();
  });

  it('keeps the command in the row details and links to the plan', async () => {
    const user = userEvent.setup();
    const { openPlan } = view([operation]);
    const table = await screen.findByRole('table', { name: 'Operations' });
    expect(table).not.toHaveTextContent('terraform plan');
    await user.click(within(table).getByRole('button', { name: 'Expand Plan' }));
    expect(within(table).getByText(operation.command.join(' '))).toBeInTheDocument();
    expect(within(table).getByText(/no longer kept/)).toBeInTheDocument();
    await user.click(within(table).getByRole('button', { name: /Review plan/ }));
    expect(openPlan).toHaveBeenCalledWith(operation.planId);
  });
});
