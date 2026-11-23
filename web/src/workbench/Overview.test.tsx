import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DriftScan, GitContext, Operation, PlanSummary, Workspace } from '../types';
import { launchScope } from '../lib/scope';
import { Overview, driftRow, gitRow, planRow } from './Overview';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const snapshot = 'Snapshot of recorded state, not live cloud inventory. State reads may contact the configured backend.';
const ws = (info: Partial<Workspace['info']> = {}): Workspace => ({
  status: 'ready',
  info: { project: 'demo', engine: 'terraform', version: '1.9.0', workspace: 'default', backend: 'local', executionMode: 'local', initialized: true, providerLock: true, warnings: [snapshot], capabilities: {} as Workspace['info']['capabilities'], ...info },
  managed: 3,
  data: 1,
  modules: [{ address: '', parent: '', count: 2 }, { address: 'module.net', parent: '', count: 2 }],
  providers: { 'registry.terraform.io/hashicorp/aws': 4 },
  types: { aws_instance: 2, aws_s3_bucket: 1, aws_caller_identity: 1 }
});
const plan = (extra: Partial<PlanSummary> = {}) => ({ id: 'p1', changes: 3, errored: false, ...extra }) as PlanSummary;
const op = (extra: Partial<Operation>) => ({ id: 'o1', kind: 'plan', status: 'succeeded', ...extra }) as Operation;
const scan = (extra: Partial<DriftScan> = {}) => ({ status: 'completed', current: true, outcome: 'no_drift', summary: { changed: 0, missing: 0, cannotVerify: 0, unrecognized: 0, total: 0, outputChanges: 0 }, ...extra }) as DriftScan;
const repo = (changed?: number): GitContext => ({ state: 'repository', head: { branch: 'main', commit: 'abcdef1234', detached: false, unborn: false, worktree: false }, clean: !changed, files: [], counts: { changed: changed ?? 0, untracked: 0, outside: 0, other: 0 }, warnings: [], incomplete: false, checkedAt: '' });

describe('Overview rows', () => {
  it('describes the plan from stored plans and operations, never running one', () => {
    expect(planRow([], [])).toMatchObject({ tone: 'neutral', text: 'No plan yet', action: 'Run plan', target: 'Plans' });
    expect(planRow([plan()], [])).toMatchObject({ tone: 'update', text: '3 pending changes · not applied', action: 'Review plan' });
    expect(planRow([plan({ changes: 0 })], [])).toMatchObject({ tone: 'ok', action: undefined });
    expect(planRow([plan()], [op({ status: 'failed' })])).toMatchObject({ tone: 'delete', text: 'The last plan failed' });
    expect(planRow([plan()], [op({ kind: 'apply', planId: 'p1', outcome: 'applied' })])).toMatchObject({ tone: 'ok' });
    expect(planRow([plan()], [op({ status: 'running' })])).toMatchObject({ text: 'A plan is running' });
  });

  it('describes drift from the latest scan only while it matches the workspace', () => {
    expect(driftRow(undefined)).toMatchObject({ text: 'Not checked yet', action: 'Check drift', target: 'Drift' });
    expect(driftRow([scan()])).toMatchObject({ tone: 'ok', text: 'No drift' });
    expect(driftRow([scan({ outcome: 'drift_detected', summary: { changed: 1, missing: 1, cannotVerify: 0, unrecognized: 0, total: 2, outputChanges: 0 } })])).toMatchObject({ tone: 'update', text: '1 changed · 1 missing remotely' });
    expect(driftRow([scan({ current: false })])).toMatchObject({ tone: 'neutral', action: 'Check drift' });
  });

  it('takes the branch from HEAD and clean or dirty only from a cached git status', () => {
    expect(gitRow(undefined, undefined)).toBeUndefined();
    expect(gitRow(repo(), undefined)).toMatchObject({ tone: 'neutral', action: 'Open Git' });
    expect(gitRow(repo(), repo(0))).toMatchObject({ tone: 'ok', text: 'main@abcdef1 · IaC tree clean' });
    expect(gitRow(repo(), repo(2))).toMatchObject({ tone: 'update', text: 'main@abcdef1 · 2 uncommitted IaC files' });
  });
});

function show(workspace: Workspace, seed: { plans?: PlanSummary[]; head?: GitContext; status?: GitContext } = {}, drift?: DriftScan[]) {
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
  client.setQueryData(['plans', 'launch'], { plans: seed.plans ?? [] });
  client.setQueryData(['operations', 'launch'], { operations: [] });
  client.setQueryData(['git-head', ''], seed.head ?? repo());
  if (seed.status) client.setQueryData(['git-status', ''], seed.status);
  const fetch = vi.spyOn(globalThis, 'fetch');
  const calls = { open: vi.fn(), type: vi.fn(), mode: vi.fn() };
  render(<QueryClientProvider client={client}><Overview workspace={workspace} scope={launchScope} gitPrefix="" root="/work/demo" drift={drift} open={calls.open} chooseType={calls.type} chooseMode={calls.mode} /></QueryClientProvider>);
  return { ...calls, fetch };
}

describe('Overview', () => {
  it('lists what needs attention with links, and links the stats strip and bars to filtered pages', async () => {
    const user = userEvent.setup();
    const { open, type, mode, fetch } = show(ws({ providerLock: false }), { plans: [plan()] });
    const attention = screen.getByRole('region', { name: 'Needs attention' });
    expect(within(attention).getByText('3 pending changes · not applied')).toBeInTheDocument();
    expect(within(attention).getByText('Not checked yet')).toBeInTheDocument();
    expect(within(attention).getByText(/Provider lockfile not detected/)).toBeInTheDocument();
    await user.click(within(attention).getByRole('button', { name: 'Review plan →' }));
    expect(open).toHaveBeenLastCalledWith('Plans');
    await user.click(within(attention).getByRole('button', { name: 'Providers →' }));
    expect(open).toHaveBeenLastCalledWith('Providers');
    const stats = screen.getByRole('navigation', { name: 'Counts' });
    await user.click(within(stats).getByRole('button', { name: '3 managed' }));
    expect(mode).toHaveBeenLastCalledWith('managed');
    await user.click(within(stats).getByRole('button', { name: '1 data source' }));
    expect(mode).toHaveBeenLastCalledWith('data');
    await user.click(within(stats).getByRole('button', { name: '1 child module' }));
    expect(open).toHaveBeenLastCalledWith('Modules');
    await user.click(screen.getByRole('button', { name: /aws_s3_bucket/ }));
    expect(type).toHaveBeenLastCalledWith('aws_s3_bucket');
    // The snapshot note is a tooltip, not a line on the page; the rest is in Details.
    expect(screen.queryByText(/Snapshot of recorded state/)).toBeNull();
    await user.click(screen.getByRole('button', { name: 'About these counts' }));
    expect(screen.getByRole('dialog', { name: 'About these counts' })).toHaveTextContent('Snapshot of recorded state');
    const details = screen.getByRole('region', { name: 'Details' });
    expect(details).toHaveTextContent('Root folder/work/demo');
    expect(details).toHaveTextContent('Branchmain@abcdef1');
    expect(details).toHaveTextContent('LockfileNot detected');
    for (const gone of ['Project', 'Workspace', 'Execution mode', 'Snapshot']) expect(within(details).queryByText(gone)).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('shows one green line when nothing needs action', () => {
    show(ws(), { plans: [plan({ changes: 0 })], status: repo(0) }, [scan()]);
    const attention = screen.getByRole('region', { name: 'Needs attention' });
    expect(attention).toHaveTextContent(/^Needs attentionNothing needs attention$/);
  });
});
