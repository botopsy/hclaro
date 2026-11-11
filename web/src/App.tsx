import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowDownAZ, ArrowLeft, ArrowRight, Box, RefreshCcw, Check, ChevronRight, CircleHelp, Command, Database, ExternalLink, Folder, FolderTree, Layers3, LoaderCircle, LockKeyhole, Menu, Moon, Network, Search, ShieldCheck, SlidersHorizontal, Sun, X } from 'lucide-react';
import { ApiError, api, openInNewTab, post } from './api';
import type { ReplaceTarget, Resource, ResourcePage } from './types';
import { comparePath, historyPath, contextPath, isExplorerView, navigate, parseRoute, productionLike, projectPath, takeLinkedResource, useRoute, workbenchKey, type Route } from './router';
import { HubView, ProductionBadge, ProjectView } from './hub/HubView';
import { Button } from './components/ui/button';
import { PlansView } from './plans/PlansView';
import { OperationsView } from './plans/OperationsView';
import { scopeKey, scopedPath } from './plans/routes';
import { ImportView } from './imports/ImportView';
import { DriftView } from './drift/DriftView';
import { DiscoveryView } from './discovery/DiscoveryView';
import { GraphView } from './graph/GraphView';
import { CompareView } from './compare/CompareView';
import { HistoryView } from './history/HistoryView';
import { ToolsView } from './hub/ToolsView';
import { LaunchTools } from './hub/LaunchTools';
import { ContextSelector } from './profiles/ContextSelector';
import { GitBadge, GitView, gitPrefix } from './git/GitView';
import { viewTrail, type NavContext, type NavItem, type NavMode, type View } from './navigation/config';
import { ImportDiscoveryTabs, Sidebar, type SidebarProject, type SwitchTarget, type SwitchTargets } from './navigation/Sidebar';
import { ConfirmSwitch } from './navigation/ConfirmSwitch';
import type { DiscoveryPrefill } from './types';
import { useWorkbenchContext } from './workbench/useWorkbenchContext';
import { useMedia, usePreferences } from './workbench/preferences';
import { ErrorState } from './workbench/ErrorState';
import { Inspector, ModuleTree, ResourceRow } from './workbench/Explorer';
import { Overview } from './workbench/Overview';
import { SettingsPanel } from './workbench/SettingsPanel';
import { PageHeading } from './workbench/PageHeading';
import { moduleName, providerName, timeLabel } from './workbench/format';
// Each project workspace gets its own Workbench instance (keyed below), so filters,
// selections and cached queries can never carry over from another context.
export default function App() {
  const route = useRoute();
  return <Workbench key={workbenchKey(route)} route={route} />;
}
// Below this width the sidebar is a dismissible drawer instead of a column.
const narrowQuery = '(width <= 900px)';
function Workbench({
  route
}: {
  route: Route;
}) {
  const scoped = route.kind === 'context' ? route : undefined;
  // Hub pages and the comparison page have no workspace of their own.
  const onHub = route.kind === 'hub' || route.kind === 'project' || route.kind === 'compare' || route.kind === 'history';
  const comparing = route.kind === 'compare';
  const historyPage = route.kind === 'history';
  const [localView, setViewState] = useState<View>(scoped ? scoped.view : 'Resources');
  // In a Hub workspace the URL holds the explorer view, so back and forward work.
  const view: View = scoped && localView !== 'Settings' ? scoped.view : localView;
  // Back and forward between explorer views of a Hub workspace also leave Settings.
  const routedView = scoped?.view;
  const [seenView, setSeenView] = useState(routedView);
  if (routedView !== seenView) {
    setSeenView(routedView);
    if (routedView) setViewState(routedView);
  }
  // The Import & discovery entry reopens the workflow last used in this tab.
  const [importTab, setImportTab] = useState<View>('Import');
  // Narrow screens: the sidebar is a drawer, closed until opened from the top bar.
  const narrow = useMedia(narrowQuery);
  const [drawerRequested, setNavOpen] = useState(false);
  const navOpen = drawerRequested && narrow;
  const menuButton = useRef<HTMLButtonElement>(null);
  // A project or workspace switch waiting for confirmation (it would discard work in this tab).
  const [pendingSwitch, setPendingSwitch] = useState<string>();
  const { preferences, changePreference } = usePreferences();
  // Icon-only mode is a desktop preference; the narrow-screen drawer always shows labels.
  const compact = preferences.sidebar === 'compact' && !narrow;
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [module, setModule] = useState<string | undefined>();
  const [provider, setProvider] = useState('');
  const [type, setType] = useState('');
  const [mode, setMode] = useState('');
  const [sort, setSort] = useState<'asc' | 'desc'>('asc');
  const [page, setPage] = useState(0);
  // A deep link (for example from a comparison) may name the resource to inspect.
  const [selected, setSelected] = useState<string | undefined>(() => scoped ? takeLinkedResource() : undefined);
  const [treeVisible, setTreeVisible] = useState(true);
  const [notice, setNotice] = useState('');
  const [planId, setPlanId] = useState<string>();
  // Managed instances chosen for a replacement plan, kept across pages and filters.
  const [replaceSelection, setReplaceSelection] = useState<ReplaceTarget[]>([]);
  const [pendingReplace, setPendingReplace] = useState<ReplaceTarget[]>();
  // Set when Drift asks for a separate normal plan: Plans opens its preflight once.
  const [planRequested, setPlanRequested] = useState(false);
  // Set by "Inspect impact" in a replace plan: Graph overlays that plan and selects the address.
  const [graphFocus, setGraphFocus] = useState<{ planId: string; address: string }>();
  // Set by "Prepare import" in Cloud discovery: the import wizard opens with it.
  const [importPrefill, setImportPrefill] = useState<DiscoveryPrefill>();
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLElement>(null);
  // A Hub workspace is opened explicitly (it runs CLI reads); "Read again" opens a fresh one.
  const [generation, setGeneration] = useState(0);
  const { projects, project, launchProject, profileProject, profile, opened, readScope, workspace, session, hubExecution, planScope, operations } = useWorkbenchContext(route, generation);
  const w = workspace.data;
  const noLaunch = !scoped && w?.diagnostic?.code === 'no_launch_project';
  // Resource IDs are derived from addresses, so they also match the same address in another
  // context of this workspace (another profile): a replacement selection never outlives its context.
  const contextId = opened.data?.id;
  const [selectionContext, setSelectionContext] = useState(contextId);
  if (selectionContext !== contextId) {
    setSelectionContext(contextId);
    setReplaceSelection([]);
    setPendingReplace(undefined);
  }
  // Git routes of this tab's project: the launch project's at the API root, a Hub project's by ID.
  const gitBase = scoped ? gitPrefix(scoped.projectId) : w && !noLaunch ? '' : undefined;
  const closed = workspace.error instanceof ApiError && workspace.error.code === 'context_closed';
  // Navigation context: which project the sidebar shows and what its items may open.
  const pageProject = route.kind === 'project' ? projects.data?.projects?.find(p => p.id === route.projectId) : undefined;
  const pageProjectWorkspace = pageProject?.status === 'ready' ? pageProject.lastWorkspace || pageProject.inspection?.cliDefault : undefined;
  const navMode: NavMode = scoped ? 'hub-workspace' : route.kind === 'project' ? 'hub-project' : noLaunch ? 'none' : 'launch';
  const navContext: NavContext = { route, mode: navMode, view, projectWorkspace: pageProjectWorkspace, scopedExecution: hubExecution };
  const sidebarProject: SidebarProject = scoped ? project ? { name: project.alias, detail: 'Hub project', status: 'ready' } : projects.isPending ? { name: opened.data?.alias || 'Hub project', detail: 'Loading…', status: 'loading' } : { name: opened.data?.alias || 'Unknown project', detail: 'Not found in the Hub', status: 'missing' } : route.kind === 'project' ? pageProject ? { name: pageProject.alias, detail: 'Choose a workspace', status: 'ready' } : projects.isPending ? { name: 'Hub project', detail: 'Loading…', status: 'loading' } : { name: 'Unknown project', detail: 'Not found in the Hub', status: 'missing' } : noLaunch ? { name: 'No project open', detail: 'Choose one in Projects', status: 'none' } : w ? { name: w.info.project || 'Local workspace', detail: 'Opened from terminal', status: 'ready' } : { name: 'Local workspace', detail: 'Opened from terminal', status: workspace.isPending ? 'loading' : 'ready' };
  const registry = projects.data?.projects || [];
  const launchRoot = projects.data?.launchRoot;
  const currentHubId = scoped?.projectId || (route.kind === 'project' ? route.projectId : undefined);
  const switchTargets: SwitchTargets = {
    launch: launchRoot && route.kind !== 'launch' ? { key: 'launch', label: registry.find(p => p.launch)?.alias || launchRoot.split(/[\\/]/).filter(Boolean).at(-1) || launchRoot, detail: launchRoot, path: '/' } : undefined,
    recent: registry.filter(p => p.status === 'ready' && p.lastOpenedAt && p.id !== currentHubId).sort((a, b) => (b.lastOpenedAt || '').localeCompare(a.lastOpenedAt || '')).slice(0, 5).map((p): SwitchTarget => ({ key: p.id, label: p.alias, detail: p.lastWorkspace ? `Workspace ${p.lastWorkspace}` : 'Choose a workspace', path: p.lastWorkspace ? contextPath(p.id, p.lastWorkspace) : projectPath(p.id) })),
    projectPage: currentHubId ? { key: 'page', label: scoped ? 'Workspaces of this project' : 'This project', detail: 'List, create or open workspaces', path: projectPath(currentHubId), current: route.kind === 'project' } : undefined
  };
  // Work kept only in this tab's workbench, lost when another workbench takes over the tab.
  const unsaved = [
    ...(replaceSelection.length ? [`${replaceSelection.length} instance${replaceSelection.length === 1 ? '' : 's'} selected for replacement`] : []),
    ...(pendingReplace ? ['A replacement plan that was not started'] : []),
    ...(importPrefill ? ['An import prepared from cloud discovery'] : [])
  ];
  function requestSwitch(path: string) {
    setNavOpen(false);
    if (unsaved.length && workbenchKey(parseRoute(path)) !== workbenchKey(route)) setPendingSwitch(path);
    else navigate(path);
  }
  function go(item: NavItem) {
    setNavOpen(false);
    const target = item.target;
    if (target.kind === 'route') {
      navigate(target.route === 'compare' ? comparePath() : historyPath());
      return;
    }
    const next = item.id === 'import-discovery' ? importTab : target.view;
    // A Hub project page opens the chosen view in the workspace last used there.
    if (route.kind === 'project' && next !== 'Settings' && pageProjectWorkspace) {
      requestSwitch(contextPath(route.projectId, pageProjectWorkspace, isExplorerView(next) ? next : 'Resources'));
      return;
    }
    setView(next);
  }
  useEffect(() => {
    if (!navOpen) return;
    document.querySelector<HTMLElement>('#app-sidebar .sidebar-close')?.focus();
    function key(event: globalThis.KeyboardEvent) {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      setNavOpen(false);
      menuButton.current?.focus();
    }
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [navOpen]);
  function setView(next: View) {
    setViewState(next);
    if (next !== 'Plans') setPlanRequested(false);
    if (next === 'Import' || next === 'Discovery') setImportTab(next);
    if (scoped && isExplorerView(next)) navigate(contextPath(scoped.projectId, scoped.workspace, next));
    else if (onHub) navigate('/');
  }
  useEffect(() => {
    // Started from the Hub, there is no launch project to show: go to the Hub instead.
    if (noLaunch && route.kind === 'launch' && view !== 'Settings') navigate('/projects', true);
  }, [noLaunch, route.kind, view]);
  const reread = () => setGeneration(g => g + 1);
  // Project tools run in a Hub workspace context. For the project opened from the terminal,
  // Tools opens its Hub entry in the workspace this view reads; unsaved projects go to the
  // Hub, which offers Save to Hub.
  const launchToolsWorkspace = w?.info.workspace || launchProject?.inspection?.cliDefault || 'default';
  function openLaunchTools() {
    if (launchProject) {
      requestSwitch(contextPath(launchProject.id, launchToolsWorkspace, 'Tools'));
      return;
    }
    setNotice('Save this project to the Hub to run project tools: use Save to Hub below.');
    navigate('/projects');
  }
  // The project opened from the terminal is inspected and read again on the server, as at
  // startup, for example after it was initialized from the Hub. No init, plan or refresh runs.
  const [rereading, setRereading] = useState(false);
  async function rereadLaunch() {
    setRereading(true);
    try {
      await post('workspace/reload', {});
      await workspace.refetch();
    } catch (e) {
      setNotice(e instanceof Error ? e.message : 'Unable to read the project again.');
    } finally {
      setRereading(false);
    }
  }
  const runningOperation = operations.data?.operations?.find(o => o.status === 'running');
  const ready = w?.status === 'ready' || w?.status === 'empty';
  const parameters = new URLSearchParams({
    query,
    provider,
    type,
    mode,
    sort,
    offset: String(page * 50),
    limit: '50'
  });
  if (module !== undefined) parameters.set('module', module);
  const resources = useQuery({
    queryKey: ['resources', readScope ? scopeKey(readScope) : 'none', parameters.toString()],
    queryFn: () => api<ResourcePage>(scopedPath(readScope!, `resources?${parameters}`)),
    enabled: ready && !!readScope
  });
  const detail = useQuery({
    queryKey: ['resource', readScope ? scopeKey(readScope) : 'none', selected],
    queryFn: () => api<Resource>(scopedPath(readScope!, `resources/${selected}`)),
    enabled: !!selected && !!readScope
  });
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setQuery(search);
      setPage(0);
    }, 180);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    if (onHub) return;
    function keyboard(event: globalThis.KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setView('Resources');
        requestAnimationFrame(() => searchRef.current?.focus());
      }
      if (event.key === 'Escape') {
        setSelected(undefined);
        setNotice('');
      }
    }
    window.addEventListener('keydown', keyboard);
    return () => window.removeEventListener('keydown', keyboard);
  }, [onHub]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (listRef.current) listRef.current.scrollTop = 0;
  }, [page, query, provider, type, mode, module, sort]);
  function chooseModule(value: string | undefined) {
    setModule(value);
    setPage(0);
    setView('Resources');
  }
  function resetFilters() {
    setSearch('');
    setQuery('');
    setModule(undefined);
    setProvider('');
    setType('');
    setMode('');
    setPage(0);
  }
  async function copy(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      setNotice(`${label} copied`);
    } catch {
      setNotice('Clipboard is unavailable. Select and copy the text manually.');
    }
  }
  const total = w ? w.managed + w.data : 0;
  const maxReplace = 50;
  function toggleReplace(resource: Resource) {
    setReplaceSelection(current => current.some(r => r.id === resource.id) ? current.filter(r => r.id !== resource.id) : current.length >= maxReplace ? current : [...current, { id: resource.id, address: resource.address }]);
  }
  const filtered = !!(query || module !== undefined || provider || type || mode);
  const workspaceControl = scoped ? <div className="workspace-control"><Layers3 size={13} aria-hidden="true" /><label htmlFor="workspace-switch">Workspace</label><select id="workspace-switch" className="workspace-switch" value={scoped.workspace} title="Terraform/OpenTofu workspace this tab reads. Switching runs nothing and never selects a workspace in the CLI." onChange={e => requestSwitch(contextPath(scoped.projectId, e.target.value, isExplorerView(view) ? view : 'Resources'))}>{Array.from(new Set([scoped.workspace, ...(project?.inspection?.workspaces || [])])).map(ws => <option key={ws} value={ws}>{ws}{ws === project?.inspection?.cliDefault ? ' (CLI default)' : ''}</option>)}</select></div> : navMode === 'launch' && !onHub ? <div className="workspace-control" title="Selected in this folder's CLI data directory. Hclaro reads it and never changes it. Save the project to the Hub to read other workspaces in their own tabs."><Layers3 size={13} aria-hidden="true" /><span>Workspace</span><strong>{w?.info.workspace || '—'}</strong><span className="hub-tag hub-tag-quiet">CLI</span></div> : undefined;
  const trail = viewTrail(view).map((label, i, all) => <span className="crumb-step" key={label}><ChevronRight size={13} />{i === all.length - 1 ? <strong>{label}</strong> : <span>{label}</span>}</span>);
  return <div className={`app density-${preferences.density} ${compact ? 'sidebar-compact' : ''} ${navOpen ? 'nav-open' : ''}`}>
  <a className="skip-link" href="#main-content">Skip to workspace</a>
  <Sidebar id="app-sidebar" context={navContext} compact={compact} onToggleCompact={() => changePreference({ sidebar: compact ? 'expanded' : 'compact' })} onCloseDrawer={() => {
      setNavOpen(false);
      menuButton.current?.focus();
    }} project={sidebarProject} targets={switchTargets} workspace={workspaceControl} resourceCount={ready && !onHub ? total : undefined} operationRunning={!!runningOperation} onSelect={go} onHome={() => {
      setNavOpen(false);
      setView('Overview');
    }} onSwitch={requestSwitch} />
  {navOpen && <div className="sidebar-backdrop" aria-hidden="true" onClick={() => setNavOpen(false)} />}
  <div className="workspace-shell">
   <header className="topbar"><Button ref={menuButton} variant="ghost" size="icon" className="nav-toggle" aria-label="Open navigation" aria-expanded={navOpen} aria-controls="app-sidebar" onClick={() => setNavOpen(true)}><Menu size={17} /></Button><nav className="breadcrumbs" aria-label="Breadcrumb"><Folder size={15} />{historyPage ? <strong>Operation history</strong> : comparing ? <><button className="crumb" onClick={() => navigate('/projects')}>Projects</button><ChevronRight size={13} /><strong>Compare workspaces</strong></> : onHub || scoped ? <><button className="crumb" onClick={() => navigate('/projects')}>Projects</button>{route.kind !== 'hub' && <><ChevronRight size={13} />{scoped ? <button className="crumb" onClick={() => navigate(projectPath(scoped.projectId))}>{project?.alias || opened.data?.alias || 'Project'}</button> : <strong>{projects.data?.projects?.find(p => p.id === (route.kind === 'project' ? route.projectId : ''))?.alias || 'Project'}</strong>}</>}{scoped && <><ChevronRight size={13} /><span>{scoped.workspace}</span>{trail}</>}</> : <><span>{w?.info.project || 'Workspace'}</span>{trail}</>}</nav><div className="topbar-actions">{!onHub && !noLaunch && <Button variant="ghost" size="small" aria-label="Find a resource" onClick={() => {
            setView('Resources');
            requestAnimationFrame(() => searchRef.current?.focus());
          }}><Search size={14} /><span className="hide-small">Search</span></Button>}<span className="session-label"><LockKeyhole size={12} />Private local session</span><Button variant="ghost" size="icon" aria-label="Toggle light and dark theme" onClick={() => changePreference({
            theme: document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'
          })}><Sun className="theme-to-light" size={17} /><Moon className="theme-to-dark" size={17} /></Button></div></header>
   {!onHub && !noLaunch && <div className="context-bar"><div className="context-item"><span className="engine-glyph">{w?.info.engine === 'tofu' ? 'O' : 'T'}</span><strong>{w?.info.engine === 'tofu' ? 'OpenTofu' : w?.info.engine === 'terraform' ? 'Terraform' : 'Engine'}</strong><span className="mono">{w?.info.version || '—'}</span></div><span className="context-divider" />{scoped ? <div className="context-item"><Layers3 size={13} /><span>Workspace</span><strong>{scoped.workspace}</strong>{productionLike(scoped.workspace, project?.alias) && <ProductionBadge />}</div> : <div className="context-item"><Layers3 size={13} /><span>Workspace</span><strong>{w?.info.workspace || 'unavailable'}</strong></div>}{profileProject ? <><span className="context-divider" /><ContextSelector projectId={profileProject} selection={profile} workspaces={scoped ? project?.inspection?.workspaces : undefined} onChosen={p => {
          // A profile that selects a workspace opens that workspace in this tab.
          if (scoped && p?.workspace && p.workspace !== scoped.workspace) navigate(contextPath(scoped.projectId, p.workspace, isExplorerView(view) ? view : 'Resources'));
        }} /></> : !scoped && w && <><span className="context-divider" /><div className="context-item muted" title="Execution profiles belong to Hub projects. Save this project to the Hub to use them.">Profile: none</div></>}<span className="context-divider" /><div className="context-item" title={w?.info.executionMode ? `Execution mode: ${w.info.executionMode}` : undefined}><Database size={13} /><span>Backend</span><strong>{w?.info.backend || 'unknown'}</strong>{w?.info.executionMode && <span className="muted">{w.info.executionMode}</span>}<span className={`dot ${ready ? '' : 'dot-muted'}`} /></div>{scoped && w && <span className="hub-tag hub-tag-quiet" title={hubExecution ? w.info.capabilities.canPlan ? 'Plans and applies run in this workspace only, after review and confirmation.' : w.info.capabilities.planReason : 'This session was started without Hub execution.'}>{hubExecution && w.info.capabilities.canPlan ? 'Plan & apply' : 'Read-only'}</span>}{gitBase !== undefined && <><span className="context-divider" /><GitBadge prefix={gitBase} onOpen={() => setView('Git')} /></>}<span className="context-spacer" />{scoped && <><Button variant="ghost" size="small" onClick={reread} disabled={w?.status === 'loading'} title="Read this workspace's recorded state again. No refresh is run."><RefreshCcw size={12} />Read again</Button><Button variant="ghost" size="small" onClick={() => openInNewTab(contextPath(scoped.projectId, scoped.workspace, isExplorerView(view) ? view : 'Resources'))} title="Open this workspace in a new browser tab"><ExternalLink size={12} />New tab</Button></>}{!scoped && <Button variant="ghost" size="small" onClick={() => void rereadLaunch()} disabled={rereading || w?.status === 'loading' || !!runningOperation} title="Inspect this folder and read its recorded state again, as at startup. No init, plan or refresh is run."><RefreshCcw size={12} />Read again</Button>}{!scoped && runningOperation && <button className="context-running" onClick={() => setView(runningOperation.kind === 'drift' ? 'Drift' : runningOperation.kind === 'graph' ? 'Graph' : 'Plans')}><LoaderCircle className="spin" size={12} />{runningOperation.kind === 'apply' ? 'Apply' : runningOperation.kind === 'drift' ? 'Drift scan' : runningOperation.kind === 'graph' ? 'Graph' : runningOperation.import ? 'Import plan' : 'Plan'} running · {runningOperation.stage}</button>}<span className="snapshot-time">Snapshot {timeLabel(w?.capturedAt)}</span></div>}
   <main id="main-content" tabIndex={-1} className="main-content">
    <PageHeading route={route} view={view} />
    {!onHub && (!scoped || hubExecution) && (view === 'Import' || view === 'Discovery') && <ImportDiscoveryTabs view={view} onChange={setView} />}
    {historyPage ? <HistoryView copy={copy} /> : comparing ? <CompareView copy={copy} /> : route.kind === 'hub' ? <HubView notify={setNotice} /> : route.kind === 'project' ? <ProjectView id={route.projectId} /> : view === 'Settings' ? <SettingsPanel preferences={preferences} changePreference={changePreference} /> : scoped && (view === 'Plans' || view === 'Operations' || view === 'Import' || view === 'Discovery' || view === 'Drift' || view === 'Graph') ? !hubExecution ? session.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /></div> : <ErrorState title="Hub workspaces are read-only in this session" message="Plans and applies run only for the project opened from the terminal. Nothing ran." /> : opened.error ? <ErrorState title="Unable to open this workspace" message={opened.error.message} retry={() => {
          void opened.refetch();
        }} retryLabel="Try again" /> : workspace.error ? <ErrorState message={workspace.error.message} retry={closed ? reread : () => {
          void workspace.refetch();
        }} retryLabel={closed ? 'Open again' : undefined} /> : !opened.data || !planScope || !w || w.status === 'loading' ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Opening this workspace…</p></div> : view === 'Plans' ? <PlansView key={opened.data.id} scope={planScope} project={opened.data.alias} profileId={profile.selected} workspaceStatus={w?.status} planId={scoped.planId} choosePlan={id => navigate(contextPath(scoped.projectId, scoped.workspace, 'Plans', id))} replace={pendingReplace} clearReplace={() => setPendingReplace(undefined)} reopen={reread} copy={copy} /> : view === 'Import' ? <ImportView key={`${opened.data.id}:${importPrefill?.token ?? 'manual'}`} scope={planScope} reopen={reread} workspaceStatus={w?.status} copy={copy} prefill={importPrefill} clearPrefill={() => setImportPrefill(undefined)} onStarted={() => navigate(contextPath(scoped.projectId, scoped.workspace, 'Plans'))} /> : view === 'Discovery' ? <DiscoveryView key={opened.data.id} scope={planScope} reopen={reread} workspaceStatus={w?.status} copy={copy} openResource={id => {
          setSelected(id);
          setView('Resources');
        }} onPrepared={prefill => {
          setImportPrefill(prefill);
          setView('Import');
        }} /> : view === 'Drift' ? <DriftView key={opened.data.id} scope={planScope} reopen={reread} project={opened.data.alias} workspaceStatus={w?.status} copy={copy} openResource={id => {
          setSelected(id);
          setView('Resources');
        }} runPlan={() => setView('Plans')} /> : view === 'Graph' ? <GraphView key={opened.data.id} scope={planScope} reopen={reread} project={opened.data.alias} workspaceStatus={w?.status} copy={copy} openResource={id => {
          setSelected(id);
          setView('Resources');
        }} openPlan={id => navigate(contextPath(scoped.projectId, scoped.workspace, 'Plans', id))} /> : <OperationsView key={opened.data.id} scope={planScope} openPlan={id => navigate(contextPath(scoped.projectId, scoped.workspace, 'Plans', id))} /> : view === 'Plans' ? <PlansView project={w?.info.project || 'this project'} profileId={profile.selected} workspaceStatus={w?.status} planId={planId} choosePlan={setPlanId} replace={pendingReplace} clearReplace={() => setPendingReplace(undefined)} openPreflight={planRequested} preflightOpened={() => setPlanRequested(false)} copy={copy} inspectImpact={(id, address) => {
          setGraphFocus({ planId: id, address });
          setView('Graph');
        }} /> : view === 'Graph' ? <GraphView key={graphFocus ? `${graphFocus.planId}:${graphFocus.address}` : 'graph'} project={w?.info.project || 'this project'} profileId={profile.selected} workspaceStatus={w?.status} focus={graphFocus} copy={copy} openResource={id => {
          setSelected(id);
          setView('Resources');
        }} openPlan={id => {
          setPlanId(id);
          setView('Plans');
        }} /> : view === 'Drift' ? <DriftView project={w?.info.project || 'this project'} profileId={profile.selected} workspaceStatus={w?.status} copy={copy} openResource={id => {
          setSelected(id);
          setView('Resources');
        }} runPlan={() => {
          setPlanRequested(true);
          setView('Plans');
        }} /> : view === 'Discovery' ? <DiscoveryView profileId={profile.selected} workspaceStatus={w?.status} copy={copy} openResource={id => {
          setSelected(id);
          setView('Resources');
        }} onPrepared={prefill => {
          setImportPrefill(prefill);
          setView('Import');
        }} /> : view === 'Import' ? <ImportView key={importPrefill?.token ?? 'manual'} workspaceStatus={w?.status} profileId={profile.selected} copy={copy} prefill={importPrefill} clearPrefill={() => setImportPrefill(undefined)} onStarted={() => {
          setPlanId(undefined);
          setView('Plans');
          void operations.refetch();
        }} /> : view === 'Operations' ? <OperationsView openPlan={id => {
          setPlanId(id);
          setView('Plans');
        }} /> : view === 'Git' && gitBase !== undefined ? <GitView projectId={scoped?.projectId} editor={preferences.editor} notify={setNotice} /> : view === 'Tools' && !scoped ? projects.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Looking up this folder in the Hub…</p></div> : <LaunchTools workspace={launchToolsWorkspace} registered={!!launchProject} onOpen={openLaunchTools} onOpenNewTab={() => launchProject && openInNewTab(contextPath(launchProject.id, launchToolsWorkspace, 'Tools'))} onSave={openLaunchTools} /> : view === 'Tools' && scoped ? opened.data ? <ToolsView context={opened.data} /> : opened.error ? <ErrorState title="Unable to open this workspace" message={opened.error.message} retry={() => {
          void opened.refetch();
        }} retryLabel="Try again" /> : <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Opening this workspace…</p></div> : opened.error ? <ErrorState title="Unable to open this workspace" message={opened.error.message} retry={() => {
          void opened.refetch();
        }} retryLabel="Try again" /> : opened.isPending && scoped || workspace.isPending || w?.status === 'loading' ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><h2>Reading your workspace</h2><p>{scoped ? <>Reading the recorded state of <strong>{scoped.workspace}</strong> through the local CLI with a per-command <code>TF_WORKSPACE</code>. No plan, refresh or workspace selection is run.</> : 'Inspecting recorded state through the local CLI. No plan or refresh is run.'}</p></div> : workspace.error ? <ErrorState message={workspace.error.message} retry={closed ? reread : () => {
          void workspace.refetch();
        }} retryLabel={closed ? 'Open again' : undefined} /> : w?.diagnostic ? <ErrorState title={w.diagnostic.code === 'uninitialized' ? 'Initialize this workspace first' : w.diagnostic.code === 'missing_cli' ? 'An IaC engine is needed' : 'Workspace needs attention'} message={w.diagnostic.message} retry={scoped ? reread : noLaunch ? undefined : () => void rereadLaunch()} retryLabel="Read again" /> : w && <>
     {view === 'Resources' && <>
      <div className="explorer-toolbar"><div className="search-field"><Search size={16} /><input ref={searchRef} value={search} onChange={e => setSearch(e.target.value)} placeholder="Search resources, addresses, providers…" aria-label="Search resources" /><kbd><Command size={10} />K</kbd>{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch('')}><X size={13} /></button>}</div><Button variant="outline" aria-label="Toggle module tree" aria-pressed={treeVisible} onClick={() => setTreeVisible(!treeVisible)}><FolderTree size={15} /><span className="hide-small">Modules</span></Button><Button variant="outline" aria-label={sort === 'asc' ? 'Sort descending' : 'Sort ascending'} onClick={() => {
                setSort(sort === 'asc' ? 'desc' : 'asc');
                setPage(0);
              }}><ArrowDownAZ size={15} /><span className="hide-small">{sort === 'asc' ? 'A–Z' : 'Z–A'}</span></Button></div>
      <div className="filter-row"><SlidersHorizontal size={13} /><select aria-label="Filter provider" value={provider} onChange={e => {
                setProvider(e.target.value);
                setPage(0);
              }}><option value="">All providers</option>{Object.keys(w.providers).sort().map(p => <option key={p} value={p}>{providerName(p)}</option>)}</select><select aria-label="Filter resource type" value={type} onChange={e => {
                setType(e.target.value);
                setPage(0);
              }}><option value="">All resource types</option>{Object.keys(w.types).sort().map(t => <option key={t}>{t}</option>)}</select><select aria-label="Filter resource mode" value={mode} onChange={e => {
                setMode(e.target.value);
                setPage(0);
              }}><option value="">Managed & data</option><option value="managed">Managed resources</option><option value="data">Data sources</option></select>{filtered && <Button variant="ghost" size="small" onClick={resetFilters}><X size={12} />Reset</Button>}<span className="filter-count">{resources.data?.total ?? '…'} resources</span></div>
      <div className={`explorer ${selected ? 'with-inspector' : ''}`}>
       {treeVisible && preferences.grouping === 'module' && <aside className="module-panel" aria-label="Module navigation"><div className="panel-label">MODULE TREE <span>{Math.max(w.modules.length - 1, 0)}</span></div><button className={`all-resources ${module === undefined ? 'chosen' : ''}`} onClick={() => chooseModule(undefined)}><Layers3 size={14} /><span>All resources</span><span className="tree-count">{total}</span></button><ModuleTree modules={w.modules} selected={module} choose={chooseModule} /><div className="tree-hint"><CircleHelp size={13} /><span>Modules group resource instances from your recorded state.</span></div></aside>}
       <section ref={listRef} className="resource-list" aria-label="Resources"><div className="list-caption"><span>{module === undefined ? 'All resources' : moduleName(module)}</span><span>RECORDED STATE</span></div>
        {resources.isPending ? <div role="status" className="empty-state"><LoaderCircle className="spin" /><p>Loading resources…</p></div> : resources.error ? <ErrorState message={resources.error.message} retry={() => {
                  void resources.refetch();
                }} /> : resources.data?.total === 0 ? <div className="empty-state"><Box size={28} /><h2>{filtered ? 'No matching resources' : 'A clean slate'}</h2><p>{filtered ? 'Try a different search or clear your filters.' : 'The state contains no resource instances. Hclaro reads state without creating resources.'}</p>{filtered && <Button variant="outline" onClick={resetFilters}>Clear filters</Button>}</div> : <>{replaceSelection.length > 0 && <div className="selection-bar" role="region" aria-label="Replacement selection"><RefreshCcw size={14} /><span><strong>{replaceSelection.length}</strong> instance{replaceSelection.length === 1 ? '' : 's'} selected for replacement{replaceSelection.length >= maxReplace && ` (maximum ${maxReplace})`}</span><Button size="small" disabled={!w.info.capabilities.canPlan || !!runningOperation} title={w.info.capabilities.canPlan ? undefined : w.info.capabilities.planReason} onClick={() => {
                  setPendingReplace(replaceSelection);
                  setView('Plans');
                }}>Plan replacement…</Button><Button variant="ghost" size="small" onClick={() => setReplaceSelection([])}><X size={12} />Clear</Button></div>}<table className="resources-table"><thead><tr><th scope="col" className="select-column"><span className="sr-only">Select for replacement</span></th><th scope="col">Resource</th><th scope="col" className="provider-column">Provider</th><th scope="col">Mode</th></tr></thead><tbody>{resources.data?.resources.map(r => <ResourceRow key={r.id} resource={r} selected={selected === r.id} onSelect={() => setSelected(r.id)} selectable={!scoped || hubExecution} chosen={replaceSelection.some(c => c.id === r.id)} onChoose={() => toggleReplace(r)} />)}</tbody></table><div className="pagination"><span>{page * 50 + 1}–{Math.min((page + 1) * 50, resources.data?.total || 0)} of {resources.data?.total}</span><div><Button variant="ghost" size="icon" aria-label="Previous page" disabled={page === 0} onClick={() => setPage(page - 1)}><ArrowLeft size={14} /></Button><span>{page + 1} / {Math.ceil((resources.data?.total || 1) / 50)}</span><Button variant="ghost" size="icon" aria-label="Next page" disabled={(page + 1) * 50 >= (resources.data?.total || 0)} onClick={() => setPage(page + 1)}><ArrowRight size={14} /></Button></div></div></>}
       </section>
       {selected && <Inspector resource={detail.data} loading={detail.isPending} error={detail.error?.message} close={() => setSelected(undefined)} copy={copy} source={gitBase !== undefined && readScope && detail.data ? { path: scopedPath(readScope, `resources/${detail.data.id}/source`), prefix: gitBase } : undefined} editor={preferences.editor} notify={setNotice} />}
      </div>
     </>}
     {view === 'Overview' && <Overview workspace={w} browse={() => setView('Resources')} chooseType={t => {
            resetFilters();
            setType(t);
            setView('Resources');
          }} />}
     {view === 'Modules' && <div className="directory-grid">{w.modules.map(m => <button className="directory-card" key={m.address} onClick={() => chooseModule(m.address)}><span className="directory-icon"><FolderTree size={21} /></span><h2>{moduleName(m.address)}</h2><p className="mono">{m.address || 'root'}</p><div><span>{m.count} direct resource{m.count === 1 ? '' : 's'}</span><ArrowRight size={15} /></div></button>)}</div>}
     {view === 'Providers' && <div className="provider-directory">{Object.entries(w.providers).sort().map(([p, count]) => <button key={p} className="provider-card" onClick={() => {
              resetFilters();
              setProvider(p);
              setView('Resources');
            }}><span className="provider-icon"><Network size={23} /></span><div><h2>{providerName(p)}</h2><p className="mono">{p || 'Provider not recorded'}</p></div><span>{count} resource{count === 1 ? '' : 's'}</span><ChevronRight size={17} /></button>)}<div className="info-note"><CircleHelp size={15} /><span>These are provider sources in state. Provider aliases and installed plugin versions are not present in the resource JSON.</span></div></div>}
     
    </>}
   </main>
   <footer className="statusbar"><span><ShieldCheck size={12} />{!onHub && w?.info.capabilities.canApplySavedPlan ? 'Changes only from a reviewed, confirmed plan' : 'Read-only · No infrastructure changes'}</span><span>{onHub ? 'Workspace Hub' : ready ? `${total} instances in state` : 'Waiting for workspace'}<span className="footer-divider">/</span>127.0.0.1</span></footer>
  </div>
  {pendingSwitch && <ConfirmSwitch items={unsaved} onCancel={() => setPendingSwitch(undefined)} onConfirm={() => {
      const path = pendingSwitch;
      setPendingSwitch(undefined);
      navigate(path);
    }} />}
  {notice && <div role="status" className="toast"><Check size={15} />{notice}<button aria-label="Dismiss notification" onClick={() => setNotice('')}><X size={13} /></button></div>}
 </div>;
}
