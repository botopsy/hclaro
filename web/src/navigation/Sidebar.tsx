import { useEffect, useRef, useState, type FocusEvent, type MouseEvent, type ReactNode } from 'react';
import { ChevronsUpDown, Clock, FileCode2, FolderKanban, FolderOpen, LoaderCircle, PanelLeftClose, PanelLeftOpen, ShieldCheck, TriangleAlert, X } from 'lucide-react';
import { HclaroLogo, HclaroMark } from '../brand/HclaroLogo';
import { groupLabels, importDiscoveryTabs, itemsIn, navState, type NavContext, type NavGroup, type NavItem, type View } from './config';

// What the project switcher shows as the current project.
export type SidebarProject = {
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

type Tip = { text: string; top: number; left: number };

export function Sidebar({ id, context, compact, onToggleCompact, onCloseDrawer, project, targets, workspace, resourceCount, operationRunning, titleFor, onSelect, onHome, onSwitch }: {
  id: string;
  context: NavContext;
  // The effective compact (icon-only) mode; never on in the narrow-screen drawer.
  compact: boolean;
  onToggleCompact: () => void;
  onCloseDrawer: () => void;
  project: SidebarProject;
  targets: SwitchTargets;
  // The Terraform workspace control for the current project, if it has one.
  workspace?: ReactNode;
  resourceCount?: number;
  operationRunning: boolean;
  // Extra explanation for an enabled item, shown as its title.
  titleFor?: (item: NavItem) => string | undefined;
  onSelect: (item: NavItem) => void;
  onHome: () => void;
  // Opens a project route; the caller confirms first when that would discard work.
  onSwitch: (path: string) => void;
}) {
  const [shownTip, setTip] = useState<Tip>();
  // Labels are visible in the expanded sidebar, so tooltips exist only in compact mode.
  const tip = compact ? shownTip : undefined;
  useEffect(() => {
    if (!tip) return;
    // WCAG 1.4.13: a tooltip can be dismissed without moving focus or the pointer.
    function key(event: KeyboardEvent) {
      if (event.key === 'Escape') setTip(undefined);
    }
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [tip]);
  function showTip(event: MouseEvent<HTMLElement> | FocusEvent<HTMLElement>, text: string) {
    if (!compact) return;
    const rect = event.currentTarget.getBoundingClientRect();
    setTip({ text, top: rect.top + rect.height / 2, left: rect.right + 10 });
  }
  const hideTip = () => setTip(undefined);

  function item(nav: NavItem) {
    const state = navState(nav, context);
    const Icon = nav.icon;
    const title = state.reason || titleFor?.(nav);
    const running = nav.badge === 'operations' && operationRunning;
    return <li key={nav.id}><button type="button" className={`nav-item ${state.active ? 'active' : ''}`} aria-label={nav.label} aria-current={state.active ? 'page' : undefined} disabled={!state.enabled} title={compact ? undefined : title} data-nav={nav.id}
      onClick={() => onSelect(nav)} onMouseEnter={e => showTip(e, title ? `${nav.label} · ${title}` : nav.label)} onMouseLeave={hideTip} onFocus={e => showTip(e, title ? `${nav.label} · ${title}` : nav.label)} onBlur={hideTip}>
     <Icon size={17} aria-hidden="true" /><span className="nav-text">{nav.label}</span>
     {nav.badge === 'resources' && resourceCount !== undefined && <span className="nav-count">{resourceCount}</span>}
     {running && <span className="nav-count running-dot" title="Operation running"><LoaderCircle className="spin" size={11} aria-hidden="true" /></span>}
    </button></li>;
  }
  function group(name: NavGroup) {
    const label = groupLabels[name];
    const headingId = `${id}-${name}`;
    return <div className={`nav-group nav-group-${name}`} role="group" aria-labelledby={label ? headingId : undefined} aria-label={label ? undefined : name === 'project' ? 'Project' : 'Project utilities'}>
     {label && <div className="nav-label" id={headingId}>{label}</div>}
     <ul>{itemsIn(name).map(item)}</ul>
    </div>;
  }

  return <aside id={id} className="sidebar" aria-label="Application navigation">
   <div className="sidebar-head">
    <a className="brand" href="/" onClick={e => {
      e.preventDefault();
      onHome();
    }} aria-label="Hclaro overview"><HclaroLogo className="brand-wordmark" decorative /><HclaroMark className="brand-compact" size={40} decorative /><span className="release-badge">BETA</span></a>
    <button type="button" className="sidebar-toggle" aria-label={compact ? 'Expand sidebar' : 'Collapse sidebar'} aria-expanded={!compact} aria-controls={`${id}-nav`} onClick={onToggleCompact} onMouseEnter={e => showTip(e, 'Expand sidebar')} onMouseLeave={hideTip} onFocus={e => showTip(e, 'Expand sidebar')} onBlur={hideTip}>{compact ? <PanelLeftOpen size={16} aria-hidden="true" /> : <PanelLeftClose size={16} aria-hidden="true" />}</button>
    <button type="button" className="sidebar-close" aria-label="Close navigation" onClick={onCloseDrawer}><X size={16} aria-hidden="true" /></button>
   </div>
   <ProjectSwitcher project={project} targets={targets} compact={compact} workspace={workspace} onSwitch={onSwitch} showTip={showTip} hideTip={hideTip} />
   {!compact && workspace && <div className="sidebar-workspace">{workspace}</div>}
   <nav id={`${id}-nav`} className="sidebar-nav" aria-label="Main">
    <div className="sidebar-scroll">
     {group('project')}
     {group('analyze')}
     {group('manage')}
    </div>
    <div className="sidebar-bottom">{group('utility')}</div>
   </nav>
   <div className="sidebar-meta">
    <div className="sidebar-foot" title="Local by design: your state stays on your machine."><ShieldCheck size={14} aria-hidden="true" /><span className="nav-text">Local by design</span><span className="foot-version"><span className="dot" />Beta · v0.1</span></div>
   </div>
   {tip && <div className="nav-tooltip" role="tooltip" style={{ top: tip.top, left: tip.left }}>{tip.text}</div>}
  </aside>;
}

function ProjectSwitcher({ project, targets, compact, workspace, onSwitch, showTip, hideTip }: {
  project: SidebarProject;
  targets: SwitchTargets;
  compact: boolean;
  workspace?: ReactNode;
  onSwitch: (path: string) => void;
  showTip: (event: MouseEvent<HTMLElement> | FocusEvent<HTMLElement>, text: string) => void;
  hideTip: () => void;
}) {
  const [open, setOpen] = useState(false);
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    panel.current?.querySelector<HTMLElement>('button:not(:disabled), select')?.focus();
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
  const Icon = project.status === 'missing' ? TriangleAlert : project.status === 'loading' ? LoaderCircle : FileCode2;
  const option = (t: SwitchTarget, icon: ReactNode) => <li key={t.key}><button type="button" className="switcher-option" aria-current={t.current ? 'page' : undefined} onClick={() => go(t.path)}>{icon}<span><strong>{t.label}</strong><span>{t.detail}</span></span></button></li>;
  return <div className="project-switcher" ref={wrapper}>
   <button ref={trigger} type="button" className={`project-tile status-${project.status}`} aria-expanded={open} aria-controls="project-switcher-panel" aria-label={compact ? `Project ${project.name}. Switch project` : undefined}
     onClick={() => {
       hideTip();
       setOpen(o => !o);
     }} onMouseEnter={e => showTip(e, `${project.name} · ${project.detail}`)} onMouseLeave={hideTip} onFocus={e => showTip(e, `${project.name} · ${project.detail}`)} onBlur={hideTip}>
    <span className="project-icon"><Icon size={18} className={project.status === 'loading' ? 'spin' : undefined} aria-hidden="true" /></span>
    <span className="project-text"><span className="project-kicker">Project</span><strong>{project.name}</strong><span>{project.detail}</span></span>
    <ChevronsUpDown size={14} className="project-chevron" aria-hidden="true" /><span className="sr-only">Switch project</span>
   </button>
   {open && <div ref={panel} id="project-switcher-panel" className="switcher-popover" role="group" aria-label="Switch project">
    {compact && workspace && <div className="switcher-workspace">{workspace}</div>}
    {targets.projectPage && <ul>{option(targets.projectPage, <FolderOpen size={14} aria-hidden="true" />)}</ul>}
    {targets.launch && <><div className="switcher-label">Opened from terminal</div><ul>{option(targets.launch, <FileCode2 size={14} aria-hidden="true" />)}</ul></>}
    {targets.recent.length > 0 && <><div className="switcher-label">Recent projects</div><ul>{targets.recent.map(t => option(t, <Clock size={14} aria-hidden="true" />))}</ul></>}
    <ul className="switcher-all">{option({ key: 'all', label: 'All projects', detail: 'Open, add or manage projects', path: '/projects' }, <FolderKanban size={14} aria-hidden="true" />)}</ul>
   </div>}
  </div>;
}

// Secondary navigation between the two workflows behind the Import & discovery entry.
export function ImportDiscoveryTabs({ view, onChange }: { view: View; onChange: (view: View) => void }) {
  return <nav className="subnav" aria-label="Import and discovery">
   {importDiscoveryTabs.map(tab => <button key={tab.view} type="button" className={view === tab.view ? 'active' : ''} aria-current={view === tab.view ? 'page' : undefined} onClick={() => onChange(tab.view)}>{tab.label}</button>)}
  </nav>;
}
