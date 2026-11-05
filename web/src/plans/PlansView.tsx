import { useCallback, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleCheck, CircleX, FileDiff, History, LoaderCircle, Play, ShieldCheck, TriangleAlert } from 'lucide-react';
import { api, post } from '../api';
import type { Operation, Plan, PlanOptions, PlanSummary, ReplaceTarget } from '../types';
import { Button } from '../components/ui/button';
import { ApplyDialog } from './ApplyDialog';
import { OperationLog } from './OperationLog';
import { PlanReview } from './PlanReview';
import { PreflightDialog } from './PreflightDialog';
import { clock } from './labels';

export function PlansView({
  project,
  workspaceStatus,
  planId,
  choosePlan,
  replace,
  clearReplace,
  copy
}: {
  project: string;
  workspaceStatus?: string;
  planId?: string;
  choosePlan: (id?: string) => void;
  // Instances chosen in the explorer; a non-empty list opens the replace preflight.
  replace?: ReplaceTarget[];
  clearReplace?: () => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const client = useQueryClient();
  const [preflight, setPreflight] = useState(false);
  const [applying, setApplying] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState('');
  // Availability is decided after the startup read; refetch when the workspace status changes.
  const options = useQuery({ queryKey: ['plan-options', workspaceStatus], queryFn: () => api<PlanOptions>('plans/options') });
  const plans = useQuery({ queryKey: ['plans'], queryFn: () => api<{ plans: PlanSummary[] }>('plans') });
  const operations = useQuery({
    queryKey: ['operations'],
    queryFn: () => api<{ operations: Operation[] }>('operations'),
    refetchInterval: q => q.state.data?.operations.some(o => o.status === 'running') ? 2000 : false
  });
  const latestOperation = operations.data?.operations[0];
  const running = latestOperation?.status === 'running' ? latestOperation : undefined;
  const shownId = planId ?? plans.data?.plans[0]?.id;
  const plan = useQuery({ queryKey: ['plan', shownId], queryFn: () => api<Plan>(`plans/${shownId}`), enabled: !!shownId });
  const finishedOperation = latestOperation && latestOperation.status !== 'running' ? latestOperation : undefined;
  // The newest plan failed or was cancelled after the newest plan: say so plainly.
  const failed = finishedOperation?.kind === 'plan' && finishedOperation.status !== 'succeeded' && !planId ? finishedOperation : undefined;
  const applied = finishedOperation?.kind === 'apply' && (!planId || planId === finishedOperation.planId) ? finishedOperation : undefined;
  // A replacement chosen in the explorer opens the preflight directly.
  const preflightOpen = preflight || !!replace?.length;
  const finished = useCallback((op: Operation) => {
    void client.invalidateQueries({ queryKey: ['operations'] });
    void client.invalidateQueries({ queryKey: ['plans'] });
    void client.invalidateQueries({ queryKey: ['plan'] });
    if (op.kind === 'apply') {
      // State was re-read after the apply; the explorer must not show the old snapshot.
      void client.invalidateQueries({ queryKey: ['workspace'] });
      void client.invalidateQueries({ queryKey: ['resources'] });
      void client.invalidateQueries({ queryKey: ['resource'] });
    }
    if (op.planId) choosePlan(op.planId);
  }, [client, choosePlan]);
  function closePreflight() {
    setPreflight(false);
    setStartError('');
    clearReplace?.();
  }
  async function start(varFiles: string[]) {
    setStarting(true);
    setStartError('');
    try {
      await post<Operation>('plans', { acknowledgeBackendAccess: true, varFiles, replace: (replace ?? []).map(r => r.id) });
      closePreflight();
      choosePlan(undefined);
      await client.invalidateQueries({ queryKey: ['operations'] });
    } catch (e) {
      setStartError(e instanceof Error ? e.message : 'The plan could not be started.');
    } finally {
      setStarting(false);
    }
  }
  async function applyStarted() {
    setApplying(false);
    await client.invalidateQueries({ queryKey: ['operations'] });
    await client.invalidateQueries({ queryKey: ['plan'] });
  }
  const canPlan = !!options.data?.canPlan && !running;
  return <div className="plans">
   <div className="plans-toolbar">
    <Button onClick={() => { setStartError(''); setPreflight(true); }} disabled={!canPlan}><Play size={14} />Run plan</Button>
    {plans.data && plans.data.plans.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Plan history</span><select aria-label="Plan history" value={shownId} onChange={e => choosePlan(e.target.value)}>{plans.data.plans.map((p, i) => <option key={p.id} value={p.id}>{i === 0 ? 'Latest · ' : ''}{clock(p.createdAt)} · {p.kind === 'replace' ? `replace ${p.replace.length} · ` : ''}{p.changes} change{p.changes === 1 ? '' : 's'}{p.errored ? ' · errored' : ''}</option>)}</select></label>}
    <span className="toolbar-note"><ShieldCheck size={13} />{options.data?.canApply ? 'Nothing is applied until you confirm a reviewed plan.' : 'Plans are reviewed here; applying is unavailable for this workspace.'}</span>
   </div>
   {workspaceStatus === 'loading' && !running ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before planning is available…</div> : options.data && !options.data.canPlan && <div className="plan-warning" role="note"><TriangleAlert size={15} />{options.data.reason || 'Planning is not available for this workspace.'}</div>}
   {running && <OperationLog key={running.id} operation={running} onFinished={finished} />}
   {failed && <FailedOperation operation={failed} />}
   {applied && <ApplyResult operation={applied} />}
   {failed && plan.data && <p className="info-line">Below is the previous successful plan, created at {clock(plan.data.createdAt)}. It does not reflect the run above.</p>}
   {plan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading plan…</p></div> : plan.error ? <div className="empty-state error-state" role="alert"><p>{plan.error.message}</p></div> : plan.data ? <PlanReview key={plan.data.id} plan={plan.data} copy={copy} busy={!!running} onApply={() => setApplying(true)} /> : !running && !failed && <div className="empty-state"><FileDiff size={30} /><h2>No plans yet</h2><p>Run a plan to see every proposed change: creates, updates, replacements, deletions, data reads and output changes. Nothing runs until you confirm.</p>{options.data?.canPlan && <Button variant="outline" onClick={() => setPreflight(true)}><Play size={14} />Run plan</Button>}</div>}
   {preflightOpen && options.data && <PreflightDialog options={options.data} project={project} replace={replace} busy={starting} error={startError} onConfirm={varFiles => void start(varFiles)} onClose={closePreflight} />}
   {applying && plan.data && <ApplyDialog plan={plan.data} onStarted={() => void applyStarted()} onClose={() => setApplying(false)} />}
  </div>;
}

function Diagnostics({ operation }: { operation: Operation }) {
  if (!operation.engineDiagnostics.length) return null;
  return <ul className="diagnostics">{operation.engineDiagnostics.map((d, i) => <li key={i} className={`diag-${d.severity}`}><strong>{d.severity === 'error' ? 'Error' : 'Warning'}: {d.summary}</strong>{d.address && <code>{d.address}</code>}{d.detail && <pre>{d.detail}</pre>}</li>)}</ul>;
}

function FailedOperation({ operation }: { operation: Operation }) {
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{operation.status === 'cancelled' ? 'Plan cancelled' : 'Plan failed'}</h2>
   <p>{operation.diagnostic?.message}</p>
   <p className="muted">No plan was kept from this run, so there is nothing to review or apply.</p>
   <Diagnostics operation={operation} />
  </section>;
}

// Process exit is not the same as infrastructure state: the outcome is reported separately
// from whether state was re-read.
function ApplyResult({ operation }: { operation: Operation }) {
  const ok = operation.status === 'succeeded';
  return <section className={ok ? 'operation-applied' : 'operation-failed'} role={ok ? 'status' : 'alert'}>
   <h2>{ok ? <CircleCheck size={17} /> : <CircleX size={17} />}{ok ? 'Apply finished' : operation.status === 'cancelled' ? 'Apply interrupted' : 'Apply failed'}</h2>
   <p>{ok ? 'The engine reported success.' : operation.diagnostic?.message}</p>
   {operation.outcome === 'uncertain' && <p className="plan-warning"><TriangleAlert size={14} />Infrastructure may be partly changed. Check the resources involved before running another plan.</p>}
   {operation.outcome === 'not_applied' && !ok && <p className="muted">The engine did not apply anything.</p>}
   <p className="muted">{operation.stateReloaded ? `State was re-read at ${clock(operation.completedAt ?? operation.startedAt)}; the explorer shows what the engine recorded, not live cloud inventory.` : 'State was not re-read. Restart Hclaro or check state with your CLI.'} The saved plan was deleted and cannot be applied again.</p>
   <Diagnostics operation={operation} />
  </section>;
}
