import { useEffect, useState, type FocusEvent, type MouseEvent, type ReactNode } from 'react';
import { Bug, CircleQuestionMark, FileText, Gift, LoaderCircle, PanelLeftClose, PanelLeftOpen, Star, X } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { sessionQuery } from '../api';
import { HclaroLogo, HclaroMark } from '../brand/HclaroLogo';
import { Menu, MenuItem } from '../components/Menu';
import { hasUnseenRelease, helpLinks, markReleaseSeen, releaseUrl } from './help';
import { groupLabels, importDiscoveryTabs, itemsIn, navState, type NavContext, type NavGroup, type NavItem, type View } from './config';

type Tip = { text: string; top: number; left: number };

export function Sidebar({ id, context, compact, onToggleCompact, onCloseDrawer, operationRunning, titleFor, onSelect, onHome }: {
  id: string;
  context: NavContext;
  // The effective compact (icon-only) mode; never on in the narrow-screen drawer.
  compact: boolean;
  onToggleCompact: () => void;
  onCloseDrawer: () => void;
  operationRunning: boolean;
  // Extra explanation for an enabled item, shown as its title.
  titleFor?: (item: NavItem) => string | undefined;
  onSelect: (item: NavItem) => void;
  onHome: () => void;
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
  const version = useQuery(sessionQuery).data?.version;
  // Set on the first Help open: whether that open found unseen release notes ("New" stays in the menu).
  const [newRelease, setNewRelease] = useState<boolean>();
  const unseen = newRelease === undefined && hasUnseenRelease(version);
  const tipProps = (text: string) => ({ onMouseEnter: (e: MouseEvent<HTMLElement>) => showTip(e, text), onMouseLeave: hideTip, onFocus: (e: FocusEvent<HTMLElement>) => showTip(e, text), onBlur: hideTip });

  function item(nav: NavItem) {
    const state = navState(nav, context);
    const Icon = nav.icon;
    const title = state.reason || titleFor?.(nav);
    const running = nav.badge === 'operations' && operationRunning;
    return <li key={nav.id}><button type="button" className={`nav-item ${state.active ? 'active' : ''}`} aria-label={nav.label} aria-current={state.active ? 'page' : undefined} disabled={!state.enabled} title={compact ? undefined : title} data-nav={nav.id}
      onClick={() => onSelect(nav)} {...tipProps(title ? `${nav.label} · ${title}` : nav.label)}>
     <Icon aria-hidden="true" /><span className="nav-text">{nav.label}</span>
     {running && <span className="running-dot" title="Operation running"><LoaderCircle className="spin" size={11} aria-hidden="true" /></span>}
    </button></li>;
  }
  function group(name: NavGroup, extra?: ReactNode) {
    const label = groupLabels[name];
    const headingId = `${id}-${name}`;
    return <div className={`nav-group nav-group-${name}`} role="group" aria-labelledby={label ? headingId : undefined} aria-label={label ? undefined : name === 'project' ? 'Project' : 'Project utilities'}>
     {label && <div className="nav-label" id={headingId}>{label}</div>}
     <ul>{itemsIn(name, context.ai).map(item)}{extra}</ul>
    </div>;
  }
  const toggleLabel = compact ? 'Expand sidebar' : 'Collapse sidebar';

  return <aside id={id} className="sidebar" aria-label="Application navigation">
   <div className="sidebar-head">
    <a className="brand" href="/" onClick={e => {
      e.preventDefault();
      onHome();
    }} aria-label="Hclaro overview"><HclaroLogo className="brand-wordmark" decorative /><HclaroMark className="brand-compact" size={40} decorative /></a>
    <button type="button" className="sidebar-close" aria-label="Close navigation" onClick={onCloseDrawer}><X size={16} aria-hidden="true" /></button>
   </div>
   <nav id={`${id}-nav`} className="sidebar-nav" aria-label="Main">
    <div className="sidebar-scroll">
     {group('project')}
     {group('analyze')}
     {group('manage')}
    </div>
    <div className="sidebar-bottom">
     {group('utility', <>
      <li><Menu className="help-menu" label={unseen ? 'Help, new release notes' : 'Help'} trigger={{ className: 'nav-item', ...tipProps('Help'), onClick: () => {
        if (newRelease === undefined) setNewRelease(unseen);
        markReleaseSeen(version);
      } }} button={<><CircleQuestionMark aria-hidden="true" /><span className="nav-text">Help</span>{unseen && <span className="unseen-dot" aria-hidden="true" />}</>}>
       <MenuItem href={helpLinks.docs}><FileText size={15} aria-hidden="true" />Documentation</MenuItem>
       <MenuItem href={releaseUrl(version)}><Gift size={15} aria-hidden="true" />What's new{newRelease && <span className="menu-new">New</span>}</MenuItem>
       <MenuItem href={helpLinks.bug}><Bug size={15} aria-hidden="true" />Report a bug</MenuItem>
       <MenuItem href={helpLinks.star}><Star size={15} aria-hidden="true" />Star on GitHub</MenuItem>
       <p className="help-note">Opens in your browser. Bug reports never include state, plans or values.</p>
      </Menu></li>
      <li className="sidebar-toggle-row"><button type="button" className="nav-item sidebar-toggle" aria-label={toggleLabel} aria-expanded={!compact} aria-controls={`${id}-nav`} onClick={onToggleCompact} {...tipProps(toggleLabel)}>{compact ? <PanelLeftOpen aria-hidden="true" /> : <PanelLeftClose aria-hidden="true" />}<span className="nav-text">Collapse</span></button></li>
     </>)}
    </div>
   </nav>
   {tip && <div className="nav-tooltip" role="tooltip" style={{ top: tip.top, left: tip.left }}>{tip.text}</div>}
  </aside>;
}

// Secondary navigation between the two workflows behind the Import & discovery entry.
export function ImportDiscoveryTabs({ view, onChange }: { view: View; onChange: (view: View) => void }) {
  return <nav className="subnav" aria-label="Import and discovery">
   {importDiscoveryTabs.map(tab => <button key={tab.view} type="button" className={view === tab.view ? 'active' : ''} aria-current={view === tab.view ? 'page' : undefined} onClick={() => onChange(tab.view)}>{tab.label}</button>)}
  </nav>;
}
