import { useCallback, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleX, FileDiff, History, LoaderCircle, Play, ShieldCheck, TriangleAlert } from 'lucide-react';
import { api, post } from '../api';
import type { Operation, Plan, PlanOptions, PlanSummary } from '../types';
import { Button } from '../components/ui/button';
import { OperationLog } from './OperationLog';
import { PlanReview } from './PlanReview';
import { PreflightDialog } from './PreflightDialog';
import { clock } from './labels';

export function PlansView({
  project,
  workspaceStatus,
  planId,
  choosePlan,
  copy
}: {
  project: string;
  workspaceStatus?: string;
  planId?: string;
  choosePlan: (id?: string) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const client = useQueryClient();
  const [preflight, setPreflight] = useState(false);
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
  // The newest operation failed or was cancelled after the newest plan: say so plainly.
  const failed = latestOperation && latestOperation.status !== 'running' && latestOperation.status !== 'succeeded' && !planId ? latestOperation : undefined;
  const finished = useCallback((op: Operation) => {
    void client.invalidateQueries({ queryKey: ['operations'] });
    void client.invalidateQueries({ queryKey: ['plans'] });
    if (op.planId) choosePlan(op.planId);
  }, [client, choosePlan]);
  async function start(varFiles: string[]) {
    setStarting(true);
    setStartError('');
    try {
      await post<Operation>('plans', { acknowledgeBackendAccess: true, varFiles });
      setPreflight(false);
      choosePlan(undefined);
      await client.invalidateQueries({ queryKey: ['operations'] });
    } catch (e) {
      setStartError(e instanceof Error ? e.message : 'The plan could not be started.');
    } finally {
      setStarting(false);
    }
  }
  const canPlan = !!options.data?.canPlan && !running;
  return <div className="plans">
   <div className="plans-toolbar">
    <Button onClick={() => { setStartError(''); setPreflight(true); }} disabled={!canPlan}><Play size={14} />Run plan</Button>
    {plans.data && plans.data.plans.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Plan history</span><select aria-label="Plan history" value={shownId} onChange={e => choosePlan(e.target.value)}>{plans.data.plans.map((p, i) => <option key={p.id} value={p.id}>{i === 0 ? 'Latest · ' : ''}{clock(p.createdAt)} · {p.changes} change{p.changes === 1 ? '' : 's'}{p.errored ? ' · errored' : ''}</option>)}</select></label>}
    <span className="toolbar-note"><ShieldCheck size={13} />Plans are reviewed here and never applied by Hclaro.</span>
   </div>
   {workspaceStatus === 'loading' ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before planning is available…</div> : options.data && !options.data.canPlan && <div className="plan-warning" role="note"><TriangleAlert size={15} />{options.data.reason || 'Planning is not available for this workspace.'}</div>}
   {running && <OperationLog key={running.id} operation={running} onFinished={finished} />}
   {failed && <FailedOperation operation={failed} />}
   {failed && plan.data && <p className="info-line">Below is the previous successful plan, created at {clock(plan.data.createdAt)}. It does not reflect the run above.</p>}
   {plan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading plan…</p></div> : plan.error ? <div className="empty-state error-state" role="alert"><p>{plan.error.message}</p></div> : plan.data ? <PlanReview key={plan.data.id} plan={plan.data} copy={copy} /> : !running && !failed && <div className="empty-state"><FileDiff size={30} /><h2>No plans yet</h2><p>Run a plan to see every proposed change: creates, updates, replacements, deletions, data reads and output changes. Nothing runs until you confirm.</p>{options.data?.canPlan && <Button variant="outline" onClick={() => setPreflight(true)}><Play size={14} />Run plan</Button>}</div>}
   {preflight && options.data && <PreflightDialog options={options.data} project={project} busy={starting} error={startError} onConfirm={varFiles => void start(varFiles)} onClose={() => setPreflight(false)} />}
  </div>;
}

function FailedOperation({ operation }: { operation: Operation }) {
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{operation.status === 'cancelled' ? 'Plan cancelled' : 'Plan failed'}</h2>
   <p>{operation.diagnostic?.message}</p>
   <p className="muted">No plan was kept from this run, so there is nothing to review or apply.</p>
   {operation.engineDiagnostics.length > 0 && <ul className="diagnostics">{operation.engineDiagnostics.map((d, i) => <li key={i} className={`diag-${d.severity}`}><strong>{d.severity === 'error' ? 'Error' : 'Warning'}: {d.summary}</strong>{d.address && <code>{d.address}</code>}{d.detail && <pre>{d.detail}</pre>}</li>)}</ul>}
  </section>;
}
