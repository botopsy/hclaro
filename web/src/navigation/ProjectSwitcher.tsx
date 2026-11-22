import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { Check, ChevronDown, FileCode2, Folder, FolderKanban, LoaderCircle, Pin, Plus, Search, TriangleAlert } from 'lucide-react';
import type { Project } from '../types';
import { contextPath, projectPath } from '../router';
import { globalPages } from './config';

// What the top bar shows as the current project.
export type CurrentProject = {
  name: string;
  detail: string;
  status: 'ready' | 'loading' | 'missing' | 'none';
};

// A destination offered by the project switcher. Paths are client routes.
export type SwitchTarget = { key: string; label: string; detail: string; path: string; current?: boolean; pinned?: boolean; launch?: boolean };

// The switcher's list (§2.1): the terminal project, then Hub projects pinned first, then by
// last opened (never opened last, in registry order). Only available projects are offered.
export function switchTargets(registry: Project[], launchRoot: string | undefined, current: { launch: boolean; hubId?: string }): SwitchTarget[] {
  const launch: SwitchTarget[] = launchRoot ? [{ key: 'launch', label: registry.find(p => p.launch)?.alias || launchRoot.split(/[\\/]/).filter(Boolean).at(-1) || launchRoot, detail: `Opened from terminal · ${launchRoot}`, path: '/', current: current.launch, launch: true }] : [];
  const hub = registry.filter(p => p.status === 'ready').sort((a, b) => Number(b.pinned) - Number(a.pinned) || (b.lastOpenedAt || '').localeCompare(a.lastOpenedAt || ''));
  return [...launch, ...hub.map((p): SwitchTarget => ({ key: p.id, label: p.alias, detail: p.lastWorkspace ? `Workspace ${p.lastWorkspace}` : 'Choose a workspace', path: p.lastWorkspace ? contextPath(p.id, p.lastWorkspace) : projectPath(p.id), current: p.id === current.hubId, pinned: p.pinned }))];
}

// The project pill of the top bar (§2.1) and its menu: search, the projects, Add and Manage.
export function ProjectSwitcher({ project, targets, onSwitch, onAdd, addDisabled }: {
  project: CurrentProject;
  targets: SwitchTarget[];
  // Opens a project route; the caller confirms first when that would discard work.
  onSwitch: (path: string) => void;
  onAdd: () => void;
  // Why a project cannot be added now (a read-only registry), if it cannot.
  addDisabled?: string;
}) {
  const { open, setOpen, wrapper, trigger, panel, go, arrows } = useSwitcherPanel();
  const [search, setSearch] = useState('');
  const query = search.trim().toLowerCase();
  const shown = targets.filter(t => !query || `${t.label} ${t.detail}`.toLowerCase().includes(query));
  const Icon = project.status === 'missing' ? TriangleAlert : project.status === 'loading' ? LoaderCircle : Folder;
  return <div className="project-switcher" ref={wrapper}>
   <button ref={trigger} type="button" className={`project-pill status-${project.status}`} aria-expanded={open} aria-controls="project-switcher-panel" title={`${project.name} · ${project.detail}`} onClick={() => {
      setSearch('');
      setOpen(o => !o);
    }}>
    <Icon size={14} className={project.status === 'loading' ? 'spin' : undefined} aria-hidden="true" /><strong>{project.name}</strong><ChevronDown size={13} aria-hidden="true" /><span className="sr-only">Switch project</span>
   </button>
   {open && <div ref={panel} id="project-switcher-panel" className="switcher-popover" role="group" aria-label="Switch project" onKeyDown={arrows}>
    <label className="switcher-search"><Search size={14} aria-hidden="true" /><input value={search} onChange={e => setSearch(e.target.value)} placeholder="Search projects" aria-label="Search projects" autoComplete="off" spellCheck={false} /></label>
    {shown.length ? <ul aria-label="Projects">{shown.map(t => <li key={t.key}><button type="button" className="switcher-option" aria-current={t.current ? 'page' : undefined} onClick={() => go(() => {
      if (!t.current) onSwitch(t.path);
    })}>
     {t.launch ? <FileCode2 size={14} aria-hidden="true" /> : t.pinned ? <Pin size={14} aria-label="Pinned" /> : <Folder size={14} aria-hidden="true" />}<span><strong>{t.label}</strong><span>{t.detail}</span></span>{t.current && <Check size={14} className="switcher-check" aria-label="Current project" />}
    </button></li>)}</ul> : <p className="switcher-empty">{targets.length ? 'Nothing matches' : 'No projects in the Hub yet'}</p>}
    <ul className="switcher-all">
     <li><button type="button" className="switcher-option" disabled={!!addDisabled} title={addDisabled} onClick={() => go(onAdd)}><Plus size={14} aria-hidden="true" /><span><strong>Add project…</strong></span></button></li>
     <li><button type="button" className="switcher-option" onClick={() => go(() => onSwitch(globalPages.hub.path))}><FolderKanban size={14} aria-hidden="true" /><span><strong>Manage projects…</strong></span></button></li>
    </ul>
   </div>}
  </div>;
}

// The open state of a top bar switcher: opening focuses the first field or entry, Esc closes
// and returns focus to the pill, a click outside closes, and Up and Down move between the
// panel's fields and enabled entries.
export function useSwitcherPanel() {
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    panel.current?.querySelector<HTMLElement>('input, button:not(:disabled)')?.focus();
    function key(event: globalThis.KeyboardEvent) {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      setOpen(false);
      trigger.current?.focus();
    }
    function pointer(event: PointerEvent) {
      if (!wrapper.current?.contains(event.target as Node)) setOpen(false);
    }
    window.addEventListener('keydown', key, true);
    document.addEventListener('pointerdown', pointer);
    return () => {
      window.removeEventListener('keydown', key, true);
      document.removeEventListener('pointerdown', pointer);
    };
  }, [open]);
  function go(action: () => void) {
    setOpen(false);
    action();
  }
  function arrows(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    const items = Array.from(panel.current?.querySelectorAll<HTMLElement>('input:not(:disabled), button:not(:disabled)') ?? []);
    const at = items.indexOf(document.activeElement as HTMLElement);
    event.preventDefault();
    items[(at + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
  }
  return { open, setOpen, wrapper, trigger, panel, go, arrows };
}
