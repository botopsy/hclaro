import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowRight, History, LoaderCircle, Terminal } from 'lucide-react';
import { api } from '../api';
import type { Operation } from '../types';
import { Button } from '../components/ui/button';
import { OperationLog } from './OperationLog';
import { clock, duration } from './labels';
import { historyPath, navigate } from '../router';
import { launchScope, scopeKey, scopedPath, type PlanScope } from './routes';

// scope names the project or Hub workspace whose operations are listed.
export function OperationsView({ openPlan, scope = launchScope }: { openPlan: (id: string) => void; scope?: PlanScope }) {
  const [selected, setSelected] = useState<string>();
  const operations = useQuery({
    queryKey: ['operations', scopeKey(scope)],
    queryFn: () => api<{ operations: Operation[] }>(scopedPath(scope, 'operations')),
    refetchInterval: q => q.state.data?.operations.some(o => o.status === 'running') ? 2000 : false
  });
  if (operations.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading operations…</p></div>;
  if (operations.error) return <div className="empty-state error-state" role="alert"><p>{operations.error.message}</p></div>;
  const list = operations.data.operations;
  const current = list.find(o => o.id === selected);
  if (!list.length) return <div className="empty-state"><Terminal size={28} /><h2>No operations yet</h2><p>Plans and applies you run appear here with their status, duration and sanitized output while Hclaro runs. Redacted metadata is also kept in <button className="link-button" onClick={() => navigate(historyPath())}>History</button> after Hclaro stops.</p></div>;
  return <div className="operations">
   <table className="resources-table operations-table"><thead><tr><th scope="col">Operation</th><th scope="col">Status</th><th scope="col">Started</th><th scope="col">Duration</th><th scope="col">Result</th></tr></thead><tbody>{list.map(o => <tr key={o.id} className={selected === o.id ? 'selected' : ''}>
    <td><button className="resource-button" aria-pressed={selected === o.id} onClick={() => setSelected(o.id)}><span className="resource-text"><strong>{o.kind === 'apply' ? 'Apply' : o.kind === 'drift' ? 'Drift scan' : o.kind === 'graph' ? 'Dependency graph' : o.replace?.length ? `Replace plan · ${o.replace.length}` : 'Plan'}</strong><span className="resource-address mono">{o.command.join(' ')}</span></span></button></td>
    <td><span className={`status-pill status-${o.status}`}>{o.status === 'running' ? o.stage : o.status}</span></td>
    <td>{clock(o.startedAt)}</td>
    <td>{duration(o.durationMs)}</td>
    <td>{o.kind === 'apply' ? <span className={o.outcome === 'uncertain' ? 'tone-warn' : 'muted'}>{o.outcome === 'applied' ? 'Applied' : o.outcome === 'uncertain' ? 'Outcome uncertain' : 'Not applied'}{o.diagnostic && ` · ${o.diagnostic.code.replaceAll('_', ' ')}`}</span> : o.planId ? <Button variant="ghost" size="small" onClick={() => openPlan(o.planId!)}>Review plan<ArrowRight size={13} /></Button> : <span className="muted">{o.diagnostic?.code.replaceAll('_', ' ') || '—'}{o.exitCode !== undefined && ` · exit ${o.exitCode}`}</span>}</td>
   </tr>)}</tbody></table>
   {current && (current.logRetained ? <OperationLog key={current.id} operation={current} scope={scope} /> : <p className="muted">The log for this operation is no longer kept. Only the 10 most recent logs are retained.</p>)}
   {/* shortcut: numbers copied from maxOperations/maxLogs in internal/operations/manager.go; serve them from the API if they become configurable. */}
   <p className="info-line">This list keeps the last 100 operations and the logs of the last 10, in memory; both disappear when Hclaro stops. Logs contain sanitized summaries, not raw engine output. Operation history keeps metadata on disk, within its retention limits, but never plan files: nothing there can be applied. <Button variant="ghost" size="small" onClick={() => navigate(historyPath())}><History size={13} />Operation history</Button></p>
  </div>;
}
