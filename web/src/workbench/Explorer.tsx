import type { KeyboardEvent } from 'react';
import { Box, Braces, ChevronDown, Copy, Database, Folder, LoaderCircle, LockKeyhole, X } from 'lucide-react';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { JsonTree } from '../components/JsonTree';
import { SourceLink } from '../git/GitView';
import type { EditorName, Module, Resource } from '../types';
import { ErrorState } from './ErrorState';
import { moduleName, providerName } from './format';
export function ResourceRow({
  resource: r,
  selected,
  onSelect,
  selectable,
  chosen,
  onChoose
}: {
  resource: Resource;
  selected: boolean;
  onSelect: () => void;
  selectable: boolean;
  chosen: boolean;
  onChoose: () => void;
}) {
  function key(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    const buttons = Array.from(event.currentTarget.closest('tbody')!.querySelectorAll<HTMLButtonElement>('[data-resource-id]'));
    const index = buttons.indexOf(event.currentTarget);
    buttons[Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
  }
  // Only managed instances can be replaced; data sources and whole modules cannot.
  return <tr className={selected ? 'selected' : ''}><td className="select-column">{selectable && r.mode === 'managed' && <input type="checkbox" checked={chosen} onChange={onChoose} aria-label={`Select ${r.address} for replacement`} />}</td><td><button className="resource-button" data-resource-id={r.id} aria-pressed={selected} onClick={onSelect} onKeyDown={key}><span className={`resource-icon ${r.mode === 'data' ? 'data-icon' : ''}`}>{r.mode === 'data' ? <Database size={17} /> : <Box size={17} />}</span><span className="resource-text"><strong>{r.name}{r.index !== undefined && <span className="instance-key">[{typeof r.index === 'string' ? JSON.stringify(r.index) : String(r.index)}]</span>}</strong><span className="resource-type">{r.type}</span><span className="resource-address mono" title={r.address}>{r.address}</span></span></button></td><td className="provider-column"><span className="provider-label"><span className="provider-dot" />{providerName(r.provider)}</span></td><td><span className={`mode-badge ${r.mode}`}>{r.mode === 'data' ? 'Data' : 'Managed'}</span></td></tr>;
}
export function ModuleTree({
  modules,
  selected,
  choose
}: {
  modules: Module[];
  selected: string | undefined;
  choose: (value: string) => void;
}) {
  function key(event: KeyboardEvent<HTMLUListElement>) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('button'));
    const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (index < 0) return;
    event.preventDefault();
    buttons[Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
  }
  function node(m: Module) {
    const children = modules.filter(c => c.address !== '' && c.parent === m.address);
    return <li key={m.address}>{children.length ? <details open><summary><ChevronDown size={12} /><span>{moduleName(m.address)}</span></summary><button className={`tree-resource ${selected === m.address ? 'chosen' : ''}`} onClick={() => choose(m.address)}><Folder size={13} /><span>Direct resources</span><span className="tree-count">{m.count}</span></button><ul>{children.map(node)}</ul></details> : <button className={`tree-resource ${selected === m.address ? 'chosen' : ''}`} onClick={() => choose(m.address)} title={m.address}><Folder size={13} /><span>{moduleName(m.address).replace(moduleName(m.parent) + '.', '')}</span><span className="tree-count">{m.count}</span></button>}</li>;
  }
  const root = modules.find(m => m.address === '');
  return <ul className="module-tree" onKeyDown={key} aria-label="Modules">{root && <li><button className={`tree-resource ${selected === '' ? 'chosen' : ''}`} onClick={() => choose('')}><Folder size={13} /><span>Root module</span><span className="tree-count">{root.count}</span></button></li>}{modules.filter(m => m.address !== '' && m.parent === '').map(node)}</ul>;
}
export function Inspector({
  resource,
  loading,
  error,
  close,
  copy,
  source,
  editor,
  notify
}: {
  resource?: Resource;
  loading: boolean;
  error?: string;
  close: () => void;
  copy: (value: string, label: string) => Promise<void>;
  source?: { path: string; prefix: string };
  editor: EditorName;
  notify: (text: string) => void;
}) {
  return <Drawer label="Resource inspector" onClose={close}><div className="inspector-header"><span><Braces size={15} />Resource details</span><Button variant="ghost" size="icon" aria-label="Close inspector" onClick={close}><X size={16} /></Button></div>{loading ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading details…</div> : error ? <ErrorState message={error} /> : resource && <div className="inspector-body"><div className="inspector-title"><span className="resource-icon"><Box size={23} /></span><span className={`mode-badge ${resource.mode}`}>{resource.mode}</span></div><h2>{resource.name}</h2><p className="inspector-type mono">{resource.type}</p><div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy resource address" onClick={() => {
            void copy(resource.address, 'Address');
          }}><Copy size={13} /></Button></div><code>{resource.address}</code></div><dl className="metadata"><div><dt>Provider</dt><dd className="mono">{resource.provider || 'Not recorded'}</dd></div><div><dt>Module</dt><dd className="mono">{resource.module || 'Root module'}</dd></div><div><dt>Instance key</dt><dd className="mono">{resource.index === undefined ? '—' : JSON.stringify(resource.index)}</dd></div><div><dt>Source</dt><dd>Recorded state</dd></div></dl>{source && <SourceLink prefix={source.prefix} sourcePath={source.path} editor={editor} notify={notify} />}<div className="attributes-header"><h3>Attributes</h3><Button variant="ghost" size="icon" aria-label="Copy redacted JSON" onClick={() => {
          void copy(JSON.stringify(resource.attributes, null, 2), 'Redacted JSON');
        }}><Copy size={14} /></Button></div><div className="redaction-note"><LockKeyhole size={12} />Sensitive, secret-named and credential-like values are hidden.</div><div className="json-viewer"><JsonTree value={resource.attributes} /></div></div>}</Drawer>;
}
