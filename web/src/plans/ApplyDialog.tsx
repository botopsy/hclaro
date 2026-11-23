import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { CircleCheck, CircleHelp, CircleX, Info, LoaderCircle, RefreshCw, Rocket, TriangleAlert, UserCheck } from 'lucide-react';
import { send } from '../api';
import type { Approval, Operation, Plan, PreflightCheck, PreflightReport, PreflightResource } from '../types';
import { Button } from '../components/ui/button';
import { Dialog } from '../components/Dialog';
import { Disclosure } from '../components/Disclosure';
import { RecordedContext } from '../profiles/ContextSummary';
import { AuthPanel } from '../profiles/AuthPanel';
import { actionOrder, meta } from './labels';
import { launchScope, planRoute, type PlanScope } from '../lib/scope';
import { productionLike } from '../router';
import { engineLabel } from '../lib/format';
import { ProductionBadge } from '../components/ProductionBadge';
import { errorMessage } from '../lib/errorMessage';

const errorText = (e: unknown, fallback: string) => (errorMessage(e, fallback));

function statusLabel(c: PreflightCheck) {
  if (c.status === 'pass') return 'Passed';
  if (c.status === 'unknown') return c.severity === 'block' ? 'Unknown — blocks' : 'Unknown';
  if (c.severity === 'block') return 'Blocks';
  if (c.severity === 'warn') return 'Warning';
  return 'Note';
}

function StatusIcon({ check }: { check: PreflightCheck }) {
  if (check.status === 'pass') return <CircleCheck size={15} aria-hidden="true" />;
  if (check.status === 'unknown') return <CircleHelp size={15} aria-hidden="true" />;
  if (check.severity === 'block') return <CircleX size={15} aria-hidden="true" />;
  if (check.severity === 'warn') return <TriangleAlert size={15} aria-hidden="true" />;
  return <Info size={15} aria-hidden="true" />;
}

function tone(c: PreflightCheck) {
  if (c.status === 'pass') return 'pass';
  if (c.severity === 'block') return 'block';
  if (c.severity === 'warn') return 'warn';
  return 'info';
}

function CheckRow({ check, children }: { check: PreflightCheck; children?: React.ReactNode }) {
  return <li className={`preflight-check check-${tone(check)}`}>
   <span className="check-status"><StatusIcon check={check} /><span>{statusLabel(check)}</span></span>
   <div>
    <strong>{check.title}</strong>{check.source === 'policy' && <span className="check-source">policy</span>}
    <p>{check.evidence}</p>
    {check.remediation && check.status !== 'pass' && <p className="check-remediation">{check.remediation}</p>}
    {children}
   </div>
  </li>;
}

function ResourceList({ label, items }: { label: string; items: PreflightResource[] }) {
  return <ul className="preflight-resources" aria-label={label}>{items.map((r, i) => {
    const { label: action, icon: Icon, tone } = meta(r.action);
    return <li key={`${r.address}-${i}`}><span className={`action-chip tone-${tone}`}><Icon size={12} aria-hidden="true" />{action}</span><code>{r.address}</code>{r.deposed && <span className="muted">deposed</span>}{r.action === 'forget' && <span className="muted">removed from state only</span>}{r.requested && <span className="requested-tag">requested</span>}</li>;
  })}</ul>;
}

// Preflight and final confirmation before a saved-plan apply. Every check runs on the server
// and is evaluated again when the apply starts; the dialog shows the results, collects an
// acknowledgement per warning and the typed phrase, and only then asks for a one-time
// approval and applies. Closing the dialog declines and nothing is applied. scope names the
// project or Hub workspace the plan belongs to; every request goes to that scope only.
export function ApplyDialog({ plan, scope, project, onStarted, onClose }: { plan: Plan; scope: PlanScope; project?: string; onStarted: (operation: Operation) => void; onClose: () => void }) {
  const [report, setReport] = useState<PreflightReport>();
  const [acked, setAcked] = useState<Set<string>>(new Set());
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(true);
  // Cancel has initial focus so Enter never applies by accident.
  const cancel = useRef<HTMLButtonElement>(null);
  const phraseLabel = useId();
  // A newer preflight replaces the binding on the server, so only the latest response counts.
  const latest = useRef(0);
  // Keyed by value, so a caller that builds a new scope object each render does not rerun
  // the preflight.
  const contextId = scope.kind === 'context' ? scope.contextId : undefined;
  const target = useCallback((step: 'preflight' | 'approval' | 'apply') => planRoute(contextId === undefined ? launchScope : { kind: 'context', contextId }, plan.id, step), [contextId, plan.id]);
  const runPreflight = useCallback((verifyIdentity = false) => {
    const request = ++latest.current;
    const current = () => request === latest.current;
    Promise.resolve()
      .then(() => send<PreflightReport>('POST', target('preflight'), { verifyIdentity }))
      .then(r => {
        if (!current()) return;
        setReport(r);
        // Acknowledgements and the phrase are given again for every new result.
        setAcked(new Set());
        setTyped('');
      }, e => current() && setError(errorText(e, 'The preflight checks could not run.')))
      .finally(() => {
        if (!current()) return;
        setChecking(false);
        setVerifying(false);
      });
  }, [target]);
  // Runs the checks again on request; the first run starts with checking already set.
  const rerun = (verifyIdentity = false) => {
    setChecking(true);
    setVerifying(verifyIdentity);
    runPreflight(verifyIdentity);
  };
  useEffect(() => {
    runPreflight();
    const requests = latest;
    return () => {
      requests.current++;
    };
  }, [runPreflight]);

  const blockers = report?.checks.filter(c => c.severity === 'block' && c.status !== 'pass') ?? [];
  const warnings = report?.checks.filter(c => report.acknowledge.includes(c.id)) ?? [];
  const others = report?.checks.filter(c => !blockers.includes(c) && !warnings.includes(c)) ?? [];
  const identity = report?.checks.find(c => c.id === 'cloud.aws.identity');
  // The execution profile's expected account (M20): a mismatch is not a sign-in problem.
  const expected = report?.checks.find(c => c.id === 'cloud.aws.expected');
  // A profile that names an AWS profile binds the approval to the verified identity.
  const reviewed = report?.checks.find(c => c.id === 'cloud.aws.reviewed');
  const mismatch = expected?.status === 'fail';
  const exec = report?.context.executionContext;
  const [signIn, setSignIn] = useState(false);
  const allAcked = warnings.every(w => acked.has(w.id));
  const matches = !report?.phrase || typed === report.phrase;
  const ready = !!report && !report.blocked && !!report.binding && allAcked && matches && !checking;

  async function apply() {
    if (!report?.binding || !ready) return;
    setBusy(true);
    setError('');
    try {
      const approval = await send<Approval>('POST', target('approval'), { preflightBinding: report.binding, acknowledged: [...acked] });
      onStarted(await send<Operation>('POST', target('apply'), { confirmationToken: approval.confirmationToken, confirmation: typed }));
    } catch (e) {
      setError(errorText(e, 'The apply could not be started.'));
      setBusy(false);
      // The approval was used or refused; run the checks again before another attempt.
      rerun();
    }
  }

  const engine = engineLabel(plan.engine);
  const impact = report?.impact;
  const counts = impact ? actionOrder.filter(a => a !== 'no-op' && impact.summary[a]) : [];
  const ctx = report?.context;
  // The heading names exactly where the plan is applied: the server's answer once it is in.
  const where = { project: ctx?.project || project || 'this project', workspace: ctx?.workspace || plan.workspace };
  const production = productionLike(where.workspace, where.project, ctx?.executionContext?.profileName);
  const destructive = !!impact && impact.destructive > 0;
  const phrase = report?.phrase && !report.blocked ? report.phrase : '';
  return <Dialog title={`Apply to ${where.project} / ${where.workspace}`} wide initial={cancel} onClose={onClose} footer={<div className="apply-footer">
    {phrase && <span id={phraseLabel}>Type <code>{phrase}</code> to confirm</span>}
    <div className="apply-footer-row">
     {phrase && <input value={typed} onChange={e => setTyped(e.target.value)} autoComplete="off" spellCheck={false} aria-label="Confirmation phrase" aria-describedby={phraseLabel} />}
     <Button ref={cancel} variant="ghost" onClick={onClose}>Cancel</Button>
     {/* Neutral until every condition holds; then destructive when the plan deletes, replaces or forgets. */}
     <Button variant={!ready ? 'outline' : destructive ? 'destructive' : 'default'} onClick={() => void apply()} disabled={!ready || busy}><Rocket size={14} />{busy ? 'Starting…' : impact ? `Apply ${impact.total} change${impact.total === 1 ? '' : 's'}` : 'Apply plan'}</Button>
    </div>
   </div>}>
   {production && <ProductionBadge />}
   <p className="run-sentence">{engine} applies the saved plan you reviewed to workspace <strong>{plan.workspace}</strong>. This changes real infrastructure, and a saved plan doesn’t prompt again, so this is the final confirmation.</p>

   {ctx && <section className="apply-facts" aria-label="Context identity">
    <dl>
     <div><dt>Project</dt><dd>{ctx.project || '—'}</dd></div>
     <div><dt>Workspace</dt><dd><strong>{ctx.workspace}</strong></dd></div>
     <div><dt>Engine</dt><dd>{engineLabel(ctx.engine)} {ctx.engineVersion}</dd></div>
     <div><dt>Backend</dt><dd>{ctx.backend} <span className="muted">({ctx.executionMode})</span></dd></div>
     <div className="span-2"><dt>Profile</dt><dd><RecordedContext context={ctx.executionContext} /></dd></div>
     <div><dt>Saved plan SHA-256</dt><dd><code title={ctx.artifactSha256}>{ctx.artifactSha256.slice(0, 16)}…</code></dd></div>
     <div><dt>Context fingerprint</dt><dd><code>{ctx.fingerprint}</code></dd></div>
    </dl>
    {(exec?.awsProfile || report?.identity.state === 'verified') && <p className="context-line"><strong>AWS identity:</strong> {exec?.awsProfile ? <>{exec.awsAuth === 'aws-vault' ? 'aws-vault' : 'AWS CLI'} profile <code>{exec.awsProfile}</code>{exec.awsRegion && <> · <code>{exec.awsRegion}</code></>}</> : 'inherited credentials'}{exec?.awsExpectedAccount && <> · expects <code>{exec.awsExpectedAccount}</code></>} · {report?.identity.state === 'verified' ? <>STS reports <code>{report.identity.account}</code> <code className="auth-arn">{report.identity.arn}</code>{report.identity.verifiedAt && <span className="muted"> at {new Date(report.identity.verifiedAt).toLocaleTimeString()}</span>}. To use another account, change the profile in the top bar and plan again.</> : <span className="muted">not verified</span>}</p>}
   </section>}

   {checking && <p role="status"><LoaderCircle className="spin" size={13} /> {verifying ? 'Verifying the AWS caller identity…' : 'Running preflight checks…'}</p>}

   {report && <>
    <div className="apply-checks">
     <p role="status" className={report.blocked ? 'preflight-verdict verdict-block' : 'preflight-verdict verdict-ok'}>
      {report.blocked ? <><CircleX size={15} aria-hidden="true" /> {report.blockers} blocking check{report.blockers === 1 ? '' : 's'} failed. Apply is unavailable until {report.blockers === 1 ? 'it is' : 'they are'} resolved.</> : <><CircleCheck size={15} aria-hidden="true" /> No blocking checks.{warnings.length > 0 && ` Acknowledge ${warnings.length === 1 ? 'the warning' : `each of the ${warnings.length} warnings`} below.`}</>}
     </p>
     <span className="muted">Checked now and again when the apply starts{report.expiresAt && ` · valid until ${new Date(report.expiresAt).toLocaleTimeString()}`}</span>
     {[identity, expected, reviewed].some(c => c && c.status !== 'pass') && <>
      <Button variant="outline" size="small" onClick={() => rerun(true)} disabled={checking} title="Calls STS GetCallerIdentity with this plan’s credentials. Read-only, but it contacts AWS."><UserCheck size={14} />{report.identity.state === 'verified' ? 'Verify again' : 'Verify AWS identity'}</Button>
      {exec?.profileId && exec.projectId && exec.awsProfile && <Button variant="ghost" size="small" aria-expanded={signIn} onClick={() => setSignIn(v => !v)}>Sign in</Button>}
     </>}
     <Button variant="ghost" size="small" onClick={() => rerun()} disabled={checking || busy}><RefreshCw size={14} />Run checks again</Button>
    </div>

    {blockers.length > 0 && <section aria-labelledby="pf-blockers"><h3 id="pf-blockers">Blocking</h3><ul className="preflight-checks">{blockers.map(c => <CheckRow key={c.id} check={c} />)}</ul></section>}

    {warnings.length > 0 && <section aria-labelledby="pf-warnings"><h3 id="pf-warnings">Warnings to acknowledge</h3><ul className="preflight-checks">{warnings.map(c => <CheckRow key={c.id} check={c}>
     <label className="ack-field"><input type="checkbox" checked={acked.has(c.id)} disabled={report.blocked} onChange={e => setAcked(prev => {
       const next = new Set(prev);
       if (e.target.checked) next.add(c.id); else next.delete(c.id);
       return next;
     })} /> I reviewed this: {c.title.toLowerCase()}</label>
    </CheckRow>)}</ul></section>}

    {mismatch && <p className="plan-warning" role="alert"><CircleX size={15} /><span><strong>AWS account mismatch.</strong> The verified identity is not the account this profile expects. Signing in again with the same profile will not fix this: change the AWS profile or sign in to the expected account. Nothing will be applied.</span></p>}
    {signIn && exec?.profileId && exec.projectId && <div className="profile-auth"><AuthPanel projectId={exec.projectId} profileId={exec.profileId} /><p className="muted">A sign-in does not resume this apply. When it completes, run the checks again; the earlier checks no longer count.</p></div>}

    <Disclosure summary={`${others.length} passed and informational checks`}><ul className="preflight-checks">{others.map(c => <CheckRow key={c.id} check={c} />)}</ul></Disclosure>

    {impact && <section aria-labelledby="pf-impact" className="preflight-impact">
     <h3 id="pf-impact">Impact of the whole saved plan</h3>
     <div className="apply-impact">
      <div className="apply-counts" role="list" aria-label="Changes to apply">{counts.map(a => {
       const { label, icon: Icon, tone } = meta(a);
       return <span role="listitem" key={a} className={`apply-count tone-${tone}`}><Icon size={13} aria-hidden="true" />{impact.summary[a]} {label.toLowerCase()}</span>;
      })}</div>
      {impact.imports > 0 && <span>and imports {impact.imports} existing object{impact.imports === 1 ? '' : 's'} into state</span>}
     </div>
     {impact.requested.length > 0 && <p>Replacement was requested for <strong>{impact.requested.length}</strong> instance{impact.requested.length === 1 ? '' : 's'}. {impact.extra > 0 ? <>The plan also changes <strong>{impact.extra}</strong> other resource instance{impact.extra === 1 ? '' : 's'}; they are applied too.</> : 'No other resource changes.'} Hclaro never uses -target: the whole saved plan is applied, including changes unrelated to the replacement.</p>}
     {destructive && <div className="data-loss" role="alert">
      <p className="data-loss-head"><TriangleAlert size={15} aria-hidden="true" /><span><strong>{impact.destructive} object{impact.destructive === 1 ? ' is' : 's are'} deleted, replaced or forgotten.</strong> Data stored in them can be lost, and dependent services can be unavailable while they’re recreated.</span></p>
      {impact.highRisk.length > 0 && <ResourceList label="High-risk resources" items={impact.highRisk} />}
     </div>}
     <Disclosure summary={`All planned changes (${impact.total})`}><ResourceList label="Planned changes" items={impact.changes} />{impact.truncated && <p className="muted">The list is limited to the first {impact.changes.length} changes; the counts above cover all of them.</p>}</Disclosure>
     <Disclosure summary="Command"><code className="command-line">{report.command.join(' ')}</code></Disclosure>
    </section>}
   </>}
   {error && <p className="dialog-error" role="alert">{error}</p>}
  </Dialog>;
}
