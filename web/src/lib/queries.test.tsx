import { renderHook, waitFor, cleanup } from '@testing-library/react';
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Operation } from '../types';
import { activeOperation, operationsQuery } from './queries';
import { launchScope } from './scope';

const op = (id: string, status: Operation['status']) => ({ id, kind: 'plan', status, stage: status, startedAt: '2026-11-19T09:00:00Z', command: [] }) as unknown as Operation;

// Serves each status in turn, then repeats the last one; counts requests.
function serve(statuses: Operation['status'][]) {
  let calls = 0;
  vi.stubGlobal('fetch', vi.fn(async () => {
    const status = statuses[Math.min(calls, statuses.length - 1)];
    calls++;
    return Response.json({ operations: [op('a', status)] });
  }));
  return () => calls;
}

function render() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return renderHook(() => useQuery(operationsQuery(launchScope)), { wrapper });
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('operationsQuery', () => {
  // B02: a queued or cancelling operation is active, so the list keeps polling until it ends.
  it('keeps polling through queued and cancelling, and stops at a terminal state', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = serve(['queued', 'queued', 'cancelling', 'cancelled']);
    const { result } = render();
    await waitFor(() => expect(result.current.data?.operations[0].status).toBe('queued'));
    for (const want of ['queued', 'cancelling', 'cancelled']) {
      await vi.advanceTimersByTimeAsync(2000);
      await waitFor(() => expect(result.current.data?.operations[0].status).toBe(want));
    }
    const settled = calls();
    await vi.advanceTimersByTimeAsync(10000);
    expect(calls()).toBe(settled);
  });

  it('does not poll when every operation has ended', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = serve(['succeeded']);
    const { result } = render();
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await vi.advanceTimersByTimeAsync(10000);
    expect(calls()).toBe(1);
  });

  it('is disabled without a scope', () => {
    expect(operationsQuery(undefined).enabled).toBe(false);
  });
});

describe('activeOperation', () => {
  it('picks the newest queued, running or cancelling operation', () => {
    expect(activeOperation([op('new', 'succeeded'), op('q', 'queued'), op('r', 'running')])?.id).toBe('q');
    expect(activeOperation([op('c', 'cancelling')])?.id).toBe('c');
    expect(activeOperation([op('a', 'failed'), op('b', 'cancelled'), op('c', 'unknown')])).toBeUndefined();
    expect(activeOperation(undefined)).toBeUndefined();
  });
});
