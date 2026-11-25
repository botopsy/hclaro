import { useQuery } from '@tanstack/react-query';
import { ArrowRight, LoaderCircle, Terminal } from 'lucide-react';
import { Button } from '../components/ui/button';
import { OperationLog } from './OperationLog';
import { navigate } from '../router';
import { globalPages } from '../navigation/config';
import { launchScope, type PlanScope } from '../lib/scope';
import { operationsQuery, useLimits } from '../lib/queries';
import { clock, duration } from '../lib/format';
import { ErrorState } from '../components/ErrorState';
import { EmptyState } from '../components/EmptyState';
import { Disclosure } from '../components/Disclosure';
import { DataTable, type Column } from '../components/DataTable';
import type { Operation } from '../types';

const opLabel = (o: Operation) => o.kind === 'apply' ? 'Apply' : o.kind === 'drift' ? 'Drift scan' : o.kind === 'graph' ? 'Dependency graph' : o.replace?.length ? `Replace plan · ${o.replace.length}` : 'Plan';

// scope names the project or Hub workspace whose operations are listed.
export function OperationsView({ openPlan, openPlans, scope = launchScope }: { openPlan: (id: string) => void; openPlans: () => void; scope?: PlanScope }) {
  const operations = useQuery(operationsQuery(scope));
  const limits = useLimits();
  const history = <button className="link-button" onClick={() => navigate(globalPages.history.path)}>Operation history<ArrowRight size={13} aria-hidden="true" /></button>;
  if (operations.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading operations…</p></div>;
  if (operations.error) return <ErrorState inline message={operations.error.message} />;
  const list = operations.data.operations;
  const columns: Column<Operation>[] = [
    { key: 'op', header: 'Operation', cell: o => <strong>{opLabel(o)}</strong> },
    { key: 'status', header: 'Status', className: 'operations-status', cell: o => <span className={`status-pill status-${o.status}`}>{o.status === 'running' ? o.stage : o.status}</span> },
    { key: 'started', header: 'Started', className: 'operations-time', cell: o => clock(o.startedAt) },
    { key: 'duration', header: 'Duration', className: 'operations-time', cell: o => duration(o.durationMs) },
    { key: 'result', header: 'Result', className: 'operations-result', cell: o => o.kind === 'apply' ? <span className={o.outcome === 'uncertain' ? 'tone-warn' : 'muted'}>{o.outcome === 'applied' ? 'Applied' : o.outcome === 'uncertain' ? 'Outcome uncertain' : 'Not applied'}{o.diagnostic && ` · ${o.diagnostic.code.replaceAll('_', ' ')}`}</span> : o.planId ? <button className="link-button" onClick={() => openPlan(o.planId!)}>Review plan<ArrowRight size={13} aria-hidden="true" /></button> : <span className="muted">{o.diagnostic?.code.replaceAll('_', ' ') || '—'}{o.exitCode !== undefined && ` · exit ${o.exitCode}`}</span> }
  ];
  return <div className="operations">
   <div className="page-toolbar">{history}</div>
   {!list.length ? <EmptyState icon={<Terminal size={28} />} title="No operations in this session" action={<Button onClick={openPlans}>Go to Plans</Button>}>Plans, applies, drift scans and graphs you run appear here with their status and sanitized output while Hclaro runs.</EmptyState> : <div className="operations-table">
    <DataTable label="Operations" columns={columns} rows={list} rowKey={o => o.id} rowLabel={opLabel} expand={o => <div className="operation-details">
     <code className="history-command">{o.command.join(' ')}</code>
     {o.logRetained ? <OperationLog operation={o} scope={scope} /> : <p className="muted">The log for this operation is no longer kept. Only the 10 most recent logs are retained.</p>}
    </div>} />
   </div>}
   {limits && <Disclosure summary="How this list is kept"><p>This list keeps the last {limits.operations} operations and the logs of the last {limits.operationLogs}, in memory; both disappear when Hclaro stops. Logs contain sanitized summaries, not raw engine output. Operation history keeps metadata on disk, within its retention limits, but never plan files: nothing there can be applied.</p></Disclosure>}
  </div>;
}
