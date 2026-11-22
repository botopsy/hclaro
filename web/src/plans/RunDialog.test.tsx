import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CloudCog, LockKeyhole, ShieldCheck } from 'lucide-react';
import { RunDialog, RunState, runFailure, type RunUses } from './RunDialog';
import type { ExecutionContext, ExecutionSummary, Operation, ScopeView } from '../types';

const facts = [
  { icon: CloudCog, title: 'Contacts providers and the local backend', detail: 'using your credentials.' },
  { icon: LockKeyhole, title: 'May lock the state', detail: 'while planning.' },
  { icon: ShieldCheck, title: 'Does not change infrastructure.', detail: 'Planning only.' }
];
const launch: RunUses = { project: 'network', engine: 'terraform', version: '1.9.8', workspace: 'default', backend: 'local', executionMode: 'local-cli' };
const scope: ScopeView = { contextId: 'a'.repeat(32), projectId: 'p1', alias: 'billing', rootPath: '/repo/billing', engine: 'tofu', version: '1.8.0', workspace: 'dev', profile: { id: 'x', name: 'staging', revision: 2 }, backend: 's3', executionMode: 'local-cli' };
const op = (o: Partial<Operation> = {}): Operation => ({
  id: 'op1', kind: 'plan', status: 'running', stage: 'Planning', startedAt: '2026-11-05T12:04:00Z', durationMs: 4000, command: ['terraform', 'plan'],
  varFiles: [], replace: [], stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true, ...o
});
const line = (seq: number, text: string, level = 'info') => `event: log\ndata: ${JSON.stringify({ seq, time: '2026-11-05T12:04:0' + seq + 'Z', level, text })}\n\n`;
const status = (o: Operation) => `event: status\ndata: ${JSON.stringify(o)}\n\nevent: end\ndata: {}\n\n`;

let stream = '';
let cancels = 0;
beforeEach(() => {
  cancels = 0;
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    if (url.endsWith('/cancel') && init.method === 'POST') {
      cancels++;
      return Response.json(op({ cancelRequested: true }));
    }
    if (url === '/api/v1/session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (url.includes('/events')) return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('RunDialog', () => {
  it('names the command, lists three facts and what the run will use; runs only on the button', () => {
    const run = vi.fn();
    render(<RunDialog title="Run a plan" sentence={<>Terraform will run <code>plan</code>.</>} facts={facts} noun="plan" uses={launch} action="Run plan" onRun={run} onClose={() => {}} />);
    const dialog = screen.getByRole('dialog', { name: 'Run a plan' });
    expect(dialog.querySelectorAll('.preflight-list li')).toHaveLength(3);
    const box = within(screen.getByRole('region', { name: 'This plan will use' }));
    expect(box.getByText('network')).toBeInTheDocument();
    expect(box.getByText('Terraform 1.9.8')).toBeInTheDocument();
    expect(box.getByText('local · local-cli')).toBeInTheDocument();
    expect(box.getByText('None (inherited environment)')).toBeInTheDocument();
    expect(run).not.toHaveBeenCalled();
    const button = screen.getByRole('button', { name: 'Run plan' });
    expect(button).toHaveFocus();
    fireEvent.click(button);
    expect(run).toHaveBeenCalledOnce();
  });

  it('reads a Hub scope: project, root folder, this-tab workspace and the recorded profile', () => {
    render(<RunDialog title="Check drift" sentence="x" facts={facts} noun="scan" uses={{ ...launch, scope, executionContext: { profileId: 'x', profileName: 'staging', awsProfile: 'stg', awsRegion: 'eu-west-1' } as ExecutionContext }} action="Check drift" onRun={() => {}} onClose={() => {}} />);
    const box = within(screen.getByRole('region', { name: 'This scan will use' }));
    expect(box.getByText('billing')).toBeInTheDocument();
    expect(box.getByText('/repo/billing')).toBeInTheDocument();
    expect(box.getByText('dev')).toBeInTheDocument();
    expect(box.getByText(/this tab; the CLI default is not changed/)).toBeInTheDocument();
    expect(box.getByText('OpenTofu 1.8.0')).toBeInTheDocument();
    expect(box.getByText('staging')).toBeInTheDocument();
    expect(box.getByText(/AWS_PROFILE=stg · eu-west-1/)).toBeInTheDocument();
  });

  it('cannot run while the context has issues or the page blocks it', () => {
    const context = { alias: 'network', rootPath: '/r', workspace: 'default', workspaceSource: 'launch', engine: 'terraform', backend: 'local', executionMode: 'local-cli', issues: [{ code: 'env_missing', message: 'TF_VAR_x is missing.' }], context: { options: {}, env: [], notices: [], varFiles: [], fingerprint: 'f' } } as unknown as ExecutionSummary;
    const { rerender } = render(<RunDialog title="Run a plan" sentence="x" facts={facts} noun="plan" uses={{ ...launch, context }} action="Run plan" onRun={() => {}} onClose={() => {}} />);
    expect(screen.getByRole('alert')).toHaveTextContent('TF_VAR_x is missing.');
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    rerender(<RunDialog title="Run a plan" sentence="x" facts={facts} noun="plan" uses={launch} action="Run plan" blockedReason="Another operation is running." onRun={() => {}} onClose={() => {}} />);
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('Another operation is running.');
  });
});

describe('runFailure', () => {
  const failed = (o: Partial<Operation>) => runFailure(op({ status: 'failed', ...o }));
  it('names the lock without its holder', () => {
    expect(failed({ diagnostic: { code: 'state_locked', message: 'm' } }).title).toBe('State is locked by another process');
  });
  it('covers an unreachable backend and expired credentials with the profile', () => {
    expect(failed({ diagnostic: { code: 'network', message: 'm' } }).title).toBe('Backend unreachable');
    const creds = failed({ diagnostic: { code: 'credentials', message: 'm' }, executionContext: { awsProfile: 'stg' } as ExecutionContext });
    expect(creds.title).toBe('Credentials missing or expired');
    expect(creds.text).toContain('with profile stg');
  });
  it('shows the exit code and the first error line of an engine error', () => {
    const e = failed({ exitCode: 1, diagnostic: { code: 'plan_failed', message: 'The plan failed.' }, engineDiagnostics: [{ severity: 'warning', summary: 'w' }, { severity: 'error', summary: 'Invalid reference' }] });
    expect(e).toEqual({ title: 'Engine error (exit 1)', text: 'Invalid reference' });
    expect(failed({ diagnostic: { code: 'plan_failed', message: 'The plan failed.' } }).text).toBe('The plan failed.');
  });
});

describe('RunState', () => {
  it('while running shows the start time, the last four log lines and Cancel', async () => {
    stream = [1, 2, 3, 4, 5, 6].map(n => line(n, `line ${n}`)).join('') + status(op());
    render(<RunState operation={op()} running="Planning…" succeeded="Plan ready" onRetry={() => {}} />);
    expect(await screen.findByText('line 6')).toBeInTheDocument();
    expect(screen.queryByText('line 2')).not.toBeInTheDocument();
    expect(screen.getByText('line 3')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Planning…');
    expect(screen.getByText(/^started /)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Cancelling…'));
    expect(cancels).toBe(1);
  });

  it('when it fails shows the cause, the full log on request and Try again', async () => {
    stream = [1, 2, 3, 4, 5].map(n => line(n, `line ${n}`)).join('') + status(op({ status: 'failed', diagnostic: { code: 'state_locked', message: 'm' } }));
    const retry = vi.fn();
    const finished = vi.fn();
    render(<RunState operation={op()} running="Planning…" succeeded="Plan ready" onRetry={retry} onFinished={finished} />);
    expect(await screen.findByRole('heading', { name: 'State is locked by another process' })).toBeInTheDocument();
    expect(finished).toHaveBeenCalledWith(expect.objectContaining({ status: 'failed' }));
    fireEvent.click(screen.getByRole('button', { name: 'View log' }));
    expect(screen.getByText('line 1')).toBeInTheDocument();
    expect(screen.getByText('line 5')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it('when it succeeds says so', async () => {
    stream = line(1, 'done') + status(op({ status: 'succeeded' }));
    render(<RunState operation={op()} running="Planning…" succeeded="Plan ready" onRetry={() => {}} />);
    expect(await screen.findByRole('heading', { name: 'Plan ready' })).toBeInTheDocument();
  });
});
