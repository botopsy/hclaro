import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleDollarSign, CircleHelp, CircleSlash, CloudOff, Gift, LoaderCircle, LockKeyhole, Play, Power, ShieldCheck, Square, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, ApiError, post } from '../api';
import type { CostAggregate, CostEstimate, CostItemStatus, CostLineItem, CostProviderCapabilities, ExternalDataDisclosure, Plan } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { clock } from './labels';
import { ActionBadge } from './PlanReview';

// Amounts arrive as decimal strings rounded to cents. They are shown as received, with an
// explicit sign and currency code, so no floating-point rounding is added in the browser.
export function signed(amount: string) {
  if (/^-/.test(amount)) return `−${amount.slice(1)}`;
  return /^0(\.0+)?$/.test(amount) ? amount : `+${amount}`;
}

// Unpriced statuses are labelled with text and an icon; they never show an amount.
const statusLabels: Record<CostItemStatus, { label: string; icon: LucideIcon; hint: string }> = {
  estimated: { label: 'Estimated', icon: CircleDollarSign, hint: 'Priced from list prices.' },
  partial: { label: 'Partly estimated', icon: CircleHelp, hint: 'Fixed charges are priced; usage-based charges are not included.' },
  usage_unknown: { label: 'Usage-based', icon: CircleHelp, hint: 'The cost depends on usage, which is not known. Not estimated.' },
  unsupported: { label: 'Not estimated', icon: CircleSlash, hint: 'The provider does not support this resource type.' },
  free: { label: 'No direct price', icon: Gift, hint: 'The provider reports no price for this resource type. Related usage can still cost money.' },
  not_estimated: { label: 'Not estimated', icon: CircleSlash, hint: 'No reliable estimate for this change.' }
};

function StatusLabel({ status }: { status: CostItemStatus }) {
  const { label, icon: Icon, hint } = statusLabels[status];
  return <span className={`cost-status cost-${status}`} title={hint}><Icon size={12} aria-hidden="true" />{label}</span>;
}

export function CostTab({ plan }: { plan: Plan }) {
  const client = useQueryClient();
  const provider = useQuery({ queryKey: ['cost-provider'], queryFn: () => api<CostProviderCapabilities>('cost/provider') });
  const [disclosing, setDisclosing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const caps = provider.data;
  const ready = caps?.state === 'ready' && caps.canEstimate;
  const estimate = useQuery({
    queryKey: ['cost-estimate', plan.id],
    enabled: ready,
    queryFn: async () => {
      try {
        return await api<CostEstimate>(`plans/${plan.id}/cost-estimate`);
      } catch (e) {
        if (e instanceof ApiError && e.code === 'estimate_not_found') return null;
        throw e;
      }
    },
    refetchInterval: query => query.state.data?.status === 'running' ? 1000 : false
  });
  async function run(action: () => Promise<unknown>) {
    setBusy(true);
    setError('');
    try {
      await action();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The request failed.');
    } finally {
      setBusy(false);
    }
  }
  const start = () => run(async () => {
    const started = await post<CostEstimate>(`plans/${plan.id}/cost-estimates`, {});
    client.setQueryData(['cost-estimate', plan.id], started);
  });
  const cancel = (id: string) => run(async () => {
    client.setQueryData(['cost-estimate', plan.id], await post<CostEstimate>(`cost-estimates/${id}/cancel`, {}));
  });
  const optOut = () => run(async () => {
    client.setQueryData(['cost-provider'], await post<CostProviderCapabilities>('cost/opt-out', {}));
  });

  const finished = estimate.data?.status === 'completed' ? estimate.data.id : '';
  useEffect(() => {
    if (finished) void client.invalidateQueries({ queryKey: ['cost-history'] });
  }, [finished, client]);
  if (provider.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" />Checking cost estimation…</div>;
  if (provider.error || !caps) return <div className="empty-state error-state" role="alert"><p>{provider.error?.message ?? 'Cost estimation status is unavailable.'}</p></div>;
  const current = estimate.data ?? undefined;
  return <section className="cost-tab" aria-label="Cost estimate">
   <p className="cost-disclaimer"><LockKeyhole size={13} aria-hidden="true" />Estimates are approximate list-price figures for this plan’s changes, not your cloud bill and not billed amounts.</p>
   {caps.state === 'strict_local' && <Unavailable icon={CloudOff} title="Not available in strict local-only mode" reason={caps.reason} />}
   {caps.state === 'unavailable' && <Unavailable icon={CloudOff} title="Cost estimation is not available" reason={caps.reason} />}
   {caps.state === 'cli_missing' && <Unavailable icon={CircleSlash} title="Infracost CLI not found" reason={caps.reason} />}
   {caps.state === 'off' && <div className="cost-gate">
    <h2><ShieldCheck size={16} />Cost estimation is off</h2>
    <p>Estimates use the Infracost CLI you installed ({caps.disclosure?.endpoint ? <>prices come from <code>{caps.disclosure.endpoint}</code></> : 'a remote pricing API'}). Nothing is run or sent until you review what leaves this device and turn it on for this session.</p>
    <Button onClick={() => setDisclosing(true)}><ShieldCheck size={14} />Review data disclosure…</Button>
   </div>}
   {caps.state === 'ready' && !caps.canEstimate && <Unavailable icon={CircleSlash} title="Cost estimates are not available here" reason={caps.reason} />}
   {ready && <>
    <div className="cost-actions">
     <span className="muted">Infracost {caps.cliVersion} · prices from <code>{caps.disclosure?.endpoint}</code> · on for this session</span>
     <Button variant="ghost" size="small" onClick={() => void optOut()} disabled={busy}><Power size={13} />Turn off</Button>
    </div>
    {plan.contextChanged
     ? <p className="plan-warning" role="alert"><TriangleAlert size={14} />The context changed since this plan was made. Run a new plan to estimate current changes.</p>
     : current?.status !== 'running' && <div className="cost-start"><p>Estimating sends price lookups for this plan’s resources to <code>{caps.disclosure?.endpoint}</code>. Plans and applies do not wait for it.</p><Button onClick={() => void start()} disabled={busy}><Play size={14} />{current ? 'Estimate again' : 'Estimate cost'}</Button></div>}
    {estimate.error && <p className="plan-warning" role="alert">{estimate.error.message}</p>}
    {current && <EstimateView estimate={current} busy={busy} onCancel={() => void cancel(current.id)} />}
    <SavedEstimates />
   </>}
   {error && <p className="dialog-error" role="alert">{error}</p>}
   {disclosing && caps.disclosure && <DisclosureDialog disclosure={caps.disclosure} onClose={() => setDisclosing(false)} onEnabled={next => { client.setQueryData(['cost-provider'], next); setDisclosing(false); }} />}
  </section>;
}

function Unavailable({ icon: Icon, title, reason }: { icon: LucideIcon; title: string; reason?: string }) {
  return <div className="empty-state cost-unavailable" role="status"><Icon size={24} /><h2>{title}</h2>{reason && <p>{reason}</p>}<p className="muted">Plans and applies work as usual without estimates.</p></div>;
}

function DisclosureDialog({ disclosure: d, onClose, onEnabled }: { disclosure: ExternalDataDisclosure; onClose: () => void; onEnabled: (caps: CostProviderCapabilities) => void }) {
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const dialog = useRef<HTMLDivElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  useModal(dialog, close, onClose);
  async function enable() {
    setBusy(true);
    setError('');
    try {
      onEnabled(await post<CostProviderCapabilities>('cost/opt-in', { acknowledged: true, disclosureVersion: d.version }));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Cost estimation could not be turned on.');
    } finally {
      setBusy(false);
    }
  }
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog wide-dialog" role="dialog" aria-modal="true" aria-labelledby="cost-disclosure-title" aria-describedby="cost-disclosure-summary">
    <div className="dialog-header"><h2 id="cost-disclosure-title">Before estimating with the {d.providerName}</h2><Button ref={close} variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="cost-disclosure-summary"><strong>{d.externalCalls ? 'This calls an external pricing API' : 'This runs locally'}: <code>{d.endpoint}</code>.</strong> {d.offline ? '' : 'There is no offline estimate.'} Turning it on lasts for this Hclaro session; each estimate is still a separate request.</p>
    <div className="cost-disclosure">
     <section><h3>What it reads</h3><ul>{d.reads.map(x => <li key={x}>{x}</li>)}</ul></section>
     <section><h3>What can leave this device</h3><ul>{d.sentOffDevice.map(x => <li key={x}>{x}</li>)}</ul></section>
     <section><h3>Never sent</h3><ul>{d.neverSent.map(x => <li key={x}>{x}</li>)}</ul></section>
     <section><h3>Authentication</h3><p>{d.authentication}</p></section>
     <section><h3>Stored on this computer</h3><p>{d.storage}</p></section>
    </div>
    <p className="muted">Provider documentation: <span className="mono">{d.documentation}</span></p>
    <label className="check consent"><input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} /><span>I understand that estimating sends price lookups to {d.endpoint}</span></label>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Keep off</Button><Button onClick={() => void enable()} disabled={!consent || busy}><Power size={14} />{busy ? 'Checking Infracost…' : 'Turn on for this session'}</Button></div>
   </div>
  </div>;
}

function EstimateView({ estimate: e, busy, onCancel }: { estimate: CostEstimate; busy: boolean; onCancel: () => void }) {
  if (e.status === 'running') return <div className="cost-running" role="status"><LoaderCircle className="spin" size={16} /><span>Estimating with Infracost…</span><Button variant="outline" size="small" onClick={onCancel} disabled={busy || e.cancelRequested}><Square size={12} />{e.cancelRequested ? 'Cancelling…' : 'Cancel'}</Button></div>;
  if (e.status !== 'completed' || !e.breakdown) return <div className="operation-failed" role="alert"><h2><TriangleAlert size={16} />{e.status === 'cancelled' ? 'Estimate cancelled' : 'Estimate failed'}</h2><p>{e.error?.message}</p><p className="muted">The plan is unaffected and can still be applied.</p></div>;
  const b = e.breakdown;
  const c = b.coverage;
  const partial = c.percent !== null && c.percent < 100;
  return <div className={`cost-result ${e.stale ? 'stale' : ''}`}>
   {e.stale && <p className="plan-warning" role="alert"><TriangleAlert size={14} />Out of date: {e.staleReason}</p>}
   <div className="cost-headline">
    <span className="muted">Estimated recurring monthly change{partial ? ' (partial)' : ''}</span>
    <strong className={`cost-delta ${b.monthlyDelta.startsWith('-') ? 'down' : /^0(\.0+)?$/.test(b.monthlyDelta) ? '' : 'up'}`}>{signed(b.monthlyDelta)} <abbr title={`Currency: ${b.currency}`}>{b.currency}</abbr> / month</strong>
    <span className="muted">{c.percent === null ? 'No changed resource has a price.' : `Covers ${c.estimated + c.partial} of ${c.costBearing} cost-bearing change${c.costBearing === 1 ? '' : 's'} (${c.percent}%).`}{c.free > 0 && ` ${c.free} without a direct price.`}</span>
   </div>
   <dl className="cost-split">
    <div><dt>New resources</dt><dd>{signed(b.addedMonthly)} {b.currency}</dd></div>
    <div><dt>Removed resources</dt><dd>{b.removedMonthly === '0.00' ? '0.00' : signed(`-${b.removedMonthly}`)} {b.currency}</dd></div>
    <div><dt>Changed resources</dt><dd>{signed(b.changedDelta)} {b.currency}</dd></div>
    <div><dt>Priced configuration total</dt><dd>{b.pastMonthly} → {b.projectedMonthly} {b.currency}</dd></div>
   </dl>
   <ul className="cost-caveats" aria-label="Estimate caveats">{b.caveats.map(cv => <li key={cv.code}><CircleHelp size={13} aria-hidden="true" />{cv.message}{cv.count ? ` (${cv.count})` : ''}</li>)}</ul>
   {b.types.length > 0 && <table className="resources-table cost-types"><caption>By resource type</caption><thead><tr><th scope="col">Type</th><th scope="col">Changes</th><th scope="col">Not estimated</th><th scope="col">Monthly change</th></tr></thead><tbody>{b.types.map(t => <tr key={t.resourceType}><th scope="row"><code>{t.resourceType}</code></th><td>{t.count}</td><td>{t.notEstimated || '—'}</td><td>{t.delta === null ? <span className="muted">{t.notEstimated ? 'Not estimated' : 'No direct price'}</span> : `${signed(t.delta)} ${b.currency}`}</td></tr>)}</tbody></table>}
   <table className="resources-table cost-items"><caption>Changed resources</caption><thead><tr><th scope="col">Action</th><th scope="col">Resource</th><th scope="col">Estimate</th><th scope="col">Before</th><th scope="col">After</th><th scope="col">Monthly change</th></tr></thead><tbody>{b.items.map(item => <ItemRow key={item.address} item={item} currency={b.currency} />)}</tbody></table>
   {b.truncated && <p className="muted">Only the first 2,000 changed resources are listed; totals include every one.</p>}
   <p className="muted cost-provenance">Estimated {e.finishedAt ? clock(e.finishedAt) : ''} with Infracost {e.providerVersion} · prices from <code>{e.pricingSource}</code> · {e.basis} · plan <span className="mono">{e.artifactDigest}</span> · context <span className="mono">{e.fingerprint}</span></p>
  </div>;
}

function amount(value: string | null, currency: string) {
  return value === null ? <span className="muted">—</span> : <>{value} {currency}</>;
}

function ItemRow({ item, currency }: { item: CostLineItem; currency: string }) {
  const priced = item.delta !== null;
  return <tr>
   <td><ActionBadge action={item.action} /></td>
   <td><span className="resource-text"><strong className="mono change-address">{item.address}</strong><span className="resource-type">{item.resourceType}</span></span>
    {item.note && <span className="muted cost-note">{item.note}</span>}
    {item.components.length > 0 && <details className="cost-components"><summary>Price components ({item.components.length})</summary><ul>{item.components.map((c, i) => <li key={i}><span>{c.name}</span> <span className="muted">{c.monthlyQuantity ?? '?'} {c.unit} × {c.price ?? '?'}</span> {c.monthlyCost === null ? <span className="muted">not priced (usage)</span> : <strong>{c.monthlyCost} {currency}</strong>}</li>)}</ul></details>}
   </td>
   <td><StatusLabel status={item.status} />{item.unpricedComponents > 0 && item.status === 'partial' && <span className="muted cost-unpriced">{item.unpricedComponents} unpriced</span>}</td>
   <td>{priced ? amount(item.pastMonthly, currency) : ''}</td>
   <td>{priced ? amount(item.monthly, currency) : ''}</td>
   <td>{priced ? <strong>{signed(item.delta!)} {currency}</strong> : ''}</td>
  </tr>;
}

function SavedEstimates() {
  const client = useQueryClient();
  const saved = useQuery({ queryKey: ['cost-history'], queryFn: () => api<{ records: CostAggregate[]; persisted: boolean }>('cost/history') });
  const records = saved.data?.records ?? [];
  if (!saved.data?.persisted || records.length === 0) return null;
  return <details className="cost-saved">
   <summary>Earlier estimates for this project ({records.length}, saved locally as totals only)</summary>
   <table className="resources-table"><thead><tr><th scope="col">When</th><th scope="col">Workspace</th><th scope="col">Monthly change</th><th scope="col">Coverage</th></tr></thead><tbody>{records.map(r => <tr key={r.id}><td>{clock(r.createdAt)}</td><td>{r.workspace}</td><td>{signed(r.monthlyDelta)} {r.currency}</td><td>{r.coveragePercent === null ? '—' : `${r.coveragePercent}%`}</td></tr>)}</tbody></table>
   <Button variant="ghost" size="small" onClick={() => void post('cost/history/clear', {}).then(() => client.invalidateQueries({ queryKey: ['cost-history'] }))}>Clear saved estimates</Button>
  </details>;
}
