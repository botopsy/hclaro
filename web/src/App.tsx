import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowDownAZ, ArrowLeft, ArrowRight, Box, Braces, Check, ChevronDown, ChevronRight, CircleHelp, Command, Copy, Database, FileCode2, Folder, FolderTree, Layers3, LayoutDashboard, LoaderCircle, LockKeyhole, Monitor, Moon, Network, Search, Settings2, ShieldCheck, SlidersHorizontal, Sun, Terminal, Workflow, X } from 'lucide-react';
import { api } from './api';
import type { Module, Operation, Resource, ResourcePage, Workspace } from './types';
import { Button } from './components/ui/button';
import { JsonTree } from './components/JsonTree';
import { PlansView } from './plans/PlansView';
import { OperationsView } from './plans/OperationsView';
type View = 'Overview' | 'Resources' | 'Modules' | 'Providers' | 'Settings' | 'Plans' | 'Operations';
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
  name: 'Settings',
  icon: Settings2
}] as const;
export default function App() {
  const [view, setView] = useState<View>('Resources');
  const [preferences, setPreferences] = useState(readPreferences);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [module, setModule] = useState<string | undefined>();
  const [provider, setProvider] = useState('');
  const [type, setType] = useState('');
  const [mode, setMode] = useState('');
  const [sort, setSort] = useState<'asc' | 'desc'>('asc');
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<string>();
  const [treeVisible, setTreeVisible] = useState(true);
  const [notice, setNotice] = useState('');
  const [planId, setPlanId] = useState<string>();
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLElement>(null);
  const workspace = useQuery({
    queryKey: ['workspace'],
    queryFn: () => api<Workspace>('workspace'),
    refetchInterval: q => q.state.data?.status === 'loading' ? 700 : false
  });
  const w = workspace.data;
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
    queryKey: ['resources', parameters.toString()],
    queryFn: () => api<ResourcePage>(`resources?${parameters}`),
    enabled: ready
  });
  const detail = useQuery({
    queryKey: ['resource', selected],
    queryFn: () => api<Resource>(`resources/${selected}`),
    enabled: !!selected
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
  }, []);
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
  const filtered = !!(query || module !== undefined || provider || type || mode);
  return <div className={`app density-${preferences.density}`}>
  <a className="skip-link" href="#main-content">Skip to workspace</a>
  <aside className="sidebar" aria-label="Application navigation">
   <a className="brand" href="#" onClick={e => {
        e.preventDefault();
        setView('Overview');
      }} aria-label="Hclaro overview"><span className="brand-mark"><span /><span /><span /></span><span>hclaro<span className="alpha">ALPHA</span></span></a>
   <div className="project-tile"><span className="project-icon"><FileCode2 size={19} /></span><div><strong>{w?.info.project || 'Local workspace'}</strong><span>Infrastructure workspace</span></div></div>
   <div className="nav-label">WORKSPACE</div>
   <nav>{navigation.map(({
          name,
          icon: Icon
        }) => <button key={name} aria-label={name} className={`nav-item ${view === name ? 'active' : ''}`} aria-current={view === name ? 'page' : undefined} onClick={() => setView(name)}><Icon size={17} /><span>{name}</span>{name === 'Resources' && ready && <span className="nav-count">{total}</span>}</button>)}</nav>
   <div className="nav-label future-label">WORKFLOWS</div>
   {([['Plans', Workflow], ['Operations', Terminal]] as const).map(([name, Icon]) => <button key={name} aria-label={name} className={`nav-item ${view === name ? 'active' : ''}`} aria-current={view === name ? 'page' : undefined} onClick={() => setView(name)}><Icon size={17} /><span>{name}</span>{name === 'Operations' && runningOperation && <span className="nav-count running-dot" aria-label="Operation running"><LoaderCircle className="spin" size={11} /></span>}</button>)}
   <div className="sidebar-bottom"><div className="local-note"><ShieldCheck size={18} /><div><strong>Local by design</strong><span>Your state stays on your machine.</span></div></div><div className="sidebar-foot"><span><span className="dot" />Read-only alpha</span><span>v0.1</span></div></div>
  </aside>
  <div className="workspace-shell">
   <header className="topbar"><div className="breadcrumbs"><Folder size={15} /><span>{w?.info.project || 'Workspace'}</span><ChevronRight size={13} /><strong>{view}</strong></div><div className="topbar-actions"><Button variant="ghost" size="small" aria-label="Find a resource" onClick={() => {
            setView('Resources');
            requestAnimationFrame(() => searchRef.current?.focus());
          }}><Search size={14} /><span className="hide-small">Search</span></Button><span className="session-label"><LockKeyhole size={12} />Private local session</span><Button variant="ghost" size="icon" aria-label="Toggle light and dark theme" onClick={() => changePreference({
            theme: document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'
          })}><Sun className="theme-to-light" size={17} /><Moon className="theme-to-dark" size={17} /></Button></div></header>
   <div className="context-bar"><div className="context-item"><span className="engine-glyph">{w?.info.engine === 'tofu' ? 'O' : 'T'}</span><strong>{w?.info.engine === 'tofu' ? 'OpenTofu' : w?.info.engine === 'terraform' ? 'Terraform' : 'Engine'}</strong><span className="mono">{w?.info.version || '—'}</span></div><span className="context-divider" /><div className="context-item"><Layers3 size={13} /><span>Workspace</span><strong>{w?.info.workspace || 'unavailable'}</strong></div><span className="context-divider" /><div className="context-item"><Database size={13} /><span>Backend</span><strong>{w?.info.backend || 'unknown'}</strong><span className={`dot ${ready ? '' : 'dot-muted'}`} /></div><span className="context-spacer" />{runningOperation && <button className="context-running" onClick={() => setView('Plans')}><LoaderCircle className="spin" size={12} />Plan running · {runningOperation.stage}</button>}<span className="snapshot-time">Snapshot {timeLabel(w?.capturedAt)}</span></div>
   <main id="main-content" tabIndex={-1} className="main-content">
    <div className="page-heading"><div><div className="eyebrow">YOUR INFRASTRUCTURE, IN FOCUS</div><h1>{view === 'Resources' ? 'Resource explorer' : view === 'Overview' ? 'Workspace overview' : view}</h1><p>{view === 'Resources' ? 'A clear view of everything recorded in your state.' : view === 'Overview' ? 'The shape of your infrastructure, at a glance.' : view === 'Modules' ? 'Explore the structure behind your resources.' : view === 'Providers' ? 'Provider sources referenced by this state snapshot.' : view === 'Plans' ? 'See every change a plan proposes before anything happens.' : view === 'Operations' ? 'Plans run during this session, with sanitized output.' : 'Make this workspace feel like yours.'}</p></div><span className="read-only-badge"><ShieldCheck size={13} />{view === 'Plans' || view === 'Operations' ? 'Review only' : 'Read-only'}</span></div>
    {view === 'Settings' ? <SettingsPanel preferences={preferences} changePreference={changePreference} /> : view === 'Plans' ? <PlansView project={w?.info.project || 'this project'} workspaceStatus={w?.status} planId={planId} choosePlan={setPlanId} copy={copy} /> : view === 'Operations' ? <OperationsView openPlan={id => {
          setPlanId(id);
          setView('Plans');
        }} /> : workspace.isPending || w?.status === 'loading' ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><h2>Reading your workspace</h2><p>Inspecting recorded state through the local CLI. No plan or refresh is run.</p></div> : workspace.error ? <ErrorState message={workspace.error.message} retry={() => {
          void workspace.refetch();
        }} /> : w?.diagnostic ? <ErrorState title={w.diagnostic.code === 'uninitialized' ? 'Initialize this workspace first' : w.diagnostic.code === 'missing_cli' ? 'An IaC engine is needed' : 'Workspace needs attention'} message={w.diagnostic.message} /> : w && <>
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
                }} /> : resources.data?.total === 0 ? <div className="empty-state"><Box size={28} /><h2>{filtered ? 'No matching resources' : 'A clean slate'}</h2><p>{filtered ? 'Try a different search or clear your filters.' : 'The state contains no resource instances. Hclaro reads state without creating resources.'}</p>{filtered && <Button variant="outline" onClick={resetFilters}>Clear filters</Button>}</div> : <><table className="resources-table"><thead><tr><th scope="col">Resource</th><th scope="col" className="provider-column">Provider</th><th scope="col">Mode</th></tr></thead><tbody>{resources.data?.resources.map(r => <ResourceRow key={r.id} resource={r} selected={selected === r.id} onSelect={() => setSelected(r.id)} />)}</tbody></table><div className="pagination"><span>{page * 50 + 1}–{Math.min((page + 1) * 50, resources.data?.total || 0)} of {resources.data?.total}</span><div><Button variant="ghost" size="icon" aria-label="Previous page" disabled={page === 0} onClick={() => setPage(page - 1)}><ArrowLeft size={14} /></Button><span>{page + 1} / {Math.ceil((resources.data?.total || 1) / 50)}</span><Button variant="ghost" size="icon" aria-label="Next page" disabled={(page + 1) * 50 >= (resources.data?.total || 0)} onClick={() => setPage(page + 1)}><ArrowRight size={14} /></Button></div></div></>}
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
   <footer className="statusbar"><span><ShieldCheck size={12} />Read-only · No infrastructure changes</span><span>{ready ? `${total} instances in state` : 'Waiting for workspace'}<span className="footer-divider">/</span>127.0.0.1</span></footer>
  </div>
  {notice && <div role="status" className="toast"><Check size={15} />{notice}<button aria-label="Dismiss notification" onClick={() => setNotice('')}><X size={13} /></button></div>}
 </div>;
}
function ErrorState({
  title = 'Unable to read workspace',
  message,
  retry
}: {
  title?: string;
  message: string;
  retry?: () => void;
}) {
  return <div className="empty-state error-state" role="alert"><CircleHelp size={28} /><h2>{title}</h2><p>{message}</p>{retry && <Button variant="outline" onClick={retry}>Retry connection</Button>}<span className="safe-note"><ShieldCheck size={13} />No infrastructure was changed.</span></div>;
}
function ResourceRow({
  resource: r,
  selected,
  onSelect
}: {
  resource: Resource;
  selected: boolean;
  onSelect: () => void;
}) {
  function key(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    const buttons = Array.from(event.currentTarget.closest('tbody')!.querySelectorAll<HTMLButtonElement>('[data-resource-id]'));
    const index = buttons.indexOf(event.currentTarget);
    buttons[Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
  }
  return <tr className={selected ? 'selected' : ''}><td><button className="resource-button" data-resource-id={r.id} aria-pressed={selected} onClick={onSelect} onKeyDown={key}><span className={`resource-icon ${r.mode === 'data' ? 'data-icon' : ''}`}>{r.mode === 'data' ? <Database size={17} /> : <Box size={17} />}</span><span className="resource-text"><strong>{r.name}{r.index !== undefined && <span className="instance-key">[{typeof r.index === 'string' ? JSON.stringify(r.index) : String(r.index)}]</span>}</strong><span className="resource-type">{r.type}</span><span className="resource-address mono" title={r.address}>{r.address}</span></span></button></td><td className="provider-column"><span className="provider-label"><span className="provider-dot" />{providerName(r.provider)}</span></td><td><span className={`mode-badge ${r.mode}`}>{r.mode === 'data' ? 'Data' : 'Managed'}</span></td></tr>;
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
  return <aside className="inspector" aria-label="Resource inspector"><div className="inspector-header"><span><Braces size={15} />Resource details</span><Button variant="ghost" size="icon" aria-label="Close inspector" onClick={close}><X size={16} /></Button></div>{loading ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading details…</div> : error ? <ErrorState message={error} /> : resource && <div className="inspector-body"><div className="inspector-title"><span className="resource-icon"><Box size={23} /></span><span className={`mode-badge ${resource.mode}`}>{resource.mode}</span></div><h2>{resource.name}</h2><p className="inspector-type mono">{resource.type}</p><div className="address-box"><div className="label-row"><span>RESOURCE ADDRESS</span><Button variant="ghost" size="icon" aria-label="Copy resource address" onClick={() => {
            void copy(resource.address, 'Address');
          }}><Copy size={13} /></Button></div><code>{resource.address}</code></div><dl className="metadata"><div><dt>Provider</dt><dd className="mono">{resource.provider || 'Not recorded'}</dd></div><div><dt>Module</dt><dd className="mono">{resource.module || 'Root module'}</dd></div><div><dt>Instance key</dt><dd className="mono">{resource.index === undefined ? '—' : JSON.stringify(resource.index)}</dd></div><div><dt>Source</dt><dd>Recorded state</dd></div></dl><div className="attributes-header"><h3>Attributes</h3><Button variant="ghost" size="icon" aria-label="Copy redacted JSON" onClick={() => {
          void copy(JSON.stringify(resource.attributes, null, 2), 'Redacted JSON');
        }}><Copy size={14} /></Button></div><div className="redaction-note"><LockKeyhole size={12} />Sensitive & unrecognized values are redacted.</div><div className="json-viewer"><JsonTree value={resource.attributes} /></div></div>}</aside>;
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
      }) => <div key={label} className="stat"><span>{label}<Icon size={16} /></span><strong>{value.toLocaleString()}</strong><small>Recorded in state</small></div>)}</div><div className="overview-columns"><section className="surface distribution"><div className="surface-heading"><div><h2>Resource distribution</h2><p>Instances by resource type</p></div><Braces size={18} /></div>{Object.entries(w.types).sort((a, b) => b[1] - a[1]).map(([type, count]) => <button className="distribution-row" key={type} onClick={() => chooseType(type)}><span><span className="mono">{type}</span><strong>{count}</strong></span><progress value={count} max={w.managed + w.data} aria-label={`${type}: ${count} instances`} /></button>)}{w.managed + w.data === 0 && <p className="empty-description">No resource instances in this snapshot.</p>}</section><section className="surface workspace-info"><div className="surface-heading"><div><h2>Workspace context</h2><p>Connected through your local CLI</p></div><Terminal size={18} /></div><dl className="metadata"><div><dt>Project</dt><dd>{w.info.project}</dd></div><div><dt>Workspace</dt><dd>{w.info.workspace}</dd></div><div><dt>Engine</dt><dd>{w.info.engine} {w.info.version}</dd></div><div><dt>Backend type</dt><dd>{w.info.backend}</dd></div><div><dt>Execution mode</dt><dd>{w.info.executionMode}</dd></div><div><dt>Provider lockfile</dt><dd>{w.info.providerLock ? 'Present' : 'Not detected'}</dd></div><div><dt>Snapshot</dt><dd>{timeLabel(w.capturedAt)}</dd></div></dl><Button variant="outline" onClick={browse}>Explore resources<ArrowRight size={14} /></Button></section></div><div className="workspace-warnings">{w.info.warnings.map(warning => <div className="info-note" key={warning}><CircleHelp size={15} /><span>{warning}</span></div>)}</div></div>;
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
      })}><option value="module">Module tree</option><option value="flat">Flat list</option></select></div><div className="settings-security"><ShieldCheck size={23} /><div><h2>Your machine. Your infrastructure.</h2><p>No telemetry, cloud accounts, or external services. Only display preferences are saved locally. To change engines, restart with <code>--engine terraform</code> or <code>--engine tofu</code>.</p></div></div></div>;
}
