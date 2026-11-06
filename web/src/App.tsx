import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowDownAZ, Import, ArrowLeft, ArrowRight, Box, RefreshCcw, ScanSearch, Braces, Check, ChevronDown, ChevronRight, CircleHelp, Command, Copy, Database, ExternalLink, FileCode2, Folder, FolderKanban, FolderTree, GitCompareArrows, History, Layers3, LayoutDashboard, LoaderCircle, LockKeyhole, Monitor, Moon, Network, Search, Settings2, ShieldCheck, SlidersHorizontal, Sun, Terminal, Waypoints, Workflow, Wrench, X } from 'lucide-react';
import { api, ApiError, openInNewTab, post } from './api';
import type { ContextInfo, Module, Operation, ReplaceTarget, Resource, ResourcePage, Workspace } from './types';
import { comparePath, historyPath, contextPath, isExplorerView, navigate, productionLike, projectPath, takeLinkedResource, useRoute, type Route } from './router';
import { HubView, ProductionBadge, ProjectView, projectsQuery } from './hub/HubView';
import { Button } from './components/ui/button';
import { Drawer } from './components/Drawer';
import { JsonTree } from './components/JsonTree';
import { PlansView } from './plans/PlansView';
import { OperationsView } from './plans/OperationsView';
import { ImportView } from './imports/ImportView';
import { DriftView } from './drift/DriftView';
import { GraphView } from './graph/GraphView';
import { CompareView } from './compare/CompareView';
import { HistoryView } from './history/HistoryView';
import { ToolsView } from './hub/ToolsView';
import { ContextSelector } from './profiles/ContextSelector';
import { useProfileSelection } from './profiles/selection';
type View = 'Overview' | 'Resources' | 'Modules' | 'Providers' | 'Tools' | 'Settings' | 'Plans' | 'Drift' | 'Graph' | 'Import' | 'Operations';
type Preferences = {
  theme: 'system' | 'light' | 'dark';
  density: 'comfortable' | 'compact';
  grouping: 'module' | 'flat';
};
const defaults: Preferences = {
  theme: 'system',
  density: 'comfortable',
  grouping: 'module'
};
function readPreferences(): Preferences {
  try {
    const saved = JSON.parse(localStorage.getItem('hclaro.preferences') || '{}');
    return {
      theme: ['system', 'light', 'dark'].includes(saved.theme) ? saved.theme : defaults.theme,
      density: saved.density === 'compact' ? 'compact' : 'comfortable',
      grouping: saved.grouping === 'flat' ? 'flat' : 'module'
    };
  } catch {
    return defaults;
  }
}
function providerName(name: string) {
  return name.split('/').at(-1) || 'unknown';
}
function moduleName(name: string) {
  return name ? name.replace(/^module\./, '') : 'Root module';
}
function timeLabel(time?: string) {
  return time ? new Date(time).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit'
  }) : '—';
}
const navigation = [{
  name: 'Overview',
  icon: LayoutDashboard
}, {
  name: 'Resources',
  icon: Layers3
}, {
  name: 'Modules',
  icon: FolderTree
}, {
  name: 'Providers',
  icon: Network
}, {
  name: 'Tools',
  icon: Wrench
}, {
  name: 'Settings',
  icon: Settings2
}] as const;
// Each project workspace gets its own Workbench instance (keyed below), so filters,
// selections and cached queries can never carry over from another context.
export default function App() {
  const route = useRoute();
  return <Workbench key={route.kind === 'context' ? `${route.projectId}/${route.workspace}` : 'launch'} route={route} />;
}
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
  const [preferences, setPreferences] = useState(readPreferences);
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
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLElement>(null);
  // A Hub workspace is opened explicitly (it runs CLI reads); "Read again" opens a fresh one.
  const [generation, setGeneration] = useState(0);
  // The launch view also reads the registry (no engine command) to find its own Hub entry.
  const projects = useQuery({
    ...projectsQuery,
    enabled: !!scoped || route.kind === 'launch'
  });
  const project = projects.data?.projects?.find(p => p.id === scoped?.projectId);
  const launchProject = !scoped ? projects.data?.projects?.find(p => p.launch) : undefined;
  // Execution profiles belong to registered projects. The choice is kept per browser tab.
  const profileProject = scoped ? scoped.projectId : launchProject?.id;
  const profile = useProfileSelection(profileProject);
  const profileReady = !profileProject || profile.profiles.isFetched;
  const opened = useQuery({
    queryKey: ['context-open', scoped?.projectId, scoped?.workspace, profile.selected, generation],
    queryFn: () => post<ContextInfo>('contexts', {
      projectId: scoped!.projectId,
      workspace: scoped!.workspace,
      reload: generation > 0,
      ...(profile.selected ? { profileId: profile.selected } : {})
    }),
    enabled: !!scoped && profileReady
  });
  // Launch routes keep their original paths; a Hub context reads only through its own ID.
  const base = scoped ? opened.data ? `contexts/${opened.data.id}` : undefined : '';
  const workspace = useQuery({
    queryKey: ['workspace', base],
    queryFn: () => api<Workspace>(base || 'workspace'),
    enabled: base !== undefined,
    refetchInterval: q => q.state.data?.status === 'loading' ? 700 : false
  });
  const w = workspace.data;
  const noLaunch = !scoped && w?.diagnostic?.code === 'no_launch_project';
  const closed = workspace.error instanceof ApiError && workspace.error.code === 'context_closed';
  function setView(next: View) {
    setViewState(next);
    if (next !== 'Plans') setPlanRequested(false);
    if (scoped && isExplorerView(next)) navigate(contextPath(scoped.projectId, scoped.workspace, next), true);
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
      navigate(contextPath(launchProject.id, launchToolsWorkspace, 'Tools'));
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
  const operations = useQuery({
    queryKey: ['operations'],
    queryFn: () => api<{ operations: Operation[] }>('operations'),
    refetchInterval: q => q.state.data?.operations?.some(o => o.status === 'running') ? 2000 : false
  });
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
    queryKey: ['resources', base, parameters.toString()],
    queryFn: () => api<ResourcePage>(`${base ? `${base}/` : ''}resources?${parameters}`),
    enabled: ready && base !== undefined
  });
  const detail = useQuery({
    queryKey: ['resource', base, selected],
    queryFn: () => api<Resource>(`${base ? `${base}/` : ''}resources/${selected}`),
    enabled: !!selected && base !== undefined
  });
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setQuery(search);
      setPage(0);
    }, 180);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    try {
      localStorage.setItem('hclaro.preferences', JSON.stringify(preferences));
    } catch {/* Preferences are optional. */}
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const apply = () => {
      document.documentElement.dataset.theme = preferences.theme === 'system' ? media.matches ? 'dark' : 'light' : preferences.theme;
    };
    apply();
    media.addEventListener('change', apply);
    return () => media.removeEventListener('change', apply);
  }, [preferences]);
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
  const changePreference = (change: Partial<Preferences>) => setPreferences(p => ({
    ...p,
    ...change
  }));
  const total = w ? w.managed + w.data : 0;
  const maxReplace = 50;
  function toggleReplace(resource: Resource) {
    setReplaceSelection(current => current.some(r => r.id === resource.id) ? current.filter(r => r.id !== resource.id) : current.length >= maxReplace ? current : [...current, { id: resource.id, address: resource.address }]);
  }
  const filtered = !!(query || module !== undefined || provider || type || mode);
  return <div className={`app density-${preferences.density}`}>
  <a className="skip-link" href="#main-content">Skip to workspace</a>
  <aside className="sidebar" aria-label="Application navigation">
   <a className="brand" href="#" onClick={e => {
        e.preventDefault();
        setView('Overview');
      }} aria-label="Hclaro overview"><span className="brand-mark"><span /><span /><span /></span><span>hclaro<span className="alpha">ALPHA</span></span></a>
   <button className={`nav-item ${onHub && !comparing && !historyPage ? 'active' : ''}`} aria-label="Projects" aria-current={route.kind === 'hub' ? 'page' : undefined} onClick={() => navigate('/projects')}><FolderKanban size={17} /><span>Projects</span></button>
   <button className={`nav-item ${comparing ? 'active' : ''}`} aria-label="Compare workspaces" aria-current={comparing ? 'page' : undefined} onClick={() => navigate(comparePath())}><GitCompareArrows size={17} /><span>Compare</span></button>
   <button className={`nav-item ${historyPage ? 'active' : ''}`} aria-label="Operation history" aria-current={historyPage ? 'page' : undefined} onClick={() => navigate(historyPath())}><History size={17} /><span>History</span></button>
   <div className="project-tile"><span className="project-icon"><FileCode2 size={19} /></span><div><strong>{scoped ? project?.alias || opened.data?.alias || 'Hub project' : noLaunch ? 'No project open' : w?.info.project || 'Local workspace'}</strong><span>{scoped ? `Hub workspace · ${scoped.workspace}` : noLaunch ? 'Choose one in Projects' : 'Opened from terminal'}</span></div></div>
   <div className="nav-label">WORKSPACE</div>
   <nav>{navigation.map(({
          name,
          icon: Icon
        }) => {
          // Project tools run in a Hub workspace context only.
          const unavailable = noLaunch && name !== 'Settings';
          const launchTools = name === 'Tools' && !scoped;
          const active = !onHub && view === name;
          return <button key={name} aria-label={name} disabled={unavailable} title={unavailable ? 'Open a project from Projects first.' : launchTools ? launchProject ? `Opens project tools for this folder in a Hub tab, workspace ${launchToolsWorkspace}.` : 'Project tools run in Hub workspaces. Save this project to the Hub first.' : undefined} className={`nav-item ${active ? 'active' : ''}`} aria-current={active ? 'page' : undefined} onClick={() => launchTools ? openLaunchTools() : setView(name)}><Icon size={17} /><span>{name}</span>{name === 'Resources' && ready && <span className="nav-count">{total}</span>}</button>;
        })}</nav>
   <div className="nav-label future-label">WORKFLOWS</div>
   {([['Plans', Workflow], ['Drift', ScanSearch], ['Graph', Waypoints], ['Import', Import], ['Operations', Terminal]] as const).map(([name, Icon]) => {
        // Plans, applies and imports belong to the project Hclaro was launched with.
        const unavailable = !!scoped || noLaunch;
        const active = !onHub && view === name;
        return <button key={name} aria-label={name} disabled={unavailable} title={scoped ? 'Hub workspaces are read-only. Plans, drift scans, dependency graphs, applies and imports run only for the project opened from the terminal.' : noLaunch ? 'Available for a project opened from the terminal.' : undefined} className={`nav-item ${active ? 'active' : ''}`} aria-current={active ? 'page' : undefined} onClick={() => setView(name)}><Icon size={17} /><span>{name}</span>{name === 'Operations' && runningOperation && <span className="nav-count running-dot" aria-label="Operation running"><LoaderCircle className="spin" size={11} /></span>}</button>;
      })}
   <div className="sidebar-bottom"><div className="local-note"><ShieldCheck size={18} /><div><strong>Local by design</strong><span>Your state stays on your machine.</span></div></div><div className="sidebar-foot"><span><span className="dot" />Alpha</span><span>v0.1</span></div></div>
  </aside>
  <div className="workspace-shell">
   <header className="topbar"><nav className="breadcrumbs" aria-label="Breadcrumb"><Folder size={15} />{historyPage ? <strong>Operation history</strong> : comparing ? <><button className="crumb" onClick={() => navigate('/projects')}>Projects</button><ChevronRight size={13} /><strong>Compare workspaces</strong></> : onHub || scoped ? <><button className="crumb" onClick={() => navigate('/projects')}>Projects</button>{route.kind !== 'hub' && <><ChevronRight size={13} />{scoped ? <button className="crumb" onClick={() => navigate(projectPath(scoped.projectId))}>{project?.alias || opened.data?.alias || 'Project'}</button> : <strong>{projects.data?.projects?.find(p => p.id === (route.kind === 'project' ? route.projectId : ''))?.alias || 'Project'}</strong>}</>}{scoped && <><ChevronRight size={13} /><span>{scoped.workspace}</span><ChevronRight size={13} /><strong>{view}</strong></>}</> : <><span>{w?.info.project || 'Workspace'}</span><ChevronRight size={13} /><strong>{view}</strong></>}</nav><div className="topbar-actions">{!onHub && !noLaunch && <Button variant="ghost" size="small" aria-label="Find a resource" onClick={() => {
            setView('Resources');
            requestAnimationFrame(() => searchRef.current?.focus());
          }}><Search size={14} /><span className="hide-small">Search</span></Button>}<span className="session-label"><LockKeyhole size={12} />Private local session</span><Button variant="ghost" size="icon" aria-label="Toggle light and dark theme" onClick={() => changePreference({
            theme: document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'
          })}><Sun className="theme-to-light" size={17} /><Moon className="theme-to-dark" size={17} /></Button></div></header>
   {!onHub && !noLaunch && <div className="context-bar"><div className="context-item"><span className="engine-glyph">{w?.info.engine === 'tofu' ? 'O' : 'T'}</span><strong>{w?.info.engine === 'tofu' ? 'OpenTofu' : w?.info.engine === 'terraform' ? 'Terraform' : 'Engine'}</strong><span className="mono">{w?.info.version || '—'}</span></div><span className="context-divider" />{scoped ? <div className="context-item"><Layers3 size={13} /><label htmlFor="workspace-switch">Workspace</label><select id="workspace-switch" className="workspace-switch" value={scoped.workspace} onChange={e => navigate(contextPath(scoped.projectId, e.target.value, isExplorerView(view) ? view : 'Resources'))}>{Array.from(new Set([scoped.workspace, ...(project?.inspection?.workspaces || [])])).map(ws => <option key={ws} value={ws}>{ws}{ws === project?.inspection?.cliDefault ? ' (CLI default)' : ''}</option>)}</select>{productionLike(scoped.workspace, project?.alias) && <ProductionBadge />}</div> : <div className="context-item"><Layers3 size={13} /><span>Workspace</span><strong>{w?.info.workspace || 'unavailable'}</strong></div>}{profileProject ? <><span className="context-divider" /><ContextSelector projectId={profileProject} selection={profile} workspaces={scoped ? project?.inspection?.workspaces : undefined} onChosen={p => {
          // A profile that selects a workspace opens that workspace in this tab.
          if (scoped && p?.workspace && p.workspace !== scoped.workspace) navigate(contextPath(scoped.projectId, p.workspace, isExplorerView(view) ? view : 'Resources'));
        }} /></> : !scoped && w && <><span className="context-divider" /><div className="context-item muted" title="Execution profiles belong to Hub projects. Save this project to the Hub to use them.">Profile: none</div></>}<span className="context-divider" /><div className="context-item"><Database size={13} /><span>Backend</span><strong>{w?.info.backend || 'unknown'}</strong><span className={`dot ${ready ? '' : 'dot-muted'}`} /></div><span className="context-spacer" />{scoped && <><Button variant="ghost" size="small" onClick={reread} disabled={w?.status === 'loading'} title="Read this workspace's recorded state again. No refresh is run."><RefreshCcw size={12} />Read again</Button><Button variant="ghost" size="small" onClick={() => openInNewTab(contextPath(scoped.projectId, scoped.workspace, isExplorerView(view) ? view : 'Resources'))} title="Open this workspace in a new browser tab"><ExternalLink size={12} />New tab</Button></>}{!scoped && <Button variant="ghost" size="small" onClick={() => void rereadLaunch()} disabled={rereading || w?.status === 'loading' || !!runningOperation} title="Inspect this folder and read its recorded state again, as at startup. No init, plan or refresh is run."><RefreshCcw size={12} />Read again</Button>}{!scoped && runningOperation && <button className="context-running" onClick={() => setView(runningOperation.kind === 'drift' ? 'Drift' : runningOperation.kind === 'graph' ? 'Graph' : 'Plans')}><LoaderCircle className="spin" size={12} />{runningOperation.kind === 'apply' ? 'Apply' : runningOperation.kind === 'drift' ? 'Drift scan' : runningOperation.kind === 'graph' ? 'Graph' : runningOperation.import ? 'Import plan' : 'Plan'} running · {runningOperation.stage}</button>}<span className="snapshot-time">Snapshot {timeLabel(w?.capturedAt)}</span></div>}
   <main id="main-content" tabIndex={-1} className="main-content">
    {historyPage ? <div className="page-heading"><div><div className="eyebrow">ON THIS MACHINE</div><h1>Operation history</h1><p>What ran, when, against which workspace, and what each reviewed plan proposed. Redacted metadata only.</p></div><span className="read-only-badge"><ShieldCheck size={13} />Never re-runs</span></div> : comparing ? <div className="page-heading"><div><div className="eyebrow">WORKSPACE HUB</div><h1>Compare workspaces</h1><p>Line up the recorded state of two workspaces: dev and prod, or one stack in two roots. Only when you ask.</p></div><span className="read-only-badge"><ShieldCheck size={13} />Read-only</span></div> : onHub ? <div className="page-heading"><div><div className="eyebrow">WORKSPACE HUB</div><h1>{route.kind === 'hub' ? 'Projects' : 'Project workspaces'}</h1><p>{route.kind === 'hub' ? 'Every Terraform and OpenTofu root you work with, in one place.' : 'Open a CLI workspace to read its recorded state in this tab.'}</p></div><span className="read-only-badge"><ShieldCheck size={13} />{route.kind === 'project' ? 'Changes ask first' : 'Read-only'}</span></div> : <div className="page-heading"><div><div className="eyebrow">{scoped ? `HUB WORKSPACE · ${scoped.workspace.toUpperCase()}` : 'YOUR INFRASTRUCTURE, IN FOCUS'}</div><h1>{view === 'Resources' ? 'Resource explorer' : view === 'Overview' ? 'Workspace overview' : view === 'Import' ? 'Import an existing object' : view === 'Drift' ? 'Drift detection' : view === 'Graph' ? 'Dependency graph' : view === 'Tools' ? 'Project tools' : view}</h1><p>{view === 'Resources' ? 'A clear view of everything recorded in your state.' : view === 'Overview' ? 'The shape of your infrastructure, at a glance.' : view === 'Modules' ? 'Explore the structure behind your resources.' : view === 'Providers' ? 'Provider sources referenced by this state snapshot.' : view === 'Tools' ? 'Validate, check formatting, and list provider requirements and output names. Read-only, and only when you ask.' : view === 'Plans' ? 'See every change a plan proposes before anything happens.' : view === 'Drift' ? 'Compare what state records with what providers report, only when you ask.' : view === 'Graph' ? 'See what depends on what, and what a change could reach. Only a plan confirms what will change.' : view === 'Import' ? 'Bring an object that already exists into state through a reviewed import block and plan.' : view === 'Operations' ? 'Plans and applies run during this session, with sanitized output.' : 'Make this workspace feel like yours.'}</p></div><span className="read-only-badge"><ShieldCheck size={13} />{view === 'Plans' || view === 'Operations' || view === 'Import' ? 'Confirm before apply' : view === 'Drift' ? 'Never writes state' : 'Read-only'}</span></div>}
    {historyPage ? <HistoryView copy={copy} /> : comparing ? <CompareView copy={copy} /> : route.kind === 'hub' ? <HubView notify={setNotice} /> : route.kind === 'project' ? <ProjectView id={route.projectId} /> : view === 'Settings' ? <SettingsPanel preferences={preferences} changePreference={changePreference} /> : view === 'Plans' ? <PlansView project={w?.info.project || 'this project'} profileId={profile.selected} workspaceStatus={w?.status} planId={planId} choosePlan={setPlanId} replace={pendingReplace} clearReplace={() => setPendingReplace(undefined)} openPreflight={planRequested} preflightOpened={() => setPlanRequested(false)} copy={copy} inspectImpact={(id, address) => {
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
        }} /> : view === 'Import' ? <ImportView workspaceStatus={w?.status} profileId={profile.selected} copy={copy} onStarted={() => {
          setPlanId(undefined);
          setView('Plans');
          void operations.refetch();
        }} /> : view === 'Operations' ? <OperationsView openPlan={id => {
          setPlanId(id);
          setView('Plans');
        }} /> : view === 'Tools' && scoped ? opened.data ? <ToolsView context={opened.data} /> : opened.error ? <ErrorState title="Unable to open this workspace" message={opened.error.message} retry={() => {
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
                }}>Plan replacement…</Button><Button variant="ghost" size="small" onClick={() => setReplaceSelection([])}><X size={12} />Clear</Button></div>}<table className="resources-table"><thead><tr><th scope="col" className="select-column"><span className="sr-only">Select for replacement</span></th><th scope="col">Resource</th><th scope="col" className="provider-column">Provider</th><th scope="col">Mode</th></tr></thead><tbody>{resources.data?.resources.map(r => <ResourceRow key={r.id} resource={r} selected={selected === r.id} onSelect={() => setSelected(r.id)} selectable={!scoped} chosen={replaceSelection.some(c => c.id === r.id)} onChoose={() => toggleReplace(r)} />)}</tbody></table><div className="pagination"><span>{page * 50 + 1}–{Math.min((page + 1) * 50, resources.data?.total || 0)} of {resources.data?.total}</span><div><Button variant="ghost" size="icon" aria-label="Previous page" disabled={page === 0} onClick={() => setPage(page - 1)}><ArrowLeft size={14} /></Button><span>{page + 1} / {Math.ceil((resources.data?.total || 1) / 50)}</span><Button variant="ghost" size="icon" aria-label="Next page" disabled={(page + 1) * 50 >= (resources.data?.total || 0)} onClick={() => setPage(page + 1)}><ArrowRight size={14} /></Button></div></div></>}
       </section>
       {selected && <Inspector resource={detail.data} loading={detail.isPending} error={detail.error?.message} close={() => setSelected(undefined)} copy={copy} />}
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
  {notice && <div role="status" className="toast"><Check size={15} />{notice}<button aria-label="Dismiss notification" onClick={() => setNotice('')}><X size={13} /></button></div>}
 </div>;
}
function ErrorState({
  title = 'Unable to read workspace',
  message,
  retry,
  retryLabel = 'Retry connection'
}: {
  title?: string;
  message: string;
  retry?: () => void;
  retryLabel?: string;
}) {
  return <div className="empty-state error-state" role="alert"><CircleHelp size={28} /><h2>{title}</h2><p>{message}</p>{retry && <Button variant="outline" onClick={retry}>{retryLabel}</Button>}<span className="safe-note"><ShieldCheck size={13} />No infrastructure was changed.</span></div>;
}
function ResourceRow({
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
function ModuleTree({
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
function Inspector({
  resource,
  loading,
  error,
  close,
  copy
}: {
  resource?: Resource;
  loading: boolean;
  error?: string;
  close: () => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  return <Drawer label="Resource inspector" onClose={close}><div className="inspector-header"><span><Braces size={15} />Resource details</span><Button variant="ghost" size="icon" aria-label="Close inspector" onClick={close}><X size={16} /></Button></div>{loading ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading details…</div> : error ? <ErrorState message={error} /> : resource && <div className="inspector-body"><div className="inspector-title"><span className="resource-icon"><Box size={23} /></span><span className={`mode-badge ${resource.mode}`}>{resource.mode}</span></div><h2>{resource.name}</h2><p className="inspector-type mono">{resource.type}</p><div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy resource address" onClick={() => {
            void copy(resource.address, 'Address');
          }}><Copy size={13} /></Button></div><code>{resource.address}</code></div><dl className="metadata"><div><dt>Provider</dt><dd className="mono">{resource.provider || 'Not recorded'}</dd></div><div><dt>Module</dt><dd className="mono">{resource.module || 'Root module'}</dd></div><div><dt>Instance key</dt><dd className="mono">{resource.index === undefined ? '—' : JSON.stringify(resource.index)}</dd></div><div><dt>Source</dt><dd>Recorded state</dd></div></dl><div className="attributes-header"><h3>Attributes</h3><Button variant="ghost" size="icon" aria-label="Copy redacted JSON" onClick={() => {
          void copy(JSON.stringify(resource.attributes, null, 2), 'Redacted JSON');
        }}><Copy size={14} /></Button></div><div className="redaction-note"><LockKeyhole size={12} />Sensitive, secret-named and credential-like values are hidden.</div><div className="json-viewer"><JsonTree value={resource.attributes} /></div></div>}</Drawer>;
}
function Overview({
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
function SettingsPanel({
  preferences,
  changePreference
}: {
  preferences: Preferences;
  changePreference: (change: Partial<Preferences>) => void;
}) {
  return <div className="settings-panel"><div className="setting-row"><div><h2>Appearance</h2><p>Follow your system, or choose a theme.</p></div><div className="segmented">{(['system', 'light', 'dark'] as const).map((theme, i) => {
          const Icon = [Monitor, Sun, Moon][i];
          return <button aria-pressed={preferences.theme === theme} key={theme} onClick={() => changePreference({
            theme
          })}><Icon size={14} />{theme}</button>;
        })}</div></div><div className="setting-row"><div><h2>Resource density</h2><p>Adjust the space between resource rows.</p></div><select aria-label="Resource density" value={preferences.density} onChange={e => changePreference({
        density: e.target.value as Preferences['density']
      })}><option value="comfortable">Comfortable</option><option value="compact">Compact</option></select></div><div className="setting-row"><div><h2>Default grouping</h2><p>Browse by module or use a flat resource list.</p></div><select aria-label="Default grouping" value={preferences.grouping} onChange={e => changePreference({
        grouping: e.target.value as Preferences['grouping']
      })}><option value="module">Module tree</option><option value="flat">Flat list</option></select></div><div className="settings-security"><ShieldCheck size={23} /><div><h2>Your machine. Your infrastructure.</h2><p>No telemetry, cloud accounts, or external services. Only display preferences are saved in this browser. Hub projects are saved as paths and names in your user configuration directory. To change engines, restart with <code>--engine terraform</code> or <code>--engine tofu</code>.</p></div></div></div>;
}
