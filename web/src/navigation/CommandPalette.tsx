import { useId, useRef, useState, type KeyboardEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Box, FileCode2, Folder, Plus, Search, type LucideIcon } from 'lucide-react';
import { api } from '../api';
import { Dialog } from '../components/Dialog';
import { scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { useDebounced } from '../lib/useDebounced';
import type { ResourcePage } from '../types';
import { groupLabels, itemsIn, navItem, navState, type NavContext, type NavGroup, type NavItem } from './config';
import type { SwitchTarget } from './ProjectSwitcher';

// Actions only open a page and its run dialog; none of them starts an operation.
export type PaletteAction = 'plans' | 'drift' | 'graph' | 'add';
const actions: { id: PaletteAction; label: string }[] = [
  { id: 'plans', label: 'Run plan…' },
  { id: 'drift', label: 'Check drift…' },
  { id: 'graph', label: 'Generate dependency graph…' },
  { id: 'add', label: 'Add project…' }
];

type Entry = { key: string; group: string; label: string; detail?: string; icon: LucideIcon; mono?: boolean; run: () => void };

// The command palette (§4.12): one search box over actions, pages, resources and projects.
export function CommandPalette({ context, targets, resourceScope, addDisabled, onClose, onAction, onPage, onResource, onSwitch }: {
  context: NavContext;
  targets: SwitchTarget[];
  // Where resources are searched (the existing resource search API); none while not readable.
  resourceScope?: PlanScope;
  addDisabled?: boolean;
  onClose: () => void;
  onAction: (action: PaletteAction) => void;
  onPage: (item: NavItem) => void;
  onResource: (id: string) => void;
  // Opens a project route; the caller confirms first when that would discard work.
  onSwitch: (path: string) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  const id = useId();
  const [search, setSearch] = useState('');
  const [active, setActive] = useState(0);
  const query = search.trim().toLowerCase();
  const debounced = useDebounced(query, 180);
  const resources = useQuery({
    queryKey: ['resources', resourceScope ? scopeKey(resourceScope) : 'none', 'palette', debounced],
    queryFn: () => api<ResourcePage>(scopedPath(resourceScope!, `resources?${new URLSearchParams({ query: debounced, offset: '0', limit: '8' })}`)),
    enabled: !!resourceScope && !!debounced
  });
  const enabled = (item: NavItem) => navState(item, context).enabled;
  const all: Entry[] = [
    ...actions.filter(a => a.id === 'add' ? !addDisabled : enabled(navItem(a.id))).map(a => ({ key: `action:${a.id}`, group: 'Actions', label: a.label, icon: a.id === 'add' ? Plus : navItem(a.id).icon, run: () => onAction(a.id) })),
    ...(Object.keys(groupLabels) as NavGroup[]).flatMap(g => itemsIn(g, context.ai)).filter(enabled).map(item => ({ key: `page:${item.id}`, group: 'Pages', label: item.label, icon: item.icon, run: () => onPage(item) })),
    ...(resourceScope && query && debounced === query ? resources.data?.resources ?? [] : []).map(r => ({ key: `resource:${r.id}`, group: 'Resources', label: r.address, icon: Box, mono: true, run: () => onResource(r.id) })),
    ...targets.filter(t => !t.current).map(t => ({ key: `project:${t.key}`, group: 'Projects', label: t.label, detail: t.detail, icon: t.launch ? FileCode2 : Folder, run: () => onSwitch(t.path) }))
  ];
  // Resources are already filtered by the server; everything else is filtered here.
  const shown = all.filter(e => e.group === 'Resources' || !query || `${e.label} ${e.detail ?? ''}`.toLowerCase().includes(query));
  const at = Math.min(active, shown.length - 1);
  function choose(entry: Entry | undefined) {
    if (!entry) return;
    onClose();
    entry.run();
  }
  function keys(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter') {
      event.preventDefault();
      choose(shown[at]);
      return;
    }
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp' || !shown.length) return;
    event.preventDefault();
    const next = (at + (event.key === 'ArrowDown' ? 1 : -1) + shown.length) % shown.length;
    setActive(next);
    document.getElementById(`${id}-${next}`)?.scrollIntoView?.({ block: 'nearest' });
  }
  return <Dialog title="Search" onClose={onClose} initial={input}>
   <label className="switcher-search"><Search size={14} aria-hidden="true" /><input ref={input} role="combobox" aria-expanded="true" aria-controls={`${id}-list`} aria-activedescendant={shown.length ? `${id}-${at}` : undefined} aria-autocomplete="list" value={search} onChange={e => {
      setSearch(e.target.value);
      setActive(0);
    }} onKeyDown={keys} placeholder="Search pages, resources, projects and actions…" aria-label="Search pages, resources, projects and actions" autoComplete="off" spellCheck={false} /></label>
   {shown.length ? <ul id={`${id}-list`} role="listbox" aria-label="Results" className="palette-list">{shown.map((e, i) => <li key={e.key} role="presentation">
     {e.group !== shown[i - 1]?.group && <span className="switcher-label" aria-hidden="true">{e.group}</span>}
     <div id={`${id}-${i}`} role="option" aria-selected={i === at} aria-label={`${e.label}${e.detail ? ` · ${e.detail}` : ''} (${e.group})`} className="switcher-option" onMouseDown={ev => ev.preventDefault()} onMouseMove={() => setActive(i)} onClick={() => choose(e)}>
      <e.icon size={14} aria-hidden="true" /><span><strong className={e.mono ? 'mono' : undefined}>{e.label}</strong>{e.detail && <span>{e.detail}</span>}</span>
     </div>
    </li>)}</ul> : <p className="switcher-empty" role="status">Nothing matches</p>}
  </Dialog>;
}
