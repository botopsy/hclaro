import { useCallback, useEffect, useRef, useState } from 'react';
import { CircleCheck, CircleHelp, CircleX, Fingerprint, Info, LoaderCircle, RefreshCw, Rocket, ShieldCheck, Terminal, TriangleAlert, UserCheck, X } from 'lucide-react';
import { post } from '../api';
import type { Approval, Operation, Plan, PreflightCheck, PreflightReport, PreflightResource } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { RecordedContext } from '../profiles/ContextSummary';
import { actionOrder, engineLabel, meta } from './labels';
import { launchScope, planRoute, type PlanScope } from './routes';

const errorText = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

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
    return <li key={`${r.address}-${i}`}><span className={`action-chip tone-${tone}`}><Icon size={12} aria-hidden="true" />{action}</span><code>{r.address}</code>{r.deposed && <span className="muted">deposed</span>}{r.requested && <span className="requested-tag">requested</span>}</li>;
  })}</ul>;
}

// Preflight and final confirmation before a saved-plan apply. Every check runs on the server
// and is evaluated again when the apply starts; the dialog shows the results, collects an
// acknowledgement per warning and the typed phrase, and only then asks for a one-time
// approval and applies. Closing the dialog declines and nothing is applied. scope names the
// project or Hub workspace the plan belongs to; every request goes to that scope only.
export function ApplyDialog({ plan, scope, onStarted, onClose }: { plan: Plan; scope: PlanScope; onStarted: (operation: Operation) => void; onClose: () => void }) {
  const [report, setReport] = useState<PreflightReport>();
  const [acked, setAcked] = useState<Set<string>>(new Set());
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(true);
  const cancel = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  // Cancel has initial focus so Enter never applies by accident.
  useModal(dialog, cancel, onClose);
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
      .then(() => post<PreflightReport>(target('preflight'), { verifyIdentity }))
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
  const allAcked = warnings.every(w => acked.has(w.id));
  const matches = !report?.phrase || typed === report.phrase;
  const ready = !!report && !report.blocked && !!report.binding && allAcked && matches && !checking;

  async function apply() {
    if (!report?.binding || !ready) return;
    setBusy(true);
    setError('');
    try {
      const approval = await post<Approval>(target('approval'), { preflightBinding: report.binding, acknowledged: [...acked] });
      onStarted(await post<Operation>(target('apply'), { confirmationToken: approval.confirmationToken, confirmation: typed }));
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
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog apply-dialog preflight-dialog" role="dialog" aria-modal="true" aria-labelledby="apply-title" aria-describedby="apply-summary">
    <div className="dialog-header"><h2 id="apply-title">Apply this plan</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="apply-summary">{engine} will apply the saved plan you reviewed to workspace <strong>{plan.workspace}</strong>. This changes real infrastructure. A saved plan does not prompt again, so this is the final confirmation. Hclaro checks everything below on the server now and again when the apply starts.</p>

    {ctx && <section className="preflight-identity" aria-label="Context identity">
     <dl>
      <div><dt>Project</dt><dd>{ctx.project || '—'}</dd></div>
      <div><dt>Workspace</dt><dd><strong>{ctx.workspace}</strong></dd></div>
      <div><dt>Engine</dt><dd>{engineLabel(ctx.engine)} {ctx.engineVersion}</dd></div>
      <div><dt>Backend</dt><dd>{ctx.backend} <span className="muted">({ctx.executionMode})</span></dd></div>
      <div><dt>Saved plan</dt><dd><code title={ctx.artifactSha256}>{ctx.artifactSha256.slice(0, 16)}…</code></dd></div>
      <div><dt>Context fingerprint</dt><dd><code>{ctx.fingerprint}</code></dd></div>
     </dl>
     <p className="context-line"><strong>Execution context:</strong> <RecordedContext context={ctx.executionContext} /></p>
    </section>}

    {checking && <p role="status"><LoaderCircle className="spin" size={13} /> {verifying ? 'Verifying the AWS caller identity…' : 'Running preflight checks…'}</p>}

    {report && <>
     <p role="status" className={report.blocked ? 'preflight-verdict verdict-block' : 'preflight-verdict verdict-ok'}>
      {report.blocked ? <><CircleX size={15} aria-hidden="true" /> {report.blockers} blocking check{report.blockers === 1 ? '' : 's'} failed. Apply is unavailable until {report.blockers === 1 ? 'it is' : 'they are'} resolved.</> : <><ShieldCheck size={15} aria-hidden="true" /> No blocking checks.{warnings.length > 0 && ` Acknowledge ${warnings.length === 1 ? 'the warning' : `each of the ${warnings.length} warnings`} below.`}</>}
     </p>

     {blockers.length > 0 && <section aria-labelledby="pf-blockers"><h3 id="pf-blockers">Blocking</h3><ul className="preflight-checks">{blockers.map(c => <CheckRow key={c.id} check={c} />)}</ul></section>}

     {warnings.length > 0 && <section aria-labelledby="pf-warnings"><h3 id="pf-warnings">Warnings to acknowledge</h3><ul className="preflight-checks">{warnings.map(c => <CheckRow key={c.id} check={c}>
      <label className="ack-field"><input type="checkbox" checked={acked.has(c.id)} disabled={report.blocked} onChange={e => setAcked(prev => {
        const next = new Set(prev);
        if (e.target.checked) next.add(c.id); else next.delete(c.id);
        return next;
      })} /> I reviewed this: {c.title.toLowerCase()}</label>
     </CheckRow>)}</ul></section>}

     {identity && identity.status !== 'pass' && report.identity.state !== 'verified' && <div className="identity-probe">
      <Button variant="outline" onClick={() => rerun(true)} disabled={checking}><UserCheck size={14} />Verify AWS identity</Button>
      <span className="muted">Calls STS GetCallerIdentity with this plan’s credentials. Read-only, but it contacts AWS.</span>
     </div>}

     <details className="preflight-passed"><summary>Passed and informational checks ({others.length})</summary><ul className="preflight-checks">{others.map(c => <CheckRow key={c.id} check={c} />)}</ul></details>

     {impact && <section aria-labelledby="pf-impact" className="preflight-impact">
      <h3 id="pf-impact">Impact of the whole saved plan</h3>
      <div className="apply-counts" role="list" aria-label="Changes to apply">{counts.map(a => {
       const { label, icon: Icon, tone } = meta(a);
       return <span role="listitem" key={a} className={`apply-count tone-${tone}`}><Icon size={13} aria-hidden="true" />{impact.summary[a]} {label.toLowerCase()}</span>;
      })}</div>
      {impact.requested.length > 0 && <p>Replacement was requested for <strong>{impact.requested.length}</strong> instance{impact.requested.length === 1 ? '' : 's'}. {impact.extra > 0 ? <>The plan also changes <strong>{impact.extra}</strong> other resource instance{impact.extra === 1 ? '' : 's'}; they are applied too.</> : 'No other resource changes.'}</p>}
      {impact.imports > 0 && <p>Imports <strong>{impact.imports}</strong> existing object{impact.imports === 1 ? '' : 's'} into state. Import writes state.</p>}
      {impact.destructive > 0 && <p className="plan-warning" role="alert"><TriangleAlert size={15} /><span>This plan destroys, replaces or forgets <strong>{impact.destructive}</strong> object{impact.destructive === 1 ? '' : 's'}. Data stored in them can be lost, and dependent services can be unavailable while they are recreated. Hclaro cannot guarantee zero downtime.</span></p>}
      {impact.highRisk.length > 0 && <><h4>High-risk resources ({impact.destructive})</h4><ResourceList label="High-risk resources" items={impact.highRisk} /></>}
      <details><summary>All planned changes ({impact.total})</summary><ResourceList label="Planned changes" items={impact.changes} />{impact.truncated && <p className="muted">The list is limited to the first {impact.changes.length} changes; the counts above cover all of them.</p>}</details>
     </section>}

     <div className="command-preview"><span className="muted"><Terminal size={12} /> Command</span><code>{report.command.join(' ')}</code><span className="muted"><Fingerprint size={12} /> Saved plan SHA-256 <code>{report.context.artifactSha256.slice(0, 16)}…</code>{report.expiresAt && <> · checks valid until {new Date(report.expiresAt).toLocaleTimeString()}</>}</span></div>
     {report.phrase && !report.blocked && <label className="phrase-field"><span>Type <code>{report.phrase}</code> to confirm</span><input value={typed} onChange={e => setTyped(e.target.value)} autoComplete="off" spellCheck={false} aria-label="Confirmation phrase" /></label>}
    </>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions">
     <Button variant="ghost" onClick={() => rerun()} disabled={checking || busy}><RefreshCw size={14} />Run checks again</Button>
     <Button ref={cancel} variant="ghost" onClick={onClose}>Cancel</Button>
     <Button variant={impact && impact.destructive > 0 ? 'destructive' : 'default'} onClick={() => void apply()} disabled={!ready || busy}><Rocket size={14} />{busy ? 'Starting…' : 'Apply plan'}</Button>
    </div>
   </div>
  </div>;
}
