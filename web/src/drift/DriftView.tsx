import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Box, CircleCheck, CircleX, CloudCog, Copy, History, LoaderCircle, LockKeyhole, Play, ScanSearch, Search, ShieldCheck, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, ApiError, send } from '../api';
import { operationActive, type DriftClassification, type DriftDetail, type DriftOptions, type DriftResource, type DriftScan, type EditorName, type Operation } from '../types';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { JsonTree } from '../components/JsonTree';
import { EmptyState } from '../components/EmptyState';
import { Disclosure } from '../components/Disclosure';
import { DataTable, type Column } from '../components/DataTable';
import { Chip, type Tone } from '../components/Chip';
import { RunDialog, RunState } from '../plans/RunDialog';
import { RecordedContext } from '../profiles/ContextSummary';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { activeOperation, operationsQuery, useLimits } from '../lib/queries';
import { AskAI } from '../assist/AskAI';
import { useVarFileChoice, VarFileChoice } from '../profiles/VarFileChoice';
import { SourceLink } from '../git/editor';
import { clock, engineLabel } from '../lib/format';
import { errorMessage } from '../lib/errorMessage';
import { ErrorState } from '../components/ErrorState';

// Classifications always carry a symbol and a word, never color alone (§6).
const classes: Record<DriftClassification, { label: string; symbol: string; word: string; tone: Tone; card: string }> = {
  changed: { label: 'Changed', symbol: '~', word: 'changed', tone: 'update', card: 'update' },
  missing: { label: 'Missing remotely', symbol: '−', word: 'missing', tone: 'delete', card: 'destroy' },
  cannot_verify: { label: 'Cannot verify', symbol: '?', word: 'cannot verify', tone: 'neutral', card: 'neutral' },
  unrecognized: { label: 'Unrecognized', symbol: '!', word: 'unrecognized', tone: 'update', card: 'warn' }
};
const order: DriftClassification[] = ['changed', 'missing', 'cannot_verify', 'unrecognized'];
type Grouping = 'module' | 'none';

function DriftChip({ value }: { value: DriftClassification }) {
  const c = classes[value] ?? classes.unrecognized;
  return <Chip tone={c.tone}><span aria-hidden="true">{c.symbol}</span>{c.word}</Chip>;
}

// An outcome card: the finding, one sentence and its actions on one line.
function ResultCard({ tone, icon: Icon, title, children, actions }: { tone: 'update' | 'ok'; icon: LucideIcon; title: string; children: ReactNode; actions?: ReactNode }) {
  return <section className={`drift-card drift-card-${tone}`} role="status">
   <Icon size={17} aria-hidden="true" />
   <div><h2>{title}</h2><p>{children}</p></div>
   {actions}
  </section>;
}

// The long explanation lives in a collapsed "How this works" (§3).
function HowItWorks() {
  const limits = useLimits();
  return <>
   <p>A drift scan runs a <strong>refresh-only plan</strong>: it compares what state records with what providers report now. It is not a normal plan, so it does not show whether infrastructure matches your configuration, and it never writes state.</p>
   {limits && <p>The last {limits.driftScans} scan results are kept in memory for up to {limits.driftMaxAgeHours} hours and are gone when Hclaro stops.</p>}
  </>;
}

export type DriftSource = { prefix: string; editor: EditorName; notify: (text: string) => void };

export function DriftView({
  scope = launchScope,
  reopen,
  project,
  profileId = '',
  workspaceStatus,
  openResource,
  runPlan,
  copy,
  source
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
  // Open in editor from the drift drawer; absent where git source links are unavailable.
  source?: DriftSource;
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
  const operations = useQuery(operationsQuery(scope));
  const running = activeOperation(operations.data?.operations);
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
      const started = await send<{ operation: Operation; scan: DriftScan }>('POST', scopedPath(scope, 'drift-scans'), body);
      setPreflight(false);
      setScanId(started.scan.id);
      await client.invalidateQueries({ queryKey: ['operations'] });
      await client.invalidateQueries({ queryKey: ['drift-scans'] });
    } catch (e) {
      setStartError(errorMessage(e, 'The scan could not be started.'));
      void options.refetch();
    } finally {
      setStarting(false);
    }
  }
  const canScan = !!options.data?.canScan && !running;
  const check = () => {
    setStartError('');
    setPreflight(true);
  };
  if (scoped && options.error instanceof ApiError && (options.error.code === 'context_closed' || options.error.code === 'scope_changed')) return <ErrorState icon={TriangleAlert} iconSize={26} title="Open this workspace again" message={options.error.message} retry={reopen} retryLabel="Open again" note="Nothing ran." />;
  const history = scans.data?.scans ?? [];
  return <div className="plans drift">
   {history.length > 1 && <div className="plans-toolbar">
    <label className="history-select"><History size={14} aria-hidden="true" /><span className="sr-only">Scan history</span><select aria-label="Scan history" value={shownId} onChange={e => setScanId(e.target.value)}>{history.map((s, i) => <option key={s.id} value={s.id}>{i === 0 ? 'Latest · ' : ''}{clock(s.startedAt)} · {s.workspace} · {statusLabel(s)}{!s.current && s.status !== 'running' ? ' · stale' : ''}</option>)}</select></label>
   </div>}
   {workspaceStatus === 'loading' && !running ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before scanning is available…</div> : options.data && !options.data.canScan && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span><strong>Drift scans are unavailable here.</strong> {options.data.reason}</span></div>}
   {running && running.kind === 'drift' && <RunState key={running.id} scope={scope} operation={running} running="Checking drift…" succeeded="Drift check finished" onFinished={finished} onRetry={check} />}
   {running && running.kind !== 'drift' && <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Another operation is running in this project. Scanning is available when it finishes.</div>}
   {scan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading scan…</p></div> : scan.error ? <ErrorState inline message={scan.error.message} /> : scan.data && scan.data.status !== 'running' ? <ScanResult key={scan.data.id} scope={scope} scan={scan.data} operation={operations.data?.operations.find(o => o.id === scan.data.operationId)} openResource={openResource} runPlan={runPlan} rescan={check} canScan={canScan} copy={copy} source={source} /> : !running && !shownId && scans.data && <EmptyState icon={<ScanSearch size={28} />} title="No drift scans yet" action={<Button onClick={check} disabled={!canScan}><ScanSearch size={14} />Check drift</Button>} how={<HowItWorks />}>Compare what state records with what your providers report right now. It contacts provider APIs, so it only runs when you ask.</EmptyState>}
   {preflight && options.data && <DriftDialog options={options.data} project={project} busy={starting} error={startError} onConfirm={(varFiles, fingerprint) => void start(varFiles, fingerprint)} onClose={() => {
      setPreflight(false);
      setStartError('');
    }} />}
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

// The shared run dialog (§2.4), shown before every scan: it is not a local-only read.
function DriftDialog({ options, project, busy, error, onConfirm, onClose }: {
  options: DriftOptions;
  project: string;
  busy: boolean;
  error?: string;
  onConfirm: (varFiles: string[], contextFingerprint?: string) => void;
  onClose: () => void;
}) {
  const choice = useVarFileChoice({ ...options, executionContext: options.executionContext ?? undefined });
  const summary = options.context;
  const facts = [
    { icon: CloudCog, title: `Queries provider APIs and the ${options.backend} backend`, detail: 'using your existing credentials. Expired sign-ins (for example SSO) or a missing VPN make the scan fail; a failed scan is never reported as “no drift”.' },
    { icon: LockKeyhole, title: 'May lock the state', detail: 'while scanning, if the backend supports locking. Other operations wait until the scan finishes or is cancelled.' },
    { icon: ShieldCheck, title: 'Writes no state and changes no infrastructure.', detail: <>Hclaro never runs <code>refresh</code> or applies a refresh-only plan; the plan file is deleted as soon as its redacted result is read.</> }
  ];
  return <RunDialog title="Check drift" noun="scan" action="Check drift" facts={facts} busy={busy} error={error} blockedReason={options.profileError} uses={{ ...options, project }}
   sentence={<>{engineLabel(options.engine)} will run <code>plan -refresh-only</code> in <strong>{project}</strong> for workspace <strong>{options.scope?.workspace ?? options.workspace}</strong> only.</>}
   onRun={() => onConfirm(choice.chosen, summary?.context.profileId ? summary.context.fingerprint : undefined)} onClose={onClose}>
   <VarFileChoice choice={choice} />
  </RunDialog>;
}

// A failed or cancelled scan produced no result, which is never the same as no drift.
function Failure({ scope, scan, operation, retry }: { scope: PlanScope; scan: DriftScan; operation?: Operation; retry: () => void }) {
  const note = <p className="muted">No drift result was produced. This is not the same as no drift.</p>;
  // The operation's retained, sanitized log backs "View log"; its cause wording is shared with plans (§2.5).
  if (operation && !operationActive(operation) && operation.status !== 'succeeded') return <><RunState key={operation.id} scope={scope} operation={operation} running="Checking drift…" succeeded="" onRetry={retry} />{note}</>;
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{scan.status === 'cancelled' ? 'Scan cancelled' : 'Scan failed'}</h2>
   {scan.diagnostic && <p>{scan.diagnostic.message}</p>}
   {note}
   {scan.engineDiagnostics.length > 0 && <ul className="diagnostics">{scan.engineDiagnostics.map((d, i) => <li key={i} className={`diag-${d.severity}`}><strong>{d.severity === 'error' ? 'Error' : 'Warning'}: {d.summary}</strong>{d.address && <code>{d.address}</code>}{d.detail && <pre>{d.detail}</pre>}</li>)}</ul>}
   <div className="run-actions"><Button size="small" onClick={retry}>Try again</Button></div>
  </section>;
}

function ScanResult({ scope, scan, operation, openResource, runPlan, rescan, canScan, copy, source }: { scope: PlanScope; scan: DriftScan; operation?: Operation; openResource: (id: string) => void; runPlan: () => void; rescan: () => void; canScan: boolean; copy: (value: string, label: string) => Promise<void>; source?: DriftSource }) {
  const [filter, setFilter] = useState<DriftClassification[]>([]);
  const [search, setSearch] = useState('');
  const [grouping, setGrouping] = useState<Grouping>('module');
  const [selected, setSelected] = useState<string>();
  const visible = useMemo(() => scan.resources.filter(r => (!filter.length || filter.includes(r.classification)) && (!search || `${r.address} ${r.type} ${r.provider}`.toLowerCase().includes(search.toLowerCase()))), [scan.resources, filter, search]);
  const groups = useMemo(() => {
    const map = new Map<string, DriftResource[]>();
    for (const r of visible) map.set(r.module || 'Root module', [...(map.get(r.module || 'Root module') ?? []), r]);
    return [...map.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([key, rows]) => ({ key, label: <span className="mono">{key}</span>, rows }));
  }, [visible]);
  const counts: Record<DriftClassification, number> = { changed: scan.summary.changed, missing: scan.summary.missing, cannot_verify: scan.summary.cannotVerify, unrecognized: scan.summary.unrecognized };
  const engine = engineLabel(scan.engine);
  const s = scan.summary;
  const failed = scan.status === 'failed' || scan.status === 'cancelled';
  const again = <Button variant="ghost" size="small" onClick={rescan} disabled={!canScan}><ScanSearch size={13} />Check again</Button>;
  const ai = scan.current && <AskAI scope={scope} request={{ kind: 'drift', scanId: scan.id }} label="Review with AI" />;
  function toggle(c: DriftClassification) {
    setFilter(current => current.includes(c) ? current.filter(v => v !== c) : [...current, c]);
  }
  const columns: Column<DriftResource>[] = [
    { key: 'drift', header: 'Drift', className: 'drift-chip-column', cell: r => <DriftChip value={r.classification} /> },
    { key: 'resource', header: 'Resource', cell: r => <button type="button" className="address-button mono" title="Show what changed" onClick={() => setSelected(r.id)}>{r.address}<span className="drift-type">{r.type}{r.deposed && ` · deposed object ${r.deposed}`}</span></button> },
    { key: 'detail', header: 'Detail', className: 'drift-detail-column', cell: r => <>{r.classification === 'missing' ? 'Not found during scan' : `${r.attributeChanges} attribute${r.attributeChanges === 1 ? '' : 's'}`}{r.cannotVerify > 0 && r.classification !== 'cannot_verify' && <span className="muted"> · {r.cannotVerify} cannot verify</span>}</> }
  ];
  return <div className="plan-review drift-result">
   <div className="plan-meta">
    <div><span className="muted">Scanned</span><strong>{clock(scan.completedAt ?? scan.startedAt)}</strong></div>
    <div><span className="muted">Engine</span><strong>{engine} {scan.engineVersion}</strong></div>
    <div><span className="muted">Workspace</span><strong>{scan.workspace}</strong></div>
    <div><span className="muted">Variable files</span><strong>{scan.varFiles.length ? scan.varFiles.join(', ') : 'Automatic only'}</strong></div>
    <div><span className="muted">Execution context</span><strong><RecordedContext context={scan.executionContext} /></strong></div>
   </div>
   {!scan.current && <div className="plan-warning" role="alert"><TriangleAlert size={15} /><span><strong>This scan no longer matches.</strong> {scan.staleReason} {scan.withheld && 'Its resource list is withheld so it cannot be mistaken for the current workspace.'}</span>{canScan && <Button variant="outline" size="small" onClick={rescan}><ScanSearch size={13} />Scan again</Button>}</div>}
   {failed ? <Failure scope={scope} scan={scan} operation={operation} retry={rescan} /> : scan.status === 'partial' ? <section className="operation-failed" role="alert">
    <h2><CircleX size={17} />Scan incomplete</h2>
    {scan.diagnostic && <p>{scan.diagnostic.message}</p>}
    <p className="muted">Objects listed below did drift. Objects not listed were not necessarily checked, so this is not a complete result.</p>
    {scan.engineDiagnostics.length > 0 && <ul className="diagnostics">{scan.engineDiagnostics.map((d, i) => <li key={i} className={`diag-${d.severity}`}><strong>{d.severity === 'error' ? 'Error' : 'Warning'}: {d.summary}</strong>{d.address && <code>{d.address}</code>}{d.detail && <pre>{d.detail}</pre>}</li>)}</ul>}
    <div className="run-actions">{again}{ai}</div>
   </section> : scan.outcome === 'no_drift' && scan.exhaustive ? <ResultCard tone="ok" icon={CircleCheck} title="No drift found" actions={again}>
    Scanned at {clock(scan.completedAt ?? scan.startedAt)}. Every object recorded in state matched what providers reported. This says nothing about whether your configuration matches; run a normal plan for that.
   </ResultCard> : <ResultCard tone="update" icon={TriangleAlert} title={scan.outcome === 'cannot_verify' ? `${s.cannotVerify} object${s.cannotVerify === 1 ? '' : 's'} could not be fully verified` : `${s.changed + s.missing} object${s.changed + s.missing === 1 ? '' : 's'} changed outside ${engine}`} actions={<>{again}{ai}</>}>
    Compared with what state records. Nothing was changed: state still holds the recorded values until you decide what to do.
   </ResultCard>}
   {scan.warnings.map(w => <div className="plan-warning" key={w} role="note"><TriangleAlert size={15} />{w}</div>)}
   {scan.current && !failed && <>
    <div className="summary-grid" role="group" aria-label="Filter by classification">{order.filter(c => c !== 'unrecognized' || counts.unrecognized).map(c => {
     const { label, symbol, card } = classes[c];
     return <button key={c} className={`summary-card tone-${card} ${filter.includes(c) ? 'chosen' : ''}`} aria-pressed={filter.includes(c)} disabled={!counts[c]} onClick={() => toggle(c)}><span><span aria-hidden="true">{symbol}</span>{label}</span><strong>{counts[c]}</strong></button>;
    })}</div>
    {scan.resources.length > 0 && <>
     <div className="filter-row plan-filters">
      <div className="search-field compact"><Search size={14} aria-hidden="true" /><input value={search} onChange={e => setSearch(e.target.value)} placeholder="Filter by address, type or provider…" aria-label="Filter drifted resources" />{search && <button className="clear-search" aria-label="Clear filter" onClick={() => setSearch('')}><X size={12} /></button>}</div>
      <label className="history-select"><span className="muted">Group by</span><select aria-label="Group by" value={grouping} onChange={e => setGrouping(e.target.value as Grouping)}><option value="module">Module</option><option value="none">None</option></select></label>
      {(filter.length > 0 || search) && <Button variant="ghost" size="small" onClick={() => {
        setFilter([]);
        setSearch('');
      }}><X size={12} />Reset</Button>}
      <span className="filter-count">{visible.length} shown</span>
     </div>
     <div className="drift-table">
      <DataTable label="Drifted resources" columns={columns} rowKey={r => r.id} rowLabel={r => r.address} rows={grouping === 'none' ? visible : undefined} groups={grouping === 'none' ? undefined : groups} active={selected} />
      {visible.length === 0 && <p className="drift-no-match">No drift matches{search ? ` “${search}”` : ' these filters'}.</p>}
     </div>
     {selected && <DriftDrawer scope={scope} scanId={scan.id} changeId={selected} engine={engine} close={() => setSelected(undefined)} openResource={openResource} runPlan={runPlan} copy={copy} source={source} />}
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
  return <code>{String(value)}</code>;
}

// §4.4 drift details: what changed, what you can do, and the next steps. Nothing here runs anything.
function DriftDrawer({ scope, scanId, changeId, engine, close, openResource, runPlan, copy, source }: { scope: PlanScope; scanId: string; changeId: string; engine: string; close: () => void; openResource: (id: string) => void; runPlan: () => void; copy: (value: string, label: string) => Promise<void>; source?: DriftSource }) {
  const detail = useQuery({ queryKey: ['drift-detail', scopeKey(scope), scanId, changeId], queryFn: () => api<DriftDetail>(scopedPath(scope, `drift-scans/${scanId}/resources/${changeId}`)) });
  const d = detail.data;
  const recordedId = (d?.recorded as { id?: unknown } | null | undefined)?.id;
  return <Drawer label="Drift details" onClose={close}>
   <div className="inspector-header"><span><ScanSearch size={15} aria-hidden="true" />Drift details</span><Button variant="ghost" size="icon" aria-label="Close drift details" onClick={close}><X size={16} /></Button></div>
   {detail.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading…</div> : detail.error ? <ErrorState inline message={detail.error.message} /> : d && <>
    <div className="inspector-body drift-drawer">
     <div className="inspector-title"><span className="resource-icon" aria-hidden="true"><Box size={23} /></span><div className="inspector-name"><h2>{d.name}</h2><p className="inspector-type mono">{d.type}</p></div><DriftChip value={d.classification} /></div>
     <div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy resource address" onClick={() => void copy(d.address, 'Address')}><Copy size={13} /></Button></div><code>{d.address}</code></div>
     <dl className="metadata"><div><dt>Provider</dt><dd className="mono">{d.provider || 'Not recorded'}</dd></div><div><dt>Module</dt><dd className="mono">{d.module || 'Root module'}</dd></div><div><dt>Instance key</dt><dd className="mono">{d.index === undefined ? '—' : JSON.stringify(d.index)}</dd></div></dl>
     {d.classification === 'unrecognized' && <p className="plan-warning"><TriangleAlert size={14} />Unrecognized action in a refresh-only plan. Inspect it with the CLI; it is not treated as safe.</p>}
     {d.classification === 'missing' ? <section className="drift-missing">
      <h3>Not found by the provider</h3>
      <p>State records this object{recordedId !== undefined && <> with ID <Observed value={recordedId} kind="" side="recorded" /></>}, but the provider reported it as not found during the scan. It may have been deleted outside {engine}. State still records it until a reviewed operation changes state.</p>
     </section> : <section>
      <h3>What changed</h3>
      {d.attributes.length ? <table className="diff-table drift-diff"><thead><tr><th scope="col">Attribute</th><th scope="col">Recorded in state</th><th scope="col">Provider reports now</th></tr></thead><tbody>{d.attributes.map(a => <tr key={a.path}>
       <th scope="row"><code>{a.path}</code>{a.sensitive && <span className="flag"><LockKeyhole size={10} />sensitive</span>}</th>
       <td className="drift-recorded"><Observed value={a.recorded} kind={a.kind} side="recorded" /></td>
       <td className="drift-observed"><Observed value={a.observed} kind={a.kind} side="observed" /></td>
      </tr>)}</tbody></table> : <p className="muted">No attribute differences.</p>}
      {d.truncated && <p className="muted">Only the first 1,000 attribute differences are shown.</p>}
      <p className="redaction-note"><LockKeyhole size={12} />Sensitive, secret-named and credential-like values stay redacted on both sides; a difference in a hidden value is still listed.</p>
     </section>}
     <Disclosure summary="Full values"><div className="json-viewer"><JsonTree name="recorded in state" value={d.recorded ?? {}} /><JsonTree name="reported during scan" value={d.observed ?? {}} /></div></Disclosure>
     {source && d.resourceId && <SourceLink prefix={source.prefix} sourcePath={scopedPath(scope, `resources/${d.resourceId}/source`)} editor={source.editor} notify={source.notify} />}
     <section>
      <h3>What you can do</h3>
      <ul className="drift-options">
       <li><strong>Keep the configuration:</strong> run a normal plan; it proposes changing the object back{d.classification === 'missing' ? ' (or recreating it)' : ''}.</li>
       <li><strong>Keep the live value:</strong> update the configuration to match, then plan.</li>
      </ul>
      <p className="muted">Nothing here changes state or infrastructure.</p>
     </section>
    </div>
    <div className="drift-drawer-footer">
     <Button variant="outline" size="small" disabled={!d.resourceId} title={d.resourceId ? undefined : 'Not in the current snapshot'} onClick={() => d.resourceId && openResource(d.resourceId)}>Resource details</Button>
     <AskAI scope={scope} request={{ kind: 'drift', scanId }} label="Review with AI" />
     <Button size="small" className="drift-run-plan" onClick={runPlan}><Play size={13} />Run plan…</Button>
    </div>
   </>}
  </Drawer>;
}
