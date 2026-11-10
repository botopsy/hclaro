import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, ChevronLeft, ChevronRight, CircleCheck, CircleDashed, CircleHelp, CircleX, Copy, Download, GitBranch, GitCompareArrows, History, Info, LoaderCircle, LockKeyhole, Search, Settings2, ShieldCheck, Trash2, TriangleAlert, X } from 'lucide-react';
import { api, download, send } from '../api';
import type { HistoryEntry, HistoryKind, HistoryPage, HistoryRetention, HistoryStatus, Operation, PlanComparison } from '../types';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { useModal } from '../components/useModal';
import { ActionBadge } from '../plans/PlanReview';
import { OperationLog } from '../plans/OperationLog';
import { actionOrder, duration, engineLabel } from '../plans/labels';

const PAGE = 50;
const kinds: Record<HistoryKind, string> = {
  plan: 'Plan',
  replace: 'Replace plan',
  import: 'Import plan',
  apply: 'Apply',
  drift: 'Drift scan',
  graph: 'Dependency graph',
  init: 'Initialize',
  workspace: 'Workspace',
  tool: 'Project tool'
};
const statusLabels: Record<HistoryStatus, string> = {
  running: 'Running',
  succeeded: 'Succeeded',
  failed: 'Failed',
  cancelled: 'Cancelled',
  unknown: 'Unknown'
};
// Results are worded so an engine exit never reads as verified success.
const results: Record<string, { label: string; tone: string }> = {
  running: { label: 'In progress', tone: 'neutral' },
  planned: { label: 'Planned · not applied', tone: 'muted' },
  not_planned: { label: 'No plan kept', tone: 'muted' },
  applied: { label: 'Engine reported applied', tone: 'create' },
  partial: { label: 'Partial · may be changed', tone: 'warn' },
  not_applied: { label: 'Nothing applied', tone: 'muted' },
  interrupted: { label: 'Interrupted · unknown', tone: 'warn' },
  drift_detected: { label: 'Drift detected', tone: 'update' },
  no_drift: { label: 'No drift found', tone: 'create' },
  cannot_verify: { label: 'Cannot verify', tone: 'warn' },
  not_completed: { label: 'Not completed', tone: 'muted' },
  completed: { label: 'Completed', tone: 'create' },
  unverified: { label: 'Unverified', tone: 'warn' },
  not_run: { label: 'Not run', tone: 'muted' },
  passed: { label: 'Passed', tone: 'create' },
  findings: { label: 'Findings', tone: 'update' },
  not_checked: { label: 'Not checked', tone: 'muted' }
};
const chipActions = ['create', 'update', 'replace', 'delete', 'import', 'forget', 'unknown'];
const isPlan = (e: HistoryEntry) => !!e.plan && e.kind !== 'apply';

export function label(e: HistoryEntry) {
  return kinds[e.kind] + (e.action ? ` · ${e.action.replaceAll('-', ' ')}` : '');
}
function when(time?: string) {
  if (!time) return '—';
  const d = new Date(time);
  return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}
function message(err: unknown) {
  return err instanceof Error ? err.message : 'Something went wrong.';
}

export function StatusBadge({ status }: { status: HistoryStatus }) {
  const Icon = status === 'succeeded' ? CircleCheck : status === 'failed' ? CircleX : status === 'running' ? LoaderCircle : status === 'cancelled' ? CircleDashed : CircleHelp;
  return <span className={`status-pill status-${status}`}><Icon size={12} className={status === 'running' ? 'spin' : undefined} aria-hidden="true" />{statusLabels[status]}</span>;
}

function ResultBadge({ result }: { result: string }) {
  const r = results[result] ?? { label: result.replaceAll('_', ' '), tone: 'muted' };
  return <span className={`history-result tone-${r.tone}`}>{r.label}</span>;
}

function Chips({ entry }: { entry: HistoryEntry }) {
  if (!entry.plan) {
    const d = entry.outcome.drift;
    if (d) return <span className="muted">{d.changed} changed · {d.missing} missing</span>;
    return <span className="muted">—</span>;
  }
  const counts = entry.plan.counts;
  const shown = chipActions.filter(a => counts[a]);
  if (!shown.length) return <span className="muted">No changes</span>;
  const symbols: Record<string, string> = { create: '+', delete: '−', update: '~', replace: '±' };
  return <span className="history-chips">{shown.map(a => <span key={a} className={`history-chip chip-${a}`} title={`${counts[a]} to ${a}`}>{symbols[a] ? <>{symbols[a]}{counts[a]}<span className="sr-only"> {a}</span></> : `${counts[a]} ${a}`}</span>)}</span>;
}

export function HistoryView({ copy }: { copy: (value: string, label: string) => Promise<void> }) {
  const client = useQueryClient();
  const [projectId, setProjectId] = useState('');
  const [workspace, setWorkspace] = useState('');
  const [kind, setKind] = useState('');
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  // The page belongs to one filter combination; changing a filter starts at the first page.
  const [paging, setPaging] = useState({ key: '', page: 0 });
  const [selected, setSelected] = useState<string>();
  const [compare, setCompare] = useState<string[]>([]);
  const [comparing, setComparing] = useState(false);
  const [dialog, setDialog] = useState<'retention' | 'clear'>();
  const [error, setError] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setQuery(search.trim()), 250);
    return () => clearTimeout(t);
  }, [search]);
  const filters = new URLSearchParams();
  if (projectId) filters.set('projectId', projectId);
  if (workspace) filters.set('workspace', workspace);
  if (kind) filters.set('kind', kind);
  if (status) filters.set('status', status);
  if (query) filters.set('query', query.slice(0, 200));
  const filterKey = filters.toString();
  const page = paging.key === filterKey ? paging.page : 0;
  const setPage = (n: number) => setPaging({ key: filterKey, page: n });
  const list = useQuery({
    queryKey: ['history', filters.toString(), page],
    queryFn: () => api<HistoryPage>(`history?${filters.toString()}${filters.size ? '&' : ''}offset=${page * PAGE}&limit=${PAGE}`),
    refetchInterval: q => q.state.data?.entries.some(e => e.status === 'running') ? 2000 : false
  });
  const data = list.data;
  const project = data?.projects.find(p => p.id === projectId);
  const filtered = !!(projectId || kind || status || query);
  const refresh = () => client.invalidateQueries({ queryKey: ['history'] });
  const chosen = compare.map(id => data?.entries.find(e => e.id === id)).filter((e): e is HistoryEntry => !!e);
  const compatible = chosen.length === 2 && chosen[0].projectId === chosen[1].projectId && chosen[0].workspace === chosen[1].workspace && chosen[0].engine === chosen[1].engine;
  const toggleCompare = (e: HistoryEntry) => setCompare(current => current.includes(e.id) ? current.filter(id => id !== e.id) : [...current.slice(-1), e.id]);
  async function exportAs(format: 'json' | 'markdown') {
    setError('');
    const params = new URLSearchParams(filters);
    params.set('format', format);
    try {
      await download(`history/export?${params.toString()}`, `hclaro-history.${format === 'json' ? 'json' : 'md'}`);
    } catch (err) {
      setError(message(err));
    }
  }
  const writable = data?.status === 'ready';
  return <div className="plans history-page">
   {data?.notice && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span>{data.notice}</span></div>}
   <div className="filter-row plan-filters history-filters">
    <div className="search-field compact"><Search size={14} /><input value={search} maxLength={200} onChange={e => setSearch(e.target.value)} placeholder="Search operations, addresses, commits…" aria-label="Search history" />{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch('')}><X size={12} /></button>}</div>
    <select aria-label="Filter by project" value={projectId} onChange={e => { setProjectId(e.target.value); setWorkspace(''); }}><option value="">All projects</option>{data?.projects.map(p => <option key={p.id} value={p.id}>{p.name}{p.registered ? '' : ' (not in Hub)'}</option>)}</select>
    <select aria-label="Filter by workspace" value={workspace} disabled={!project} onChange={e => setWorkspace(e.target.value)}><option value="">{project ? 'All workspaces' : 'Choose a project first'}</option>{project?.workspaces.map(w => <option key={w}>{w}</option>)}</select>
    <select aria-label="Filter by operation" value={kind} onChange={e => setKind(e.target.value)}><option value="">All operations</option>{Object.entries(kinds).map(([k, l]) => <option key={k} value={k}>{l}</option>)}</select>
    <select aria-label="Filter by status" value={status} onChange={e => setStatus(e.target.value)}><option value="">Any status</option>{Object.entries(statusLabels).map(([k, l]) => <option key={k} value={k}>{l}</option>)}</select>
    {filtered && <Button variant="ghost" size="small" onClick={() => { setProjectId(''); setWorkspace(''); setKind(''); setStatus(''); setSearch(''); }}><X size={12} />Reset</Button>}
    <span className="filter-count">{data ? `${data.total} of ${data.stored}` : '…'}</span>
   </div>
   <div className="plans-toolbar">
    <Button variant="outline" size="small" disabled={!compatible} title={chosen.length < 2 ? 'Tick two plans of the same project, workspace and engine.' : compatible ? undefined : 'These plans belong to different projects, workspaces or engines.'} onClick={() => setComparing(true)}><GitCompareArrows size={14} />Compare summaries{chosen.length > 0 && ` (${chosen.length}/2)`}</Button>
    <Button variant="ghost" size="small" disabled={!data?.total} onClick={() => void exportAs('markdown')}><Download size={14} />Markdown</Button>
    <Button variant="ghost" size="small" disabled={!data?.total} onClick={() => void exportAs('json')}><Download size={14} />JSON</Button>
    <Button variant="ghost" size="small" disabled={!writable} onClick={() => setDialog('retention')}><Settings2 size={14} />Retention</Button>
    <Button variant="ghost" size="small" disabled={!writable || !data?.stored} onClick={() => setDialog('clear')}><Trash2 size={14} />Clear…</Button>
    <span className="toolbar-note">Exports are redacted and leave out log lines.</span>
   </div>
   {error && <p className="dialog-error" role="alert">{error}</p>}
   {list.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading history…</p></div> : list.error ? <div className="empty-state error-state" role="alert"><p>{list.error.message}</p><Button variant="outline" size="small" onClick={() => void list.refetch()}>Retry</Button></div> : !data!.stored ? <div className="empty-state"><History size={30} /><h2>No operations recorded yet</h2><p>{data!.status === 'ready' ? 'Plans, applies, drift scans, graphs, initializations, workspace commands and project tools you run appear here, also after Hclaro restarts. Only redacted metadata is kept: no plan files, state, variable values or credentials.' : 'Operation history is not being recorded in this session.'}</p></div> : !data!.total ? <div className="empty-state"><Search size={26} /><h2>No matching operations</h2><p>Change or reset the filters.</p></div> : <>
    <div className="table-scroll"><table className="resources-table history-table">
     <thead><tr><th scope="col"><span className="sr-only">Compare</span></th><th scope="col">Started</th><th scope="col">Operation</th><th scope="col">Project · workspace</th><th scope="col">Status</th><th scope="col">Changes</th><th scope="col">Result</th></tr></thead>
     <tbody>{data!.entries.map(e => <tr key={e.id} className={selected === e.id ? 'selected' : ''}>
      <td>{isPlan(e) && <input type="checkbox" aria-label={`Select ${label(e)} of ${when(e.startedAt)} for comparison`} checked={compare.includes(e.id)} onChange={() => toggleCompare(e)} />}</td>
      <td className="history-when"><time dateTime={e.startedAt}>{when(e.startedAt)}</time><span className="muted">{e.status === 'running' ? 'running' : duration(e.durationMs)}</span></td>
      <td><button className="resource-button" aria-pressed={selected === e.id} onClick={() => setSelected(e.id)}><span className="resource-text"><strong>{label(e)}</strong><span className="resource-address mono">{e.command.join(' ')}</span></span></button></td>
      <td><span className="history-target"><strong>{e.projectName || '—'}</strong><span className="muted">{e.workspace || 'no workspace'} · {engineLabel(e.engine)}{e.engineVersion ? ` ${e.engineVersion}` : ''}</span></span></td>
      <td><StatusBadge status={e.status} /></td>
      <td><Chips entry={e} /></td>
      <td><ResultBadge result={e.outcome.result} />{e.plan?.execution && <span className="muted history-applied"><ArrowRight size={11} />{results[e.plan.execution.result]?.label ?? e.plan.execution.result}</span>}</td>
     </tr>)}</tbody>
    </table></div>
    {data!.total > PAGE && <div className="pager"><Button variant="ghost" size="small" disabled={page === 0} onClick={() => setPage(page - 1)}><ChevronLeft size={13} />Newer</Button><span className="muted">{page * PAGE + 1}–{Math.min(data!.total, (page + 1) * PAGE)} of {data!.total}</span><Button variant="ghost" size="small" disabled={(page + 1) * PAGE >= data!.total} onClick={() => setPage(page + 1)}>Older<ChevronRight size={13} /></Button></div>}
   </>}
   <p className="info-line"><ShieldCheck size={13} />History is stored on this machine only, with owner-only permissions{data ? `, keeping up to ${data.retention.maxEntries} operations for ${data.retention.maxAgeDays} days within ${Math.round(data.retention.maxBytes / (1 << 20) * 10) / 10} MiB` : ''}. It records metadata and sanitized log excerpts only: no state, plan files or attribute values, so nothing here can re-run an operation or apply a plan. Live logs and drift and graph results stay in the running session only.</p>
   {selected && <HistoryDetails key={selected} id={selected} copy={copy} writable={writable} onClose={() => setSelected(undefined)} onDeleted={() => { setSelected(undefined); setCompare(c => c.filter(id => id !== selected)); void refresh(); }} />}
   {comparing && compatible && <CompareDialog left={chosen[0]} right={chosen[1]} onClose={() => setComparing(false)} />}
   {dialog === 'retention' && data && <RetentionDialog current={data.retention} onClose={() => setDialog(undefined)} onSaved={() => { setDialog(undefined); void refresh(); }} />}
   {dialog === 'clear' && data && <ClearDialog projectName={project?.name} projectId={projectId} workspace={workspace} onClose={() => setDialog(undefined)} onCleared={() => { setDialog(undefined); setSelected(undefined); setCompare([]); void refresh(); }} />}
  </div>;
}

function HistoryDetails({ id, copy, writable, onClose, onDeleted }: { id: string; copy: (value: string, label: string) => Promise<void>; writable: boolean; onClose: () => void; onDeleted: () => void }) {
  const detail = useQuery({
    queryKey: ['history', 'entry', id],
    queryFn: () => api<HistoryEntry>(`history/${id}`),
    refetchInterval: q => q.state.data?.status === 'running' ? 2000 : false
  });
  const live = useQuery({
    queryKey: ['operation', id],
    queryFn: () => api<Operation>(`operations/${id}`),
    enabled: !!detail.data?.live,
    retry: false
  });
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState('');
  const [allChanges, setAllChanges] = useState(false);
  const e = detail.data;
  async function remove() {
    setError('');
    try {
      await send('DELETE', `history/${id}`, {});
      onDeleted();
    } catch (err) {
      setError(message(err));
    }
  }
  const changes = e?.plan?.changes ?? [];
  const shown = allChanges ? changes : changes.slice(0, 25);
  const sortedCounts = e?.plan ? Object.entries(e.plan.counts).sort(([a], [b]) => actionOrder.indexOf(a as never) - actionOrder.indexOf(b as never)) : [];
  return <Drawer label="Operation details" onClose={onClose}>
   <div className="inspector-header"><span><History size={15} />Operation details</span><Button variant="ghost" size="icon" aria-label="Close operation details" onClick={onClose}><X size={16} /></Button></div>
   {detail.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading…</div> : detail.error ? <div className="empty-state error-state" role="alert"><p>{detail.error.message}</p></div> : e && <div className="inspector-body history-details">
    <div className="inspector-title"><StatusBadge status={e.status} /><strong>{label(e)}</strong></div>
    <dl className="metadata">
     <div><dt>Project</dt><dd>{e.projectName}{!e.registered && <span className="muted"> · not in the Hub</span>}</dd></div>
     <div><dt>Workspace</dt><dd>{e.workspace || <span className="muted">none</span>}</dd></div>
     <div><dt>Engine</dt><dd>{engineLabel(e.engine)} <span className="mono">{e.engineVersion || 'version unknown'}</span></dd></div>
     <div><dt>Started</dt><dd><time dateTime={e.startedAt}>{new Date(e.startedAt).toLocaleString()}</time></dd></div>
     <div><dt>Finished</dt><dd>{e.finishedAt ? <time dateTime={e.finishedAt}>{new Date(e.finishedAt).toLocaleString()}</time> : <span className="muted">{e.status === 'running' ? 'still running' : 'not recorded'}</span>}</dd></div>
     <div><dt>Duration</dt><dd>{duration(e.durationMs)}{e.exitCode !== undefined && <span className="muted"> · exit {e.exitCode}</span>}</dd></div>
     {e.context?.profileName && <div><dt>Profile</dt><dd>{e.context.profileName} <span className="muted">revision {e.context.profileRevision}</span></dd></div>}
     {(e.context?.fingerprint || e.plan?.fingerprint) && <div><dt>Context</dt><dd className="mono" title="Equal fingerprints mean the same engine, workspace, variable files, configuration and environment names.">{e.plan?.fingerprint || e.context?.fingerprint}</dd></div>}
     {e.git && <div><dt>Git</dt><dd className="history-git"><GitBranch size={12} aria-hidden="true" /><span className="mono">{e.git.commit.slice(0, 12)}</span>{e.git.branch && <span className="muted"> · {e.git.branch}</span>} <Button variant="ghost" size="icon" aria-label="Copy commit" onClick={() => void copy(e.git!.commit, 'Commit')}><Copy size={12} /></Button></dd></div>}
     <div><dt>ID</dt><dd className="mono">{e.id}</dd></div>
    </dl>
    <p className="mono history-command">{e.command.join(' ')}</p>
    {e.plan && <section className="history-stage" aria-labelledby="stage-planned">
     <h3 id="stage-planned"><span className="stage-number">1</span>Planned</h3>
     <div className="history-counts">{sortedCounts.length ? sortedCounts.map(([a, n]) => <span key={a} className="history-count"><ActionBadge action={a === 'import' ? 'read' : a} /><strong>{n}</strong>{a === 'import' && <span className="muted">import</span>}</span>) : <span className="muted">No resource changes.</span>}{e.plan.outputs > 0 && <span className="muted">{e.plan.outputs} output change(s)</span>}{e.plan.drift > 0 && <span className="muted">{e.plan.drift} changed outside</span>}</div>
     {e.plan.errored && <div className="plan-warning" role="note"><TriangleAlert size={15} />The engine reported errors; this plan is incomplete.</div>}
     {changes.length > 0 && <ul className="history-changes">{shown.map((c, i) => <li key={`${c.address}-${i}`}><ActionBadge action={c.action === 'import' ? 'read' : c.action} /><code>{c.address}</code>{c.action === 'import' && <span className="flag">import</span>}</li>)}</ul>}
     {changes.length > 25 && <Button variant="ghost" size="small" onClick={() => setAllChanges(!allChanges)}>{allChanges ? 'Show fewer' : `Show all ${changes.length}`}</Button>}
     {!!e.plan.omitted && <p className="muted">{e.plan.omitted} more change(s) were not kept.</p>}
     {e.plan.varFiles.length > 0 && <p className="muted">Variable files: <span className="mono">{e.plan.varFiles.join(', ')}</span></p>}
     <p className="redaction-note"><LockKeyhole size={12} />Addresses and actions only. Attribute values, outputs and the plan file were never kept.{isPlan(e) && ' This plan cannot be applied from history; run a new plan and review it.'}</p>
    </section>}
    <section className="history-stage" aria-labelledby="stage-executed">
     <h3 id="stage-executed"><span className="stage-number">{e.plan ? 2 : 1}</span>{isPlan(e) ? 'Executed' : 'Outcome'}</h3>
     {isPlan(e) ? e.plan!.execution ? <p><ResultBadge result={e.plan!.execution.result} /> Applied by a later operation{e.plan!.execution.finishedAt && <> at <time dateTime={e.plan!.execution.finishedAt}>{new Date(e.plan!.execution.finishedAt).toLocaleString()}</time></>}. <span className="muted">An engine exit is not proof that resources are healthy.</span></p> : <p><ResultBadge result={e.outcome.result} /> {e.outcome.summary}{e.outcome.result === 'planned' && ' Hclaro has no record of this plan being applied.'}</p> : <p><ResultBadge result={e.outcome.result} /> {e.outcome.summary}</p>}
     {e.kind === 'apply' && <p className="muted">{e.outcome.stateReloaded ? 'State was read again after the apply.' : 'State was not read again after the apply.'} Not verified against your provider.</p>}
     {e.outcome.drift && <p className="muted">{e.outcome.drift.changed} changed · {e.outcome.drift.missing} missing · {e.outcome.drift.cannotVerify} cannot be verified</p>}
     {e.diagnostic && <div className="operation-failed" role="note"><strong>{e.diagnostic.code.replaceAll('_', ' ')}</strong><p>{e.diagnostic.message}</p></div>}
    </section>
    <section className="history-stage" aria-labelledby="stage-log">
     <h3 id="stage-log">Log excerpt</h3>
     {e.live && live.data?.logRetained ? <OperationLog key={live.data.id} operation={live.data} onFinished={() => void detail.refetch()} /> : e.excerpt.length ? <>
      {!!e.excerptOmitted && <p className="muted">{e.excerptOmitted} earlier line(s) were not kept.</p>}
      <ol className="log-lines" aria-label="Sanitized log excerpt">{e.excerpt.map((l, i) => <li key={i} className={`log-${l.level}`}><span className="muted">{l.time ? new Date(l.time).toLocaleTimeString() : ''}</span><span>{l.text}</span></li>)}</ol>
     </> : <p className="muted">{e.status === 'running' ? 'The log is available in the session that runs this operation.' : 'No log lines were kept.'}</p>}
     <p className="redaction-note"><LockKeyhole size={12} />Sanitized summaries, not raw engine output. Lines that looked like secrets were withheld.</p>
    </section>
    {writable && e.status !== 'running' && <div className="history-delete">{confirming ? <><span>Delete this entry from history? This does not change infrastructure, state or files.</span><Button variant="ghost" size="small" onClick={() => setConfirming(false)}>Keep</Button><Button variant="destructive" size="small" onClick={() => void remove()}><Trash2 size={13} />Delete entry</Button></> : <Button variant="ghost" size="small" onClick={() => setConfirming(true)}><Trash2 size={13} />Delete from history</Button>}</div>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
   </div>}
  </Drawer>;
}

function CompareDialog({ left, right, onClose }: { left: HistoryEntry; right: HistoryEntry; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  useModal(ref, close, onClose);
  const result = useQuery({
    queryKey: ['history', 'compare', left.id, right.id],
    queryFn: () => api<PlanComparison>(`history/compare?left=${left.id}&right=${right.id}`),
    retry: false
  });
  const c = result.data;
  const [older, newer] = left.startedAt <= right.startedAt ? [left, right] : [right, left];
  return <div className="dialog-backdrop">
   <div ref={ref} className="dialog dialog-wide" role="dialog" aria-modal="true" aria-labelledby="history-compare-title">
    <div className="dialog-header"><h2 id="history-compare-title">Compare plan summaries</h2><Button ref={close} variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p className="muted">{older.projectName} · {older.workspace || 'no workspace'}: plan of {when(older.startedAt)} → plan of {when(newer.startedAt)}. Addresses and actions only; values were never kept.</p>
    {result.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Comparing…</div> : result.error ? <div className="empty-state error-state" role="alert"><p>{result.error.message}</p></div> : c && <>
     {c.notices.map(n => <div className="plan-warning" key={n} role="note"><Info size={15} />{n}</div>)}
     <p className="info-line compare-facts"><span>{c.added.length} newly planned</span><span>{c.removed.length} no longer planned</span><span>{c.changed.length} with a different action</span><span>{c.same} unchanged</span></p>
     <div className="history-compare">
      <CompareList title="Newly planned" items={c.added.map(a => ({ address: a.address, action: a.action }))} />
      <CompareList title="No longer planned" items={c.removed.map(a => ({ address: a.address, action: a.action }))} />
      <section><h3>Different action</h3>{c.changed.length ? <ul className="history-changes">{c.changed.map(x => <li key={x.address}><ActionBadge action={x.left} /><ArrowRight size={12} aria-label="became" /><ActionBadge action={x.right} /><code>{x.address}</code></li>)}</ul> : <p className="muted">None.</p>}</section>
     </div>
    </>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Close</Button></div>
   </div>
  </div>;
}

function CompareList({ title, items }: { title: string; items: { address: string; action: string }[] }) {
  return <section><h3>{title}</h3>{items.length ? <ul className="history-changes">{items.map(i => <li key={i.address}><ActionBadge action={i.action === 'import' ? 'read' : i.action} /><code>{i.address}</code></li>)}</ul> : <p className="muted">None.</p>}</section>;
}

function RetentionDialog({ current, onClose, onSaved }: { current: HistoryRetention; onClose: () => void; onSaved: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const first = useRef<HTMLInputElement>(null);
  useModal(ref, first, onClose);
  const [entries, setEntries] = useState(String(current.maxEntries));
  const [days, setDays] = useState(String(current.maxAgeDays));
  const [mib, setMib] = useState(String(Math.round(current.maxBytes / (1 << 20) * 100) / 100));
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  return <div className="dialog-backdrop">
   <div ref={ref} className="dialog" role="dialog" aria-modal="true" aria-labelledby="retention-title">
    <div className="dialog-header"><h2 id="retention-title">History retention</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p>The oldest operations are removed first once any limit is reached. Running operations are always kept. Lower limits apply immediately.</p>
    <form className="hub-form" onSubmit={async ev => {
        ev.preventDefault();
        setBusy(true);
        setError('');
        try {
          await send('PATCH', 'history/retention', { maxEntries: Number(entries), maxAgeDays: Number(days), maxBytes: Math.round(Number(mib) * (1 << 20)) });
          onSaved();
        } catch (err) {
          setError(message(err));
        } finally {
          setBusy(false);
        }
      }}>
     <label>Operations to keep<input ref={first} type="number" min={10} max={5000} step={1} value={entries} onChange={e => setEntries(e.target.value)} required /></label>
     <label>Days to keep<input type="number" min={1} max={3650} step={1} value={days} onChange={e => setDays(e.target.value)} required /></label>
     <label>Size limit (MiB)<input type="number" min={0.0625} max={16} step="any" value={mib} onChange={e => setMib(e.target.value)} required /></label>
     {error && <p className="dialog-error" role="alert">{error}</p>}
     <div className="dialog-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" disabled={busy}>Save</Button></div>
    </form>
   </div>
  </div>;
}

function ClearDialog({ projectName, projectId, workspace, onClose, onCleared }: { projectName?: string; projectId: string; workspace: string; onClose: () => void; onCleared: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useModal(ref, cancel, onClose);
  const [scope, setScope] = useState<'filtered' | 'all'>(projectId ? 'filtered' : 'all');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  return <div className="dialog-backdrop">
   <div ref={ref} className="dialog" role="dialog" aria-modal="true" aria-labelledby="clear-title">
    <div className="dialog-header"><h2 id="clear-title">Clear operation history</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p>This removes entries from Hclaro's local history only. It does not change infrastructure, state, plans or project files, and it cannot be undone. Entries of running operations are kept.</p>
    {projectId && <fieldset className="history-scope"><legend className="sr-only">What to clear</legend>
     <label><input type="radio" name="scope" checked={scope === 'filtered'} onChange={() => setScope('filtered')} />Only {projectName}{workspace ? ` · ${workspace}` : ''}</label>
     <label><input type="radio" name="scope" checked={scope === 'all'} onChange={() => setScope('all')} />All projects</label>
    </fieldset>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button ref={cancel} type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button variant="destructive" disabled={busy} onClick={async () => {
        setBusy(true);
        setError('');
        try {
          await send('DELETE', 'history', scope === 'filtered' && projectId ? { confirm: true, projectId, ...(workspace ? { workspace } : {}) } : { confirm: true });
          onCleared();
        } catch (err) {
          setError(message(err));
        } finally {
          setBusy(false);
        }
      }}><Trash2 size={14} />Clear history</Button></div>
   </div>
  </div>;
}
