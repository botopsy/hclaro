import { useEffect, useMemo, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, ArrowLeftRight, ArrowRight, Braces, ChevronLeft, ChevronRight, CircleDashed, CircleHelp, Copy, Database, Download, Equal, ExternalLink, GitCompareArrows, History, Info, ListFilter, LoaderCircle, LockKeyhole, Network, Pencil, Plus, RefreshCcw, Search, ShieldCheck, Trash2, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, download, openInNewTab, post } from '../api';
import type { Comparison, ComparisonAttribute, ComparisonDetail, ComparisonMatch, ComparisonRule, ComparisonSide, ComparisonTarget, MatchStatus, Project, StatusCounts, TargetStatus } from '../types';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { JsonTree } from '../components/JsonTree';
import { useModal } from '../components/useModal';
import { contextPath, productionLike } from '../router';
import { clock, engineLabel } from '../plans/labels';
import { ProductionBadge, projectsQuery } from '../hub/HubView';
import { profilesQuery } from '../profiles/selection';
import { RecordedContext } from '../profiles/ContextSummary';

const statuses: Record<MatchStatus, { label: string; short: string; icon: LucideIcon; tone: string; key: keyof StatusCounts }> = {
  'attribute-different': { label: 'Attribute differences', short: 'Different', icon: Pencil, tone: 'update', key: 'different' },
  'only-left': { label: 'Only in left', short: 'Only left', icon: ArrowLeft, tone: 'neutral', key: 'onlyLeft' },
  'only-right': { label: 'Only in right', short: 'Only right', icon: ArrowRight, tone: 'neutral', key: 'onlyRight' },
  unknown: { label: 'Unknown', short: 'Unknown', icon: CircleDashed, tone: 'muted', key: 'unknown' },
  uncomparable: { label: 'Uncomparable', short: 'Uncomparable', icon: CircleHelp, tone: 'warn', key: 'uncomparable' },
  same: { label: 'Same', short: 'Same', icon: Equal, tone: 'create', key: 'same' }
};
const order: MatchStatus[] = ['attribute-different', 'only-left', 'only-right', 'unknown', 'uncomparable', 'same'];
const statusHelp: Record<MatchStatus, string> = {
  'attribute-different': 'Matched on both sides; at least one visible attribute value differs.',
  'only-left': 'Recorded only in the left workspace. Addresses match exactly: a renamed address shows here and as only-right unless a mapping rule pairs them.',
  'only-right': 'Recorded only in the right workspace. Addresses match exactly: a renamed address shows here and as only-left unless a mapping rule pairs them.',
  unknown: 'No visible difference, but redacted values were not compared, so equality cannot be confirmed.',
  uncomparable: 'Matched by address, but the type, mode or provider differs, or a mapping rule conflicts.',
  same: 'Every compared attribute value is equal, after your rules.'
};

type Draft = { left: TargetDraft; right: TargetDraft; rules: ComparisonRule[] };
// profileId undefined means "the project's default profile", "" means no profile.
type TargetDraft = { projectId: string; workspace: string; profileId?: string };
const emptyDraft: Draft = { left: { projectId: '', workspace: '' }, right: { projectId: '', workspace: '' }, rules: [] };
const hex = /^[0-9a-f]{32}$/;
const workspacePattern = /^[A-Za-z0-9_-]{1,90}$/;

// The draft is kept per browser tab. It holds IDs, workspace names and rule text, which the
// server refuses when it looks like a credential.
function readDraft(): Draft {
  try {
    const saved = JSON.parse(sessionStorage.getItem('hclaro.compare.draft') || 'null') as Draft | null;
    if (!saved || typeof saved !== 'object' || !Array.isArray(saved.rules)) return emptyDraft;
    const target = (t?: TargetDraft): TargetDraft => ({
      projectId: t && hex.test(t.projectId) ? t.projectId : '',
      workspace: t && typeof t.workspace === 'string' ? t.workspace.slice(0, 90) : '',
      profileId: t?.profileId === '' || t?.profileId && hex.test(t.profileId) ? t.profileId : undefined
    });
    const rules = saved.rules.slice(0, 50).filter(r => r && ['map', 'ignore', 'substitute'].includes(r.kind)).map(r => ({ kind: r.kind, left: str(r.left), right: str(r.right), path: str(r.path), resourceType: str(r.resourceType) }));
    return { left: target(saved.left), right: target(saved.right), rules };
  } catch {
    return emptyDraft;
  }
}
function str(v: unknown) {
  return typeof v === 'string' ? v.slice(0, 256) : '';
}
function writeDraft(draft: Draft) {
  try {
    sessionStorage.setItem('hclaro.compare.draft', JSON.stringify(draft));
  } catch {/* Without storage the draft lasts until the page reloads. */}
}
function readComparisonId() {
  try {
    const id = sessionStorage.getItem('hclaro.compare.id') || '';
    return hex.test(id) ? id : undefined;
  } catch {
    return undefined;
  }
}
function writeComparisonId(id: string) {
  try {
    sessionStorage.setItem('hclaro.compare.id', id);
  } catch {/* Session only. */}
}
// Rules are sent without empty optional fields.
function cleanRules(rules: ComparisonRule[]): ComparisonRule[] {
  return rules.map(r => {
    const out: ComparisonRule = { kind: r.kind };
    for (const key of ['left', 'right', 'path', 'resourceType'] as const) {
      const value = r[key]?.trim();
      if (value) out[key] = value;
    }
    return out;
  });
}

export function CompareView({ copy }: { copy: (value: string, label: string) => Promise<void> }) {
  const client = useQueryClient();
  const [draft, setDraftState] = useState<Draft>(readDraft);
  const [comparisonId, setComparisonIdState] = useState<string | undefined>(readComparisonId);
  const [preflight, setPreflight] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState('');
  const [ruleError, setRuleError] = useState<{ index: number; reason: string }>();
  const projects = useQuery(projectsQuery);
  const leftProfiles = useQuery(profilesQuery(draft.left.projectId || undefined));
  const rightProfiles = useQuery(profilesQuery(draft.right.projectId || undefined));
  const history = useQuery({ queryKey: ['comparisons'], queryFn: () => api<{ comparisons: Comparison[] }>('comparisons') });
  function setDraft(next: Draft) {
    setDraftState(next);
    writeDraft(next);
    setRuleError(undefined);
  }
  function setComparisonId(id: string) {
    setComparisonIdState(id);
    writeComparisonId(id);
  }
  const effective = (t: TargetDraft, defaultId?: string): ComparisonTarget => ({ projectId: t.projectId, workspace: t.workspace.trim(), ...((t.profileId ?? defaultId ?? '') ? { profileId: t.profileId ?? defaultId } : {}) });
  const left = effective(draft.left, leftProfiles.data?.defaultId);
  const right = effective(draft.right, rightProfiles.data?.defaultId);
  const sameTarget = left.projectId === right.projectId && left.workspace === right.workspace && (left.profileId ?? '') === (right.profileId ?? '');
  const ready = [left, right].every(t => hex.test(t.projectId) && workspacePattern.test(t.workspace)) && !sameTarget;
  async function start(reload: boolean) {
    setStarting(true);
    setStartError('');
    try {
      const started = await post<Comparison>('comparisons', { left, right, rules: cleanRules(draft.rules), reload, acknowledgeStateRead: true });
      setPreflight(false);
      setComparisonId(started.id);
      client.setQueryData(['comparison', started.id], started);
      void client.invalidateQueries({ queryKey: ['comparisons'] });
      void client.invalidateQueries({ queryKey: ['compare-target'] });
    } catch (e) {
      const message = e instanceof Error ? e.message : 'The comparison could not be started.';
      const match = /^Rule (\d+):/.exec(message);
      if (match) setRuleError({ index: Number(match[1]) - 1, reason: message });
      setStartError(message);
    } finally {
      setStarting(false);
    }
  }
  const projectList = projects.data?.projects ?? [];
  return <div className="plans compare-page">
   <section className="compare-targets" aria-label="Comparison targets">
    <TargetCard side="left" draft={draft.left} projects={projectList} onChange={t => setDraft({ ...draft, left: t })} />
    <Button variant="ghost" size="icon" className="compare-swap" aria-label="Swap left and right" title="Swap left and right" onClick={() => setDraft({ ...draft, left: draft.right, right: draft.left })}><ArrowLeftRight size={16} /></Button>
    <TargetCard side="right" draft={draft.right} projects={projectList} onChange={t => setDraft({ ...draft, right: t })} />
   </section>
   {projects.data && projectList.length === 0 && <div className="plan-warning" role="note"><Info size={15} /><span>Register projects in the Workspace Hub first. Comparisons read Hub projects only.</span></div>}
   <RulesEditor rules={draft.rules} onChange={rules => setDraft({ ...draft, rules })} error={ruleError} />
   <div className="plans-toolbar">
    <Button onClick={() => { setStartError(''); setPreflight(true); }} disabled={!ready}><GitCompareArrows size={14} />Compare…</Button>
    {history.data && history.data.comparisons.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Comparison history</span><select aria-label="Comparison history" value={comparisonId ?? ''} onChange={e => setComparisonId(e.target.value)}>{!history.data.comparisons.some(c => c.id === comparisonId) && <option value="">Choose a comparison</option>}{history.data.comparisons.map(c => <option key={c.id} value={c.id}>{clock(c.createdAt)} · {c.left.alias}/{c.left.target.workspace} ↔ {c.right.alias}/{c.right.target.workspace}{c.status === 'stale' ? ' · stale' : ''}</option>)}</select></label>}
    <span className="toolbar-note" role="status">{sameTarget && left.projectId ? 'Choose a different workspace, project or profile for one side.' : !ready ? 'Choose a project and workspace for both sides.' : 'Nothing is read until you compare.'}</span>
   </div>
   {startError && !preflight && <p className="dialog-error" role="alert">{startError}</p>}
   {comparisonId ? <ComparisonResult key={comparisonId} id={comparisonId} copy={copy} compareAgain={() => setPreflight(true)} canCompare={ready} /> : <div className="empty-state"><GitCompareArrows size={30} /><h2>Compare two workspaces</h2><p>Choose a left and a right workspace (for example <strong>dev</strong> and <strong>prod</strong>, or the same stack in two project roots). Hclaro reads each side's recorded state and lists what is only on one side, what differs and what could not be compared.</p></div>}
   {preflight && <ComparePreflight left={left} right={right} projects={projectList} rules={draft.rules.length} busy={starting} error={startError} onConfirm={reload => void start(reload)} onClose={() => { setPreflight(false); setStartError(''); }} />}
  </div>;
}

function TargetCard({ side, draft, projects, onChange }: { side: 'left' | 'right'; draft: TargetDraft; projects: Project[]; onChange: (t: TargetDraft) => void }) {
  const client = useQueryClient();
  const project = projects.find(p => p.id === draft.projectId);
  const profiles = useQuery(profilesQuery(draft.projectId || undefined));
  const profileId = draft.profileId ?? profiles.data?.defaultId ?? '';
  const [listing, setListing] = useState(false);
  const [listError, setListError] = useState('');
  const validWorkspace = workspacePattern.test(draft.workspace.trim());
  const target = useQuery({
    queryKey: ['compare-target', draft.projectId, draft.workspace.trim(), profileId],
    queryFn: () => api<TargetStatus>(`comparisons/target?projectId=${draft.projectId}&workspace=${encodeURIComponent(draft.workspace.trim())}${profileId ? `&profileId=${profileId}` : ''}`),
    enabled: !!project && validWorkspace,
    refetchInterval: q => q.state.data?.status === 'loading' ? 1000 : false
  });
  const names = project?.inspection?.workspaces ?? [];
  const label = side === 'left' ? 'Left' : 'Right';
  const listId = `compare-workspaces-${side}`;
  async function listWorkspaces() {
    if (!project) return;
    setListing(true);
    setListError('');
    try {
      await post(`projects/${project.id}/inspect`, {});
      await client.invalidateQueries({ queryKey: ['projects'] });
    } catch (e) {
      setListError(e instanceof Error ? e.message : 'Workspaces could not be listed.');
    } finally {
      setListing(false);
    }
  }
  const profileList = profiles.data?.profiles ?? [];
  const chosenProfile = profileList.find(p => p.id === profileId);
  return <section className={`compare-target compare-${side}`} aria-label={`${label} target`}>
   <div className="compare-target-head"><span className="eyebrow">{label.toUpperCase()}</span>{productionLike(project?.alias, draft.workspace) && <ProductionBadge />}</div>
   <label className="field"><span>Project</span><select aria-label={`${label} project`} value={draft.projectId} onChange={e => onChange({ projectId: e.target.value, workspace: '', profileId: undefined })}><option value="">Choose a project…</option>{projects.map(p => <option key={p.id} value={p.id} disabled={p.status !== 'ready'}>{p.alias}{p.status !== 'ready' ? ` (${p.problem || p.status})` : ''}</option>)}</select></label>
   <div className="field"><label htmlFor={`${listId}-input`}>Workspace</label><div className="field-row"><input id={`${listId}-input`} aria-label={`${label} workspace`} list={listId} value={draft.workspace} disabled={!project} maxLength={90} spellCheck={false} placeholder={project?.inspection?.cliDefault || 'default'} onChange={e => onChange({ ...draft, workspace: e.target.value })} /><datalist id={listId}>{names.map(n => <option key={n} value={n} />)}</datalist><Button variant="outline" size="small" disabled={!project || listing} onClick={() => void listWorkspaces()} title="Runs version and workspace list for this project. Read-only."><RefreshCcw size={12} className={listing ? 'spin' : undefined} />List</Button></div>
    {draft.workspace && !validWorkspace && <span className="field-error" role="alert">Use 1–90 letters, digits, - or _.</span>}
    {listError && <span className="field-error" role="alert">{listError}</span>}
    {project && !project.inspection && <span className="muted field-hint">Type a workspace name, or list the project's workspaces.</span>}
    {project?.inspection?.diagnostic && <span className="field-error">{project.inspection.diagnostic.message}</span>}
   </div>
   <label className="field"><span>Execution profile</span><select aria-label={`${label} execution profile`} value={profileId} disabled={!project} onChange={e => onChange({ ...draft, profileId: e.target.value })}><option value="">No profile (inherited environment)</option>{profileList.map(p => <option key={p.id} value={p.id}>{p.name}{p.workspace ? ` · ${p.workspace} only` : ''}{p.id === profiles.data?.defaultId ? ' · default' : ''}</option>)}</select></label>
   {chosenProfile?.workspace && validWorkspace && chosenProfile.workspace !== draft.workspace.trim() && <span className="field-error" role="alert">Profile {chosenProfile.name} selects workspace {chosenProfile.workspace}.</span>}
   {project && <dl className="compare-identity">
    <div><dt>Root</dt><dd className="mono" title={project.rootPath}>{project.rootPath}</dd></div>
    <div><dt>Engine</dt><dd>{target.data?.engine ? `${engineLabel(target.data.engine)} ${target.data.version || ''}` : project.inspection?.engine ? `${engineLabel(project.inspection.engine)} ${project.inspection.version || ''}` : project.enginePreference ? engineLabel(project.enginePreference) : 'Detected when read'}</dd></div>
    <div><dt>Backend</dt><dd><Database size={12} />{project.backend} · {project.executionMode}{!project.initialized && ' · not initialized'}</dd></div>
    <div><dt>State read</dt><dd>{!validWorkspace ? <span className="muted">Choose a workspace</span> : target.data?.open ? target.data.capturedAt ? <>Last read {clock(target.data.capturedAt)} in this session</> : target.data.status === 'loading' ? 'Reading…' : <span className="muted">Last read did not succeed</span> : <span className="muted">Not read in this session</span>}</dd></div>
   </dl>}
  </section>;
}

function RulesEditor({ rules, onChange, error }: { rules: ComparisonRule[]; onChange: (rules: ComparisonRule[]) => void; error?: { index: number; reason: string } }) {
  const [open, setOpen] = useState(rules.length > 0);
  function update(i: number, patch: Partial<ComparisonRule>) {
    onChange(rules.map((r, j) => j === i ? { ...r, ...patch } : r));
  }
  function add(kind: ComparisonRule['kind']) {
    onChange([...rules, { kind, left: '', right: '', path: '', resourceType: '' }]);
    setOpen(true);
  }
  return <details className="compare-rules" open={open} onToggle={e => setOpen((e.target as HTMLDetailsElement).open)}>
   <summary><ListFilter size={14} /><strong>Matching and ignore rules</strong><span className="muted">{rules.length ? `${rules.length} rule${rules.length === 1 ? '' : 's'}` : 'Exact addresses, every attribute'}</span></summary>
   <p className="info-line"><TriangleAlert size={13} />Ignore and substitution rules can hide meaningful divergence. They apply only to this comparison and are kept in this browser tab, never written to disk.</p>
   {rules.length > 0 && <ol className="rule-list">{rules.map((r, i) => <li key={i} className={error?.index === i ? 'rule-invalid' : undefined}>
    <span className="rule-number">{i + 1}</span>
    {r.kind === 'map' ? <><span className="rule-kind">Map left</span><input aria-label={`Rule ${i + 1} left address`} placeholder="module.app" value={r.left ?? ''} onChange={e => update(i, { left: e.target.value })} spellCheck={false} /><span className="rule-kind">to right</span><input aria-label={`Rule ${i + 1} right address`} placeholder="module.application" value={r.right ?? ''} onChange={e => update(i, { right: e.target.value })} spellCheck={false} /></>
      : r.kind === 'ignore' ? <><span className="rule-kind">Ignore attribute</span><input aria-label={`Rule ${i + 1} attribute path`} placeholder="tags.Environment" value={r.path ?? ''} onChange={e => update(i, { path: e.target.value })} spellCheck={false} /><span className="rule-kind">for type</span><input aria-label={`Rule ${i + 1} resource type`} placeholder="any type" value={r.resourceType ?? ''} onChange={e => update(i, { resourceType: e.target.value })} spellCheck={false} /></>
      : <><span className="rule-kind">Treat left</span><input aria-label={`Rule ${i + 1} left value`} placeholder="dev" value={r.left ?? ''} onChange={e => update(i, { left: e.target.value })} spellCheck={false} /><span className="rule-kind">as right</span><input aria-label={`Rule ${i + 1} right value`} placeholder="prod" value={r.right ?? ''} onChange={e => update(i, { right: e.target.value })} spellCheck={false} /><span className="rule-kind">in</span><input aria-label={`Rule ${i + 1} attribute path`} placeholder="every attribute" value={r.path ?? ''} onChange={e => update(i, { path: e.target.value })} spellCheck={false} /></>}
    <Button variant="ghost" size="icon" aria-label={`Remove rule ${i + 1}`} onClick={() => onChange(rules.filter((_, j) => j !== i))}><Trash2 size={13} /></Button>
   </li>)}</ol>}
   {error && <p className="dialog-error" role="alert">{error.reason}</p>}
   <div className="rule-actions"><Button variant="outline" size="small" onClick={() => add('map')} disabled={rules.length >= 50}><Plus size={12} />Map renamed address</Button><Button variant="outline" size="small" onClick={() => add('ignore')} disabled={rules.length >= 50}><Plus size={12} />Ignore attribute</Button><Button variant="outline" size="small" onClick={() => add('substitute')} disabled={rules.length >= 50}><Plus size={12} />Treat values as equal</Button></div>
   <p className="muted rule-help">Addresses match exactly unless a mapping rule pairs them, for example <code>module.app</code> → <code>module.application</code>. Paths use the attribute rows' form: <code>region</code>, <code>tags.Environment</code>, <code>tags["cost-center"]</code>, <code>ingress[*].cidr_blocks</code>. A substitution such as <code>dev</code> → <code>prod</code> treats <code>dev-api</code> and <code>prod-api</code> as equal.</p>
  </details>;
}

function ComparePreflight({ left, right, projects, rules, busy, error, onConfirm, onClose }: { left: ComparisonTarget; right: ComparisonTarget; projects: Project[]; rules: number; busy: boolean; error: string; onConfirm: (reload: boolean) => void; onClose: () => void }) {
  const [reload, setReload] = useState(false);
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  const name = (t: ComparisonTarget) => projects.find(p => p.id === t.projectId)?.alias ?? 'Project';
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="compare-title" aria-describedby="compare-summary">
    <div className="dialog-header"><h2 id="compare-title">Compare workspaces</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="compare-summary">Compare <strong>{name(left)} / {left.workspace}</strong> (left) with <strong>{name(right)} / {right.workspace}</strong> (right){rules ? <> using {rules} rule{rules === 1 ? '' : 's'}</> : ''}.</p>
    <ul className="preflight-list">
     <li><Database size={16} /><span><strong>Reads recorded state</strong> of each side that is not already read in this session: <code>version</code>, <code>workspace list</code>, <code>workspace show</code> and <code>show -json</code> with a per-command <code>TF_WORKSPACE</code>. This may contact each backend with your existing credentials.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not plan, refresh, select a workspace or write state.</strong> Values are redacted before comparison; secrets are never compared or shown.</span></li>
    </ul>
    <label className="check-row"><input type="checkbox" checked={reload} onChange={e => setReload(e.target.checked)} /><span>Read both sides again, even if they were read in this session</span></label>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(reload)} disabled={busy}><GitCompareArrows size={14} />{busy ? 'Starting…' : 'Compare'}</Button></div>
   </div>
  </div>;
}

function StatusBadge({ value }: { value: MatchStatus }) {
  const { short, icon: Icon, tone } = statuses[value];
  return <span className={`action-badge tone-${tone}`} title={statusHelp[value]}><Icon size={12} aria-hidden="true" />{short}</span>;
}

function SideHeader({ label, side }: { label: string; side: ComparisonSide }) {
  return <div className={`compare-side-head status-${side.status}`}>
   <span className="eyebrow">{label}</span>
   <strong>{side.alias || 'Project'} / {side.target.workspace}</strong>
   <span className="muted">{side.engine ? `${engineLabel(side.engine)} ${side.version || ''}` : ''}{side.backend ? ` · ${side.backend}` : ''}{side.capturedAt ? ` · read ${clock(side.capturedAt)}` : ''}</span>
   <span className="compare-side-context"><RecordedContext context={side.executionContext} /></span>
   {side.status === 'reading' && <span className="info-line"><LoaderCircle className="spin" size={12} />Reading recorded state…</span>}
   {side.status === 'empty' && <span className="info-line"><Info size={12} />No state recorded in this workspace.</span>}
  </div>;
}

type Tab = 'Resources' | 'Modules' | 'Providers';

function ComparisonResult({ id, copy, compareAgain, canCompare }: { id: string; copy: (value: string, label: string) => Promise<void>; compareAgain: () => void; canCompare: boolean }) {
  const comparison = useQuery({
    queryKey: ['comparison', id],
    queryFn: () => api<Comparison>(`comparisons/${id}`),
    refetchInterval: q => q.state.data?.status === 'reading' ? 700 : false,
    retry: false
  });
  const [tab, setTab] = useState<Tab>('Resources');
  const [filter, setFilter] = useState<MatchStatus[]>([]);
  const [module, setModule] = useState<string>();
  const [exportError, setExportError] = useState('');
  const client = useQueryClient();
  const c = comparison.data;
  const settled = c?.status && c.status !== 'reading';
  // Once both reads finish, the target cards show the new read times.
  useEffect(() => {
    if (settled) void client.invalidateQueries({ queryKey: ['compare-target'] });
  }, [settled, client]);
  if (comparison.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading comparison…</p></div>;
  if (comparison.error || !c) return <div className="empty-state error-state" role="alert"><p>{comparison.error?.message ?? 'This comparison is not available.'}</p></div>;
  const s = c.summary;
  const counts = (status: MatchStatus) => s ? s.managed[statuses[status].key] + s.data[statuses[status].key] : 0;
  function toggle(status: MatchStatus) {
    setFilter(current => current.includes(status) ? current.filter(v => v !== status) : [...current, status]);
    setTab('Resources');
  }
  async function exportReport(format: 'json' | 'markdown') {
    setExportError('');
    try {
      await download(`comparisons/${id}/export?format=${format}`, `hclaro-comparison-${id.slice(0, 8)}.${format === 'json' ? 'json' : 'md'}`);
    } catch (e) {
      setExportError(e instanceof Error ? e.message : 'The report could not be exported.');
    }
  }
  return <div className="plan-review compare-result">
   <div className="compare-sides">
    <SideHeader label="LEFT" side={c.left} />
    <ArrowLeftRight size={16} className="compare-sides-arrow" aria-hidden="true" />
    <SideHeader label="RIGHT" side={c.right} />
   </div>
   <div className="drift-badge-row"><span className="not-drift-badge" tabIndex={0} aria-describedby={`not-drift-${id}`}><Info size={13} />Different is not necessarily drift</span><span id={`not-drift-${id}`} className="muted">{c.notice.replace(/^Different is not necessarily drift\.\s*/, '')}</span></div>
   <p className="info-line"><ShieldCheck size={13} />Source: recorded state of each workspace, read with <code>show -json</code> through the local CLI{c.left.capturedAt && c.right.capturedAt ? <> at {clock(c.left.capturedAt)} (left) and {clock(c.right.capturedAt)} (right)</> : ''}. Not live cloud inventory. Values are redacted before they are compared.</p>
   {c.status === 'stale' && <div className="plan-warning" role="alert"><TriangleAlert size={15} /><span><strong>This comparison no longer matches.</strong> {c.staleReason} Results are withheld so they cannot be mistaken for the current workspaces.</span>{canCompare && <Button variant="outline" size="small" onClick={compareAgain}><RefreshCcw size={13} />Compare again</Button>}</div>}
   {c.status === 'partial' && <section className="operation-failed" role="alert">
    <h2><TriangleAlert size={17} />Only one side could be read</h2>
    {([['Left', c.left], ['Right', c.right]] as [string, ComparisonSide][]).map(([label, side]) => side.error && <p key={label}><strong>{label}: </strong>{side.error.message}</p>)}
    <p className="muted">Nothing was compared. An unavailable workspace is never treated as an empty one.</p>
    {canCompare && <Button variant="outline" size="small" onClick={compareAgain}><RefreshCcw size={13} />Compare again</Button>}
   </section>}
   {c.status === 'reading' && <div className="info-line" role="status"><LoaderCircle className="spin" size={13} />Reading both workspaces. The comparison appears when both are read.</div>}
   {c.status === 'ready' && s && <>
    <div className="summary-grid" role="group" aria-label="Filter by comparison status">{order.map(status => {
     const { label, icon: Icon, tone } = statuses[status];
     return <button key={status} className={`summary-card tone-${tone} ${filter.includes(status) ? 'chosen' : ''}`} aria-pressed={filter.includes(status)} aria-label={`${label}: ${counts(status)}`} disabled={!counts(status)} title={statusHelp[status]} onClick={() => toggle(status)}><span><Icon size={14} aria-hidden="true" />{label}</span><strong>{counts(status)}</strong></button>;
    })}</div>
    <p className="info-line compare-facts"><span>{s.left.managed} managed / {s.left.data} data on the left</span><span>{s.right.managed} managed / {s.right.data} data on the right</span>{s.mapped > 0 && <span>{s.mapped} paired by mapping rules</span>}{s.ignoredAttributes > 0 && <span>{count(s.ignoredAttributes, 'value')} ignored by rules</span>}{s.normalized > 0 && <span>{count(s.normalized, 'value')} equal after substitution</span>}{s.notCompared > 0 && <span><LockKeyhole size={11} />{count(s.notCompared, 'redacted value')} not compared</span>}</p>
    {c.rules.length > 0 && <ul className="rule-hits" aria-label="Rule matches">{c.rules.map((r, i) => <li key={i}><span className="rule-number">{i + 1}</span>{ruleText(r)}<span className="muted">{count(s.ruleHits[i] ?? 0, r.kind === 'map' ? 'address' : 'value', r.kind === 'map' ? 'addresses' : 'values')}{r.kind === 'map' ? ' mapped' : ''}</span></li>)}</ul>}
    {counts('only-left') > 0 && counts('only-right') > 0 && <p className="info-line" role="note"><ListFilter size={13} />Addresses match exactly. If some of the {counts('only-left')} only-left and {counts('only-right')} only-right rows are the same object under a renamed address, add a <strong>Map renamed address</strong> rule and compare again; rule matches are counted above before you rely on them. <em>Unknown</em> rows are different: they matched, but redacted values were deliberately not compared.</p>}
    {c.warnings.map(w => <div className="plan-warning" key={w} role="note"><TriangleAlert size={15} />{w}</div>)}
    <div className="compare-tabs-row">
     <div className="segmented" role="tablist" aria-label="Comparison views">{(['Resources', 'Modules', 'Providers'] as Tab[]).map(t => <button key={t} role="tab" aria-selected={tab === t} aria-pressed={tab === t} onClick={() => setTab(t)}>{t}</button>)}</div>
     <span className="context-spacer" />
     <Button variant="outline" size="small" onClick={() => void exportReport('markdown')}><Download size={13} />Export Markdown</Button>
     <Button variant="outline" size="small" onClick={() => void exportReport('json')}><Download size={13} />Export JSON</Button>
    </div>
    {exportError && <p className="dialog-error" role="alert">{exportError}</p>}
    <div role="tabpanel" aria-label={tab}>
     {tab === 'Resources' && <MatchList comparison={c} filter={filter} setFilter={setFilter} module={module} setModule={setModule} copy={copy} />}
     {tab === 'Modules' && <ModulesTable comparison={c} open={m => { setModule(m); setFilter([]); setTab('Resources'); }} />}
     {tab === 'Providers' && <ProvidersTable comparison={c} />}
    </div>
   </>}
  </div>;
}

function count(n: number, one: string, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

function ruleText(r: ComparisonRule) {
  if (r.kind === 'map') return <>Map <code>{r.left}</code> → <code>{r.right}</code></>;
  if (r.kind === 'ignore') return <>Ignore <code>{r.path}</code>{r.resourceType && <> for <code>{r.resourceType}</code></>}</>;
  return <>Treat <code>{r.left}</code> as <code>{r.right}</code>{r.path && <> in <code>{r.path}</code></>}</>;
}

const PAGE = 100;

function MatchList({ comparison, filter, setFilter, module, setModule, copy }: { comparison: Comparison; filter: MatchStatus[]; setFilter: (f: MatchStatus[]) => void; module?: string; setModule: (m?: string) => void; copy: (value: string, label: string) => Promise<void> }) {
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  const [mode, setMode] = useState('');
  const [type, setType] = useState('');
  // The page resets whenever a filter changes.
  const filterKey = JSON.stringify([query, mode, type, module ?? null, filter]);
  const [paging, setPaging] = useState({ key: filterKey, page: 0 });
  const page = paging.key === filterKey ? paging.page : 0;
  const setPage = (next: number) => setPaging({ key: filterKey, page: next });
  const [selected, setSelected] = useState<string>();
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 180);
    return () => clearTimeout(timer);
  }, [search]);
  const params = useMemo(() => {
    const p = new URLSearchParams();
    if (filter.length) p.set('status', filter.join(','));
    if (query) p.set('query', query);
    if (mode) p.set('mode', mode);
    if (type) p.set('type', type);
    if (module !== undefined) p.set('module', module);
    p.set('offset', String(page * PAGE));
    p.set('limit', String(PAGE));
    return p.toString();
  }, [filter, query, mode, type, module, page]);
  const matches = useQuery({ queryKey: ['comparison-matches', comparison.id, params], queryFn: () => api<{ matches: ComparisonMatch[]; total: number }>(`comparisons/${comparison.id}/resources?${params}`), placeholderData: prev => prev });
  const filtered = filter.length > 0 || !!query || !!mode || !!type || module !== undefined;
  const total = matches.data?.total ?? 0;
  return <>
   <div className="filter-row plan-filters">
    <div className="search-field compact"><Search size={14} /><input value={search} onChange={e => setSearch(e.target.value)} placeholder="Search addresses, types, providers…" aria-label="Search compared resources" />{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch('')}><X size={12} /></button>}</div>
    <select aria-label="Filter resource mode" value={mode} onChange={e => setMode(e.target.value)}><option value="">Managed & data</option><option value="managed">Managed resources</option><option value="data">Data sources</option></select>
    <select aria-label="Filter resource type" value={type} onChange={e => setType(e.target.value)}><option value="">All resource types</option>{comparison.types.map(t => <option key={t}>{t}</option>)}</select>
    {module !== undefined && <span className="filter-chip">Module <code>{module || 'root'}</code><button aria-label="Clear module filter" onClick={() => setModule(undefined)}><X size={11} /></button></span>}
    {filtered && <Button variant="ghost" size="small" onClick={() => { setFilter([]); setSearch(''); setMode(''); setType(''); setModule(undefined); }}><X size={12} />Reset</Button>}
    <span className="filter-count">{matches.data ? `${total} shown` : '…'}</span>
   </div>
   <div className={`explorer plan-explorer ${selected ? 'with-inspector' : ''}`}>
    <section className="resource-list" aria-label="Compared resources">
     {matches.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading…</p></div> : matches.error ? <div className="empty-state error-state" role="alert"><p>{matches.error.message}</p></div> : total === 0 ? <div className="empty-state"><Braces size={26} /><h2>{filtered ? 'No matching resources' : 'Neither side records resources'}</h2></div> : <>
      <table className="resources-table changes-table compare-table"><thead><tr><th scope="col">Status</th><th scope="col">Resource</th><th scope="col">Detail</th></tr></thead><tbody>{matches.data!.matches.map(m => <tr key={m.id} className={selected === m.id ? 'selected' : ''}>
       <td><StatusBadge value={m.status} /></td>
       <td><button className="resource-button" aria-pressed={selected === m.id} onClick={() => setSelected(m.id)}><span className="resource-text"><strong className="mono change-address">{m.rightAddress || m.leftAddress}</strong><span className="resource-type">{m.type}{m.mode === 'data' && ' · data source'}{m.leftAddress && m.rightAddress && m.leftAddress !== m.rightAddress && <> · left <span className="mono">{m.leftAddress}</span></>}</span></span></button></td>
       <td className="change-why">{m.status === 'attribute-different' ? <>{m.differences} attribute{m.differences === 1 ? '' : 's'}{m.paths.length > 0 && <span className="mono muted compare-paths">{m.paths.join(', ')}{m.differences > m.paths.length ? ', …' : ''}</span>}</> : m.reason ? <span className="muted">{m.status === 'unknown' ? `${m.notCompared} redacted not compared` : 'See details'}</span> : null}{m.rule ? <span className="flag">rule {m.rule}</span> : null}{m.ignored > 0 && <span className="flag">{m.ignored} ignored</span>}</td>
      </tr>)}</tbody></table>
      {total > PAGE && <div className="pager"><Button variant="ghost" size="small" disabled={page === 0} onClick={() => setPage(page - 1)}><ChevronLeft size={13} />Previous</Button><span className="muted">{page * PAGE + 1}–{Math.min(total, (page + 1) * PAGE)} of {total}</span><Button variant="ghost" size="small" disabled={(page + 1) * PAGE >= total} onClick={() => setPage(page + 1)}>Next<ChevronRight size={13} /></Button></div>}
     </>}
    </section>
    {selected && <MatchInspector comparison={comparison} matchId={selected} close={() => setSelected(undefined)} copy={copy} />}
   </div>
  </>;
}

function Value({ value, present }: { value: unknown; present: boolean }) {
  if (!present) return <span className="muted">not present</span>;
  if (value === '[redacted]') return <span className="redacted"><LockKeyhole size={11} />redacted</span>;
  if (value === null || value === undefined) return <span className="muted">null</span>;
  if (typeof value === 'object') return <code className="json-inline">{JSON.stringify(value)}</code>;
  return <code className={typeof value === 'string' ? 'json-string' : 'json-value'}>{String(value)}</code>;
}

function AttributeRow({ a }: { a: ComparisonAttribute }) {
  const cls = a.status === 'different' ? 'diff-changed' : a.status === 'only-left' ? 'diff-removed' : a.status === 'only-right' ? 'diff-added' : a.status === 'sensitive' ? 'diff-unknown' : a.status === 'ignored' ? 'diff-ignored' : '';
  return <tr className={cls}>
   <th scope="row"><code>{a.path}</code>{a.normalized && <span className="flag">equal after rule {a.rule}</span>}</th>
   {a.status === 'sensitive' ? <td colSpan={2}><span className="redacted"><LockKeyhole size={11} />redacted · not compared</span></td>
    : a.status === 'ignored' ? <td colSpan={2}><span className="muted">ignored by rule {a.rule}</span></td>
    : <><td><Value value={a.left} present={a.status !== 'only-right'} /></td><td><Value value={a.right} present={a.status !== 'only-left'} /></td></>}
  </tr>;
}

function inspectorLink(side: ComparisonSide, resourceId: string) {
  const profile = side.executionContext?.profileId || 'none';
  return `${contextPath(side.target.projectId, side.target.workspace, 'Resources')}?resource=${resourceId}&profile=${profile}`;
}

function MatchInspector({ comparison, matchId, close, copy }: { comparison: Comparison; matchId: string; close: () => void; copy: (value: string, label: string) => Promise<void> }) {
  const detail = useQuery({ queryKey: ['comparison-detail', comparison.id, matchId], queryFn: () => api<ComparisonDetail>(`comparisons/${comparison.id}/resources/${matchId}`) });
  const [showSame, setShowSame] = useState(false);
  const d = detail.data;
  const rows = d?.attributes.filter(a => showSame || a.status !== 'same' || a.normalized) ?? [];
  const same = d?.attributes.filter(a => a.status === 'same' && !a.normalized).length ?? 0;
  return <Drawer label="Comparison details" onClose={close}>
   <div className="inspector-header"><span><GitCompareArrows size={15} />Comparison details</span><Button variant="ghost" size="icon" aria-label="Close comparison details" onClick={close}><X size={16} /></Button></div>
   {detail.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading…</div> : detail.error ? <div className="empty-state error-state" role="alert"><p>{detail.error.message}</p></div> : d && <div className="inspector-body">
    <div className="inspector-title"><StatusBadge value={d.status} /><span className="mono muted">{d.type}{d.mode === 'data' ? ' · data source' : ''}</span></div>
    {(['left', 'right'] as const).map(side => {
     const address = side === 'left' ? d.leftAddress : d.rightAddress;
     const resourceId = side === 'left' ? d.leftId : d.rightId;
     const target = side === 'left' ? comparison.left : comparison.right;
     return <div className="address-box" key={side}><div className="label-row"><span>{side.toUpperCase()} · {target.alias} / {target.target.workspace}</span>{address && <Button variant="ghost" size="icon" aria-label={`Copy ${side} address`} onClick={() => void copy(address, 'Address')}><Copy size={13} /></Button>}</div>{address ? <code>{address}</code> : <span className="muted">Not recorded on this side</span>}{address && resourceId && <Button variant="ghost" size="small" onClick={() => openInNewTab(inspectorLink(target, resourceId))}>Open in {side} workspace<ExternalLink size={12} /></Button>}</div>;
    })}
    {d.reason && <p className={d.status === 'uncomparable' ? 'plan-warning' : 'info-line'}>{d.status === 'uncomparable' ? <TriangleAlert size={14} /> : <Info size={14} />}{d.reason}</p>}
    {d.providerNote && <p className="info-line"><Network size={13} />{d.providerNote}</p>}
    {d.rule ? <p className="info-line"><ListFilter size={13} />Paired by mapping rule {d.rule}.</p> : null}
    <dl className="metadata">
     <div><dt>Provider</dt><dd className="mono">{d.provider || 'Not recorded'}</dd></div>
     <div><dt>Module</dt><dd className="mono">{d.module || 'Root module'}</dd></div>
    </dl>
    {d.attributes.length > 0 && <>
     <div className="attributes-header"><h3>Attributes</h3><label className="check-row compact"><input type="checkbox" checked={showSame} onChange={e => setShowSame(e.target.checked)} /><span>Show {same} equal</span></label></div>
     <div className="redaction-note"><LockKeyhole size={12} />Sensitive, secret-named and credential-like values are redacted on the server and never compared, so a redacted value can hide a difference.</div>
     {rows.length ? <table className="diff-table compare-diff"><thead><tr><th scope="col">Attribute</th><th scope="col">Left</th><th scope="col">Right</th></tr></thead><tbody>{rows.map(a => <AttributeRow key={a.path} a={a} />)}</tbody></table> : <p className="muted">Every compared value is equal.</p>}
    </>}
    {d.truncated && <p className="muted">Only the first 2,000 attribute paths were compared.</p>}
    <details className="full-values"><summary>Full values</summary><div className="json-viewer">{d.left !== undefined && d.left !== null && <JsonTree name="left" value={d.left} />}{d.right !== undefined && d.right !== null && <JsonTree name="right" value={d.right} />}</div></details>
   </div>}
  </Drawer>;
}

function ModulesTable({ comparison, open }: { comparison: Comparison; open: (module: string) => void }) {
  return <div className="table-scroll"><table className="resources-table compare-table"><thead><tr><th scope="col">Module</th><th scope="col">Left</th><th scope="col">Right</th><th scope="col">Presence</th><th scope="col">Differences</th></tr></thead><tbody>{comparison.modules.map(m => <tr key={m.address}>
   <td><button className="resource-button" onClick={() => open(m.address)}><span className="resource-text"><strong className="mono">{m.address || 'Root module'}</strong>{m.leftAddress && <span className="resource-type">left <span className="mono">{m.leftAddress}</span></span>}</span></button></td>
   <td>{m.left}</td><td>{m.right}</td>
   <td>{m.status === 'both' ? <span className="muted">Both</span> : <StatusBadge value={m.status} />}</td>
   <td className="change-why">{m.counts.different > 0 && <span className="flag">{m.counts.different} different</span>}{m.counts.onlyLeft > 0 && <span className="flag">{m.counts.onlyLeft} only left</span>}{m.counts.onlyRight > 0 && <span className="flag">{m.counts.onlyRight} only right</span>}{m.counts.unknown > 0 && <span className="flag">{m.counts.unknown} unknown</span>}{m.counts.uncomparable > 0 && <span className="flag">{m.counts.uncomparable} uncomparable</span>}{m.counts.total === m.counts.same && m.counts.total > 0 && <span className="muted">All same</span>}</td>
  </tr>)}</tbody></table>{comparison.truncated && <p className="muted">Only the first 2,000 rows are shown.</p>}</div>;
}

function ProvidersTable({ comparison }: { comparison: Comparison }) {
  return <div className="table-scroll"><table className="resources-table compare-table"><thead><tr><th scope="col">Provider</th><th scope="col">Left</th><th scope="col">Right</th><th scope="col">Presence</th><th scope="col">Version</th></tr></thead><tbody>{comparison.providers.map(p => <tr key={p.source}>
   <td><span className="resource-text"><strong className="mono">{p.source || 'Not recorded'}</strong>{p.notes.map(n => <span key={n} className="resource-type">{n}</span>)}</span></td>
   <td>{p.left}</td><td>{p.right}</td>
   <td>{p.status === 'both' ? <span className="muted">Both</span> : <StatusBadge value={p.status} />}</td>
   <td className="muted">Not recorded in state</td>
  </tr>)}</tbody></table><div className="info-note"><CircleHelp size={15} /><span>State records provider sources per resource, not provider versions or aliases. Use Project tools → Providers for requirements.</span></div></div>;
}
