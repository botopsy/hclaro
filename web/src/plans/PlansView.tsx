import { useCallback, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleCheck, CircleX, FileDiff, History, LoaderCircle, Play, ShieldCheck, TriangleAlert } from 'lucide-react';
import { api, ApiError, post } from '../api';
import type { ImportTarget, Operation, Plan, PlanOptions, PlanSummary, ReplaceTarget, ScopedPlanOptions } from '../types';
import { Button } from '../components/ui/button';
import { ApplyDialog } from './ApplyDialog';
import { launchScope, scopeKey, scopedPath, type PlanScope } from './routes';
import { OperationLog } from './OperationLog';
import { PlanReview } from './PlanReview';
import { PreflightDialog } from './PreflightDialog';
import { clock } from './labels';

export function PlansView({
  project,
  profileId = '',
  workspaceStatus,
  planId,
  choosePlan,
  replace,
  clearReplace,
  openPreflight,
  preflightOpened,
  copy,
  inspectImpact,
  scope = launchScope,
  reopen
}: {
  project: string;
  // The project or Hub workspace whose plans this view shows. Every request goes to it only.
  scope?: PlanScope;
  // Opens a closed Hub workspace again (after its context changed).
  reopen?: () => void;
  // The execution profile this tab uses ("" for none).
  profileId?: string;
  workspaceStatus?: string;
  planId?: string;
  choosePlan: (id?: string) => void;
  // Instances chosen in the explorer; a non-empty list opens the replace preflight.
  replace?: ReplaceTarget[];
  clearReplace?: () => void;
  // Another view (Drift) asked for a separate normal plan: the preflight opens until closed.
  openPreflight?: boolean;
  preflightOpened?: () => void;
  copy: (value: string, label: string) => Promise<void>;
  // Opens the dependency graph with this plan's changes and the replaced address selected.
  inspectImpact?: (planId: string, address: string) => void;
}) {
  const client = useQueryClient();
  const [preflight, setPreflight] = useState(false);
  const [applying, setApplying] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState('');
  // Availability is decided after the startup read; refetch when the workspace status changes.
  const key = scopeKey(scope);
  const scoped = scope.kind === 'context';
  const options = useQuery({
    queryKey: ['plan-options', key, workspaceStatus, profileId],
    queryFn: async () => scoped ? fromScope(await api<ScopedPlanOptions>(scopedPath(scope, 'plans/options'))) : api<PlanOptions>(`plans/options${profileId ? `?profileId=${profileId}` : ''}`)
  });
  const plans = useQuery({ queryKey: ['plans', key], queryFn: () => api<{ plans: PlanSummary[] }>(scopedPath(scope, 'plans')) });
  const operations = useQuery({
    queryKey: ['operations', key],
    queryFn: () => api<{ operations: Operation[] }>(scopedPath(scope, 'operations')),
    refetchInterval: q => q.state.data?.operations.some(o => o.status === 'running') ? 2000 : false
  });
  const latestOperation = operations.data?.operations[0];
  const running = latestOperation?.status === 'running' ? latestOperation : undefined;
  const shownId = planId ?? plans.data?.plans[0]?.id;
  const plan = useQuery({ queryKey: ['plan', key, shownId], queryFn: () => api<Plan>(scopedPath(scope, `plans/${shownId}`)), enabled: !!shownId });
  const finishedOperation = latestOperation && latestOperation.status !== 'running' ? latestOperation : undefined;
  // The newest plan failed or was cancelled after the newest plan: say so plainly.
  const failed = finishedOperation?.kind === 'plan' && finishedOperation.status !== 'succeeded' && !planId ? finishedOperation : undefined;
  const applied = finishedOperation?.kind === 'apply' && (!planId || planId === finishedOperation.planId) ? finishedOperation : undefined;
  // A replacement chosen in the explorer opens the preflight directly.
  const preflightOpen = preflight || !!replace?.length || !!openPreflight && !!options.data?.canPlan && !running;
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
    preflightOpened?.();
  }
  async function start(varFiles: string[], contextFingerprint?: string) {
    setStarting(true);
    setStartError('');
    try {
      // The fingerprint binds the plan to the context reviewed in the dialog; the server
      // refuses it if the profile changed in the meantime.
      const ids = (replace ?? []).map(r => r.id);
      // A Hub workspace's plan is bound to the scope the options described; the server
      // refuses it if anything in that scope changed since.
      if (scoped) await post<Operation>(scopedPath(scope, 'plans'), { acknowledgeBackendAccess: true, varFiles, replace: ids, contextFingerprint: options.data?.contextFingerprint ?? '' });
      else await post<Operation>('plans', { acknowledgeBackendAccess: true, varFiles, replace: ids, ...(contextFingerprint ? { profileId, contextFingerprint } : {}) });
      closePreflight();
      choosePlan(undefined);
      await client.invalidateQueries({ queryKey: ['operations'] });
    } catch (e) {
      setStartError(e instanceof Error ? e.message : 'The plan could not be started.');
      void options.refetch();
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
  // A Hub workspace whose context changed or closed is never retargeted: open it again.
  const closedError = [options.error, plans.error, plan.error].find(e => e instanceof ApiError && (e.code === 'context_closed' || e.code === 'scope_changed'));
  if (scoped && closedError) return <div className="empty-state error-state" role="alert"><TriangleAlert size={26} /><h2>Open this workspace again</h2><p>{closedError.message}</p>{reopen && <Button variant="outline" onClick={reopen}>Open again</Button>}<span className="safe-note"><ShieldCheck size={13} />Nothing ran. Plans and approvals of the closed view were discarded.</span></div>;
  return <div className="plans">
   <div className="plans-toolbar">
    <Button onClick={() => { setStartError(''); setPreflight(true); }} disabled={!canPlan}><Play size={14} />Run plan</Button>
    {plans.data && plans.data.plans.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Plan history</span><select aria-label="Plan history" value={shownId} onChange={e => choosePlan(e.target.value)}>{plans.data.plans.map((p, i) => <option key={p.id} value={p.id}>{i === 0 ? 'Latest · ' : ''}{clock(p.createdAt)} · {p.kind === 'replace' ? `replace ${p.replace.length} · ` : p.kind === 'import' ? 'import · ' : ''}{p.changes} change{p.changes === 1 ? '' : 's'}{p.errored ? ' · errored' : ''}</option>)}</select></label>}
    <span className="toolbar-note"><ShieldCheck size={13} />{options.data?.canApply ? 'Nothing is applied until you confirm a reviewed plan.' : 'Plans are reviewed here; applying is unavailable for this workspace.'}</span>
   </div>
   {workspaceStatus === 'loading' && !running ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before planning is available…</div> : options.data && !options.data.canPlan && <div className="plan-warning" role="note"><TriangleAlert size={15} />{options.data.reason || 'Planning is not available for this workspace.'}{scoped && options.data.code === 'execution_unsupported' && ' Plans and applies from the Hub run only for initialized workspaces with a local or supported backend; remote and cloud execution stay read-only.'}</div>}
   {running && <OperationLog key={running.id} operation={running} scope={scope} onFinished={finished} />}
   {failed && <FailedOperation operation={failed} />}
   {applied && <ApplyResult operation={applied} />}
   {failed && plan.data && <p className="info-line">Below is the previous successful plan, created at {clock(plan.data.createdAt)}. It does not reflect the run above.</p>}
   {plan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading plan…</p></div> : plan.error ? <div className="empty-state error-state" role="alert"><p>{plan.error instanceof ApiError && plan.error.code === 'plan_not_found' ? 'This plan is not kept here: it was applied, replaced by a newer plan, expired, or belongs to a view that is no longer open. Nothing ran.' : plan.error.message}</p>{planId && <Button variant="outline" onClick={() => choosePlan(undefined)}>Show the latest plan</Button>}</div> : plan.data ? <PlanReview key={plan.data.id} plan={plan.data} scope={scope} copy={copy} busy={!!running} onApply={() => setApplying(true)} onInspectImpact={inspectImpact} /> : !running && !failed && <div className="empty-state"><FileDiff size={30} /><h2>No plans yet</h2><p>Run a plan to see every proposed change: creates, updates, replacements, deletions, data reads and output changes. Nothing runs until you confirm.</p>{options.data?.canPlan && <Button variant="outline" onClick={() => setPreflight(true)}><Play size={14} />Run plan</Button>}</div>}
   {preflightOpen && options.data && <PreflightDialog options={options.data} project={project} replace={replace} busy={starting} error={startError} onConfirm={(varFiles, fingerprint) => void start(varFiles, fingerprint)} onClose={closePreflight} />}
   {applying && plan.data && <ApplyDialog plan={plan.data} scope={scope} project={project} onStarted={() => void applyStarted()} onClose={() => setApplying(false)} />}
  </div>;
}

// The options of a Hub workspace describe its execution scope; the review dialog shows them
// in the launch project's shape.
function fromScope(o: ScopedPlanOptions): PlanOptions {
  return { canPlan: o.canPlan, reason: o.reason, code: o.code, canApply: o.canPlan, engine: o.scope?.engine ?? '', version: o.scope?.version ?? '', workspace: o.scope?.workspace ?? '', backend: o.scope?.backend ?? '', varFiles: o.varFiles, scope: o.scope ?? undefined, executionContext: o.executionContext ?? undefined, contextFingerprint: o.contextFingerprint };
}

function Diagnostics({ operation }: { operation: Operation }) {
  if (!operation.engineDiagnostics.length) return null;
  return <ul className="diagnostics">{operation.engineDiagnostics.map((d, i) => <li key={i} className={`diag-${d.severity}`}><strong>{d.severity === 'error' ? 'Error' : 'Warning'}: {d.summary}</strong>{d.address && <code>{d.address}</code>}{d.detail && <pre>{d.detail}</pre>}</li>)}</ul>;
}

function FailedOperation({ operation }: { operation: Operation }) {
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{operation.import ? 'Import plan' : 'Plan'} {operation.status === 'cancelled' ? 'cancelled' : 'failed'}</h2>
   <p>{operation.diagnostic?.message}</p>
   <p className="muted">No plan was kept from this run, so there is nothing to review or apply.</p>
   {operation.import && <ImportRecovery target={operation.import} />}
   <Diagnostics operation={operation} />
  </section>;
}

// Process exit is not the same as infrastructure state: the outcome is reported separately
// from whether state was re-read.
function ApplyResult({ operation }: { operation: Operation }) {
  const ok = operation.status === 'succeeded';
  // Engine success with a failed state read is not a clean success: warn, never show it as healthy.
  const clean = ok && !operation.stateReloadFailed;
  const uncertain = operation.outcome === 'uncertain';
  const engineLine = ok ? 'reported the apply as finished' : uncertain ? 'stopped part-way; some changes may have been made' : 'did not apply anything';
  const stateLine = operation.stateReloaded ? `read again at ${clock(operation.completedAt ?? operation.startedAt)}` : operation.stateReloadFailed ? 'could not be read again; the explorer still shows the state read before the apply' : 'not read again';
  return <section className={clean ? 'operation-applied' : 'operation-failed'} role={clean ? 'status' : 'alert'}>
   <h2>{clean ? <CircleCheck size={17} /> : ok ? <TriangleAlert size={17} /> : <CircleX size={17} />}{ok ? clean ? 'Apply finished' : 'Apply finished, state not confirmed' : operation.status === 'cancelled' ? 'Apply interrupted' : 'Apply failed'}{uncertain && ' · outcome uncertain'}</h2>
   {!ok && <p>{operation.diagnostic?.message}</p>}
   <dl className="metadata compact-metadata apply-status" aria-label="Apply status">
    <div><dt>Engine</dt><dd>{engineLine}</dd></div>
    <div><dt>State</dt><dd>{stateLine}</dd></div>
    <div><dt>Resource health</dt><dd>not checked. Hclaro has no health probe; the engine recording a change does not mean the service works.</dd></div>
   </dl>
   {uncertain && <div className="plan-warning" role="note"><TriangleAlert size={14} /><div><strong>Check real infrastructure before doing anything else.</strong><ol>
    <li>Do not apply again from Hclaro: this saved plan was deleted and is never retried automatically.</li>
    <li>In your terminal, in this project and workspace, list state and run a new plan to see what actually changed.</li>
    <li>Check the resources involved in your provider’s console.</li>
    <li>If a state lock remains, make sure no apply is still running before releasing it with your CLI. Hclaro never force-unlocks.</li>
   </ol></div></div>}
   {operation.import && (ok ? <p>Imported <code>{operation.import.address}</code>. It appears in the resource explorer once the reloaded state records it.</p> : <ImportRecovery target={operation.import} applied />)}
   {operation.stateReloadFailed && <p className="plan-warning" role="alert"><TriangleAlert size={14} />State could not be read again after the apply, so the explorer may be out of date. Read the workspace again, or check state with your CLI.</p>}
   <p className="muted">{operation.stateReloaded ? 'The explorer shows what the engine recorded, not live cloud inventory. ' : ''}The saved plan was deleted and cannot be applied again.</p>
   <Diagnostics operation={operation} />
  </section>;
}

// After a failed import plan or apply the user's files stay as they are; say what to do next.
function ImportRecovery({ target, applied }: { target: ImportTarget; applied?: boolean }) {
  const files = [target.file, target.generatedFile].filter(Boolean) as string[];
  return <div className="recovery" role="note">
   <strong>Your configuration was kept.</strong>
   <ul>
    <li>The import block for <code>{target.address}</code> is still in <code>{target.file}</code>{target.generatedFile && <>; any generated configuration is in <code>{target.generatedFile}</code></>}. Hclaro does not delete files you approved.</li>
    <li>To retry, fix the problem (for example the ID or the generated configuration) and run a new plan from Plans; every plan includes import blocks in your configuration.</li>
    <li>To back out, delete {files.map((f, i) => <span key={f}>{i > 0 && ' and '}<code>{f}</code></span>)}.{applied ? ' If the apply recorded the import before failing, the object is now in state: check with your CLI before deleting the block.' : ' Nothing was recorded in state.'}</li>
   </ul>
  </div>;
}
