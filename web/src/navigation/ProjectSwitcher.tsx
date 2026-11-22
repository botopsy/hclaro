import { useEffect, useRef, useState, type ReactNode } from 'react';
import { ChevronDown, Clock, FileCode2, Folder, FolderKanban, FolderOpen, LoaderCircle, TriangleAlert } from 'lucide-react';
import { globalPages } from './config';

// What the top bar shows as the current project.
export type CurrentProject = {
  name: string;
  detail: string;
  status: 'ready' | 'loading' | 'missing' | 'none';
};

// A destination offered by the project switcher. Paths are client routes.
export type SwitchTarget = { key: string; label: string; detail: string; path: string; current?: boolean };

export type SwitchTargets = {
  launch?: SwitchTarget;
  recent: SwitchTarget[];
  // The current Hub project's page, which lists and manages its workspaces.
  projectPage?: SwitchTarget;
};

// The project pill of the top bar (§2.1) and its menu.
export function ProjectSwitcher({ project, targets, onSwitch }: {
  project: CurrentProject;
  targets: SwitchTargets;
  // Opens a project route; the caller confirms first when that would discard work.
  onSwitch: (path: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    panel.current?.querySelector<HTMLElement>('button:not(:disabled)')?.focus();
    function key(event: KeyboardEvent) {
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
  function go(path: string) {
    setOpen(false);
    onSwitch(path);
  }
  const Icon = project.status === 'missing' ? TriangleAlert : project.status === 'loading' ? LoaderCircle : Folder;
  const option = (t: SwitchTarget, icon: ReactNode) => <li key={t.key}><button type="button" className="switcher-option" aria-current={t.current ? 'page' : undefined} onClick={() => go(t.path)}>{icon}<span><strong>{t.label}</strong><span>{t.detail}</span></span></button></li>;
  return <div className="project-switcher" ref={wrapper}>
   <button ref={trigger} type="button" className={`project-pill status-${project.status}`} aria-expanded={open} aria-controls="project-switcher-panel" title={`${project.name} · ${project.detail}`} onClick={() => setOpen(o => !o)}>
    <Icon size={14} className={project.status === 'loading' ? 'spin' : undefined} aria-hidden="true" /><strong>{project.name}</strong><ChevronDown size={13} aria-hidden="true" /><span className="sr-only">Switch project</span>
   </button>
   {open && <div ref={panel} id="project-switcher-panel" className="switcher-popover" role="group" aria-label="Switch project">
    {targets.projectPage && <ul>{option(targets.projectPage, <FolderOpen size={14} aria-hidden="true" />)}</ul>}
    {targets.launch && <><div className="switcher-label">Opened from terminal</div><ul>{option(targets.launch, <FileCode2 size={14} aria-hidden="true" />)}</ul></>}
    {targets.recent.length > 0 && <><div className="switcher-label">Recent projects</div><ul>{targets.recent.map(t => option(t, <Clock size={14} aria-hidden="true" />))}</ul></>}
    <ul className="switcher-all">{option({ key: 'all', label: 'All projects', detail: 'Open, add or manage projects', path: globalPages.hub.path }, <FolderKanban size={14} aria-hidden="true" />)}</ul>
   </div>}
  </div>;
}
