import type { KeyboardEvent } from 'react';
import { Box, Braces, Copy, Database, LoaderCircle, LockKeyhole, X } from 'lucide-react';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { JsonTree, type RevealFn } from '../components/JsonTree';
import { DataTable, type Column, type Density, type RowGroup } from '../components/DataTable';
import { Chip, type Tone } from '../components/Chip';
import { AskAI } from '../assist/AskAI';
import type { PlanScope } from '../lib/scope';
import type { DriftClassification, DriftScan, EditorName, Resource } from '../types';
import { ErrorState } from '../components/ErrorState';
import { providerName } from '../lib/format';
import { SourceLink } from '../git/editor';

export type GroupBy = 'none' | 'type' | 'module';
export type DriftMark = { label: string; tone: Tone };

const notChecked: DriftMark = { label: 'Not checked', tone: 'neutral' };
const driftMarks: Record<DriftClassification, DriftMark> = {
  changed: { label: 'Drifted', tone: 'update' },
  missing: { label: 'Missing', tone: 'delete' },
  cannot_verify: { label: 'Cannot verify', tone: 'neutral' },
  unrecognized: { label: 'Unrecognized', tone: 'update' }
};

// The drift of one snapshot resource in the latest finished scan, matched here in the browser.
// A stale or withheld scan proves nothing about the current state, and only a complete,
// exhaustive scan supports "No drift".
export function driftOf(scans: DriftScan[] | undefined, id: string): DriftMark {
  const scan = scans?.find(s => s.status === 'completed' || s.status === 'partial');
  if (!scan?.current || scan.withheld) return notChecked;
  const match = scan.resources.find(r => r.resourceId === id);
  if (match) return driftMarks[match.classification] ?? driftMarks.unrecognized;
  return scan.status === 'completed' && scan.exhaustive ? { label: 'No drift', tone: 'ok' } : notChecked;
}

// One -target flag per address, single-quoted for a POSIX shell (addresses can hold quotes).
export function targetFlags(addresses: string[]) {
  return addresses.map(a => `'-target=${a.replaceAll("'", `'\\''`)}'`).join(' ');
}

// The module and type prefix of an address, dimmed in the table so the name stands out.
function addressPrefix(r: Resource) {
  const prefix = `${r.module ? `${r.module}.` : ''}${r.mode === 'data' ? 'data.' : ''}${r.type}.`;
  return r.address.startsWith(prefix) ? prefix.length : 0;
}

function moveFocus(event: KeyboardEvent<HTMLButtonElement>) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
  event.preventDefault();
  const buttons = Array.from(event.currentTarget.closest('tbody')!.querySelectorAll<HTMLButtonElement>('[data-resource-id]'));
  const index = buttons.indexOf(event.currentTarget);
  buttons[Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
}

function groupRows(rows: Resource[], by: Exclude<GroupBy, 'none'>, counts: Map<string, number>): RowGroup<Resource>[] {
  const groups = new Map<string, Resource[]>();
  for (const r of rows) {
    const key = by === 'type' ? r.type : r.module;
    groups.set(key, [...(groups.get(key) ?? []), r]);
  }
  return Array.from(groups, ([key, list]) => ({ key, label: <span className="mono">{key || 'Root module'}</span>, rows: list, count: counts.get(key) }));
}

export function ResourceTable({
  resources,
  groupCounts,
  groupBy,
  showProvider,
  drift,
  selected,
  onSelectedChange,
  active,
  onOpen,
  density
}: {
  resources: Resource[];
  // Server counts over every page; a group header shows its total, its rows only this page's part.
  groupCounts?: { key: string; count: number }[];
  groupBy: GroupBy;
  showProvider: boolean;
  drift: (id: string) => DriftMark;
  selected: ReadonlySet<string>;
  onSelectedChange: (keys: Set<string>) => void;
  active?: string;
  onOpen: (id: string) => void;
  density: Density;
}) {
  const columns: Column<Resource>[] = [
    { key: 'address', header: 'Address', className: 'address-column', cell: r => {
      const cut = addressPrefix(r);
      return <><button type="button" className="address-button mono" data-resource-id={r.id} title={r.address} onClick={() => onOpen(r.id)} onKeyDown={moveFocus}><span className="address-prefix">{r.address.slice(0, cut)}</span>{r.address.slice(cut)}</button>{r.mode === 'data' && <span className="data-badge">data</span>}</>;
    } },
    { key: 'type', header: 'Type', className: 'type-column', cell: r => <Chip>{r.type}</Chip> },
    ...showProvider ? [{ key: 'provider', header: 'Provider', className: 'provider-column', cell: (r: Resource) => providerName(r.provider) }] : [],
    ...resources.some(r => r.keyAttributes) ? [{ key: 'key-attributes', header: 'Key attributes', className: 'key-attributes-column', cell: (r: Resource) => r.keyAttributes ? <span className="mono key-attributes" title={r.keyAttributes}>{r.keyAttributes}</span> : null }] : [],
    { key: 'drift', header: 'Drift', className: 'drift-column', cell: r => {
      const d = drift(r.id);
      return <Chip tone={d.tone}>{d.label}</Chip>;
    } }
  ];
  return <DataTable label="Resources" columns={columns} rowKey={r => r.id} rowLabel={r => r.address} rows={groupBy === 'none' ? resources : undefined} groups={groupBy === 'none' ? undefined : groupRows(resources, groupBy, new Map(groupCounts?.map(g => [g.key, g.count])))} selected={selected} onSelectedChange={onSelectedChange} active={active} density={density} />;
}

export function Inspector({
  resource,
  loading,
  error,
  close,
  copy,
  source,
  editor,
  notify,
  drift,
  ai,
  reveal
}: {
  resource?: Resource;
  loading: boolean;
  error?: string;
  close: () => void;
  copy: (value: string, label: string) => Promise<void>;
  source?: { path: string; prefix: string };
  editor: EditorName;
  notify: (text: string) => void;
  drift: DriftMark;
  // ai is the scope a "Review with AI" package is built in; the entry point is hidden while AI is off.
  ai?: PlanScope;
  // reveal fetches one redacted value of this resource; undefined hides the buttons (launch scope).
  reveal?: RevealFn;
}) {
  return <Drawer label="Resource inspector" onClose={close}>
   <div className="inspector-header"><span><Braces size={15} aria-hidden="true" />Resource details</span><Button variant="ghost" size="icon" aria-label="Close inspector" onClick={close}><X size={16} /></Button></div>
   {loading ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading details…</div> : error ? <ErrorState message={error} /> : resource && <div className="inspector-body">
    <div className="inspector-title"><span className="resource-icon" aria-hidden="true">{resource.mode === 'data' ? <Database size={23} /> : <Box size={23} />}</span><div className="inspector-name"><h2>{resource.name}</h2><p className="inspector-type mono">{resource.type}</p></div><span className={`mode-badge ${resource.mode}`}>{resource.mode}</span></div>
    <div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy resource address" onClick={() => {
        void copy(resource.address, 'Address');
      }}><Copy size={13} /></Button></div><code>{resource.address}</code></div>
    <dl className="metadata"><div><dt>Provider</dt><dd className="mono">{resource.provider || 'Not recorded'}</dd></div><div><dt>Module</dt><dd className="mono">{resource.module || 'Root module'}</dd></div><div><dt>Instance key</dt><dd className="mono">{resource.index === undefined ? '—' : JSON.stringify(resource.index)}</dd></div><div><dt>Drift</dt><dd><Chip tone={drift.tone}>{drift.label}</Chip></dd></div></dl>
    {source && <SourceLink prefix={source.prefix} sourcePath={source.path} editor={editor} notify={notify} />}
    <AskAI scope={ai} request={{ kind: 'resource', resourceId: resource.id }} label="Review with AI" />
    <div className="attributes-header"><h3>Attributes</h3><Button variant="ghost" size="icon" aria-label="Copy redacted JSON" onClick={() => {
        void copy(JSON.stringify(resource.attributes, null, 2), 'Redacted JSON');
      }}><Copy size={14} /></Button></div>
    <div className="redaction-note"><LockKeyhole size={12} />Sensitive, secret-named and credential-like values are hidden.</div>
    <div className="json-viewer"><JsonTree value={resource.attributes} reveal={reveal} /></div>
   </div>}
  </Drawer>;
}
