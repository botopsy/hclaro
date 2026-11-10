import { useMemo, useState, type KeyboardEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowRightLeft, Ban, Braces, Copy, Fingerprint, GitBranch, Import, LoaderCircle, LockKeyhole, RefreshCcw, Rocket, Search, TriangleAlert, Waypoints, X } from 'lucide-react';
import { api } from '../api';
import type { AttributeChange, ChangeDetail, OutputChange, Plan, PlanAction, PlanChange, PlanSourceFingerprint } from '../types';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { JsonTree } from '../components/JsonTree';
import { actionOrder, clock, engineLabel, meta, reasonLabel, replaceOrder } from './labels';
import { RecordedContext } from '../profiles/ContextSummary';
import { CostTab } from './CostTab';
import { launchScope, scopeKey, scopedPath, type PlanScope } from './routes';

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

export function PlanReview({ plan, copy, onApply, busy, onInspectImpact, scope = launchScope }: { plan: Plan; copy: (value: string, label: string) => Promise<void>; onApply?: () => void; busy?: boolean; onInspectImpact?: (planId: string, address: string) => void; scope?: PlanScope }) {
  const [tab, setTab] = useState<Tab>('changes');
  const [actions, setActions] = useState<PlanAction[]>([]);
  const [showNoOp, setShowNoOp] = useState(false);
  const [search, setSearch] = useState('');
  const [limit, setLimit] = useState(pageSize);
  const [selected, setSelected] = useState<string>();
  const source = tab === 'drift' ? plan.drift : plan.changes;
  // Action filters and the no-op toggle apply to planned changes only, never to drift records.
  const visible = useMemo(() => source.filter(c => (tab === 'drift' || (actions.length ? actions.includes(c.action) : showNoOp || c.action !== 'no-op')) && (!search || c.address.toLowerCase().includes(search.toLowerCase()) || c.type.includes(search.toLowerCase()))), [source, actions, showNoOp, search, tab]);
  const total = actionOrder.reduce((n, a) => n + (a === 'no-op' ? 0 : plan.summary[a] || 0), 0);
  const outputChanges = plan.outputs.filter(o => o.action !== 'no-op').length;
  function toggle(action: PlanAction) {
    setActions(current => current.includes(action) ? current.filter(a => a !== action) : [...current, action]);
    setTab('changes');
    setLimit(pageSize);
  }
  return <div className="plan-review">
   <div className="plan-meta">
    <div><span className="muted">Plan</span><strong>{clock(plan.createdAt)}</strong></div>
    <div><span className="muted">Engine</span><strong>{engineLabel(plan.engine)} {plan.engineVersion || plan.currentEngineVersion}</strong></div>
    <div><span className="muted">Workspace</span><strong>{plan.workspace}</strong></div>
    <div><span className="muted">Variable files</span><strong>{plan.varFiles.length ? plan.varFiles.join(', ') : 'Automatic only'}</strong></div>
    <div><span className="muted">Execution context</span><strong><RecordedContext context={plan.executionContext} /></strong></div>
    <div><span className="muted">Source</span><strong className="mono" title={plan.source?.commit}><GitBranch size={12} /> {sourceLabel(plan.source)}</strong></div>
    <div><span className="muted">Saved plan SHA-256</span><button className="hash" onClick={() => void copy(plan.artifactSha256, 'Plan digest')} title={plan.artifactSha256} aria-label="Copy saved plan SHA-256"><Fingerprint size={12} />{plan.artifactSha256.slice(0, 12)}<Copy size={11} /></button></div>
   </div>
   {plan.kind === 'replace' && <ReplaceSummary plan={plan} onInspectImpact={onInspectImpact} />}
   {plan.kind === 'import' && plan.import && <ImportSummary plan={plan} copy={copy} />}
   {plan.apply.available ? <div className="apply-banner apply-ready" role="note"><Rocket size={15} /><div><strong>Ready to apply.</strong> Applying uses exactly this saved plan, once{plan.apply.expiresAt && `, until ${clock(plan.apply.expiresAt)}`}. You confirm on the next screen.</div>{onApply && <Button variant={plan.destructive ? 'destructive' : 'default'} onClick={onApply} disabled={busy}><Rocket size={14} />Review and apply…</Button>}</div> : plan.apply.code === 'unknown_actions' ? <div className="apply-banner apply-unsupported" role="alert"><Ban size={15} /><div><strong>Apply unavailable: unsupported actions.</strong> {plan.apply.reason} Changes marked <em>Unsupported</em> below use an action combination Hclaro does not recognize, so this plan can be reviewed but never applied here.</div></div> : <div className="apply-banner" role="note"><LockKeyhole size={15} /><div><strong>Review only.</strong> {plan.apply.reason}</div></div>}
   {plan.contextChanged && <div className="plan-warning" role="alert"><TriangleAlert size={15} />Configuration, variable files, the engine or the execution profile changed since this plan was created. Run a new plan to review current changes.</div>}
   {plan.sourceStatus?.state === 'changed' && <div className="plan-warning" role="alert"><GitBranch size={15} />{plan.sourceStatus.message}{plan.sourceStatus.filesChanged ? ' Run a new plan before applying.' : ' Applying needs your acknowledgement.'}</div>}
   {plan.sourceStatus?.state === 'unknown' && <div className="plan-warning" role="alert"><GitBranch size={15} />{plan.sourceStatus.message}</div>}
   {plan.warnings.map(w => <div className="plan-warning" key={w} role="alert"><TriangleAlert size={15} />{w}</div>)}
   <div className="summary-grid" role="group" aria-label="Filter by action">{actionOrder.filter(a => a !== 'no-op' || plan.summary['no-op']).map(a => {
    const { label, icon: Icon, tone } = meta(a);
    const count = plan.summary[a] || 0;
    return <button key={a} className={`summary-card tone-${tone} ${actions.includes(a) ? 'chosen' : ''}`} aria-pressed={actions.includes(a)} disabled={!count} onClick={() => toggle(a)}><span><Icon size={14} aria-hidden="true" />{label}</span><strong>{count}</strong></button>;
   })}</div>
   <p className="plan-headline">{total === 0 && outputChanges === 0 && !plan.imports ? 'No changes. Infrastructure matches the configuration.' : `${plan.imports ? `${plan.imports} import${plan.imports === 1 ? '' : 's'}, ` : ''}${total} resource change${total === 1 ? '' : 's'} and ${outputChanges} output change${outputChanges === 1 ? '' : 's'}, including every dependent and collateral change reported by the engine.`}</p>
   <div className="segmented plan-tabs" role="tablist" aria-label="Plan sections">
    <button role="tab" aria-selected={tab === 'changes'} onClick={() => { setTab('changes'); setSelected(undefined); }}>Resource changes <span>{plan.changes.length}</span></button>
    <button role="tab" aria-selected={tab === 'outputs'} onClick={() => { setTab('outputs'); setSelected(undefined); }}>Outputs <span>{plan.outputs.length}</span></button>
    <button role="tab" aria-selected={tab === 'drift'} onClick={() => { setTab('drift'); setSelected(undefined); }}>Changed outside {engineLabel(plan.engine)} <span>{plan.drift.length}</span></button>
    <button role="tab" aria-selected={tab === 'cost'} onClick={() => { setTab('cost'); setSelected(undefined); }}>Cost estimate</button>
   </div>
   {tab === 'cost' ? <CostTab plan={plan} /> : tab === 'outputs' ? <Outputs outputs={plan.outputs} /> : <>
    {tab === 'drift' && <p className="info-line">These objects changed outside {engineLabel(plan.engine)} since the last state was recorded. They are not planned changes, but they explain differences in the plan.</p>}
    <div className="filter-row plan-filters"><div className="search-field compact"><Search size={14} /><input value={search} onChange={e => { setSearch(e.target.value); setLimit(pageSize); }} placeholder="Filter by address or type…" aria-label="Filter changes" />{search && <button className="clear-search" aria-label="Clear filter" onClick={() => setSearch('')}><X size={12} /></button>}</div>{tab === 'changes' && <label className="check"><input type="checkbox" checked={showNoOp} onChange={e => setShowNoOp(e.target.checked)} />Show unchanged</label>}{(actions.length > 0 || search) && <Button variant="ghost" size="small" onClick={() => { setActions([]); setSearch(''); }}><X size={12} />Reset</Button>}<span className="filter-count">{visible.length} shown</span></div>
    <div className={`explorer plan-explorer ${selected ? 'with-inspector' : ''}`}>
     <section className="resource-list" aria-label={tab === 'drift' ? 'Changes outside the engine' : 'Planned resource changes'}>
      {visible.length === 0 ? <div className="empty-state"><Braces size={26} /><h2>{source.length ? 'No matching changes' : tab === 'drift' ? 'No drift detected' : 'No resource changes'}</h2></div> : <table className="resources-table changes-table"><thead><tr><th scope="col">Action</th><th scope="col">Resource</th><th scope="col">Why</th></tr></thead><tbody>{visible.slice(0, limit).map(c => <ChangeRow key={c.id} change={c} selected={selected === c.id} onSelect={() => setSelected(c.id)} />)}</tbody></table>}
      {visible.length > limit && <div className="pagination"><span>Showing {limit} of {visible.length}</span><Button variant="outline" size="small" onClick={() => setLimit(limit + pageSize)}>Show more</Button></div>}
     </section>
     {selected && <ChangeInspector scope={scope} planId={plan.id} changeId={selected} close={() => setSelected(undefined)} copy={copy} />}
    </div>
   </>}
  </div>;
}

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

function ChangeRow({ change: c, selected, onSelect }: { change: PlanChange; selected: boolean; onSelect: () => void }) {
  function key(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    const buttons = Array.from(event.currentTarget.closest('tbody')!.querySelectorAll<HTMLButtonElement>('[data-change-id]'));
    const index = buttons.indexOf(event.currentTarget);
    buttons[Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
  }
  return <tr className={selected ? 'selected' : ''}>
   <td><ActionBadge action={c.action} /></td>
   <td><button className="resource-button" data-change-id={c.id} aria-pressed={selected} onClick={onSelect} onKeyDown={key}><span className="resource-text"><strong className="mono change-address">{c.address}</strong><span className="resource-type">{c.type}{c.deposed && ` · deposed object ${c.deposed}`}{c.mode === 'data' && ' · data source'}</span></span></button></td>
   <td className="change-why">{reasonLabel(c.reason) || (c.attributeChanges ? `${c.attributeChanges} attribute${c.attributeChanges === 1 ? '' : 's'}` : '')}{c.importing && <span className="flag"><Import size={11} />import</span>}{c.previousAddress && <span className="flag"><ArrowRightLeft size={11} />moved</span>}</td>
  </tr>;
}

function ChangeInspector({ scope, planId, changeId, close, copy }: { scope: PlanScope; planId: string; changeId: string; close: () => void; copy: (value: string, label: string) => Promise<void> }) {
  const detail = useQuery({ queryKey: ['plan-change', scopeKey(scope), planId, changeId], queryFn: () => api<ChangeDetail>(scopedPath(scope, `plans/${planId}/changes/${changeId}`)) });
  const d = detail.data;
  const order = d && replaceOrder(d.actions);
  return <Drawer label="Change details" onClose={close}>
   <div className="inspector-header"><span><Braces size={15} />Change details</span><Button variant="ghost" size="icon" aria-label="Close change details" onClick={close}><X size={16} /></Button></div>
   {detail.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading change…</div> : detail.error ? <div className="empty-state error-state" role="alert"><p>{detail.error.message}</p></div> : d && <div className="inspector-body">
    <div className="inspector-title"><ActionBadge action={d.action} /><span className="mono muted">{d.actions.join(', ')}</span></div>
    <div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy change address" onClick={() => void copy(d.address, 'Address')}><Copy size={13} /></Button></div><code>{d.address}</code></div>
    {order && <p className="order-note"><TriangleAlert size={13} aria-hidden="true" />{order}.</p>}
    {d.action === 'unknown' && <p className="plan-warning"><TriangleAlert size={14} />Unsupported action combination. Hclaro does not apply a plan containing it; treat it as potentially destructive and inspect it with the CLI.</p>}
    <dl className="metadata">
     {d.reason && <div><dt>Reason</dt><dd>{reasonLabel(d.reason)}</dd></div>}
     {d.previousAddress && <div><dt>Moved from</dt><dd className="mono">{d.previousAddress}</dd></div>}
     {d.deposed && <div><dt>Deposed object</dt><dd className="mono">{d.deposed}</dd></div>}
     {d.importing && <div><dt>Import</dt><dd>This object will be imported</dd></div>}
     <div><dt>Provider</dt><dd className="mono">{d.provider || 'Not recorded'}</dd></div>
     <div><dt>Module</dt><dd className="mono">{d.module || 'Root module'}</dd></div>
    </dl>
    <div className="attributes-header"><h3>Attribute changes</h3><span className="muted">{d.attributes.length}{d.truncated && '+'}</span></div>
    <div className="redaction-note"><LockKeyhole size={12} />Values Terraform marks sensitive, secret-named attributes and credential-like values are hidden; a change to a hidden value is still listed.</div>
    {d.attributes.length ? <AttributeTable rows={d.attributes} /> : <p className="muted">No attribute changes.</p>}
    {d.truncated && <p className="muted">Only the first 1,000 attribute changes are shown.</p>}
    <details className="full-values"><summary>Full values before and after</summary><div className="json-viewer"><JsonTree name="before" value={d.before ?? {}} /><JsonTree name="after" value={d.after ?? {}} /></div></details>
   </div>}
  </Drawer>;
}

function AttributeTable({ rows }: { rows: AttributeChange[] }) {
  return <table className="diff-table"><thead><tr><th scope="col">Attribute</th><th scope="col">Before</th><th scope="col">After</th></tr></thead><tbody>{rows.map(r => <tr key={r.path} className={`diff-${r.kind}`}>
   <th scope="row"><code>{r.path}</code>{r.forcesReplacement && <span className="flag destroy">forces replacement</span>}{r.sensitive && <span className="flag"><LockKeyhole size={10} />sensitive</span>}</th>
   <td><Value value={r.before} empty="not set" /></td>
   <td><Value value={r.after} unknown={r.unknown} empty="removed" /></td>
  </tr>)}</tbody></table>;
}

function Outputs({ outputs }: { outputs: OutputChange[] }) {
  if (!outputs.length) return <div className="empty-state"><Braces size={26} /><h2>No output changes</h2></div>;
  return <div className="surface outputs"><p className="redaction-note"><LockKeyhole size={12} />Sensitive outputs, secret-named outputs and credential-like values are hidden.</p><table className="diff-table"><thead><tr><th scope="col">Output</th><th scope="col">Action</th><th scope="col">Before</th><th scope="col">After</th></tr></thead><tbody>{outputs.map(o => <tr key={o.name}><th scope="row"><code>{o.name}</code>{o.sensitive && <span className="flag"><LockKeyhole size={10} />sensitive</span>}</th><td><ActionBadge action={o.action} /></td><td><Value value={o.before} empty="not set" /></td><td><Value value={o.after} unknown={o.unknown} empty="removed" /></td></tr>)}</tbody></table></div>;
}
