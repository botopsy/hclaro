import { ArrowRight, Box, Braces, CircleHelp, Database, FolderTree, Network, Terminal } from 'lucide-react';
import { Button } from '../components/ui/button';
import type { Workspace } from '../types';
import { timeLabel } from './format';
export function Overview({
  workspace: w,
  browse,
  chooseType
}: {
  workspace: Workspace;
  browse: () => void;
  chooseType: (type: string) => void;
}) {
  const stats = [{
    label: 'Managed resources',
    value: w.managed,
    icon: Box
  }, {
    label: 'Data sources',
    value: w.data,
    icon: Database
  }, {
    label: 'Child modules',
    value: Math.max(w.modules.length - 1, 0),
    icon: FolderTree
  }, {
    label: 'Providers',
    value: Object.keys(w.providers).length,
    icon: Network
  }];
  return <div className="overview"><div className="stats-grid">{stats.map(({
        label,
        value,
        icon: Icon
      }) => <div key={label} className="stat"><span>{label}<Icon size={16} /></span><strong>{value.toLocaleString()}</strong><small>Recorded in state</small></div>)}</div><div className="overview-columns"><section className="surface distribution"><div className="surface-heading"><div><h2>Resource distribution</h2><p>Instances by resource type</p></div><Braces size={18} /></div>{Object.entries(w.types).sort((a, b) => b[1] - a[1]).map(([type, count]) => <button className="distribution-row" key={type} onClick={() => chooseType(type)}><span><span className="mono">{type}</span><strong>{count}</strong></span><progress value={count} max={w.managed + w.data} aria-label={`${type}: ${count} instances`} /></button>)}{w.managed + w.data === 0 && <p className="empty-description">No resource instances in this snapshot.</p>}</section><section className="surface workspace-info"><div className="surface-heading"><div><h2>Workspace context</h2><p>Connected through your local CLI</p></div><Terminal size={18} /></div><dl className="metadata"><div><dt>Project</dt><dd>{w.info.project}</dd></div>{w.context && <div><dt>Root folder</dt><dd className="mono">{w.context.rootPath}</dd></div>}<div><dt>Workspace</dt><dd>{w.info.workspace}{w.context && ' (this tab only)'}</dd></div>{w.context && <div><dt>Data directory</dt><dd className="mono">{w.context.dataDir}</dd></div>}<div><dt>Engine</dt><dd>{w.info.engine} {w.info.version}</dd></div><div><dt>Backend type</dt><dd>{w.info.backend}</dd></div><div><dt>Execution mode</dt><dd>{w.info.executionMode}</dd></div><div><dt>Provider lockfile</dt><dd>{w.info.providerLock ? 'Present' : 'Not detected'}</dd></div><div><dt>Snapshot</dt><dd>{timeLabel(w.capturedAt)}</dd></div></dl><Button variant="outline" onClick={browse}>Explore resources<ArrowRight size={14} /></Button></section></div><div className="workspace-warnings">{w.info.warnings.map(warning => <div className="info-note" key={warning}><CircleHelp size={15} /><span>{warning}</span></div>)}</div></div>;
}
