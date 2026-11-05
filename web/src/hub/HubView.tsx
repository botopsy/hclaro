import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, ArrowDown, ArrowLeft, ArrowRight, ArrowUp, Clock, ExternalLink, Folder, FolderOpen, FolderPlus, Layers3, LoaderCircle, Pencil, Play, Pin, PinOff, Plus, RefreshCcw, Search, ShieldCheck, Terminal, Trash2, Wrench, X } from 'lucide-react';
import { api, ApiError, openInNewTab, send } from '../api';
import type { FolderListing, Project, ProjectList, WorkspaceAction } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { contextPath, navigate, productionLike, projectPath } from '../router';
import { InitWizard } from './InitWizard';
import { WorkspaceDialog } from './WorkspaceDialog';

export const projectsQuery = {
  queryKey: ['projects'],
  queryFn: () => api<ProjectList>('projects')
};
function engineName(name?: string) {
  return name === 'tofu' ? 'OpenTofu' : name === 'terraform' ? 'Terraform' : 'Automatic';
}
function when(time?: string) {
  if (!time) return 'Never opened';
  const date = new Date(time);
  return date.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
}
function message(error: unknown) {
  return error instanceof Error ? error.message : 'Unable to reach the local Hclaro server. Check that it is still running, then retry.';
}
const statusLabel: Record<Project['status'], string> = {
  ready: 'Available',
  missing: 'Folder missing',
  unreadable: 'Folder unreadable',
  not_project: 'Not a project',
  path_changed: 'Path changed'
};
export function ProductionBadge() {
  return <span className="prod-badge" title="The name looks production-like. Hclaro cannot verify how critical an environment is."><AlertTriangle size={11} />Production-like name</span>;
}
// Moves focus between items marked data-hub-item with the arrow keys.
function arrowKeys(event: KeyboardEvent<HTMLElement>) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
  const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('[data-hub-item]'));
  const index = items.indexOf(document.activeElement as HTMLElement);
  if (index < 0) return;
  event.preventDefault();
  items[Math.max(0, Math.min(items.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
}

export function HubView({ notify }: { notify: (text: string) => void }) {
  const client = useQueryClient();
  const list = useQuery(projectsQuery);
  const [search, setSearch] = useState('');
  const [dialog, setDialog] = useState<{ kind: 'add'; path?: string } | { kind: 'rename' | 'repair' | 'remove'; project: Project }>();
  const searchRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    function keyboard(event: globalThis.KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        searchRef.current?.focus();
      }
    }
    window.addEventListener('keydown', keyboard);
    return () => window.removeEventListener('keydown', keyboard);
  }, []);
  const data = list.data;
  const projects = data?.projects || [];
  const writable = data?.registry.status === 'ready';
  const query = search.trim().toLowerCase();
  const matches = projects.filter(p => !query || [p.alias, p.rootPath, ...(p.inspection?.workspaces || [])].some(v => v.toLowerCase().includes(query)));
  const ordered = [...matches.filter(p => p.pinned), ...matches.filter(p => !p.pinned)];
  const recent = projects.filter(p => p.lastOpenedAt && p.status === 'ready').sort((a, b) => (b.lastOpenedAt || '').localeCompare(a.lastOpenedAt || '')).slice(0, 3);
  const launchRegistered = projects.some(p => p.launch);
  const refresh = () => client.invalidateQueries({ queryKey: ['projects'] });
  async function update(p: Project, change: Record<string, unknown>, done: string) {
    try {
      await send('PATCH', `projects/${p.id}`, change);
      notify(done);
    } catch (error) {
      notify(message(error));
    }
    await refresh();
  }
  async function move(p: Project, step: -1 | 1) {
    const ids = projects.map(x => x.id);
    const from = ids.indexOf(p.id);
    const to = from + step;
    if (to < 0 || to >= ids.length) return;
    [ids[from], ids[to]] = [ids[to], ids[from]];
    try {
      await send('POST', 'projects/order', { ids });
    } catch (error) {
      notify(message(error));
    }
    await refresh();
  }
  if (list.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading your projects…</p></div>;
  if (list.error || !data) return <div className="empty-state error-state" role="alert"><AlertTriangle size={28} /><h2>Unable to load the Hub</h2><p>{message(list.error)}</p><Button variant="outline" onClick={() => void list.refetch()}>Retry</Button></div>;
  const env = data.environment;
  return <div className="hub">
   {data.registry.notice && <div className={`hub-banner ${writable ? '' : 'hub-banner-warn'}`} role={writable ? 'status' : 'alert'}><AlertTriangle size={15} /><span>{data.registry.notice}</span></div>}
   {env.dataDirShared && <div className="hub-banner hub-banner-warn" role="alert"><AlertTriangle size={15} /><span><strong>TF_DATA_DIR is an absolute path.</strong> Every project would share one data directory, so Hub workspaces cannot be read. Restart Hclaro without it, or with a relative path.</span></div>}
   {env.workspaceInherited && <div className="hub-banner" role="status"><Layers3 size={15} /><span>Hclaro was started with <code>TF_WORKSPACE</code>{env.inheritedWorkspace ? <> set to <code>{env.inheritedWorkspace}</code></> : ''}. Hub workspaces replace it for each command they run; the project opened from the terminal still uses it.</span></div>}
   <div className="hub-toolbar">
    <div className="search-field"><Search size={16} /><input ref={searchRef} value={search} onChange={e => setSearch(e.target.value)} placeholder="Search projects, paths, workspaces…" aria-label="Search projects" />{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch('')}><X size={13} /></button>}</div>
    <Button onClick={() => setDialog({ kind: 'add' })} disabled={!writable} title={writable ? undefined : 'The project registry is read-only.'}><Plus size={15} />Add project</Button>
   </div>
   {data.launchRoot && !launchRegistered && <section className="surface hub-launch" aria-label="Project opened from the terminal"><div><span className="eyebrow">OPENED FROM TERMINAL</span><p className="mono">{data.launchRoot}</p><p className="muted">This project is open in this session. Save it to the Hub to find it again later.</p></div><div className="hub-actions"><Button variant="outline" onClick={() => navigate('/')}>Open<ArrowRight size={14} /></Button><Button variant="outline" disabled={!writable} onClick={() => setDialog({ kind: 'add', path: data.launchRoot })}><FolderPlus size={14} />Save to Hub</Button></div></section>}
   {recent.length > 0 && !query && <section aria-label="Recently opened"><h2 className="hub-section">Recently opened</h2><div className="hub-recent">{recent.map(p => <button key={p.id} className="hub-recent-item" onClick={() => navigate(p.lastWorkspace ? contextPath(p.id, p.lastWorkspace) : projectPath(p.id))}><Clock size={14} /><span><strong>{p.alias}</strong><span className="muted">{p.lastWorkspace ? `Workspace ${p.lastWorkspace}` : 'Project'} · {when(p.lastOpenedAt)}</span></span><ArrowRight size={14} /></button>)}</div></section>}
   <section aria-label="Projects"><h2 className="hub-section">Projects <span className="muted">{projects.length}</span></h2>
    {projects.length === 0 ? <div className="empty-state"><FolderPlus size={28} /><h2>Add your first project</h2><p>Register each Terraform or OpenTofu root folder you work with, such as <code>infra/dev</code> and <code>infra/prod</code>. Hclaro stores the path and a name, nothing else, and does not scan your disk.</p><Button onClick={() => setDialog({ kind: 'add' })} disabled={!writable}><Plus size={15} />Add project</Button></div> : ordered.length === 0 ? <div className="empty-state"><Search size={24} /><h2>No matching projects</h2><p>Workspaces are searchable once a project has been opened in this session.</p><Button variant="outline" onClick={() => setSearch('')}>Clear search</Button></div> : <ul className="hub-list" onKeyDown={arrowKeys}>{ordered.map(p => <ProjectCard key={p.id} project={p} writable={writable} first={projects[0]?.id === p.id} last={projects.at(-1)?.id === p.id} onPin={() => void update(p, { pinned: !p.pinned }, p.pinned ? 'Project unpinned' : 'Project pinned')} onMove={step => void move(p, step)} onDialog={kind => setDialog({ kind, project: p })} />)}</ul>}
   </section>
   <p className="info-note"><ShieldCheck size={15} /><span>Opening a project lists its workspaces with the CLI and opening a workspace reads its recorded state; either may contact the backend. Init and workspace changes (create, set CLI default, delete) run only after you review and confirm them on a project's page. Opening a workspace never selects it, and Hclaro never refreshes state. Removing a project only forgets it here.</span></p>
   {dialog?.kind === 'add' && <AddProjectDialog initialPath={dialog.path} onClose={() => setDialog(undefined)} onAdded={p => {
        setDialog(undefined);
        notify(`${p.alias} added`);
        void refresh();
      }} />}
   {dialog?.kind === 'rename' && <TextDialog title="Rename project" label="Name" initial={dialog.project.alias} action="Rename" onClose={() => setDialog(undefined)} onSubmit={async alias => {
        await send('PATCH', `projects/${dialog.project.id}`, { alias });
        setDialog(undefined);
        notify('Project renamed');
        await refresh();
      }} />}
   {dialog?.kind === 'repair' && <TextDialog title="Repair project path" label="New absolute path" initial={dialog.project.rootPath} action="Save path" mono description="Point this entry at the folder's new location. The folder is checked the same way as a new project." onClose={() => setDialog(undefined)} onSubmit={async rootPath => {
        await send('PATCH', `projects/${dialog.project.id}`, { rootPath });
        setDialog(undefined);
        notify('Project path updated');
        await refresh();
      }} />}
   {dialog?.kind === 'remove' && <RemoveDialog project={dialog.project} onClose={() => setDialog(undefined)} onRemoved={() => {
        setDialog(undefined);
        notify('Project removed from the Hub');
        client.removeQueries({ queryKey: ['context-open', dialog.project.id] });
        void refresh();
      }} />}
  </div>;
}

function ProjectCard({ project: p, writable, first, last, onPin, onMove, onDialog }: {
  project: Project;
  writable: boolean;
  first: boolean;
  last: boolean;
  onPin: () => void;
  onMove: (step: -1 | 1) => void;
  onDialog: (kind: 'rename' | 'repair' | 'remove') => void;
}) {
  const ready = p.status === 'ready';
  const engine = p.inspection?.engine ? `${engineName(p.inspection.engine)} ${p.inspection.version || ''}`.trim() : p.enginePreference ? `${engineName(p.enginePreference)} · version not checked` : 'Engine not checked';
  return <li className={`hub-card ${ready ? '' : 'hub-card-problem'}`}>
   <div className="hub-card-main">
    <button className="hub-card-title" data-hub-item onClick={() => navigate(projectPath(p.id))} aria-label={`Open ${p.alias}`}><span className="directory-icon"><FolderOpen size={19} /></span><span><strong>{p.alias}</strong>{p.pinned && <Pin size={12} aria-label="Pinned" className="muted" />}</span></button>
    <p className="mono hub-path" title={p.rootPath}>{p.rootPath}</p>
    <div className="hub-tags">
     <span className={`hub-status hub-status-${p.status}`}>{statusLabel[p.status]}</span>
     {p.launch && <span className="hub-tag">Opened from terminal</span>}
     {p.busy === 'init' && <span className="hub-tag hub-tag-busy"><LoaderCircle className="spin" size={11} />Initializing</span>}
     {p.busy === 'workspace' && <span className="hub-tag hub-tag-busy"><LoaderCircle className="spin" size={11} />Changing workspaces</span>}
     {ready && !p.initialized && !p.busy && <span className="hub-tag hub-tag-warn">Not initialized</span>}
     {productionLike(p.alias, p.rootPath.split(/[\\/]/).at(-1)) && <ProductionBadge />}
    </div>
    <dl className="hub-facts">
     <div><dt>Engine</dt><dd>{engine}</dd></div>
     <div><dt>Initialized</dt><dd>{ready ? p.initialized ? 'Yes' : 'Not detected' : '—'}</dd></div>
     <div><dt>Backend</dt><dd>{ready ? p.backend : '—'}</dd></div>
     <div><dt>Workspaces</dt><dd>{p.inspection && !p.inspection.diagnostic ? p.inspection.workspaces.length : 'Not listed yet'}</dd></div>
     <div><dt>CLI default</dt><dd title="Selected in this project's CLI data directory, as last listed in this session.">{p.inspection && !p.inspection.diagnostic ? p.inspection.cliDefault || 'Not marked' : 'Not listed yet'}</dd></div>
     <div><dt>Last opened in Hclaro</dt><dd title="Each browser tab reads its own workspace; this never changes the CLI default.">{p.lastWorkspace || '—'}</dd></div>
     <div><dt>Workspace changes</dt><dd title={p.workspaceActions?.reason || p.workspaceActions?.deleteReason}>{!ready ? '—' : p.workspaceActions?.change ? p.workspaceActions.delete ? 'Create, set default, delete' : 'Create, set default' : 'Unavailable'}</dd></div>
     <div><dt>Last opened</dt><dd>{when(p.lastOpenedAt)}</dd></div>
    </dl>
    {p.problem && <p className="hub-problem" role="note"><AlertTriangle size={13} />{p.problem}</p>}
    {p.inspection?.diagnostic && ready && <p className="hub-problem" role="note"><AlertTriangle size={13} />{p.inspection.diagnostic.message}</p>}
   </div>
   <div className="hub-card-actions">
    {ready ? <Button size="small" onClick={() => navigate(projectPath(p.id))}>Workspaces<ArrowRight size={13} /></Button> : <Button size="small" variant="outline" disabled={!writable} onClick={() => onDialog('repair')}><Wrench size={13} />Repair path</Button>}
    <Button variant="ghost" size="icon" aria-label={p.pinned ? `Unpin ${p.alias}` : `Pin ${p.alias}`} disabled={!writable} onClick={onPin}>{p.pinned ? <PinOff size={14} /> : <Pin size={14} />}</Button>
    <Button variant="ghost" size="icon" aria-label={`Rename ${p.alias}`} disabled={!writable} onClick={() => onDialog('rename')}><Pencil size={14} /></Button>
    <Button variant="ghost" size="icon" aria-label={`Move ${p.alias} up`} disabled={!writable || first} onClick={() => onMove(-1)}><ArrowUp size={14} /></Button>
    <Button variant="ghost" size="icon" aria-label={`Move ${p.alias} down`} disabled={!writable || last} onClick={() => onMove(1)}><ArrowDown size={14} /></Button>
    <Button variant="ghost" size="icon" aria-label={`Remove ${p.alias} from the Hub`} disabled={!writable} onClick={() => onDialog('remove')}><Trash2 size={14} /></Button>
   </div>
  </li>;
}

function AddProjectDialog({ initialPath = '', onClose, onAdded }: {
  initialPath?: string;
  onClose: () => void;
  onAdded: (p: Project) => void;
}) {
  const [path, setPath] = useState(initialPath);
  const [alias, setAlias] = useState('');
  const [engine, setEngine] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const dialog = useRef<HTMLDivElement>(null);
  const first = useRef<HTMLInputElement>(null);
  useModal(dialog, first, onClose);
  async function submit() {
    setBusy(true);
    setError('');
    try {
      onAdded(await send<Project>('POST', 'projects', { path: path.trim(), alias: alias.trim(), engine }));
    } catch (e) {
      setError(e instanceof ApiError && e.code === 'duplicate_project' ? 'This folder is already in the Hub.' : message(e));
    } finally {
      setBusy(false);
    }
  }
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog dialog-wide" role="dialog" aria-modal="true" aria-labelledby="add-title">
    <div className="dialog-header"><h2 id="add-title">Add a project</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p>Choose the root folder of one Terraform or OpenTofu configuration. Hclaro saves its path, name and engine choice in your user configuration directory. Nothing in the folder is run or changed.</p>
    <form className="hub-form" onSubmit={e => {
        e.preventDefault();
        void submit();
      }}>
     <label>Root folder<div className="hub-path-row"><input ref={first} className="mono" value={path} onChange={e => setPath(e.target.value)} placeholder="/Users/me/infra/prod" spellCheck={false} autoComplete="off" required /><Button type="button" variant="outline" onClick={() => setBrowsing(!browsing)} aria-expanded={browsing}><Folder size={14} />Browse…</Button></div></label>
     {browsing && <FolderChooser start={path} choose={p => {
          setPath(p);
          setBrowsing(false);
        }} />}
     <label>Name <span className="muted">(optional; defaults to the folder name)</span><input value={alias} onChange={e => setAlias(e.target.value)} maxLength={80} autoComplete="off" /></label>
     <label>Engine<select value={engine} onChange={e => setEngine(e.target.value)}><option value="">Automatic (Terraform, then OpenTofu)</option><option value="terraform">Terraform</option><option value="tofu">OpenTofu</option></select></label>
     {error && <p className="dialog-error" role="alert">{error}</p>}
     <div className="dialog-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" disabled={busy || !path.trim()}><Plus size={14} />{busy ? 'Adding…' : 'Add project'}</Button></div>
    </form>
   </div>
  </div>;
}

// Lists one folder at a time; nothing is scanned recursively.
function FolderChooser({ start, choose }: { start: string; choose: (path: string) => void }) {
  const [path, setPath] = useState(start.startsWith('/') || /^[A-Za-z]:\\/.test(start) ? start : '');
  const [listing, setListing] = useState<FolderListing>();
  const [error, setError] = useState('');
  useEffect(() => {
    let live = true;
    send<FolderListing>('POST', 'projects/browse', { path }).then(l => {
      if (live) {
        setListing(l);
        setError('');
      }
    }, e => {
      if (live) setError(message(e));
    });
    return () => {
      live = false;
    };
  }, [path]);
  const join = (name: string) => listing ? `${listing.path.replace(/[\\/]$/, '')}${listing.path.includes('\\') ? '\\' : '/'}${name}` : name;
  return <div className="folder-chooser" aria-label="Choose a folder" role="group">
   {error ? <p className="dialog-error" role="alert">{error} <button type="button" className="link" onClick={() => setPath('')}>Go to home folder</button></p> : !listing ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Listing folder…</p> : <>
    <div className="folder-current"><Button type="button" variant="ghost" size="icon" aria-label="Parent folder" disabled={!listing.parent} onClick={() => setPath(listing.parent || '')}><ArrowLeft size={14} /></Button><span className="mono" title={listing.path}>{listing.path}</span><Button type="button" size="small" disabled={!listing.project} onClick={() => choose(listing.path)} title={listing.project ? undefined : 'No .tf, .tofu or state files in this folder.'}>Use this folder</Button></div>
    {listing.project && <p className="folder-hint"><ShieldCheck size={13} />Terraform/OpenTofu files found here.</p>}
    <ul className="folder-list" onKeyDown={arrowKeys}>{listing.entries.map(name => <li key={name}><button type="button" data-hub-item onClick={() => setPath(join(name))}><Folder size={13} />{name}</button></li>)}{listing.entries.length === 0 && <li className="muted">No subfolders.</li>}</ul>
    {listing.truncated && <p className="muted">Only the first 500 folders are shown. Type the path instead.</p>}
   </>}
  </div>;
}

function TextDialog({ title, label, initial, action, description, mono, onClose, onSubmit }: {
  title: string;
  label: string;
  initial: string;
  action: string;
  description?: string;
  mono?: boolean;
  onClose: () => void;
  onSubmit: (value: string) => Promise<void>;
}) {
  const [value, setValue] = useState(initial);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const dialog = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  useModal(dialog, input, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="text-dialog-title">
    <div className="dialog-header"><h2 id="text-dialog-title">{title}</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    {description && <p>{description}</p>}
    <form className="hub-form" onSubmit={async e => {
        e.preventDefault();
        setBusy(true);
        setError('');
        try {
          await onSubmit(value.trim());
        } catch (err) {
          setError(message(err));
        } finally {
          setBusy(false);
        }
      }}>
     <label>{label}<input ref={input} className={mono ? 'mono' : undefined} value={value} onChange={e => setValue(e.target.value)} spellCheck={false} autoComplete="off" required /></label>
     {error && <p className="dialog-error" role="alert">{error}</p>}
     <div className="dialog-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" disabled={busy || !value.trim()}>{action}</Button></div>
    </form>
   </div>
  </div>;
}

function RemoveDialog({ project, onClose, onRemoved }: { project: Project; onClose: () => void; onRemoved: () => void }) {
  const [error, setError] = useState('');
  const cancel = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, cancel, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="remove-title">
    <div className="dialog-header"><h2 id="remove-title">Remove {project.alias} from the Hub?</h2></div>
    <p>Hclaro forgets this entry. <strong>Nothing on disk or in your backend is deleted</strong>: configuration, <code>.terraform</code>, state and workspaces stay as they are. Open workspace views of this project close.</p>
    <p className="mono hub-path">{project.rootPath}</p>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button ref={cancel} variant="ghost" onClick={onClose}>Cancel</Button><Button variant="outline" onClick={async () => {
          try {
            await send('DELETE', `projects/${project.id}`, {});
            onRemoved();
          } catch (e) {
            setError(message(e));
          }
        }}><Trash2 size={14} />Remove from Hub</Button></div>
   </div>
  </div>;
}

// The workspace navigator for one project. Workspaces are listed with `workspace list`
// when the page opens; choosing one only changes this tab's context.
export function ProjectView({ id }: { id: string }) {
  const client = useQueryClient();
  // While init runs without its dialog open, poll so the page notices when it finishes.
  const list = useQuery({ ...projectsQuery, refetchInterval: q => q.state.data?.projects.some(p => p.id === id && p.busy) ? 2000 : false });
  const project = list.data?.projects.find(p => p.id === id);
  const [initOpen, setInitOpen] = useState(false);
  const [workspaceDialog, setWorkspaceDialog] = useState<{ action: WorkspaceAction; name?: string }>();
  // Set-default and delete are advanced: hidden until the user asks for them in this view.
  const [advanced, setAdvanced] = useState(false);
  // Listing workspaces waits while an operation runs or its dialog is open: the root is claimed.
  const inspect = useQuery({
    queryKey: ['inspect', id],
    queryFn: () => send<Project>('POST', `projects/${id}/inspect`, {}),
    enabled: project?.status === 'ready' && !project.busy && !initOpen && !workspaceDialog
  });
  const initFinished = useCallback(() => {
    void client.invalidateQueries({ queryKey: ['projects'] });
    void client.invalidateQueries({ queryKey: ['inspect', id] });
    client.removeQueries({ queryKey: ['context-open', id] });
  }, [client, id]);
  const workspaceFinished = useCallback(() => {
    void client.invalidateQueries({ queryKey: ['projects'] });
  }, [client]);
  const current = inspect.data || project;
  const inspection = inspect.data?.inspection;
  useEffect(() => {
    if (inspect.data) void client.invalidateQueries({ queryKey: ['projects'] });
  }, [inspect.data, client]);
  if (list.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading project…</p></div>;
  if (!current) return <div className="empty-state error-state" role="alert"><AlertTriangle size={28} /><h2>Project not found</h2><p>This project is not registered in the Hub. It may have been removed.</p><Button variant="outline" onClick={() => navigate('/projects')}><ArrowLeft size={14} />Back to projects</Button></div>;
  const env = list.data?.environment;
  // Tools open in the last workspace used here when it still exists, else the CLI default.
  const listed = inspection?.workspaces || [];
  const toolsWorkspace = current.lastWorkspace && listed.includes(current.lastWorkspace) ? current.lastWorkspace : inspection?.cliDefault || 'default';
  return <div className="hub project-view">
   <div className="hub-toolbar"><Button variant="ghost" onClick={() => navigate('/projects')}><ArrowLeft size={14} />All projects</Button><span className="context-spacer" />{current.status === 'ready' && <Button variant="outline" onClick={() => void inspect.refetch()} disabled={inspect.isFetching}><RefreshCcw size={14} className={inspect.isFetching ? 'spin' : undefined} />List workspaces again</Button>}</div>
   {current.busy === 'workspace' && !workspaceDialog && <div className="hub-banner" role="status"><LoaderCircle className="spin" size={15} /><span><strong>A workspace command is running.</strong> Workspaces can be listed when it finishes.</span></div>}
   {current.busy === 'init' && !initOpen && <div className="hub-banner" role="status"><LoaderCircle className="spin" size={15} /><span><strong>Initialization is running.</strong> Workspaces can be listed when it finishes.</span><span className="context-spacer" /><Button size="small" variant="outline" onClick={() => setInitOpen(true)}>Show progress</Button></div>}
   <section className="surface"><div className="surface-heading"><div><h2>{current.alias} {productionLike(current.alias) && <ProductionBadge />}</h2><p className="mono">{current.rootPath}</p></div>{current.status === 'ready' ? <div className="hub-actions"><Button size="small" variant="outline" onClick={() => navigate(contextPath(id, toolsWorkspace, 'Tools'))} title={`Validate, format check, providers and outputs. Opens in workspace ${toolsWorkspace}.`}><Wrench size={13} />Project tools</Button><Button size="small" variant={current.initialized ? 'outline' : 'default'} onClick={() => setInitOpen(true)} disabled={!!current.busy && current.busy !== 'init'}><Play size={13} />{current.initialized ? 'Run init again…' : 'Initialize…'}</Button></div> : <FolderOpen size={18} />}</div>
    <dl className="metadata"><div><dt>Engine</dt><dd>{inspection?.engine ? `${engineName(inspection.engine)} ${inspection.version || ''}` : engineName(current.enginePreference)}</dd></div><div><dt>Initialized</dt><dd>{current.initialized ? 'Yes' : 'Not detected'}</dd></div><div><dt>Backend type</dt><dd>{current.backend}</dd></div><div><dt>Execution mode</dt><dd>{current.executionMode}</dd></div><div><dt>Data directory</dt><dd className="mono">{env?.dataDir || '.terraform'}</dd></div><div><dt>Provider lockfile</dt><dd>{current.providerLock ? 'Present' : 'Not detected'}</dd></div></dl>
   </section>
   <section aria-labelledby="workspaces-title"><div className="workspace-heading"><h2 className="hub-section" id="workspaces-title">CLI workspaces</h2>{current.status === 'ready' && <><label className="workspace-advanced"><input type="checkbox" checked={advanced} onChange={e => setAdvanced(e.target.checked)} />Advanced actions</label><Button size="small" variant="outline" disabled={!current.workspaceActions?.change || !!current.busy || !inspection || !!inspection.diagnostic} title={current.workspaceActions?.change ? undefined : current.workspaceActions?.reason} onClick={() => setWorkspaceDialog({ action: 'new' })}><Plus size={13} />New workspace…</Button></>}</div>
    {current.status !== 'ready' ? <div className="empty-state error-state" role="alert"><AlertTriangle size={24} /><h2>{statusLabel[current.status]}</h2><p>{current.problem}</p><Button variant="outline" onClick={() => navigate('/projects')}>Repair or remove in the Hub</Button></div> : inspect.isPending || inspect.isFetching && !inspection ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><h2>Listing workspaces</h2><p>Running <code>workspace list</code> through the local CLI. This may contact the backend. No workspace is selected or created.</p></div> : inspect.error ? <div className="empty-state error-state" role="alert"><AlertTriangle size={24} /><h2>Workspaces unavailable</h2><p>{message(inspect.error)}</p><Button variant="outline" onClick={() => void inspect.refetch()}>Try again</Button></div> : inspection?.diagnostic ? <div className="empty-state error-state" role="alert"><AlertTriangle size={24} /><h2>{inspection.diagnostic.code === 'uninitialized' ? 'Initialize this project first' : inspection.diagnostic.code === 'missing_cli' ? 'An IaC engine is needed' : 'Workspaces unavailable'}</h2><p>{inspection.diagnostic.code === 'uninitialized' ? 'Backend initialization was not detected. Review and run init here, or run it yourself in a terminal. Nothing was initialized automatically.' : inspection.diagnostic.message}</p>{inspection.diagnostic.code === 'uninitialized' && <Button onClick={() => setInitOpen(true)}><Play size={14} />Review and initialize…</Button>}<span className="safe-note"><ShieldCheck size={13} />Nothing was initialized or changed.</span></div> : inspection && inspection.workspaces.length === 0 ? <div className="empty-state"><Layers3 size={24} /><h2>No workspaces listed</h2><p>The CLI returned no workspace names Hclaro can display.</p></div> : inspection && <>
     <ul className="workspace-list" onKeyDown={arrowKeys}>{inspection.workspaces.map(ws => {
         const isDefault = ws === inspection.cliDefault;
         const deleteBlocked = !current.workspaceActions?.delete ? current.workspaceActions?.deleteReason : ws === 'default' ? 'The default workspace cannot be deleted.' : isDefault ? 'This is the CLI default. Set another workspace as CLI default first.' : !inspection.cliDefault ? 'The CLI default is unknown.' : undefined;
         return <li key={ws}><button className="workspace-open" data-hub-item onClick={() => navigate(contextPath(id, ws))}><Layers3 size={15} /><strong>{ws}</strong>{isDefault && <span className="hub-tag" title="Selected in this project's CLI data directory. Opening another workspace here does not change it.">CLI default</span>}{ws === current.lastWorkspace && <span className="hub-tag hub-tag-quiet" title="Last opened in Hclaro. Each tab reads its own workspace.">Last opened here</span>}{productionLike(ws) && <ProductionBadge />}<span className="context-spacer" /><span className="muted">Read state</span><ArrowRight size={14} /></button><Button variant="ghost" size="icon" aria-label={`Open ${ws} in a new tab`} title="Open in a new browser tab" onClick={() => openInNewTab(contextPath(id, ws))}><ExternalLink size={14} /></Button>
          {advanced && <><Button variant="ghost" size="small" disabled={isDefault || !current.workspaceActions?.change || !!current.busy} title={isDefault ? 'Already the CLI default.' : current.workspaceActions?.change ? 'Runs workspace select after you confirm. Affects terminals in this folder.' : current.workspaceActions?.reason} onClick={() => setWorkspaceDialog({ action: 'select', name: ws })} aria-label={`Set ${ws} as CLI default`}><Terminal size={13} />Set CLI default…</Button><Button variant="ghost" size="icon" disabled={!!deleteBlocked || !!current.busy} title={deleteBlocked || 'Delete this workspace if its state is empty'} aria-label={`Delete workspace ${ws}`} onClick={() => setWorkspaceDialog({ action: 'delete', name: ws })}><Trash2 size={14} /></Button></>}
         </li>;
       })}</ul>
     {inspection.hiddenWorkspaces > 0 && <p className="muted">{inspection.hiddenWorkspaces} workspace name{inspection.hiddenWorkspaces === 1 ? '' : 's'} with unusual characters {inspection.hiddenWorkspaces === 1 ? 'is' : 'are'} not shown.</p>}
     {current.workspaceActions && !current.workspaceActions.change && <p className="muted workspace-support"><AlertTriangle size={13} />Workspace changes are unavailable: {current.workspaceActions.reason}</p>}
     {advanced && current.workspaceActions?.change && !current.workspaceActions.delete && <p className="muted workspace-support"><AlertTriangle size={13} />{current.workspaceActions.deleteReason}</p>}
     <p className="info-note"><ShieldCheck size={15} /><span>Choosing a workspace changes only this browser tab: each command Hclaro runs for it gets <code>TF_WORKSPACE</code> in its own environment. Your terminal's selected workspace stays <strong>{inspection.cliDefault || 'unchanged'}</strong>. Hub workspaces are read-only. Changing the CLI default is a separate, confirmed action under <strong>Advanced actions</strong>.</span></p>
    </>}
   </section>
   {workspaceDialog && <WorkspaceDialog projectId={id} action={workspaceDialog.action} initialName={workspaceDialog.name} onClose={() => {
        setWorkspaceDialog(undefined);
        void client.invalidateQueries({ queryKey: ['projects'] });
        void client.invalidateQueries({ queryKey: ['inspect', id] });
      }} onFinished={workspaceFinished} onOpen={ws => {
        setWorkspaceDialog(undefined);
        navigate(contextPath(id, ws));
      }} />}
   {initOpen && <InitWizard projectId={id} onClose={() => {
        setInitOpen(false);
        void client.invalidateQueries({ queryKey: ['projects'] });
      }} onFinished={initFinished} />}
  </div>;
}
