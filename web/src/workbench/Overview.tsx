import { skipToken, useQuery } from '@tanstack/react-query';
import { CircleHelp } from 'lucide-react';
import { api } from '../api';
import type { DriftScan, GitContext, Operation, PlanSummary, Workspace } from '../types';
import { scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { activeOperation, operationsQuery } from '../lib/queries';
import { StatusDot, type Tone } from '../components/Chip';
import { CopyButton } from '../components/CopyButton';
import { Popover } from '../components/Menu';
import { shortCommit } from '../git/GitView';
import { dayTime } from '../lib/format';

type Target = 'Plans' | 'Drift' | 'Providers' | 'Git' | 'Modules';
type Row = { label: string; tone: Tone; text: string; action?: string; target?: Target };
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
// The server's "Snapshot of recorded state, not live cloud inventory" warning; §4.1 moves it into a tooltip.
const snapshotNote = 'not live cloud inventory';

export function planRow(plans: PlanSummary[] | undefined, operations: Operation[] | undefined): Row {
  const row = (tone: Tone, text: string, action?: string): Row => ({ label: 'Plan', tone, text, action, target: action ? 'Plans' : undefined });
  if (activeOperation(operations)?.kind === 'plan') return row('neutral', 'A plan is running', 'Open Plans');
  const last = operations?.[0];
  const latest = plans?.[0];
  if (last?.kind === 'plan' && last.status !== 'succeeded') return row('delete', last.status === 'cancelled' ? 'The last plan was cancelled' : 'The last plan failed', 'Open Plans');
  if (!latest) return row('neutral', 'No plan yet', 'Run plan');
  if (last?.kind === 'apply' && last.planId === latest.id) return last.outcome === 'applied' ? row('ok', `Last plan applied ${dayTime(last.completedAt)}`) : row('update', 'The last apply did not finish cleanly', 'Open Plans');
  if (latest.errored) return row('delete', 'The last plan has errors', 'Open Plans');
  if (latest.changes > 0) return row('update', `${plural(latest.changes, 'pending change')} · not applied`, 'Review plan');
  return row('ok', 'The last plan has no changes');
}

export function driftRow(scans: DriftScan[] | undefined): Row {
  const row = (tone: Tone, text: string, action?: string): Row => ({ label: 'Drift', tone, text, action, target: action ? 'Drift' : undefined });
  const scan = scans?.[0];
  if (!scan) return row('neutral', 'Not checked yet', 'Check drift');
  if (scan.status === 'running') return row('neutral', 'A drift check is running', 'View drift');
  if (scan.status === 'failed' || scan.status === 'cancelled') return row('delete', scan.status === 'failed' ? 'The last drift check failed' : 'The last drift check was cancelled', 'Open Drift detection');
  if (!scan.current) return row('neutral', 'The last check no longer matches this workspace', 'Check drift');
  const s = scan.summary;
  if (scan.outcome === 'drift_detected') return row('update', [s.changed && `${s.changed} changed`, s.missing && `${s.missing} missing remotely`, s.outputChanges && plural(s.outputChanges, 'output change')].filter(Boolean).join(' · ') || 'Drift detected', 'View drift');
  if (scan.outcome === 'no_drift') return row('ok', 'No drift');
  return row('update', s.cannotVerify ? `${plural(s.cannotVerify, 'resource')} could not be verified` : 'Drift could not be verified', 'View drift');
}

// Branch from the HEAD file (no Git process); clean or dirty only from the status the Git page
// already read, because navigating never runs `git status`.
export function gitRow(head: GitContext | undefined, status: GitContext | undefined): Row | undefined {
  if (!head) return undefined;
  if (head.state !== 'repository') return { label: 'Git', tone: 'neutral', text: head.message || 'Git context unavailable' };
  const branch = branchLabel(head);
  if (status?.state !== 'repository') return { label: 'Git', tone: 'neutral', text: `${branch} · open Git to check for uncommitted files`, action: 'Open Git', target: 'Git' };
  const changed = status.counts.changed;
  return changed ? { label: 'Git', tone: 'update', text: `${branch} · ${plural(changed, 'uncommitted IaC file')}`, action: 'Open Git', target: 'Git' } : { label: 'Git', tone: 'ok', text: `${branch} · IaC tree clean` };
}

function branchLabel(c: GitContext) {
  const h = c.head;
  if (!h) return '—';
  if (h.detached) return `detached @ ${shortCommit(h.commit)}`;
  if (h.unborn) return `${h.branch || 'branch'} · no commits`;
  return `${h.branch}@${shortCommit(h.commit)}`;
}

export function Overview({ workspace: w, scope, gitPrefix, root, drift, open, chooseType, chooseMode }: {
  workspace: Workspace;
  // Where this tab's plans and drift scans live; undefined when this workspace cannot run them.
  scope?: PlanScope;
  gitPrefix?: string;
  root?: string;
  drift?: DriftScan[];
  open: (view: Target) => void;
  chooseType: (type: string) => void;
  chooseMode: (mode: 'managed' | 'data') => void;
}) {
  const plans = useQuery({ queryKey: ['plans', scope ? scopeKey(scope) : 'none'], queryFn: () => api<{ plans: PlanSummary[] }>(scopedPath(scope!, 'plans')), enabled: !!scope });
  const operations = useQuery(operationsQuery(scope));
  const head = useQuery({ queryKey: ['git-head', gitPrefix], queryFn: () => api<GitContext>(`${gitPrefix}git/head`), enabled: gitPrefix !== undefined, retry: false });
  const status = useQuery<GitContext>({ queryKey: ['git-status', gitPrefix], queryFn: skipToken, enabled: gitPrefix !== undefined }).data;
  const note = w.info.warnings.find(x => x.includes(snapshotNote));
  const rows: Row[] = [
    ...(scope ? [planRow(plans.data?.plans, operations.data?.operations), driftRow(drift)] : []),
    ...(w.info.providerLock ? [] : [{ label: 'Lockfile', tone: 'update' as const, text: 'Provider lockfile not detected, so provider versions are unknown', action: 'Providers', target: 'Providers' as const }]),
    ...[gitRow(head.data, status)].filter((r): r is Row => !!r),
    ...w.info.warnings.filter(x => x !== note).map(text => ({ label: 'Workspace', tone: 'update' as const, text }))
  ];
  const allClear = rows.every(r => r.tone === 'ok');
  const types = Object.entries(w.types).sort((a, b) => b[1] - a[1]);
  const largest = types[0]?.[1] ?? 0;
  const providers = Object.keys(w.providers).sort();
  const stats: { value: number; label: string; go: () => void }[] = [
    { value: w.managed, label: 'managed', go: () => chooseMode('managed') },
    { value: w.data, label: w.data === 1 ? 'data source' : 'data sources', go: () => chooseMode('data') },
    { value: Math.max(w.modules.length - 1, 0), label: w.modules.length === 2 ? 'child module' : 'child modules', go: () => open('Modules') },
    { value: providers.length, label: providers.length === 1 ? 'provider' : 'providers', go: () => open('Providers') }
  ];
  return <div className="overview">
   <section className="overview-panel attention" aria-labelledby="overview-attention">
    <h2 id="overview-attention">Needs attention</h2>
    {allClear ? <p className="attention-row"><StatusDot tone="ok" label="Nothing needs attention" /></p> : <ul>{rows.map(r => <li key={`${r.label}-${r.text}`} className="attention-row">
     <StatusDot tone={r.tone} label={r.label} />
     <span className="attention-text">{r.text}</span>
     {r.action && r.target && <button type="button" className="link-button" onClick={() => open(r.target!)}>{r.action} →</button>}
    </li>)}</ul>}
   </section>
   <nav className="overview-stats" aria-label="Counts">
    {stats.map(s => <button type="button" key={s.label} className="overview-stat" onClick={s.go}><strong>{s.value.toLocaleString()}</strong> {s.label}</button>)}
    {note && <Popover className="snapshot-tip" label="About these counts" align="end" button={<CircleHelp size={14} aria-hidden="true" />}><p>{note}</p></Popover>}
   </nav>
   <div className="overview-columns">
    <section className="overview-panel" aria-labelledby="overview-types">
     <h2 id="overview-types">Resources by type</h2>
     {types.map(([type, count]) => <button type="button" className="type-bar" key={type} onClick={() => chooseType(type)}><span><span className="mono">{type}</span><span>{count}</span></span><progress value={count} max={largest} aria-label={`${type}: ${count} instances`} /></button>)}
     {!types.length && <p className="muted">No resource instances in this snapshot.</p>}
    </section>
    <section className="overview-panel" aria-labelledby="overview-details">
     <h2 id="overview-details">Details</h2>
     <dl className="overview-details">
      {root && <div><dt>Root folder</dt><dd className="mono">{root}<CopyButton value={root} label="Copy root folder" iconOnly /></dd></div>}
      <div><dt>Engine</dt><dd>{w.info.engine} {w.info.version}</dd></div>
      {head.data?.state === 'repository' && <div><dt>Branch</dt><dd className="mono">{branchLabel(head.data)}</dd></div>}
      <div><dt>Backend</dt><dd>{w.info.backend}</dd></div>
      <div><dt>Lockfile</dt><dd className={w.info.providerLock ? undefined : 'tone-warn'}>{w.info.providerLock ? 'Present' : 'Not detected'}</dd></div>
      <div><dt>{providers.length === 1 ? 'Provider' : 'Providers'}</dt><dd className="mono">{providers.length ? providers.map(p => <span key={p}>{p || 'Provider not recorded'}</span>) : '—'}</dd></div>
     </dl>
    </section>
   </div>
  </div>;
}
