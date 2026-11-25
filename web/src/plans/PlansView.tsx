import { useCallback, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleCheck, CircleX, CloudCog, FileDiff, GitBranch, History, LoaderCircle, LockKeyhole, Play, RefreshCcw, ShieldCheck, TriangleAlert } from 'lucide-react';
import { api, ApiError, send } from '../api';
import { operationActive, type ImportTarget, type Operation, type Plan, type PlanOptions, type PlanSourceFingerprint, type PlanSummary, type ReplaceTarget, type ScopedPlanOptions } from '../types';
import { Button } from '../components/ui/button';
import { ApplyDialog } from './ApplyDialog';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { activeOperation, operationsQuery } from '../lib/queries';
import { PlanReview, sourceLabel, type PlanSource } from './PlanReview';
import { RunDialog, RunState } from './RunDialog';
import { useVarFileChoice, VarFileChoice } from '../profiles/VarFileChoice';
import { clock, engineLabel } from '../lib/format';
import { errorMessage } from '../lib/errorMessage';
import { ErrorState } from '../components/ErrorState';
import { EmptyState } from '../components/EmptyState';
import { Disclosure } from '../components/Disclosure';
import { planTotal } from './labels';

// Survives leaving the page, so a discarded plan is not reopened from Drift or elsewhere (shortcut: cleared on reload, the server keeps the review-only record).
const discardedIds = new Set<string>();
export const forgetDiscardedPlans = () => discardedIds.clear();

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
  openDrift,
  openHistory,
  openGit,
  scope = launchScope,
  reopen,
  source
}: {
  project: string;
  // Opens a planned change's configuration block in the editor; omitted when Git context is unavailable.
  source?: PlanSource;
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
  // Navigation to Drift detection, History and Git; omitted where the page is unavailable.
  openDrift?: () => void;
  openHistory?: () => void;
  openGit?: () => void;
}) {
  const client = useQueryClient();
  const [preflight, setPreflight] = useState(false);
  const [applying, setApplying] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState('');
  // The apply whose plan the user chose to show again after a clean apply.
  const [revealedApply, setRevealedApply] = useState<string>();
  // Availability is decided after the startup read; refetch when the workspace status changes.
  const key = scopeKey(scope);
  const scoped = scope.kind === 'context';
  const options = useQuery({
    queryKey: ['plan-options', key, workspaceStatus, profileId],
    queryFn: async () => scoped ? fromScope(await api<ScopedPlanOptions>(scopedPath(scope, 'plans/options'))) : api<PlanOptions>(`plans/options${profileId ? `?profileId=${profileId}` : ''}`)
  });
  // Plans the user discarded stay out of the list so the page is ready for a fresh run.
  const [discarded, setDiscarded] = useState<string[]>([...discardedIds]);
  const plans = useQuery({ queryKey: ['plans', key], queryFn: () => api<{ plans: PlanSummary[] }>(scopedPath(scope, 'plans')), select: d => ({ plans: d.plans.filter(p => !discarded.includes(p.id)) }) });
  const operations = useQuery(operationsQuery(scope));
  const latestOperation = operations.data?.operations[0];
  const running = activeOperation(operations.data?.operations);
  const shownId = planId ?? plans.data?.plans[0]?.id;
  const plan = useQuery({ queryKey: ['plan', key, shownId], queryFn: () => api<Plan>(scopedPath(scope, `plans/${shownId}`)), enabled: !!shownId });
  const finishedOperation = latestOperation && !operationActive(latestOperation) ? latestOperation : undefined;
  // The newest plan failed or was cancelled after the newest plan: say so plainly.
  const failed = finishedOperation?.kind === 'plan' && finishedOperation.status !== 'succeeded' && !planId ? finishedOperation : undefined;
  const applied = finishedOperation?.kind === 'apply' && (!planId || planId === finishedOperation.planId) ? finishedOperation : undefined;
  // A running operation and the failed plan it may become share one card (same key).
  const live = running ?? failed;
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
      if (scoped) await send<Operation>('POST', scopedPath(scope, 'plans'), { acknowledgeBackendAccess: true, varFiles, replace: ids, contextFingerprint: options.data?.contextFingerprint ?? '' });
      else await send<Operation>('POST', 'plans', { acknowledgeBackendAccess: true, varFiles, replace: ids, ...(contextFingerprint ? { profileId, contextFingerprint } : {}) });
      closePreflight();
      choosePlan(undefined);
      await client.invalidateQueries({ queryKey: ['operations'] });
    } catch (e) {
      setStartError(errorMessage(e, 'The plan could not be started.'));
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
  const runPlan = () => {
    setStartError('');
    setPreflight(true);
  };
  // A Hub workspace whose context changed or closed is never retargeted: open it again.
  const closedError = [options.error, plans.error, plan.error].find(e => e instanceof ApiError && (e.code === 'context_closed' || e.code === 'scope_changed'));
  if (scoped && closedError) return <ErrorState icon={TriangleAlert} iconSize={26} title="Open this workspace again" message={closedError.message} retry={reopen} retryLabel="Open again" note="Nothing ran. Plans and approvals of the closed view were discarded." />;
  // §4.3: the empty state has the only Run plan button; every other state keeps it in the toolbar.
  const content = !!shownId || !!live || !!applied;
  const empty = !content && !!plans.data && !options.isPending;
  // A clean apply leaves nothing to review: the plan is applied and its file deleted. Anything uncertain keeps the plan in view.
  const hidePlan = !!applied && isCleanApply(applied) && revealedApply !== applied.id;
  const appliedPlan = applied && plan.data?.id === applied.planId ? plan.data : undefined;
  return <div className="plans">
   {content && <div className="plans-toolbar">
    <Button variant="outline" onClick={runPlan} disabled={!canPlan}><Play size={14} />Run plan</Button>
    {plans.data && plans.data.plans.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Plan history</span><select aria-label="Plan history" value={shownId} onChange={e => choosePlan(e.target.value)}>{plans.data.plans.map((p, i) => <option key={p.id} value={p.id}>{i === 0 ? 'Latest · ' : ''}{clock(p.createdAt)} · {p.kind === 'replace' ? `replace ${p.replace.length} · ` : p.kind === 'import' ? 'import · ' : ''}{p.changes} change{p.changes === 1 ? '' : 's'}{p.errored ? ' · errored' : ''}</option>)}</select></label>}
   </div>}
   {workspaceStatus === 'loading' && !running ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before planning is available…</div> : options.data && !options.data.canPlan && <div className="plan-warning" role="note"><TriangleAlert size={15} />{options.data.reason || 'Planning is not available for this workspace.'}{scoped && options.data.code === 'execution_unsupported' && ' Plans and applies from the Hub run only for initialized workspaces with a local or supported backend; remote and cloud execution stay read-only.'}</div>}
   {live && <RunState key={live.id} operation={live} scope={scope} running={live.kind === 'apply' ? 'Applying…' : 'Planning…'} succeeded={live.kind === 'apply' ? 'Apply finished' : 'Plan ready'} onFinished={running ? finished : undefined} onRetry={runPlan}>
    {live.kind === 'plan' && <>
     <p className="muted">No plan was kept from this run, so there is nothing to review or apply.</p>
     {live.import && <ImportRecovery target={live.import} />}
     {live.engineDiagnostics.length > 0 && <Disclosure summary={`Engine diagnostics (${live.engineDiagnostics.length})`}><Diagnostics operation={live} /></Disclosure>}
    </>}
   </RunState>}
   {applied && <ApplyResult operation={applied} changes={appliedPlan && planTotal(appliedPlan.summary)} source={appliedPlan?.source} canPlan={canPlan} onRunPlan={runPlan} openHistory={openHistory} openGit={openGit} />}
   {failed && plan.data && <p className="info-line">Below is the previous successful plan, created at {clock(plan.data.createdAt)}. It does not reflect the run above.</p>}
   {hidePlan ? <Button variant="ghost" onClick={() => setRevealedApply(applied.id)}>Show the plan that was applied</Button> : plan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading plan…</p></div> : plan.error ? <ErrorState inline message={plan.error instanceof ApiError && plan.error.code === 'plan_not_found' ? 'This plan is not kept here: it was applied, replaced by a newer plan, expired, or belongs to a view that is no longer open. Nothing ran.' : plan.error.message}>{planId && <Button variant="outline" onClick={() => choosePlan(undefined)}>Show the latest plan</Button>}</ErrorState> : plan.data ? <PlanReview key={plan.data.id} plan={plan.data} scope={scope} source={source} copy={copy} busy={!!running} canPlan={canPlan} onApply={() => setApplying(true)} onDiscarded={() => { discardedIds.add(shownId!); setDiscarded([...discardedIds]); choosePlan(undefined); }} onRunPlan={runPlan} onOpenDrift={openDrift} onInspectImpact={inspectImpact} /> : empty && <EmptyState icon={<FileDiff size={28} />} title="No plans yet" action={<Button onClick={runPlan} disabled={!canPlan}><Play size={14} />Run plan</Button>} how={<p>A plan lists every proposed change: creates, updates, replacements, deletions, data reads and output changes. Hclaro keeps the saved plan for a limited time, and nothing is applied until you review it and confirm.</p>}>Run a plan to see every change {options.data ? engineLabel(options.data.engine) : 'the engine'} proposes for {project}.</EmptyState>}
   {preflightOpen && options.data && <PlanDialog options={options.data} project={project} replace={replace} busy={starting} error={startError} onConfirm={(varFiles, fingerprint) => void start(varFiles, fingerprint)} onClose={closePreflight} />}
   {applying && plan.data && <ApplyDialog plan={plan.data} scope={scope} project={project} onStarted={() => void applyStarted()} onClose={() => setApplying(false)} />}
  </div>;
}

// The "Run a plan" dialog (§4.3 step 2), shown before every plan: planning is not a local-only read.
function PlanDialog({ options, project, replace = [], busy, error, onConfirm, onClose }: {
  options: PlanOptions;
  project: string;
  replace?: ReplaceTarget[];
  busy: boolean;
  error?: string;
  onConfirm: (varFiles: string[], contextFingerprint?: string) => void;
  onClose: () => void;
}) {
  const choice = useVarFileChoice(options);
  const summary = options.context;
  const engine = engineLabel(options.engine);
  const replacing = replace.length > 0;
  return <RunDialog title={replacing ? 'Plan a replacement' : 'Run a plan'} noun="plan" action={replacing ? 'Plan replacement' : 'Run plan'} busy={busy} error={error} blockedReason={options.profileError} uses={{ ...options, project }} onClose={onClose} onRun={() => onConfirm(choice.chosen, summary?.context.profileId ? summary.context.fingerprint : undefined)}
   sentence={<>{engine} will run <code>plan</code> in <strong>{project}</strong> for workspace <strong>{options.workspace}</strong>{replacing && <> with one <code>-replace</code> per selected instance</>}. Review what this does before continuing.</>}
   facts={[
     { icon: CloudCog, title: `Contacts providers and the ${options.backend} backend`, detail: 'using your existing credentials. Data sources are read and resources are refreshed to compare with configuration.' },
     { icon: LockKeyhole, title: 'May lock the state', detail: 'while planning, if the backend supports locking. Other operations wait until the plan finishes or is cancelled.' },
     { icon: ShieldCheck, title: 'Does not change infrastructure.', detail: options.canApply ? 'You review the saved plan first; it is applied only if you confirm it explicitly.' : 'The saved plan is deleted after Hclaro extracts redacted review data. Applying is unavailable here.' }
   ]}>
   {replacing && <div className="replace-targets"><span className="muted">Instances to replace ({replace.length})</span><ul>{replace.map(r => <li key={r.id}><RefreshCcw size={12} aria-hidden="true" /><code>{r.address}</code></li>)}</ul><p>This is a full plan, not a targeted one: Hclaro never uses -target, so every other change the engine proposes is included, shown for review and applied together. Up to 50 instances per plan.</p></div>}
   <VarFileChoice choice={choice} />
  </RunDialog>;
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

// isCleanApply: every part of the apply is confirmed (the engine finished, state was read again,
// the saved plan file is gone), so the short card is enough and the old plan needs no review.
export function isCleanApply(op: Operation) {
  return op.status === 'succeeded' && !op.stateReloadFailed && op.stateReloaded && !op.planFileLeft && op.outcome !== 'uncertain';
}

// Process exit is not the same as infrastructure state: the outcome is reported separately
// from whether state was re-read.
function ApplyResult({ operation, changes, source, canPlan, onRunPlan, openHistory, openGit }: { operation: Operation; changes?: number; source?: PlanSourceFingerprint; canPlan: boolean; onRunPlan: () => void; openHistory?: () => void; openGit?: () => void }) {
  const ok = operation.status === 'succeeded';
  // Engine success with a failed state read is not a clean success: warn, never show it as healthy.
  const clean = ok && !operation.stateReloadFailed;
  const uncertain = operation.outcome === 'uncertain';
  const engineLine = ok ? 'reported the apply as finished' : uncertain ? 'stopped part-way; some changes may have been made' : 'did not apply anything';
  const planFile = operation.planFileLeft ? 'Hclaro could not delete the saved plan file, which holds values in plaintext; the terminal running Hclaro names its folder. Delete it yourself. It cannot be applied again.' : 'The saved plan was deleted and cannot be applied again.';
  // §4.3 step 5: the short card only when every part is confirmed; anything uncertain keeps the full report.
  if (isCleanApply(operation)) {
    const dirty = source?.dirtyKnown && source.dirty ? source.dirtyFiles : 0;
    return <section className="operation-applied applied-card" role="status">
     <div className="applied-head">
      <span className="applied-icon" aria-hidden="true"><CircleCheck size={18} /></span>
      <div><h2>{changes === undefined ? 'Applied' : `Applied: ${changes} change${changes === 1 ? '' : 's'}`}</h2><p>The saved plan was used and deleted. Run a new plan to make more changes.</p></div>
      <div className="applied-actions">{openHistory && <Button variant="outline" onClick={openHistory}>View in History</Button>}<Button onClick={onRunPlan} disabled={!canPlan}><Play size={14} />Run new plan</Button></div>
     </div>
     <ol className="apply-steps" aria-label="Apply status">
      <li><CircleCheck size={13} aria-hidden="true" />Planned</li>
      <li><CircleCheck size={13} aria-hidden="true" />Applied</li>
      <li><CircleCheck size={13} aria-hidden="true" />State read again at {clock(operation.completedAt ?? operation.startedAt)}</li>
      <li className="muted" title="Hclaro has no health probe; the engine recording a change does not mean the service works.">Service health: not checked</li>
     </ol>
     {operation.import && <p>Imported <code>{operation.import.address}</code>. It appears in the resource explorer once the reloaded state records it.</p>}
     {dirty > 0 && <p className="plan-warning" role="note"><GitBranch size={14} /><span>Applied from {sourceLabel({ ...source!, dirty: false })} with {dirty} uncommitted IaC file{dirty === 1 ? '' : 's'}. Commit {dirty === 1 ? 'it' : 'them'} so the repo matches what is deployed.</span>{openGit && <Button variant="outline" size="small" onClick={openGit}>Open Git</Button>}</p>}
     <Diagnostics operation={operation} />
    </section>;
  }
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
    <li>Do not apply again from Hclaro: this saved plan is never retried automatically.</li>
    <li>In your terminal, in this project and workspace, list state and run a new plan to see what actually changed.</li>
    <li>Check the resources involved in your provider’s console.</li>
    <li>If a state lock remains, make sure no apply is still running before releasing it with your CLI. Hclaro never force-unlocks.</li>
   </ol></div></div>}
   {operation.import && (ok ? <p>Imported <code>{operation.import.address}</code>. It appears in the resource explorer once the reloaded state records it.</p> : <ImportRecovery target={operation.import} applied />)}
   {operation.stateReloadFailed && <p className="plan-warning" role="alert"><TriangleAlert size={14} />State could not be read again after the apply, so the explorer may be out of date. Read the workspace again, or check state with your CLI.</p>}
   {(operation.stateReloaded || !operation.planFileLeft) && <p className="muted">{operation.stateReloaded ? 'The explorer shows what the engine recorded, not live cloud inventory. ' : ''}{!operation.planFileLeft && planFile}</p>}
   {/* A failed apply's diagnostic already says the file was left. */}
   {operation.planFileLeft && ok && <p className="plan-warning" role="alert"><TriangleAlert size={14} />{planFile}</p>}
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
