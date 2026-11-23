import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, ArrowDown, ArrowLeft, ArrowRight, ArrowUp, ExternalLink, Folder, FolderOpen, FolderPlus, Layers3, LoaderCircle, Pin, Play, Plus, RefreshCcw, Search, ShieldCheck, Terminal, Trash2, Wrench, X } from 'lucide-react';
import { ApiError, openInNewTab, send } from '../api';
import type { FolderListing, GitContext, Project, WorkspaceAction } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { contextPath, navigate, productionLike, projectPath } from '../router';
import { globalPages } from '../navigation/config';
import { ProductionBadge } from '../components/ProductionBadge';
import { projectsQuery } from '../lib/queries';
import { InitWizard } from './InitWizard';
import { WorkspaceDialog } from './WorkspaceDialog';
import { ContextSelector } from '../profiles/ContextSelector';
import { useProfileSelection } from '../profiles/selection';
import { dateTime, engineLabel } from '../lib/format';
import { errorMessage } from '../lib/errorMessage';
import { ErrorState } from '../components/ErrorState';
import { PageHeader } from '../components/PageHeader';
import { EmptyState } from '../components/EmptyState';
import { Disclosure } from '../components/Disclosure';
import { Dialog } from '../components/Dialog';
import { Drawer } from '../components/Drawer';
import { DataTable, type Column } from '../components/DataTable';
import { Chip, StatusDot, type Tone } from '../components/Chip';
import { gitPrefix, shortCommit } from '../git/GitView';

const when = (time?: string) => time ? dateTime(time) : 'Never opened';
const statusLabel: Record<Project['status'], string> = {
  ready: 'Available',
  missing: 'Folder missing',
  unreadable: 'Folder unreadable',
  not_project: 'Not a project',
  path_changed: 'Path changed'
};
// The one status a project row shows: a running command, then a folder problem, then init.
function projectStatus(p: Project): { tone: Tone; label: string } {
  if (p.busy === 'init') return { tone: 'read', label: 'Initializing' };
  if (p.busy === 'workspace') return { tone: 'read', label: 'Changing workspaces' };
  if (p.status !== 'ready') return { tone: 'delete', label: statusLabel[p.status] };
  if (!p.initialized) return { tone: 'update', label: 'Not initialized' };
  return { tone: 'ok', label: 'Available' };
}
const engineText = (p: Project) => p.inspection?.engine ? `${engineLabel(p.inspection.engine, 'Automatic')} ${p.inspection.version || ''}`.trim() : p.enginePreference ? `${engineLabel(p.enginePreference, 'Automatic')} · version not checked` : 'Not checked';
const listed = (p: Project) => p.inspection && !p.inspection.diagnostic ? p.inspection : undefined;
// Opens the last workspace used here, as the project switcher does, else the workspace list.
const openPath = (p: Project) => p.lastWorkspace ? contextPath(p.id, p.lastWorkspace) : projectPath(p.id);
// Moves focus between items marked data-hub-item with the arrow keys.
function arrowKeys(event: KeyboardEvent<HTMLElement>) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
  const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('[data-hub-item]'));
  const index = items.indexOf(document.activeElement as HTMLElement);
  if (index < 0) return;
  event.preventDefault();
  items[Math.max(0, Math.min(items.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))]?.focus();
}

// §4.10: the Projects page. Listing projects reads only the registry; nothing runs.
export function HubView({ notify }: { notify: (text: string) => void }) {
  const client = useQueryClient();
  const list = useQuery(projectsQuery);
  const [search, setSearch] = useState('');
  const [details, setDetails] = useState<string>();
  const [dialog, setDialog] = useState<{ kind: 'add'; path?: string } | { kind: 'repair' | 'remove'; project: Project }>();
  const closeDetails = useCallback(() => setDetails(undefined), []);
  const data = list.data;
  const projects = data?.projects || [];
  const writable = data?.registry.status === 'ready';
  const readOnly = writable ? undefined : 'The project registry is read-only.';
  const query = search.trim().toLowerCase();
  const matches = projects.filter(p => !query || [p.alias, p.rootPath, ...(p.inspection?.workspaces || [])].some(v => v.toLowerCase().includes(query)));
  const ordered = [...matches.filter(p => p.pinned), ...matches.filter(p => !p.pinned)];
  const launchRegistered = projects.some(p => p.launch);
  const refresh = () => client.invalidateQueries({ queryKey: ['projects'] });
  async function update(p: Project, change: Record<string, unknown>, done: string) {
    try {
      await send('PATCH', `projects/${p.id}`, change);
      notify(done);
    } catch (error) {
      notify(errorMessage(error));
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
      notify(errorMessage(error));
    }
    await refresh();
  }
  const header = <PageHeader title="Projects" count={data ? projects.length : undefined}>{projects.length > 0 && <>
    <div className="search-field"><Search size={15} aria-hidden="true" /><input value={search} onChange={e => setSearch(e.target.value)} placeholder="Search projects, paths, workspaces…" aria-label="Search projects" />{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch('')}><X size={13} /></button>}</div>
    <Button onClick={() => setDialog({ kind: 'add' })} disabled={!writable} title={readOnly}><Plus size={15} />Add project</Button>
   </>}</PageHeader>;
  if (list.isPending) return <div className="hub">{header}<div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading your projects…</p></div></div>;
  if (list.error || !data) return <div className="hub">{header}<ErrorState inline icon={AlertTriangle} title="Unable to load projects" message={errorMessage(list.error)} retry={() => void list.refetch()} retryLabel="Retry" /></div>;
  const env = data.environment;
  const open = projects.find(p => p.id === details);
  const columns: Column<Project>[] = [
    { key: 'pin', header: <span className="sr-only">Pinned</span>, className: 'projects-pin', cell: p => <Button variant="ghost" size="icon" aria-label={p.pinned ? `Unpin ${p.alias}` : `Pin ${p.alias}`} aria-pressed={p.pinned} title={readOnly ?? (p.pinned ? 'Pinned: listed first' : 'Pin to list first')} disabled={!writable} onClick={() => void update(p, { pinned: !p.pinned }, p.pinned ? 'Project unpinned' : 'Project pinned')}><Pin size={14} className={p.pinned ? 'projects-pinned' : 'muted'} /></Button> },
    { key: 'project', header: 'Project', cell: p => <button type="button" className="projects-name" data-hub-item title={p.rootPath} onClick={() => setDetails(p.id)}><span><strong>{p.alias}</strong>{p.launch && <Chip tone="accent" title="Opened from the terminal in this session">OPEN</Chip>}{productionLike(p.alias, p.rootPath.split(/[\\/]/).at(-1)) && <ProductionBadge />}</span><span className="mono muted">{p.rootPath}</span></button> },
    { key: 'status', header: 'Status', className: 'projects-nowrap', cell: p => {
      const s = projectStatus(p);
      return <span title={p.problem}><StatusDot tone={s.tone} label={s.label} /></span>;
    } },
    { key: 'engine', header: 'Engine', className: 'projects-nowrap muted', cell: engineText },
    { key: 'backend', header: 'Backend', className: 'projects-nowrap muted', cell: p => p.status === 'ready' ? p.backend : '—' },
    { key: 'workspaces', header: 'Workspaces', className: 'projects-nowrap muted', cell: p => listed(p)?.workspaces.length ?? <span title="Listed when you open the project or its details">—</span> },
    { key: 'opened', header: 'Last opened', className: 'projects-nowrap muted', cell: p => when(p.lastOpenedAt) },
    { key: 'actions', header: <span className="sr-only">Actions</span>, className: 'projects-actions', cell: p => {
      const at = projects.indexOf(p);
      return <>
       <Button variant="ghost" size="icon" aria-label={`Move ${p.alias} up`} title={readOnly} disabled={!writable || at === 0} onClick={() => void move(p, -1)}><ArrowUp size={14} /></Button>
       <Button variant="ghost" size="icon" aria-label={`Move ${p.alias} down`} title={readOnly} disabled={!writable || at === projects.length - 1} onClick={() => void move(p, 1)}><ArrowDown size={14} /></Button>
       <Button variant="ghost" size="small" aria-label={`Details for ${p.alias}`} onClick={() => setDetails(p.id)}>Details</Button>
       {p.status === 'ready' ? <Button variant="outline" size="small" aria-label={`Open ${p.alias}`} onClick={() => navigate(openPath(p))}>Open<ArrowRight size={13} /></Button> : <Button variant="outline" size="small" title={readOnly} disabled={!writable} onClick={() => setDialog({ kind: 'repair', project: p })}><Wrench size={13} />Repair path</Button>}
      </>;
    } }
  ];
  return <div className="hub">
   {header}
   {data.registry.notice && <div className={`hub-banner ${writable ? '' : 'hub-banner-warn'}`} role={writable ? 'status' : 'alert'}><AlertTriangle size={15} /><span>{data.registry.notice}</span></div>}
   {env.dataDirShared && <div className="hub-banner hub-banner-warn" role="alert"><AlertTriangle size={15} /><span><strong>TF_DATA_DIR is an absolute path.</strong> Every project would share one data directory, so Hub workspaces cannot be read. Restart Hclaro without it, or with a relative path.</span></div>}
   {env.workspaceInherited && <div className="hub-banner" role="status"><Layers3 size={15} /><span>Hclaro was started with <code>TF_WORKSPACE</code>{env.inheritedWorkspace ? <> set to <code>{env.inheritedWorkspace}</code></> : ''}. Hub workspaces replace it for each command they run; the project opened from the terminal still uses it.</span></div>}
   {data.launchRoot && !launchRegistered && <section className="hub-launch" aria-label="Project opened from the terminal"><span>Opened from the terminal: <span className="mono">{data.launchRoot}</span>. Save it to find it here again later.</span><Button variant="outline" size="small" onClick={() => navigate('/')}>Open<ArrowRight size={13} /></Button><Button variant="outline" size="small" disabled={!writable} title={readOnly} onClick={() => setDialog({ kind: 'add', path: data.launchRoot })}><FolderPlus size={13} />Save to Hub</Button></section>}
   {projects.length === 0 ? <EmptyState icon={<Folder size={28} />} title="No projects yet" action={<Button onClick={() => setDialog({ kind: 'add' })} disabled={!writable} title={readOnly}><Plus size={15} />Add project</Button>}>Add a Terraform or OpenTofu root folder to start. Hclaro saves only its path and name.</EmptyState>
    : ordered.length === 0 ? <EmptyState icon={<Search size={28} />} title="No matching projects" action={<Button variant="outline" onClick={() => setSearch('')}>Clear search</Button>}>Workspaces are searchable once a project's workspaces have been listed in this session.</EmptyState>
    : <div className="projects-table" onKeyDown={arrowKeys}><DataTable label="Projects" columns={columns} rows={ordered} rowKey={p => p.id} rowLabel={p => p.alias} active={details} /></div>}
   <Disclosure summary="How this works"><p>Listing projects reads only Hclaro's registry. Listing a project's workspaces runs <code>workspace list</code> with the CLI and opening a workspace reads its recorded state; either may contact the backend. Init and workspace changes (create, set CLI default, delete) run only after you review and confirm them. Opening a workspace never selects it, and Hclaro never refreshes state. Removing a project only forgets it here.</p></Disclosure>
   {open && <ProjectDrawer project={open} writable={writable} notify={notify} onClose={closeDetails} onRepair={() => setDialog({ kind: 'repair', project: open })} onRemove={() => setDialog({ kind: 'remove', project: open })} />}
   {dialog?.kind === 'add' && <AddProjectDialog initialPath={dialog.path} onClose={() => setDialog(undefined)} onAdded={p => {
        setDialog(undefined);
        notify(`${p.alias} added`);
        void refresh();
      }} />}
   {dialog?.kind === 'repair' && <TextDialog title="Repair project path" label="New absolute path" initial={dialog.project.rootPath} action="Save path" mono description="Point this entry at the folder's new location. The folder is checked the same way as a new project." onClose={() => setDialog(undefined)} onSubmit={async rootPath => {
        await send('PATCH', `projects/${dialog.project.id}`, { rootPath });
        setDialog(undefined);
        notify('Project path updated');
        await refresh();
      }} />}
   {dialog?.kind === 'remove' && <RemoveDialog project={dialog.project} onClose={() => setDialog(undefined)} onRemoved={warning => {
        setDialog(undefined);
        setDetails(undefined);
        notify(warning ? `Project removed from Hclaro. ${warning}` : 'Project removed from Hclaro');
        client.removeQueries({ queryKey: ['context-open', dialog.project.id] });
        void refresh();
      }} />}
  </div>;
}

// The project details drawer. Workspaces are listed with the CLI only when asked (or already
// listed in this session); set-default and delete open the reviewed, confirmed dialog.
function ProjectDrawer({ project: p, writable, notify, onClose, onRepair, onRemove }: {
  project: Project;
  writable: boolean;
  notify: (text: string) => void;
  onClose: () => void;
  onRepair: () => void;
  onRemove: () => void;
}) {
  const client = useQueryClient();
  const nameId = useId();
  const [name, setName] = useState(p.alias);
  const [renaming, setRenaming] = useState(false);
  const [renameError, setRenameError] = useState('');
  const [workspaceDialog, setWorkspaceDialog] = useState<{ action: WorkspaceAction; name: string }>();
  const profile = useProfileSelection(p.id);
  const ready = p.status === 'ready';
  const inspect = useQuery({ queryKey: ['inspect', p.id], queryFn: () => send<Project>('POST', `projects/${p.id}/inspect`, {}), enabled: false, retry: false });
  useEffect(() => {
    if (inspect.data) void client.invalidateQueries({ queryKey: ['projects'] });
  }, [inspect.data, client]);
  // Git is never run from here: the branch shows only when the Git page read it in this session.
  const git = client.getQueryData<GitContext>(['git-status', gitPrefix(p.id)]);
  const inspection = inspect.data?.inspection ?? p.inspection;
  const canList = ready && !p.busy && !workspaceDialog;
  async function rename() {
    setRenaming(true);
    setRenameError('');
    try {
      await send('PATCH', `projects/${p.id}`, { alias: name.trim() });
      notify('Project renamed');
      await client.invalidateQueries({ queryKey: ['projects'] });
    } catch (error) {
      setRenameError(errorMessage(error));
    } finally {
      setRenaming(false);
    }
  }
  return <Drawer label="Project details" onClose={onClose}>
   <div className="inspector-header"><span><FolderOpen size={15} aria-hidden="true" />Project details</span><Button variant="ghost" size="icon" aria-label="Close project details" onClick={onClose}><X size={16} /></Button></div>
   <div className="inspector-body project-drawer">
    <form className="project-rename" onSubmit={e => {
        e.preventDefault();
        void rename();
      }}>
     <label htmlFor={nameId}>Name</label>
     <div><input id={nameId} value={name} onChange={e => setName(e.target.value)} maxLength={80} autoComplete="off" disabled={!writable} /><Button type="submit" variant="outline" disabled={!writable || renaming || !name.trim() || name.trim() === p.alias} title={writable ? undefined : 'The project registry is read-only.'}>Rename</Button></div>
     {renameError && <p className="dialog-error" role="alert">{renameError}</p>}
    </form>
    <div className="project-root"><span className="project-label">Root folder</span><span className="mono">{p.rootPath}</span>{!ready && <Button variant="outline" size="small" disabled={!writable} title={writable ? undefined : 'The project registry is read-only.'} onClick={onRepair}><Wrench size={13} />Repair path…</Button>}</div>
    {p.problem && <p className="hub-problem" role="note"><AlertTriangle size={13} />{p.problem}</p>}
    <dl className="project-facts">
     <dt>Status</dt><dd>{projectStatus(p).label}</dd>
     <dt>Engine</dt><dd>{engineText(p)}</dd>
     <dt>Initialized</dt><dd>{ready ? p.initialized ? 'Yes' : 'Not detected' : '—'}</dd>
     <dt>Backend</dt><dd>{ready ? `${p.backend} · ${p.executionMode}` : '—'}</dd>
     <dt>Git</dt><dd className={git?.head?.branch ? 'mono' : 'muted'} title={git ? undefined : 'Open the Git page to read the branch. Hclaro does not run git from this list.'}>{git?.head ? `${git.head.branch || 'detached'}${git.head.commit ? ` @ ${shortCommit(git.head.commit)}` : ''}` : 'Not read yet'}</dd>
     <dt>Last opened</dt><dd>{when(p.lastOpenedAt)}</dd>
    </dl>
    <section className="project-workspaces" aria-labelledby={`${nameId}-ws`}>
     <div className="project-workspaces-head"><span className="project-label" id={`${nameId}-ws`}>Workspaces</span>{ready && <Button variant="ghost" size="small" disabled={!canList || inspect.isFetching} title={p.busy ? 'A command is running for this project.' : 'Runs workspace list with the CLI. This may contact the backend.'} onClick={() => void inspect.refetch()}><RefreshCcw size={13} className={inspect.isFetching ? 'spin' : undefined} />{inspection ? 'List again' : 'List workspaces'}</Button>}</div>
     {!ready ? <p className="muted">Workspaces are listed once the folder is available.</p>
      : inspect.isFetching && !inspection ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Running <code>workspace list</code>…</p>
      : inspect.error ? <p className="hub-problem" role="alert"><AlertTriangle size={13} />{errorMessage(inspect.error)}</p>
      : inspection?.diagnostic ? <p className="hub-problem" role="note"><AlertTriangle size={13} />{inspection.diagnostic.code === 'uninitialized' ? 'Backend initialization was not detected. Review and run init from the project page.' : inspection.diagnostic.message}</p>
      : !inspection ? <p className="muted">Not listed yet in this session.</p>
      : inspection.workspaces.length === 0 ? <p className="muted">The CLI returned no workspace names Hclaro can display.</p>
      : <ul className="project-workspace-list">{inspection.workspaces.map(ws => {
          const isDefault = ws === inspection.cliDefault;
          const changeBlocked = !p.workspaceActions?.change ? p.workspaceActions?.reason || 'Workspace changes are unavailable.' : p.busy ? 'A command is running for this project.' : undefined;
          const deleteBlocked = changeBlocked || (!p.workspaceActions?.delete ? p.workspaceActions?.deleteReason : ws === 'default' ? 'The default workspace cannot be deleted.' : isDefault ? 'This is the CLI default. Set another workspace as CLI default first.' : !inspection.cliDefault ? 'The CLI default is unknown.' : undefined);
          return <li key={ws}>
           <button type="button" className="link mono" title="Open this workspace in this tab. It reads its recorded state and never changes the CLI default." onClick={() => navigate(contextPath(p.id, ws))}>{ws}</button>
           {isDefault && <Chip title="Selected in this project's CLI data directory. Opening another workspace here does not change it.">CLI default</Chip>}
           {ws === p.lastWorkspace && <span className="muted">last opened in Hclaro</span>}
           {productionLike(ws) && <ProductionBadge />}
           <span className="context-spacer" />
           {!isDefault && <Button variant="ghost" size="small" disabled={!!changeBlocked} title={changeBlocked || 'Runs workspace select after you review and confirm it. Affects terminals in this folder.'} aria-label={`Set ${ws} as CLI default`} onClick={() => setWorkspaceDialog({ action: 'select', name: ws })}><Terminal size={13} />Set as CLI default</Button>}
           <Button variant="ghost" size="small" className="project-delete" disabled={!!deleteBlocked} title={deleteBlocked || 'Delete this workspace if its state is empty, after you review and confirm it.'} aria-label={`Delete workspace ${ws}`} onClick={() => setWorkspaceDialog({ action: 'delete', name: ws })}><Trash2 size={13} />Delete</Button>
          </li>;
        })}</ul>}
     {inspection && inspection.hiddenWorkspaces > 0 && <p className="muted">{inspection.hiddenWorkspaces} workspace name{inspection.hiddenWorkspaces === 1 ? '' : 's'} with unusual characters {inspection.hiddenWorkspaces === 1 ? 'is' : 'are'} not shown.</p>}
     {ready && <p className="muted">Create workspaces from the workspace switcher in the top bar. Init, execution profiles and a separate session are on the <button type="button" className="link" onClick={() => navigate(projectPath(p.id))}>project page</button>.</p>}
    </section>
   </div>
   <div className="drift-drawer-footer">
    <Button variant="ghost" className="project-delete" disabled={!writable} title={writable ? 'Removes it from Hclaro only; files on disk are not touched.' : 'The project registry is read-only.'} onClick={onRemove}><Trash2 size={14} />Remove from Hclaro</Button>
    <Button className="drift-run-plan" disabled={!ready} title={ready ? undefined : statusLabel[p.status]} onClick={() => navigate(openPath(p))}>Open project<ArrowRight size={14} /></Button>
   </div>
   {workspaceDialog && <WorkspaceDialog projectId={p.id} profileId={profile.selected} action={workspaceDialog.action} initialName={workspaceDialog.name} onClose={() => {
        setWorkspaceDialog(undefined);
        void client.invalidateQueries({ queryKey: ['projects'] });
        void inspect.refetch();
      }} onFinished={() => void client.invalidateQueries({ queryKey: ['projects'] })} onOpen={ws => {
        setWorkspaceDialog(undefined);
        navigate(contextPath(p.id, ws));
      }} />}
  </Drawer>;
}

export function AddProjectDialog({ initialPath = '', onClose, onAdded }: {
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
  const first = useRef<HTMLInputElement>(null);
  const form = useId();
  async function submit() {
    setBusy(true);
    setError('');
    try {
      onAdded(await send<Project>('POST', 'projects', { path: path.trim(), alias: alias.trim(), engine }));
    } catch (e) {
      setError(e instanceof ApiError && e.code === 'duplicate_project' ? 'This folder is already in Hclaro.' : errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return <Dialog title="Add a project" wide onClose={onClose} initial={first} footer={<>
    <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
    <Button type="submit" form={form} disabled={busy || !path.trim()} title={path.trim() ? undefined : 'Choose a root folder first.'}><Plus size={14} />{busy ? 'Adding…' : 'Add project'}</Button>
   </>}>
   <p>Choose the root folder of one Terraform or OpenTofu configuration. Hclaro saves its path, name and engine choice in your user configuration directory. Nothing in the folder is run or changed.</p>
   <form id={form} className="hub-form" onSubmit={e => {
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
   </form>
  </Dialog>;
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
      if (live) setError(errorMessage(e));
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
          setError(errorMessage(err));
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

function RemoveDialog({ project, onClose, onRemoved }: { project: Project; onClose: () => void; onRemoved: (warning: string) => void }) {
  const [error, setError] = useState('');
  const cancel = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, cancel, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="remove-title">
    <div className="dialog-header"><h2 id="remove-title">Remove {project.alias} from Hclaro?</h2></div>
    <p>Hclaro forgets this entry. <strong>Nothing on disk or in your backend is deleted</strong>: configuration, <code>.terraform</code>, state and workspaces stay as they are. Open workspace views of this project close.</p>
    <p className="mono hub-path">{project.rootPath}</p>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button ref={cancel} variant="ghost" onClick={onClose}>Cancel</Button><Button variant="outline" onClick={async () => {
          try {
            const res = await send<{ warning?: string }>('DELETE', `projects/${project.id}`, {});
            onRemoved(res.warning ?? '');
          } catch (e) {
            setError(errorMessage(e));
          }
        }}><Trash2 size={14} />Remove from Hclaro</Button></div>
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
  // The execution profile this tab uses for this project; init and workspace commands use it.
  const profile = useProfileSelection(id);
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
    // An init of the project opened from the terminal makes the server read it again.
    void client.invalidateQueries({ queryKey: ['workspace'] });
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
  if (!current) return <ErrorState inline icon={AlertTriangle} title="Project not found" message="This project is not registered in the Hub. It may have been removed."><Button variant="outline" onClick={() => navigate(globalPages.hub.path)}><ArrowLeft size={14} />Back to projects</Button></ErrorState>;
  const env = list.data?.environment;
  // Tools open in the last workspace used here when it still exists, else the CLI default.
  const listed = inspection?.workspaces || [];
  const toolsWorkspace = current.lastWorkspace && listed.includes(current.lastWorkspace) ? current.lastWorkspace : inspection?.cliDefault || 'default';
  return <div className="hub project-view">
   <div className="hub-toolbar"><Button variant="ghost" onClick={() => navigate(globalPages.hub.path)}><ArrowLeft size={14} />All projects</Button><span className="context-spacer" />{current.status === 'ready' && <Button variant="outline" onClick={() => void inspect.refetch()} disabled={inspect.isFetching}><RefreshCcw size={14} className={inspect.isFetching ? 'spin' : undefined} />List workspaces again</Button>}</div>
   {current.busy === 'workspace' && !workspaceDialog && <div className="hub-banner" role="status"><LoaderCircle className="spin" size={15} /><span><strong>A workspace command is running.</strong> Workspaces can be listed when it finishes.</span></div>}
   {current.busy === 'init' && !initOpen && <div className="hub-banner" role="status"><LoaderCircle className="spin" size={15} /><span><strong>Initialization is running.</strong> Workspaces can be listed when it finishes.</span><span className="context-spacer" /><Button size="small" variant="outline" onClick={() => setInitOpen(true)}>Show progress</Button></div>}
   <section className="surface"><div className="surface-heading"><div><h2>{current.alias} {productionLike(current.alias) && <ProductionBadge />}</h2><p className="mono">{current.rootPath}</p></div>{current.status === 'ready' ? <div className="hub-actions"><Button size="small" variant="outline" onClick={() => navigate(contextPath(id, toolsWorkspace, 'Tools'))} title={`Validate, format check, providers and outputs. Opens in workspace ${toolsWorkspace}.`}><Wrench size={13} />Project tools</Button><Button size="small" variant={current.initialized ? 'outline' : 'default'} onClick={() => setInitOpen(true)} disabled={!!current.busy && current.busy !== 'init'}><Play size={13} />{current.initialized ? 'Run init again…' : 'Initialize…'}</Button></div> : <FolderOpen size={18} />}</div>
    {current.status === 'ready' && <div className="project-profile"><ContextSelector projectId={id} selection={profile} workspaces={inspection?.workspaces} /></div>}
    <dl className="metadata"><div><dt>Engine</dt><dd>{inspection?.engine ? `${engineLabel(inspection.engine, 'Automatic')} ${inspection.version || ''}` : engineLabel(current.enginePreference, 'Automatic')}</dd></div><div><dt>Initialized</dt><dd>{current.initialized ? 'Yes' : 'Not detected'}</dd></div><div><dt>Backend type</dt><dd>{current.backend}</dd></div><div><dt>Execution mode</dt><dd>{current.executionMode}</dd></div><div><dt>Data directory</dt><dd className="mono">{env?.dataDir || '.terraform'}</dd></div><div><dt>Provider lockfile</dt><dd>{current.providerLock ? 'Present' : 'Not detected'}</dd></div></dl>
    {current.status === 'ready' && <SeparateSession rootPath={current.rootPath} engine={current.enginePreference} />}
   </section>
   <section aria-labelledby="workspaces-title"><div className="workspace-heading"><h2 className="hub-section" id="workspaces-title">CLI workspaces</h2>{current.status === 'ready' && <><label className="workspace-advanced"><input type="checkbox" checked={advanced} onChange={e => setAdvanced(e.target.checked)} />Advanced actions</label><Button size="small" variant="outline" disabled={!current.workspaceActions?.change || !!current.busy || !inspection || !!inspection.diagnostic} title={current.workspaceActions?.change ? undefined : current.workspaceActions?.reason} onClick={() => setWorkspaceDialog({ action: 'new' })}><Plus size={13} />New workspace…</Button></>}</div>
    {current.status !== 'ready' ? <ErrorState inline icon={AlertTriangle} iconSize={24} title={statusLabel[current.status]} message={current.problem} retry={() => navigate(globalPages.hub.path)} retryLabel="Repair or remove in the Hub" /> : inspect.isPending || inspect.isFetching && !inspection ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><h2>Listing workspaces</h2><p>Running <code>workspace list</code> through the local CLI. This may contact the backend. No workspace is selected or created.</p></div> : inspect.error ? <ErrorState inline icon={AlertTriangle} iconSize={24} title="Workspaces unavailable" message={errorMessage(inspect.error)} retry={() => void inspect.refetch()} retryLabel="Try again" /> : inspection?.diagnostic ? <ErrorState icon={AlertTriangle} iconSize={24} title={inspection.diagnostic.code === 'uninitialized' ? 'Initialize this project first' : inspection.diagnostic.code === 'missing_cli' ? 'An IaC engine is needed' : 'Workspaces unavailable'} message={inspection.diagnostic.code === 'uninitialized' ? 'Backend initialization was not detected. Review and run init here, or run it yourself in a terminal. Nothing was initialized automatically.' : inspection.diagnostic.message} note="Nothing was initialized or changed.">{inspection.diagnostic.code === 'uninitialized' && <Button onClick={() => setInitOpen(true)}><Play size={14} />Review and initialize…</Button>}</ErrorState> : inspection && inspection.workspaces.length === 0 ? <div className="empty-state"><Layers3 size={24} /><h2>No workspaces listed</h2><p>The CLI returned no workspace names Hclaro can display.</p></div> : inspection && <>
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
   {workspaceDialog && <WorkspaceDialog projectId={id} profileId={profile.selected} action={workspaceDialog.action} initialName={workspaceDialog.name} onClose={() => {
        setWorkspaceDialog(undefined);
        void client.invalidateQueries({ queryKey: ['projects'] });
        void client.invalidateQueries({ queryKey: ['inspect', id] });
      }} onFinished={workspaceFinished} onOpen={ws => {
        setWorkspaceDialog(undefined);
        navigate(contextPath(id, ws));
      }} />}
   {initOpen && <InitWizard projectId={id} profileId={profile.selected} onClose={() => {
        setInitOpen(false);
        void client.invalidateQueries({ queryKey: ['projects'] });
      }} onFinished={initFinished} />}
  </div>;
}

// A folder name can come from a cloned repository, so nothing in it may expand. Windows paths
// become a PowerShell single-quoted literal (no $, ` or % expansion), doubling every quote
// PowerShell treats as a single quote; POSIX paths are single-quoted for sh.
const windowsPath = (path: string) => /^[A-Za-z]:\\|^\\\\/.test(path);
function shellQuote(path: string) {
  return windowsPath(path) ? `'${path.replace(/['\u2018\u2019\u201A\u201B]/g, q => q + q)}'` : `'${path.replaceAll("'", `'\\''`)}'`;
}

// An optional fallback: a second Hclaro process opened from one folder. It is not the Hub's
// execution path and shares no tabs, approvals or root claims with this session.
function SeparateSession({ rootPath, engine }: { rootPath: string; engine?: string }) {
  const [note, setNote] = useState('');
  const command = `hclaro --path ${shellQuote(rootPath)}${engine === 'terraform' || engine === 'tofu' ? ` --engine ${engine}` : ''}`;
  return <details className="separate-session">
   <summary>Open in a separate session (optional)</summary>
   <p>Starts another Hclaro process for this folder only, pinned to its CLI default workspace. It is an optional fallback; Hub tabs keep their own workspace and profile and are not affected.</p>
   <p><strong>The two sessions do not coordinate.</strong> Do not plan, apply, init or change workspaces of this folder in both at the same time.</p>
   <p className="muted">Run in {windowsPath(rootPath) ? 'PowerShell' : 'a POSIX shell (sh, bash or zsh)'}.</p>
   <div className="init-command"><code>{command}</code><Button size="small" variant="outline" onClick={() => void navigator.clipboard.writeText(command).then(() => setNote('Command copied'), () => setNote('Clipboard is unavailable. Select and copy the command manually.'))}><Terminal size={13} />Copy command</Button><span className="muted" role="status">{note}</span></div>
  </details>;
}
