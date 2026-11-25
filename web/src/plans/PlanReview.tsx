import { useEffect, useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRightLeft, Ban, Braces, Clock, Copy, Fingerprint, GitBranch, Import, LoaderCircle, LockKeyhole, Pencil, Play, RefreshCcw, Rocket, Search, TriangleAlert, Waypoints, X } from 'lucide-react';
import { api, send } from '../api';
import type { AttributeChange, ChangeDetail, EditorName, OutputChange, Plan, PlanAction, PlanChange, PlanSourceFingerprint } from '../types';
import { Button } from '../components/ui/button';
import { DataTable, type Column } from '../components/DataTable';
import { Dialog } from '../components/Dialog';
import { Disclosure } from '../components/Disclosure';
import { JsonTree } from '../components/JsonTree';
import { actionOrder, meta, planTotal, reasonLabel, replaceOrder } from './labels';
import { RecordedContext } from '../profiles/ContextSummary';
import { CostTab } from './CostTab';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { AskAI } from '../assist/AskAI';
import { clock, engineLabel } from '../lib/format';
import { ErrorState } from '../components/ErrorState';
import { SourceLink } from '../git/editor';
import { errorMessage } from '../lib/errorMessage';

// Where a change's source link goes: the Git API prefix, the editor from Settings and a notifier.
export type PlanSource = { prefix: string; editor: EditorName; notify: (text: string) => void };
type Tab = 'changes' | 'outputs' | 'drift' | 'cost';
const pageSize = 200;

export function ActionBadge({ action }: { action: string }) {
  const { label, icon: Icon, tone } = meta(action);
  return <span className={`action-badge tone-${tone}`}><Icon size={12} aria-hidden="true" />{label}</span>;
}

function Value({ value, unknown, empty = '—' }: { value: unknown; unknown?: boolean; empty?: string }) {
  if (unknown || value === '[known after apply]') return <span className="unknown-value">known after apply</span>;
  if (value === '[redacted]') return <span className="redacted"><LockKeyhole size={11} />redacted</span>;
  if (value === null || value === undefined) return <span className="muted">{empty}</span>;
  if (typeof value === 'object') return <code className="json-inline">{JSON.stringify(value)}</code>;
  return <code className={typeof value === 'string' ? 'json-string' : 'json-value'}>{String(value)}</code>;
}

// sourceLabel describes the Git source a plan was made from.
export function sourceLabel(source?: PlanSourceFingerprint) {
  if (!source) return 'Not recorded';
  if (!source.repository) return 'Not in Git';
  const at = source.commit ? source.commit.slice(0, 7) : 'no commits';
  const where = source.detached ? `detached @ ${at}` : `${source.branch || 'branch'} @ ${at}`;
  return source.dirtyKnown && source.dirty ? `${where} · ${source.dirtyFiles} uncommitted IaC file${source.dirtyFiles === 1 ? '' : 's'}` : where;
}

export function PlanReview({ plan, copy, onApply, onDiscarded, onRunPlan, onOpenDrift, busy, canPlan = true, onInspectImpact, scope = launchScope, source: editorSource }: { plan: Plan; copy: (value: string, label: string) => Promise<void>; onApply?: () => void; onDiscarded?: () => void; onRunPlan?: () => void; onOpenDrift?: () => void; busy?: boolean; canPlan?: boolean; onInspectImpact?: (planId: string, address: string) => void; scope?: PlanScope; source?: PlanSource }) {
  const [tab, setTab] = useState<Tab>('changes');
  const [actions, setActions] = useState<PlanAction[]>([]);
  const [showNoOp, setShowNoOp] = useState(false);
  const [search, setSearch] = useState('');
  const [limit, setLimit] = useState(pageSize);
  const expired = useExpired(plan);
  const client = useQueryClient();
  // Discard deletes the saved plan file on the server; the card then shows the server's note.
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [discarding, setDiscarding] = useState(false);
  const [discardError, setDiscardError] = useState('');
  async function discard() {
    setDiscarding(true);
    setDiscardError('');
    try {
      await send('POST', scopedPath(scope, `plans/${plan.id}/discard`), {});
      setConfirmDiscard(false);
      onDiscarded?.();
      await client.invalidateQueries({ queryKey: ['plan'] });
      void client.invalidateQueries({ queryKey: ['plans'] });
      void client.invalidateQueries({ queryKey: ['operations'] });
    } catch (e) {
      setDiscardError(errorMessage(e, 'The plan could not be discarded.'));
    } finally {
      setDiscarding(false);
    }
  }
  const engine = engineLabel(plan.engine);
  const source = tab === 'drift' ? plan.drift : plan.changes;
  // Action filters and the no-op toggle apply to planned changes only, never to drift records.
  const visible = useMemo(() => source.filter(c => (tab === 'drift' || (actions.length ? actions.includes(c.action) : showNoOp || c.action !== 'no-op')) && (!search || c.address.toLowerCase().includes(search.toLowerCase()) || c.type.includes(search.toLowerCase()))), [source, actions, showNoOp, search, tab]);
  const total = planTotal(plan.summary);
  const outputChanges = plan.outputs.filter(o => o.action !== 'no-op').length;
  function toggle(action: PlanAction) {
    setActions(current => current.includes(action) ? current.filter(a => a !== action) : [...current, action]);
    setTab('changes');
    setLimit(pageSize);
  }
  const ai = <AskAI scope={scope} request={{ kind: 'plan', planId: plan.id }} label="Review with AI" />;
  return <div className="plan-review">
   <div className="plan-meta">
    <div><span className="muted">Plan time</span><strong>{clock(plan.createdAt)}</strong></div>
    <div><span className="muted">Engine</span><strong>{engine} {plan.engineVersion || plan.currentEngineVersion}</strong></div>
    <div><span className="muted">Workspace</span><strong>{plan.workspace}</strong></div>
    <div><span className="muted">Variable files</span><strong>{plan.varFiles.length ? plan.varFiles.join(', ') : 'Automatic only'}</strong></div>
    <div><span className="muted">Execution context</span><strong><RecordedContext context={plan.executionContext} /></strong></div>
    <div><span className="muted">Source</span><strong className="mono" title={plan.source?.commit}><GitBranch size={12} /> {sourceLabel(plan.source)}</strong></div>
    <div><span className="muted">Saved plan SHA-256</span><button className="hash" onClick={() => void copy(plan.artifactSha256, 'Plan digest')} title={plan.artifactSha256} aria-label="Copy saved plan SHA-256"><Fingerprint size={12} />{plan.artifactSha256.slice(0, 12)}<Copy size={11} /></button></div>
   </div>
   {plan.kind === 'replace' && <ReplaceSummary plan={plan} onInspectImpact={onInspectImpact} />}
   {plan.kind === 'import' && plan.import && <ImportSummary plan={plan} copy={copy} />}
   {expired ? <div className="apply-banner apply-expired" role="alert"><Clock size={16} aria-hidden="true" /><div><strong>This saved plan expired{plan.apply.expiresAt ? ` at ${clock(plan.apply.expiresAt)}` : ''}.</strong> It can no longer be applied, because state or configuration may have changed since. Run a new plan to review the changes again.</div>{onRunPlan && <Button onClick={onRunPlan} disabled={!canPlan}><Play size={14} />Run new plan</Button>}</div>
    : plan.apply.available ? <div className="apply-banner apply-ready" role="note"><Rocket size={16} aria-hidden="true" /><div><strong>Ready to apply.</strong> Applies exactly this saved plan, once{plan.apply.expiresAt && `, until ${clock(plan.apply.expiresAt)}`}.{destructiveLine(plan) && <span className="apply-banner-warning"><TriangleAlert size={13} aria-hidden="true" />{destructiveLine(plan)}</span>}</div><div className="apply-banner-actions"><Button variant="ghost" onClick={() => { setDiscardError(''); setConfirmDiscard(true); }} disabled={busy}>Discard</Button>{confirmDiscard && <Dialog title="Delete the saved plan?" onClose={() => !discarding && setConfirmDiscard(false)} footer={<><Button variant="ghost" onClick={() => setConfirmDiscard(false)} disabled={discarding}>Keep</Button><Button variant="destructive" onClick={() => void discard()} disabled={discarding}>{discarding ? 'Deleting…' : 'Delete plan'}</Button></>}><p>It can't be applied afterwards.</p>{discardError && <p className="dialog-error" role="alert">{discardError}</p>}</Dialog>}{ai}{onApply && <Button variant={plan.destructive ? 'destructive' : 'default'} onClick={onApply} disabled={busy}>Review and apply…</Button>}</div></div>
    : plan.apply.code === 'unknown_actions' ? <div className="apply-banner apply-unsupported" role="alert"><Ban size={16} aria-hidden="true" /><div><strong>Apply unavailable: unsupported actions.</strong> {plan.apply.reason} Changes marked <em>Unsupported</em> below use an action combination Hclaro does not recognize, so this plan can be reviewed but never applied here.</div><div className="apply-banner-actions">{ai}</div></div>
    : <div className="apply-banner" role="note"><LockKeyhole size={16} aria-hidden="true" /><div><strong>Review only.</strong> {plan.apply.reason}</div><div className="apply-banner-actions">{ai}</div></div>}
   {plan.contextChanged && plan.apply.code !== 'configuration_unreadable' && <div className="plan-warning" role="alert"><TriangleAlert size={15} />{plan.apply.configurationReason ?? 'Configuration, variable files, the engine or the execution profile changed since this plan was created. Run a new plan to review current changes.'}</div>}
   {plan.sourceStatus?.state === 'changed' && <div className="plan-warning" role="alert"><GitBranch size={15} />{plan.sourceStatus.message}{plan.sourceStatus.filesChanged ? ' Run a new plan before applying.' : ' Applying needs your acknowledgement.'}</div>}
   {plan.sourceStatus?.state === 'unknown' && <div className="plan-warning" role="alert"><GitBranch size={15} />{plan.sourceStatus.message}</div>}
   {plan.warnings.map(w => <div className="plan-warning" key={w} role="alert"><TriangleAlert size={15} />{w}</div>)}
   <div className="summary-grid" role="group" aria-label="Filter by action">{actionOrder.map(a => {
    const { label, icon: Icon, tone } = meta(a);
    const count = plan.summary[a] || 0;
    return <button key={a} className={`summary-card tone-${tone} ${actions.includes(a) ? 'chosen' : ''}`} aria-pressed={actions.includes(a)} disabled={!count} onClick={() => toggle(a)}><span><Icon size={14} aria-hidden="true" />{label}</span><strong>{count}</strong></button>;
   })}</div>
   <p className="plan-headline">{total === 0 && outputChanges === 0 && !plan.imports ? 'No changes. Infrastructure matches the configuration.' : `${plan.imports ? `${plan.imports} import${plan.imports === 1 ? '' : 's'}, ` : ''}${total} resource change${total === 1 ? '' : 's'} and ${outputChanges} output change${outputChanges === 1 ? '' : 's'}, including every dependent and collateral change reported by the engine.`}</p>
   <div className="segmented plan-tabs" role="tablist" aria-label="Plan sections">
    <button role="tab" aria-selected={tab === 'changes'} onClick={() => setTab('changes')}>Resource changes <span>{plan.changes.length}</span></button>
    <button role="tab" aria-selected={tab === 'outputs'} onClick={() => setTab('outputs')}>Outputs <span>{plan.outputs.length}</span></button>
    <button role="tab" aria-selected={tab === 'drift'} onClick={() => setTab('drift')}>Changed outside {engine} <span>{plan.drift.length}</span></button>
    <button role="tab" aria-selected={tab === 'cost'} onClick={() => setTab('cost')}>Cost estimate</button>
   </div>
   {tab === 'cost' ? <CostTab plan={plan} scope={scope} /> : tab === 'outputs' ? <Outputs outputs={plan.outputs} /> : <>
    {tab === 'drift' && <p className="info-line">The engine noticed these objects changed outside {engine} while planning. The plan already accounts for them; they are not planned changes.{onOpenDrift && <Button variant="ghost" size="small" onClick={onOpenDrift}>See drift detection →</Button>}</p>}
    <div className="filter-row plan-filters"><div className="search-field compact"><Search size={14} /><input value={search} onChange={e => { setSearch(e.target.value); setLimit(pageSize); }} placeholder="Filter by address or type…" aria-label="Filter changes" />{search && <button className="clear-search" aria-label="Clear filter" onClick={() => setSearch('')}><X size={12} /></button>}</div>{tab === 'changes' && <label className="check"><input type="checkbox" checked={showNoOp} onChange={e => setShowNoOp(e.target.checked)} />Show unchanged</label>}{(actions.length > 0 || search) && <Button variant="ghost" size="small" onClick={() => { setActions([]); setSearch(''); }}><X size={12} />Reset</Button>}<span className="filter-count">{visible.length} shown</span></div>
    <section className="plan-changes" aria-label={tab === 'drift' ? 'Changes outside the engine' : 'Planned resource changes'}>
     {visible.length === 0 ? <div className="empty-state"><Braces size={26} /><h2>{source.length ? 'No matching changes' : tab === 'drift' ? 'No drift detected' : 'No resource changes'}</h2></div> : <DataTable key={tab} label={tab === 'drift' ? 'Changes outside the engine' : 'Planned resource changes'} rows={visible.slice(0, limit)} rowKey={c => c.id} rowLabel={c => c.address} columns={tab === 'drift' ? driftColumns : changeColumns} expand={c => <ChangeDetails scope={scope} planId={plan.id} changeId={c.id} copy={copy} source={editorSource} />} />}
     {visible.length > limit && <div className="pagination"><span>Showing {limit} of {visible.length}</span><Button variant="outline" size="small" onClick={() => setLimit(limit + pageSize)}>Show more</Button></div>}
    </section>
   </>}
  </div>;
}

// useExpired is true once the saved plan's deadline passes, on the server's word or this clock's.
// The server checks the deadline again on every apply request; this only stops offering apply.
function useExpired(plan: Plan) {
  const deadline = plan.apply.expiresAt ? Date.parse(plan.apply.expiresAt) : NaN;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!plan.apply.available || Number.isNaN(deadline) || now >= deadline) return;
    const timer = setTimeout(() => setNow(Date.now()), Math.min(deadline - now, 2 ** 31 - 1));
    return () => clearTimeout(timer);
  }, [plan.apply.available, deadline, now]);
  return plan.apply.code === 'plan_expired' || plan.apply.available && now >= deadline;
}

// destructiveLine is the one amber line of the Ready card: "Deletes 2 and replaces 1 objects."
export function destructiveLine(plan: Pick<Plan, 'summary'>) {
  const parts = ([['delete', 'deletes'], ['replace', 'replaces'], ['forget', 'forgets']] as const).filter(([a]) => plan.summary[a]).map(([a, verb]) => ({ verb, n: plan.summary[a]! }));
  if (!parts.length) return '';
  const words = parts.map(p => `${p.verb} ${p.n}`);
  const text = words.length > 1 ? `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}` : words[0];
  const objects = parts.reduce((n, p) => n + p.n, 0) === 1 ? 'object' : 'objects';
  return `${text[0].toUpperCase()}${text.slice(1)} ${objects}.`;
}

const resourceCell = (c: PlanChange) => <span className="resource-text"><strong className="mono change-address">{c.address}</strong><span className="resource-type">{c.type}{c.deposed && ` · deposed object ${c.deposed}`}{c.mode === 'data' && ' · data source'}</span></span>;
const changeColumns: Column<PlanChange>[] = [
  { key: 'action', header: 'Action', cell: c => <ActionBadge action={c.action} /> },
  { key: 'resource', header: 'Resource', cell: resourceCell },
  { key: 'why', header: 'Why', className: 'change-why', cell: c => <>{reasonLabel(c.reason) || (c.attributeChanges ? `${c.attributeChanges} attribute${c.attributeChanges === 1 ? '' : 's'}` : '')}{c.importing && <span className="flag"><Import size={11} />import</span>}{c.previousAddress && <span className="flag"><ArrowRightLeft size={11} />moved</span>}</> }
];
const driftColumns: Column<PlanChange>[] = [
  { key: 'action', header: 'Drift', cell: () => <span className="action-badge tone-update"><Pencil size={12} aria-hidden="true" />Changed</span> },
  { key: 'resource', header: 'Resource', cell: resourceCell },
  changeColumns[2]
];

// Replacement is never only what was selected: show requested instances next to every
// other change, and the risks the plan itself cannot rule out.
function ReplaceSummary({ plan, onInspectImpact }: { plan: Plan; onInspectImpact?: (planId: string, address: string) => void }) {
  const requested = new Set(plan.replace);
  const collateral = plan.changes.filter(c => c.action !== 'no-op' && c.action !== 'read' && !(requested.has(c.address) && !c.deposed));
  const destructiveCollateral = collateral.filter(c => c.action === 'delete' || c.action === 'replace' || c.action === 'unknown');
  return <section className="replace-summary" aria-label="Requested replacements">
   <h2><RefreshCcw size={15} />Replacement requested for {plan.replace.length} instance{plan.replace.length === 1 ? '' : 's'}</h2>
   <ul>{plan.replace.map(address => {
    const change = plan.changes.find(c => c.address === address && !c.deposed);
    return <li key={address}><code>{address}</code>{change ? <ActionBadge action={change.action} /> : <span className="flag destroy">not in plan</span>}</li>;
   })}</ul>
   {collateral.length > 0 && <p className={destructiveCollateral.length ? 'plan-warning' : 'info-line'} role={destructiveCollateral.length ? 'alert' : undefined}><TriangleAlert size={14} />{collateral.length} other change{collateral.length === 1 ? ' is' : 's are'} part of this plan{destructiveCollateral.length > 0 && `, including ${destructiveCollateral.length} deletion${destructiveCollateral.length === 1 ? '' : 's'} or replacement${destructiveCollateral.length === 1 ? '' : 's'} you did not select`}. Review them below; they are applied together.</p>}
   <p className="info-line">Hclaro never uses -target, so applying this plan also applies every unrelated change in it. Replacement is limited to 50 instances per plan.</p>
   <p className="muted">Replacing destroys and recreates objects. Unless the provider creates the new object first, expect downtime, and data stored in the old object can be lost. The plan shows the order for each change; it cannot prove what depends on these objects outside {engineLabel(plan.engine)}.</p>
   {onInspectImpact && plan.replace.length > 0 && <div className="replace-impact"><span className="muted">Optional: see what depends on {plan.replace.length === 1 ? 'this resource' : 'these resources'} in the dependency graph, next to the changes this plan confirms. It does not change what is applied.</span><Button variant="outline" size="small" onClick={() => onInspectImpact(plan.id, plan.replace[0])}><Waypoints size={13} />Inspect impact</Button></div>}
  </section>;
}

// An import is never only the import: show the requested target next to every other change,
// and the configuration the engine generated, before anything can be applied.
function ImportSummary({ plan, copy }: { plan: Plan; copy: (value: string, label: string) => Promise<void> }) {
  const target = plan.import!;
  const change = plan.changes.find(c => c.address === target.address && !c.deposed);
  const others = plan.changes.filter(c => c !== change && c.action !== 'no-op' && c.action !== 'read');
  const destructive = others.filter(c => c.action === 'delete' || c.action === 'replace' || c.action === 'unknown');
  const targetChanges = change && change.action !== 'no-op';
  return <section className="replace-summary import-summary" aria-label="Requested import">
   <h2><Import size={15} />Import requested</h2>
   <ul><li><code>{target.address}</code>{change?.importing ? <><span className="flag"><Import size={11} />import</span>{targetChanges && <ActionBadge action={change.action} />}</> : <span className="flag destroy">not imported by this plan</span>}</li></ul>
   {targetChanges && <p className={change.action === 'replace' || change.action === 'delete' ? 'plan-warning' : 'info-line'} role={change.action === 'replace' ? 'alert' : undefined}><TriangleAlert size={14} />After importing, the plan also {change.action === 'update' ? 'updates the object to match your configuration' : change.action === 'replace' ? 'destroys and recreates the object, because the configuration differs in an attribute that cannot change in place' : `plans to ${change.action} it`}. Adjust the configuration first if that is not intended.</p>}
   {others.length > 0 && <p className={destructive.length ? 'plan-warning' : 'info-line'} role={destructive.length ? 'alert' : undefined}><TriangleAlert size={14} />{others.length} other change{others.length === 1 ? ' is' : 's are'} part of this plan{destructive.length > 0 && `, including ${destructive.length} deletion${destructive.length === 1 ? '' : 's'} or replacement${destructive.length === 1 ? '' : 's'}`}. They are applied together with the import.</p>}
   <p className="muted">The import block is in <code>{target.file}</code>{target.generatedFile && <> and the generated configuration in <code>{target.generatedFile}</code></>}. Applying records the existing object in state; it does not create it. To back out before applying, delete {target.generatedFile ? 'these files' : 'the file'}; nothing in state has changed.</p>
   {target.generatedFile && <details className="generated-config" open>
    <summary>Generated configuration <code>{target.generatedFile}</code></summary>
    <p className="redaction-note"><LockKeyhole size={12} />Shown with secret-like values hidden. The file on disk contains the values {engineLabel(plan.engine)} read from the object: review it in your editor before committing it.</p>
    {plan.generatedConfig ? <><div className="file-diff-header"><span className="muted">Redacted copy</span><Button variant="ghost" size="icon" aria-label="Copy generated configuration" onClick={() => void copy(plan.generatedConfig!, 'Generated configuration')}><Copy size={13} /></Button></div><pre>{plan.generatedConfig}</pre></> : <p className="muted">No generated configuration was found.</p>}
    {plan.generatedConfigTruncated && <p className="muted">Only the first 1 MiB is shown.</p>}
   </details>}
  </section>;
}

const shownAttributes = 10;

// The expanded row (§4.3): the attribute diff, with redacted values staying redacted.
function ChangeDetails({ scope, planId, changeId, copy, source }: { scope: PlanScope; planId: string; changeId: string; copy: (value: string, label: string) => Promise<void>; source?: PlanSource }) {
  const detail = useQuery({ queryKey: ['plan-change', scopeKey(scope), planId, changeId], queryFn: () => api<ChangeDetail>(scopedPath(scope, `plans/${planId}/changes/${changeId}`)) });
  const [all, setAll] = useState(false);
  const d = detail.data;
  if (detail.isPending) return <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Loading change…</p>;
  if (detail.error) return <ErrorState inline message={detail.error.message} />;
  if (!d) return null;
  const order = replaceOrder(d.actions);
  const rows = all ? d.attributes : d.attributes.slice(0, shownAttributes);
  return <div className="change-details" aria-label={`Change details for ${d.address}`}>
   {order && <p className="order-note"><TriangleAlert size={13} aria-hidden="true" />{order}.</p>}
   {d.action === 'unknown' && <p className="plan-warning"><TriangleAlert size={14} />Unsupported action combination. Hclaro does not apply a plan containing it; treat it as potentially destructive and inspect it with the CLI.</p>}
   <dl className="metadata compact-metadata">
    <div><dt>Engine actions</dt><dd className="mono">{d.actions.join(', ')}</dd></div>
    {d.previousAddress && <div><dt>Moved from</dt><dd className="mono">{d.previousAddress}</dd></div>}
    {d.deposed && <div><dt>Deposed object</dt><dd className="mono">{d.deposed}</dd></div>}
    {d.importing && <div><dt>Import</dt><dd>This object will be imported</dd></div>}
    <div><dt>Provider</dt><dd className="mono">{d.provider || 'Not recorded'}</dd></div>
    <div><dt>Module</dt><dd className="mono">{d.module || 'Root module'}</dd></div>
   </dl>
   <div className="redaction-note"><LockKeyhole size={12} />Values Terraform marks sensitive, secret-named attributes and credential-like values are hidden; a change to a hidden value is still listed.</div>
   {d.attributes.length ? <AttributeTable rows={rows} /> : <p className="muted">No attribute changes.</p>}
   {d.attributes.length > rows.length && <Button variant="ghost" size="small" onClick={() => setAll(true)}>+ {d.attributes.length - rows.length} more attribute{d.attributes.length - rows.length === 1 ? '' : 's'}</Button>}
   {d.truncated && <p className="muted">Only the first 1,000 attribute changes are shown.</p>}
   {source && <SourceLink prefix={source.prefix} sourcePath={scopedPath(scope, `plans/${planId}/changes/${changeId}/source`)} editor={source.editor} notify={source.notify} />}
   <div className="change-details-actions">
    <Button variant="ghost" size="small" onClick={() => void copy(d.address, 'Address')}><Copy size={12} />Copy address</Button>
   </div>
   <Disclosure summary="Full values before and after"><div className="json-viewer"><JsonTree name="before" value={d.before ?? {}} /><JsonTree name="after" value={d.after ?? {}} /></div></Disclosure>
  </div>;
}

function AttributeTable({ rows }: { rows: AttributeChange[] }) {
  return <table className="diff-table"><thead><tr><th scope="col">Attribute</th><th scope="col">Before</th><th scope="col">After</th></tr></thead><tbody>{rows.map(r => <tr key={r.path} className={`diff-${r.kind}`}>
   <th scope="row"><code>{r.path}</code>{r.forcesReplacement && <span className="flag destroy">forces replacement</span>}{r.sensitive && <span className="flag"><LockKeyhole size={10} />sensitive</span>}</th>
   <td><Value value={r.before} empty="not set" /></td>
   <td><Value value={r.after} unknown={r.unknown} empty="removed" /></td>
  </tr>)}</tbody></table>;
}

// Output values are never shown (§4.3), sensitive or not: only the name and the change.
function Outputs({ outputs }: { outputs: OutputChange[] }) {
  if (!outputs.length) return <div className="empty-state"><Braces size={26} /><h2>No output changes</h2></div>;
  return <div className="surface outputs"><table className="diff-table"><thead><tr><th scope="col">Output</th><th scope="col">Action</th><th scope="col">Value</th></tr></thead><tbody>{outputs.map(o => <tr key={o.name}><th scope="row"><code>{o.name}</code>{o.sensitive && <span className="flag"><LockKeyhole size={10} />sensitive</span>}</th><td><ActionBadge action={o.action} /></td><td className="muted">value hidden</td></tr>)}</tbody></table><p className="redaction-note"><LockKeyhole size={12} />Output values are never shown in Hclaro, including values that aren’t marked sensitive.</p></div>;
}
