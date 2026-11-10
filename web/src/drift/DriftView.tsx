import { useCallback, useMemo, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, Braces, CircleCheck, CircleDashed, CircleHelp, CircleX, CloudCog, Copy, History, LoaderCircle, LockKeyhole, Minus, Pencil, Play, ScanSearch, Search, ShieldCheck, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, ApiError, post } from '../api';
import type { DriftClassification, DriftDetail, DriftOptions, DriftResource, DriftScan, Operation } from '../types';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { JsonTree } from '../components/JsonTree';
import { useModal } from '../components/useModal';
import { OperationLog } from '../plans/OperationLog';
import { clock, engineLabel } from '../plans/labels';
import { ContextSummary, RecordedContext } from '../profiles/ContextSummary';
import { ScopeSummary } from '../plans/PreflightDialog';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../plans/routes';
import { useVarFileChoice, VarFileChoice } from '../profiles/VarFileChoice';

// Classifications are distinguished by icon and text, never by color alone.
const classes: Record<DriftClassification, { label: string; icon: LucideIcon; tone: string }> = {
  changed: { label: 'Changed', icon: Pencil, tone: 'update' },
  missing: { label: 'Missing remotely', icon: Minus, tone: 'destroy' },
  cannot_verify: { label: 'Cannot verify', icon: CircleDashed, tone: 'neutral' },
  unrecognized: { label: 'Unrecognized', icon: CircleHelp, tone: 'warn' }
};
const order: DriftClassification[] = ['changed', 'missing', 'cannot_verify', 'unrecognized'];
type Grouping = 'module' | 'provider' | 'none';

function ClassBadge({ value }: { value: DriftClassification }) {
  const { label, icon: Icon, tone } = classes[value] ?? classes.unrecognized;
  return <span className={`action-badge tone-${tone}`}><Icon size={12} aria-hidden="true" />{label}</span>;
}

function ago(time?: string) {
  if (!time) return '';
  const minutes = Math.round((Date.now() - new Date(time).getTime()) / 60000);
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  return `${hours} h ${minutes % 60} min ago`;
}

export function DriftView({
  scope = launchScope,
  reopen,
  project,
  profileId = '',
  workspaceStatus,
  openResource,
  runPlan,
  copy
}: {
  // A Hub workspace scans in its own context only (M24.09); there is no launch fallback.
  scope?: PlanScope;
  // Opens a closed Hub workspace again.
  reopen?: () => void;
  project: string;
  profileId?: string;
  workspaceStatus?: string;
  // Opens the resource inspector for an explorer ID of the current snapshot.
  openResource: (id: string) => void;
  // Opens the normal plan preflight. A scan never starts a plan by itself.
  runPlan: () => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const client = useQueryClient();
  const [preflight, setPreflight] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState('');
  const [scanId, setScanId] = useState<string>();
  const scoped = scope.kind === 'context';
  const key = scopeKey(scope);
  const options = useQuery({ queryKey: ['drift-options', key, workspaceStatus, profileId], queryFn: () => api<DriftOptions>(scoped ? scopedPath(scope, 'drift-scans/options') : `drift-scans/options${profileId ? `?profileId=${profileId}` : ''}`) });
  const scans = useQuery({ queryKey: ['drift-scans', key, workspaceStatus], queryFn: () => api<{ scans: DriftScan[] }>(scopedPath(scope, 'drift-scans')) });
  const operations = useQuery({
    queryKey: ['operations', key],
    queryFn: () => api<{ operations: Operation[] }>(scopedPath(scope, 'operations')),
    refetchInterval: q => q.state.data?.operations.some(o => o.status === 'running') ? 2000 : false
  });
  const running = operations.data?.operations.find(o => o.status === 'running');
  const shownId = scanId ?? scans.data?.scans[0]?.id;
  const scan = useQuery({ queryKey: ['drift-scan', key, shownId, workspaceStatus], queryFn: () => api<DriftScan>(scopedPath(scope, `drift-scans/${shownId}`)), enabled: !!shownId });
  const finished = useCallback(() => {
    void client.invalidateQueries({ queryKey: ['operations'] });
    void client.invalidateQueries({ queryKey: ['drift-scans'] });
    void client.invalidateQueries({ queryKey: ['drift-scan'] });
  }, [client]);
  async function start(varFiles: string[], contextFingerprint?: string) {
    setStarting(true);
    setStartError('');
    try {
      // A Hub workspace's scan is bound to the scope its options described; it takes no profile.
      const body = scoped ? { acknowledgeProviderAccess: true, varFiles, contextFingerprint: options.data?.contextFingerprint ?? '' } : { acknowledgeProviderAccess: true, varFiles, ...(contextFingerprint ? { profileId, contextFingerprint } : {}) };
      const started = await post<{ operation: Operation; scan: DriftScan }>(scopedPath(scope, 'drift-scans'), body);
      setPreflight(false);
      setScanId(started.scan.id);
      await client.invalidateQueries({ queryKey: ['operations'] });
      await client.invalidateQueries({ queryKey: ['drift-scans'] });
    } catch (e) {
      setStartError(e instanceof Error ? e.message : 'The scan could not be started.');
      void options.refetch();
    } finally {
      setStarting(false);
    }
  }
  const canScan = !!options.data?.canScan && !running;
  const latest = scans.data?.scans[0];
  if (scoped && options.error instanceof ApiError && (options.error.code === 'context_closed' || options.error.code === 'scope_changed')) return <div className="empty-state error-state" role="alert"><TriangleAlert size={26} /><h2>Open this workspace again</h2><p>{options.error.message}</p>{reopen && <Button variant="outline" onClick={reopen}>Open again</Button>}<span className="safe-note"><ShieldCheck size={13} />Nothing ran.</span></div>;
  return <div className="plans drift">
   <div className="plans-toolbar">
    <Button onClick={() => { setStartError(''); setPreflight(true); }} disabled={!canScan} title={running && running.kind !== 'drift' ? 'Another operation is running.' : undefined}><ScanSearch size={14} />Check drift</Button>
    {scans.data && scans.data.scans.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Scan history</span><select aria-label="Scan history" value={shownId} onChange={e => setScanId(e.target.value)}>{scans.data.scans.map((s, i) => <option key={s.id} value={s.id}>{i === 0 ? 'Latest · ' : ''}{clock(s.startedAt)} · {s.workspace} · {statusLabel(s)}{!s.current && s.status !== 'running' ? ' · stale' : ''}</option>)}</select></label>}
    <span className="toolbar-note" role="status">{latest ? <>Last scan {clock(latest.completedAt ?? latest.startedAt)} ({ago(latest.completedAt ?? latest.startedAt)}){latest.current ? '' : ' · no longer current'}</> : <>No scan yet. Nothing is checked until you ask.</>}</span>
   </div>
   <p className="info-line drift-explainer"><ShieldCheck size={13} />A drift scan runs a <strong>refresh-only plan</strong>: it compares what state records with what providers report now. It is not a normal plan, so it does not show whether infrastructure matches your configuration, and it never writes state.</p>
   {workspaceStatus === 'loading' && !running ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before scanning is available…</div> : options.data && !options.data.canScan && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span><strong>Drift scans are unavailable here.</strong> {options.data.reason}</span></div>}
   {running && running.kind === 'drift' && <OperationLog key={running.id} scope={scope} operation={running} onFinished={finished} />}
   {running && running.kind !== 'drift' && <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Another operation is running in this project. Scanning is available when it finishes.</div>}
   {scan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading scan…</p></div> : scan.error ? <div className="empty-state error-state" role="alert"><p>{scan.error.message}</p></div> : scan.data && scan.data.status !== 'running' ? <ScanResult key={scan.data.id} scope={scope} scan={scan.data} openResource={openResource} runPlan={runPlan} rescan={() => setPreflight(true)} canScan={canScan} copy={copy} /> : !running && !shownId && <div className="empty-state"><ScanSearch size={30} /><h2>No drift scans yet</h2><p>Check drift to compare the objects recorded in state with what providers report. It may contact provider APIs and the backend, so it only runs when you ask.</p>{options.data?.canScan && <Button variant="outline" onClick={() => setPreflight(true)}><ScanSearch size={14} />Check drift</Button>}</div>}
   {preflight && options.data && <DriftPreflight options={options.data} project={project} busy={starting} error={startError} onConfirm={(varFiles, fingerprint) => void start(varFiles, fingerprint)} onClose={() => { setPreflight(false); setStartError(''); }} />}
  </div>;
}

function statusLabel(s: DriftScan) {
  switch (s.status) {
    case 'running': return 'running';
    case 'completed': return s.outcome === 'no_drift' ? 'no drift' : s.outcome === 'cannot_verify' ? `${s.summary.cannotVerify} cannot verify` : `${s.summary.total} drifted`;
    case 'partial': return 'incomplete';
    case 'cancelled': return 'cancelled';
    default: return 'failed';
  }
}

// Shown before every scan: it is not a local-only read.
function DriftPreflight({ options, project, busy, error, onConfirm, onClose }: {
  options: DriftOptions;
  project: string;
  busy: boolean;
  error?: string;
  onConfirm: (varFiles: string[], contextFingerprint?: string) => void;
  onClose: () => void;
}) {
  const choice = useVarFileChoice({ ...options, executionContext: options.executionContext ?? undefined });
  const summary = options.context;
  const blocked = !!summary && summary.issues.length > 0;
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  const engine = engineLabel(options.engine);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="drift-title" aria-describedby="drift-summary">
    <div className="dialog-header"><h2 id="drift-title">Check drift</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="drift-summary">{engine} will run <code>plan -refresh-only</code> in <strong>{project}</strong> for workspace <strong>{options.workspace}</strong> only. Review what this does before continuing.</p>
    <ul className="preflight-list">
     <li><CloudCog size={16} /><span><strong>Queries provider APIs and the {options.backend} backend</strong> using your existing credentials. Expired sign-ins (for example SSO) or a missing VPN make the scan fail; a failed scan is never reported as “no drift”.</span></li>
     <li><LockKeyhole size={16} /><span><strong>May lock the state</strong> while scanning, if the backend supports locking. Other operations wait until the scan finishes or is cancelled.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Writes no state and changes no infrastructure.</strong> Hclaro never runs <code>refresh</code> or applies a refresh-only plan; the plan file is deleted as soon as its redacted result is read.</span></li>
    </ul>
    {summary ? <ContextSummary summary={summary} title="This scan will use" /> : options.scope ? <ScopeSummary scope={options.scope} executionContext={options.executionContext} title="This scan will use" /> : options.profileError ? <p className="dialog-error" role="alert">{options.profileError}</p> : <p className="muted">No execution profile: the scan uses the environment Hclaro was started with. Save this project to the Hub to choose a profile.</p>}
    <VarFileChoice choice={choice} />
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(choice.chosen, summary?.context.profileId ? summary.context.fingerprint : undefined)} disabled={busy || blocked || !!options.profileError} title={blocked ? 'Resolve the execution context issues first.' : undefined}><Play size={14} />{busy ? 'Starting…' : 'Check drift'}</Button></div>
   </div>
  </div>;
}

function Outcome({ scan }: { scan: DriftScan }) {
  const engine = engineLabel(scan.engine);
  const s = scan.summary;
  if (scan.status === 'completed' && scan.outcome === 'no_drift' && scan.exhaustive) {
    return <section className="operation-applied" role="status"><h2><CircleCheck size={17} />No drift found</h2><p>Every object recorded in state matched what providers reported during this scan. This says nothing about whether your configuration matches; run a normal plan for that.</p></section>;
  }
  if (scan.status === 'completed') {
    return <section className="drift-outcome" role="status"><h2><TriangleAlert size={17} />{scan.outcome === 'cannot_verify' ? `${s.cannotVerify} object${s.cannotVerify === 1 ? '' : 's'} could not be fully verified` : `${s.changed + s.missing} object${s.changed + s.missing === 1 ? '' : 's'} changed outside ${engine}`}</h2><p>Compared with what state records. Nothing was changed: state still holds the recorded values until you decide what to do.</p></section>;
  }
  const title = scan.status === 'partial' ? 'Scan incomplete' : scan.status === 'cancelled' ? 'Scan cancelled' : 'Scan failed';
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{title}</h2>
   {scan.diagnostic && <p>{scan.diagnostic.message}</p>}
   <p className="muted">{scan.status === 'partial' ? 'Objects listed below did drift. Objects not listed were not necessarily checked, so this is not a complete result.' : 'No drift result was produced. This is not the same as no drift.'}</p>
   {scan.engineDiagnostics.length > 0 && <ul className="diagnostics">{scan.engineDiagnostics.map((d, i) => <li key={i} className={`diag-${d.severity}`}><strong>{d.severity === 'error' ? 'Error' : 'Warning'}: {d.summary}</strong>{d.address && <code>{d.address}</code>}{d.detail && <pre>{d.detail}</pre>}</li>)}</ul>}
  </section>;
}

function ScanResult({ scope, scan, openResource, runPlan, rescan, canScan, copy }: { scope: PlanScope; scan: DriftScan; openResource: (id: string) => void; runPlan: () => void; rescan: () => void; canScan: boolean; copy: (value: string, label: string) => Promise<void> }) {
  const [filter, setFilter] = useState<DriftClassification[]>([]);
  const [search, setSearch] = useState('');
  const [grouping, setGrouping] = useState<Grouping>('module');
  const [selected, setSelected] = useState<string>();
  const visible = useMemo(() => scan.resources.filter(r => (!filter.length || filter.includes(r.classification)) && (!search || `${r.address} ${r.type} ${r.provider}`.toLowerCase().includes(search.toLowerCase()))), [scan.resources, filter, search]);
  const groups = useMemo(() => {
    const map = new Map<string, DriftResource[]>();
    for (const r of visible) {
      const key = grouping === 'module' ? r.module || 'Root module' : grouping === 'provider' ? r.provider.split('/').at(-1) || 'unknown' : '';
      map.set(key, [...(map.get(key) ?? []), r]);
    }
    return [...map.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [visible, grouping]);
  const counts: Record<DriftClassification, number> = { changed: scan.summary.changed, missing: scan.summary.missing, cannot_verify: scan.summary.cannotVerify, unrecognized: scan.summary.unrecognized };
  function toggle(c: DriftClassification) {
    setFilter(current => current.includes(c) ? current.filter(v => v !== c) : [...current, c]);
  }
  return <div className="plan-review drift-result">
   <div className="plan-meta">
    <div><span className="muted">Scanned</span><strong>{clock(scan.completedAt ?? scan.startedAt)}</strong></div>
    <div><span className="muted">Engine</span><strong>{engineLabel(scan.engine)} {scan.engineVersion}</strong></div>
    <div><span className="muted">Workspace</span><strong>{scan.workspace}</strong></div>
    <div><span className="muted">Variable files</span><strong>{scan.varFiles.length ? scan.varFiles.join(', ') : 'Automatic only'}</strong></div>
    <div><span className="muted">Execution context</span><strong><RecordedContext context={scan.executionContext} /></strong></div>
   </div>
   {!scan.current && <div className="plan-warning" role="alert"><TriangleAlert size={15} /><span><strong>This scan no longer matches.</strong> {scan.staleReason} {scan.withheld && 'Its resource list is withheld so it cannot be mistaken for the current workspace.'}</span>{canScan && <Button variant="outline" size="small" onClick={rescan}><ScanSearch size={13} />Scan again</Button>}</div>}
   <Outcome scan={scan} />
   {scan.warnings.map(w => <div className="plan-warning" key={w} role="note"><TriangleAlert size={15} />{w}</div>)}
   {scan.current && scan.status !== 'failed' && scan.status !== 'cancelled' && <>
    <div className="summary-grid" role="group" aria-label="Filter by classification">{order.filter(c => c !== 'unrecognized' || counts.unrecognized).map(c => {
     const { label, icon: Icon, tone } = classes[c];
     return <button key={c} className={`summary-card tone-${tone} ${filter.includes(c) ? 'chosen' : ''}`} aria-pressed={filter.includes(c)} disabled={!counts[c]} onClick={() => toggle(c)}><span><Icon size={14} aria-hidden="true" />{label}</span><strong>{counts[c]}</strong></button>;
    })}</div>
    {scan.resources.length > 0 && <>
     <div className="filter-row plan-filters">
      <div className="search-field compact"><Search size={14} /><input value={search} onChange={e => setSearch(e.target.value)} placeholder="Filter by address, type or provider…" aria-label="Filter drifted resources" />{search && <button className="clear-search" aria-label="Clear filter" onClick={() => setSearch('')}><X size={12} /></button>}</div>
      <label className="history-select"><span className="muted">Group by</span><select aria-label="Group by" value={grouping} onChange={e => setGrouping(e.target.value as Grouping)}><option value="module">Module</option><option value="provider">Provider</option><option value="none">None</option></select></label>
      {(filter.length > 0 || search) && <Button variant="ghost" size="small" onClick={() => { setFilter([]); setSearch(''); }}><X size={12} />Reset</Button>}
      <span className="filter-count">{visible.length} shown</span>
     </div>
     <div className={`explorer plan-explorer ${selected ? 'with-inspector' : ''}`}>
      <section className="resource-list" aria-label="Drifted resources">
       {visible.length === 0 ? <div className="empty-state"><Braces size={26} /><h2>No matching resources</h2></div> : groups.map(([group, rows]) => <div key={group || 'all'} className="drift-group">
        {grouping !== 'none' && <h3 className="drift-group-title"><span className="mono">{group}</span><span className="muted">{rows.length}</span></h3>}
        <table className="resources-table changes-table"><thead><tr><th scope="col">Drift</th><th scope="col">Resource</th><th scope="col">Detail</th></tr></thead><tbody>{rows.map(r => <tr key={r.id} className={selected === r.id ? 'selected' : ''}>
         <td><ClassBadge value={r.classification} /></td>
         <td><button className="resource-button" aria-pressed={selected === r.id} onClick={() => setSelected(r.id)}><span className="resource-text"><strong className="mono change-address">{r.address}</strong><span className="resource-type">{r.type}{r.deposed && ` · deposed object ${r.deposed}`}</span></span></button></td>
         <td className="change-why">{r.classification === 'missing' ? 'Not found during scan' : `${r.attributeChanges} attribute${r.attributeChanges === 1 ? '' : 's'}`}{r.cannotVerify > 0 && r.classification !== 'cannot_verify' && <span className="flag"><CircleDashed size={11} />{r.cannotVerify} cannot verify</span>}{r.resourceId && <Button variant="ghost" size="small" onClick={() => openResource(r.resourceId!)} aria-label={`Open ${r.address} in the resource inspector`}>Inspector<ArrowRight size={12} /></Button>}</td>
        </tr>)}</tbody></table>
       </div>)}
      </section>
      {selected && <DriftInspector scope={scope} scanId={scan.id} changeId={selected} close={() => setSelected(undefined)} openResource={openResource} copy={copy} />}
     </div>
    </>}
   </>}
   <div className="drift-next" role="note"><span>A refresh-only scan does not compare infrastructure with your configuration. To see what a plan would change, run a separate normal plan; it is reviewed before anything is applied.</span><Button variant="outline" size="small" onClick={runPlan}><Play size={13} />Run a normal plan…</Button></div>
  </div>;
}

function Observed({ value, kind, side }: { value: unknown; kind: string; side: 'recorded' | 'observed' }) {
  if (side === 'observed' && kind === 'cannot_verify') return <span className="unknown-value">cannot verify</span>;
  if (value === '[redacted]') return <span className="redacted"><LockKeyhole size={11} />redacted</span>;
  if (value === null || value === undefined) return <span className="muted">{side === 'recorded' ? 'not recorded' : 'not present'}</span>;
  if (typeof value === 'object') return <code className="json-inline">{JSON.stringify(value)}</code>;
  return <code className={typeof value === 'string' ? 'json-string' : 'json-value'}>{String(value)}</code>;
}

function DriftInspector({ scope, scanId, changeId, close, openResource, copy }: { scope: PlanScope; scanId: string; changeId: string; close: () => void; openResource: (id: string) => void; copy: (value: string, label: string) => Promise<void> }) {
  const detail = useQuery({ queryKey: ['drift-detail', scopeKey(scope), scanId, changeId], queryFn: () => api<DriftDetail>(scopedPath(scope, `drift-scans/${scanId}/resources/${changeId}`)) });
  const d = detail.data;
  return <Drawer label="Drift details" onClose={close}>
   <div className="inspector-header"><span><Braces size={15} />Drift details</span><Button variant="ghost" size="icon" aria-label="Close drift details" onClick={close}><X size={16} /></Button></div>
   {detail.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading…</div> : detail.error ? <div className="empty-state error-state" role="alert"><p>{detail.error.message}</p></div> : d && <div className="inspector-body">
    <div className="inspector-title"><ClassBadge value={d.classification} /><span className="mono muted">{d.actions.join(', ')}</span></div>
    <div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy resource address" onClick={() => void copy(d.address, 'Address')}><Copy size={13} /></Button></div><code>{d.address}</code></div>
    {d.classification === 'missing' && <p className="plan-warning"><TriangleAlert size={14} />The provider reported that this object no longer exists. State still records it until state is changed by a reviewed operation.</p>}
    {d.classification === 'unrecognized' && <p className="plan-warning"><TriangleAlert size={14} />Unrecognized action in a refresh-only plan. Inspect it with the CLI; it is not treated as safe.</p>}
    <dl className="metadata">
     <div><dt>Provider</dt><dd className="mono">{d.provider || 'Not recorded'}</dd></div>
     <div><dt>Module</dt><dd className="mono">{d.module || 'Root module'}</dd></div>
     <div><dt>Inspector</dt><dd>{d.resourceId ? <Button variant="ghost" size="small" onClick={() => openResource(d.resourceId!)}>Open recorded state<ArrowRight size={12} /></Button> : <span className="muted">Not in the current snapshot</span>}</dd></div>
    </dl>
    <div className="attributes-header"><h3>Attributes</h3><span className="muted">{d.attributes.length}{d.truncated && '+'}</span></div>
    <div className="redaction-note"><LockKeyhole size={12} />Values marked sensitive, secret-named attributes and credential-like values are hidden, even when a provider does not flag them; a difference in a hidden value is still listed.</div>
    {d.attributes.length ? <table className="diff-table"><thead><tr><th scope="col">Attribute</th><th scope="col">Recorded in state</th><th scope="col">Observed during scan</th></tr></thead><tbody>{d.attributes.map(a => <tr key={a.path} className={`diff-${a.kind === 'cannot_verify' ? 'unknown' : a.kind}`}>
     <th scope="row"><code>{a.path}</code>{a.sensitive && <span className="flag"><LockKeyhole size={10} />sensitive</span>}</th>
     <td><Observed value={a.recorded} kind={a.kind} side="recorded" /></td>
     <td><Observed value={a.observed} kind={a.kind} side="observed" /></td>
    </tr>)}</tbody></table> : <p className="muted">{d.classification === 'missing' ? 'The whole object is missing.' : 'No attribute differences.'}</p>}
    {d.truncated && <p className="muted">Only the first 1,000 attribute differences are shown.</p>}
    <details className="full-values"><summary>Full values</summary><div className="json-viewer"><JsonTree name="recorded in state" value={d.recorded ?? {}} /><JsonTree name="observed during scan" value={d.observed ?? {}} /></div></details>
   </div>}
  </Drawer>;
}
