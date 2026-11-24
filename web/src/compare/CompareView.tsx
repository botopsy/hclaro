import { useRef, useState } from 'react';
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, ArrowLeftRight, ArrowRight, CircleDashed, CircleHelp, Copy, Database, Download, Equal, ExternalLink, GitCompareArrows, History, Info, ListFilter, LoaderCircle, LockKeyhole, Network, Pencil, Plus, RefreshCcw, ShieldCheck, Trash2, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, download, openInNewTab, send } from '../api';
import type { Comparison, ComparisonAttribute, ComparisonDetail, ComparisonMatch, ComparisonRule, ComparisonSide, ComparisonTarget, MatchStatus, Project } from '../types';
import { Button } from '../components/ui/button';
import { DataTable, type Column } from '../components/DataTable';
import { Disclosure } from '../components/Disclosure';
import { Drawer } from '../components/Drawer';
import { EmptyState } from '../components/EmptyState';
import { JsonTree } from '../components/JsonTree';
import { useModal } from '../components/useModal';
import { contextPath, productionLike } from '../router';
import { profilesQuery } from '../profiles/selection';
import { clock } from '../lib/format';
import { ProductionBadge } from '../components/ProductionBadge';
import { projectsQuery } from '../lib/queries';
import { errorMessage } from '../lib/errorMessage';
import { readJSON, readStorage, storageKeys, writeStorage } from '../lib/storage';
import { ErrorState } from '../components/ErrorState';

const statuses: Record<MatchStatus, { short: string; icon: LucideIcon; tone: string }> = {
  'attribute-different': { short: 'Different', icon: Pencil, tone: 'update' },
  'only-left': { short: 'Only left', icon: ArrowLeft, tone: 'neutral' },
  'only-right': { short: 'Only right', icon: ArrowRight, tone: 'neutral' },
  unknown: { short: 'Unknown', icon: CircleDashed, tone: 'muted' },
  uncomparable: { short: 'Uncomparable', icon: CircleHelp, tone: 'warn' },
  same: { short: 'Same', icon: Equal, tone: 'create' }
};
const statusHelp: Record<MatchStatus, string> = {
  'attribute-different': 'Matched on both sides; at least one visible attribute value differs.',
  'only-left': 'Recorded only in the left workspace. Addresses match exactly: a renamed address shows here and as only-right unless a mapping rule pairs them.',
  'only-right': 'Recorded only in the right workspace. Addresses match exactly: a renamed address shows here and as only-left unless a mapping rule pairs them.',
  unknown: 'No visible difference, but redacted values were not compared, so equality cannot be confirmed.',
  uncomparable: 'Matched by address, but the type, mode or provider differs, or a mapping rule conflicts.',
  same: 'Every compared attribute value is equal, after your rules.'
};
// §4.9: the result table's groups, in order. Same rows are only counted.
const groups: { key: string; label: string; statuses: MatchStatus[] }[] = [
  { key: 'only-left', label: 'Only on left', statuses: ['only-left'] },
  { key: 'only-right', label: 'Only on right', statuses: ['only-right'] },
  { key: 'different', label: 'Different', statuses: ['attribute-different'] },
  { key: 'not-compared', label: 'Could not compare', statuses: ['uncomparable', 'unknown'] }
];
const tiles: { label: string; tone: string; statuses: MatchStatus[] }[] = [
  { label: 'Only on left', tone: 'neutral', statuses: ['only-left'] },
  { label: 'Only on right', tone: 'neutral', statuses: ['only-right'] },
  { label: 'Different', tone: 'update', statuses: ['attribute-different'] },
  { label: 'Same', tone: 'create', statuses: ['same'] },
  { label: 'Could not compare', tone: 'muted', statuses: ['uncomparable', 'unknown'] }
];
const countKey = { 'attribute-different': 'different', 'only-left': 'onlyLeft', 'only-right': 'onlyRight', unknown: 'unknown', uncomparable: 'uncomparable', same: 'same' } as const;

// addresses limits the comparison to selected left addresses (set by Add to Compare in Resources).
type Draft = { left: TargetDraft; right: TargetDraft; rules: ComparisonRule[]; addresses?: string[] };
// profileId undefined means "the project's default profile", "" means no profile.
export type TargetDraft = { projectId: string; workspace: string; profileId?: string };
const emptyDraft: Draft = { left: { projectId: '', workspace: '' }, right: { projectId: '', workspace: '' }, rules: [] };
const hex = /^[0-9a-f]{32}$/;
const workspacePattern = /^[A-Za-z0-9_-]{1,90}$/;

// The draft is kept per browser tab. It holds IDs, workspace names and rule text, which the
// server refuses when it looks like a credential.
function readDraft(): Draft {
  try {
    const saved = readJSON('session', storageKeys.compareDraft) as Draft | null | undefined;
    if (!saved || typeof saved !== 'object' || !Array.isArray(saved.rules)) return emptyDraft;
    const target = (t?: TargetDraft): TargetDraft => ({
      projectId: t && hex.test(t.projectId) ? t.projectId : '',
      workspace: t && typeof t.workspace === 'string' ? t.workspace.slice(0, 90) : '',
      profileId: t?.profileId === '' || t?.profileId && hex.test(t.profileId) ? t.profileId : undefined
    });
    const rules = saved.rules.slice(0, 50).filter(r => r && ['map', 'ignore', 'substitute'].includes(r.kind)).map(r => ({ kind: r.kind, left: str(r.left), right: str(r.right), path: str(r.path), resourceType: str(r.resourceType) }));
    const addresses = Array.isArray(saved.addresses) ? saved.addresses.filter(a => typeof a === 'string' && a).slice(0, 200) : [];
    return { left: target(saved.left), right: target(saved.right), rules, ...addresses.length ? { addresses } : {} };
  } catch {
    return emptyDraft;
  }
}
// Add to Compare in Resources: the left side becomes that context and the draft is limited to
// the selected addresses. Nothing runs until the user presses Compare.
export function prefillCompare(left: TargetDraft, addresses: string[]) {
  writeDraft({ ...readDraft(), left, addresses });
}
function str(v: unknown) {
  return typeof v === 'string' ? v.slice(0, 256) : '';
}
// Without storage the draft lasts until the page reloads.
function writeDraft(draft: Draft) {
  writeStorage('session', storageKeys.compareDraft, JSON.stringify(draft));
}
function readComparisonId() {
  const id = readStorage('session', storageKeys.compareId) || '';
  return hex.test(id) ? id : undefined;
}
function writeComparisonId(id: string) {
  writeStorage('session', storageKeys.compareId, id);
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
  const projectList = projects.data?.projects ?? [];
  const name = (t: ComparisonTarget) => `${projectList.find(p => p.id === t.projectId)?.alias ?? 'Project'} / ${t.workspace}`;
  // Compare stays disabled, with the reason beside it, until both sides are set (§4.9).
  const reason = ![left, right].every(t => hex.test(t.projectId)) ? 'Choose a project for both sides.'
    : ![left, right].every(t => workspacePattern.test(t.workspace)) ? 'Choose a workspace for both sides.'
    : sameTarget ? 'Choose a different workspace, project or profile for one side.' : undefined;
  const ready = !reason;
  async function start(reload: boolean) {
    setStarting(true);
    setStartError('');
    try {
      const started = await send<Comparison>('POST', 'comparisons', { left, right, rules: cleanRules(draft.rules), ...draft.addresses?.length ? { addresses: draft.addresses } : {}, reload, acknowledgeStateRead: true });
      setPreflight(false);
      setComparisonId(started.id);
      client.setQueryData(['comparison', started.id], started);
      void client.invalidateQueries({ queryKey: ['comparisons'] });
    } catch (e) {
      const message = errorMessage(e, 'The comparison could not be started.');
      const match = /^Rule (\d+):/.exec(message);
      if (match) setRuleError({ index: Number(match[1]) - 1, reason: message });
      setStartError(message);
    } finally {
      setStarting(false);
    }
  }
  return <div className="plans compare-page">
   <section className="compare-targets" aria-label="Comparison targets">
    <TargetCard side="left" draft={draft.left} projects={projectList} onChange={t => setDraft({ ...draft, left: t })} />
    <Button variant="outline" size="icon" className="compare-swap" aria-label="Swap left and right" title="Swap left and right" onClick={() => setDraft({ ...draft, left: draft.right, right: draft.left })}><ArrowLeftRight size={16} /></Button>
    <TargetCard side="right" draft={draft.right} projects={projectList} onChange={t => setDraft({ ...draft, right: t })} />
   </section>
   {projects.data && projectList.length === 0 && <div className="plan-warning" role="note"><Info size={15} /><span>Add projects first. Comparisons read Hclaro projects only.</span></div>}
   {!!draft.addresses?.length && <div className="info-line" role="note"><ListFilter size={13} />Limited to {draft.addresses.length} selected resource{draft.addresses.length === 1 ? '' : 's'} from the left side.<Button variant="ghost" size="small" onClick={() => setDraft({ ...draft, addresses: undefined })}><X size={12} />Clear</Button></div>}
   <RulesEditor rules={draft.rules} onChange={rules => setDraft({ ...draft, rules })} error={ruleError} />
   <div className="plans-toolbar">
    <Button variant={ready ? 'default' : 'outline'} onClick={() => { setStartError(''); setPreflight(true); }} disabled={!ready} title={reason} aria-describedby="compare-reason"><GitCompareArrows size={14} />Compare…</Button>
    <span id="compare-reason" className="toolbar-note" role="status">{reason ?? <>Ready: <strong>{name(left)}</strong> ↔ <strong>{name(right)}</strong>. Nothing is read until you compare.</>}</span>
    {history.data && history.data.comparisons.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Comparison history</span><select aria-label="Comparison history" value={comparisonId ?? ''} onChange={e => setComparisonId(e.target.value)}>{!history.data.comparisons.some(c => c.id === comparisonId) && <option value="">Choose a comparison</option>}{history.data.comparisons.map(c => <option key={c.id} value={c.id}>{clock(c.createdAt)} · {c.left.alias}/{c.left.target.workspace} ↔ {c.right.alias}/{c.right.target.workspace}{c.status === 'stale' ? ' · stale' : ''}</option>)}</select></label>}
   </div>
   {startError && !preflight && <p className="dialog-error" role="alert">{startError}</p>}
   {comparisonId ? <ComparisonResult key={comparisonId} id={comparisonId} copy={copy} compareAgain={() => setPreflight(true)} canCompare={ready} /> : <EmptyState icon={<GitCompareArrows size={26} />} title="Compare two workspaces">Pick two workspaces, for example <strong>dev</strong> and <strong>prod</strong>, or one stack in two project roots. Hclaro lists what exists on only one side, what differs, and what couldn't be compared.</EmptyState>}
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
  const names = project?.inspection?.workspaces ?? [];
  const label = side === 'left' ? 'Left' : 'Right';
  const listId = `compare-workspaces-${side}`;
  async function listWorkspaces() {
    if (!project) return;
    setListing(true);
    setListError('');
    try {
      await send('POST', `projects/${project.id}/inspect`, {});
      await client.invalidateQueries({ queryKey: ['projects'] });
    } catch (e) {
      setListError(errorMessage(e, 'Workspaces could not be listed.'));
    } finally {
      setListing(false);
    }
  }
  const profileList = profiles.data?.profiles ?? [];
  const chosenProfile = profileList.find(p => p.id === profileId);
  return <section className="compare-target" aria-label={`${label} target`}>
   <div className="compare-target-head"><span className="compare-side-label">{label.toUpperCase()}</span>{productionLike(project?.alias, draft.workspace) && <ProductionBadge />}</div>
   <label className="field"><span>Project</span><select aria-label={`${label} project`} value={draft.projectId} onChange={e => onChange({ projectId: e.target.value, workspace: '', profileId: undefined })}><option value="">Choose a project…</option>{projects.map(p => <option key={p.id} value={p.id} disabled={p.status !== 'ready'}>{p.alias}{p.status !== 'ready' ? ` (${p.problem || p.status})` : ''}</option>)}</select></label>
   <div className="field"><label htmlFor={`${listId}-input`}>Workspace</label><div className="field-row"><input id={`${listId}-input`} aria-label={`${label} workspace`} list={listId} value={draft.workspace} disabled={!project} maxLength={90} spellCheck={false} placeholder={project?.inspection?.cliDefault || 'default'} onChange={e => onChange({ ...draft, workspace: e.target.value })} /><datalist id={listId}>{names.map(n => <option key={n} value={n} />)}</datalist><Button variant="outline" size="small" disabled={!project || listing} onClick={() => void listWorkspaces()} title="Runs version and workspace list for this project. Read-only."><RefreshCcw size={12} className={listing ? 'spin' : undefined} />List</Button></div>
    {draft.workspace && !validWorkspace && <span className="field-error" role="alert">Use 1–90 letters, digits, - or _.</span>}
    {listError && <span className="field-error" role="alert">{listError}</span>}
    {project?.inspection?.diagnostic && <span className="field-error">{project.inspection.diagnostic.message}</span>}
   </div>
   <label className="field"><span>Execution profile</span><select aria-label={`${label} execution profile`} value={profileId} disabled={!project} onChange={e => onChange({ ...draft, profileId: e.target.value })}><option value="">No profile (inherited environment)</option>{profileList.map(p => <option key={p.id} value={p.id}>{p.name}{p.workspace ? ` · ${p.workspace} only` : ''}{p.id === profiles.data?.defaultId ? ' · default' : ''}</option>)}</select></label>
   {chosenProfile?.workspace && validWorkspace && chosenProfile.workspace !== draft.workspace.trim() && <span className="field-error" role="alert">Profile {chosenProfile.name} selects workspace {chosenProfile.workspace}.</span>}
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
  // The server refuses a comparison without this acknowledgement, so the button waits for it.
  const [understood, setUnderstood] = useState(false);
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
    <label className="check-row"><input type="checkbox" checked={understood} onChange={e => setUnderstood(e.target.checked)} /><span>I understand this reads the recorded state of both workspaces through the local CLI</span></label>
    <label className="check-row"><input type="checkbox" checked={reload} onChange={e => setReload(e.target.checked)} /><span>Read both sides again, even if they were read in this session</span></label>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(reload)} disabled={busy || !understood}><GitCompareArrows size={14} />{busy ? 'Starting…' : 'Compare'}</Button></div>
   </div>
  </div>;
}

function StatusBadge({ value }: { value: MatchStatus }) {
  const { short, icon: Icon, tone } = statuses[value];
  return <span className={`action-badge tone-${tone}`} title={statusHelp[value]}><Icon size={12} aria-hidden="true" />{short}</span>;
}

function ComparisonResult({ id, copy, compareAgain, canCompare }: { id: string; copy: (value: string, label: string) => Promise<void>; compareAgain: () => void; canCompare: boolean }) {
  const comparison = useQuery({
    queryKey: ['comparison', id],
    queryFn: () => api<Comparison>(`comparisons/${id}`),
    refetchInterval: q => q.state.data?.status === 'reading' ? 700 : false,
    retry: false
  });
  const [exportError, setExportError] = useState('');
  const c = comparison.data;
  if (comparison.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading comparison…</p></div>;
  if (comparison.error || !c) return <ErrorState inline message={comparison.error?.message ?? 'This comparison is not available.'} />;
  const s = c.summary;
  const counts = (list: MatchStatus[]) => s ? list.reduce((n, st) => n + s.managed[countKey[st]] + s.data[countKey[st]], 0) : 0;
  const side = (x: ComparisonSide) => `${x.alias || 'Project'} / ${x.target.workspace}`;
  async function exportReport(format: 'json' | 'markdown') {
    setExportError('');
    try {
      await download(`comparisons/${id}/export?format=${format}`, `hclaro-comparison-${id.slice(0, 8)}.${format === 'json' ? 'json' : 'md'}`);
    } catch (e) {
      setExportError(errorMessage(e, 'The report could not be exported.'));
    }
  }
  return <div className="plan-review compare-result">
   {c.status === 'stale' && <div className="plan-warning" role="alert"><TriangleAlert size={15} /><span><strong>This comparison no longer matches.</strong> {c.staleReason} Results are withheld so they cannot be mistaken for the current workspaces.</span>{canCompare && <Button variant="outline" size="small" onClick={compareAgain}><RefreshCcw size={13} />Compare again</Button>}</div>}
   {c.status === 'partial' && <section className="operation-failed" role="alert">
    <h2><TriangleAlert size={17} />Only one side could be read</h2>
    {([['Left', c.left], ['Right', c.right]] as [string, ComparisonSide][]).map(([label, x]) => x.error && <p key={label}><strong>{label}: </strong>{x.error.message}</p>)}
    <p className="muted">Nothing was compared. An unavailable workspace is never treated as an empty one.</p>
    {canCompare && <Button variant="outline" size="small" onClick={compareAgain}><RefreshCcw size={13} />Compare again</Button>}
   </section>}
   {c.status === 'reading' && <div className="info-line" role="status"><LoaderCircle className="spin" size={13} />Reading both workspaces. The comparison appears when both are read.</div>}
   {c.status === 'ready' && s && <>
    <div className="summary-grid" role="group" aria-label="Comparison counts">{tiles.map(t => <div key={t.label} className={`summary-card tone-${t.tone}${counts(t.statuses) ? '' : ' zero'}`} role="note" aria-label={`${t.label}: ${counts(t.statuses)}`}><span>{t.label}</span><strong>{counts(t.statuses)}</strong></div>)}</div>
    <p className="compare-summary">Compared <strong>{side(c.left)}</strong>{c.left.capturedAt && ` (read ${clock(c.left.capturedAt)})`} with <strong>{side(c.right)}</strong>{c.right.capturedAt && ` (read ${clock(c.right.capturedAt)})`}: {counts(['attribute-different'])} different, {counts(['only-left'])} only on left, {counts(['only-right'])} only on right, {counts(['uncomparable', 'unknown'])} could not be compared and {counts(['same'])} the same. Different is not necessarily drift.</p>
    {!!c.addresses?.length && <p className="info-line" role="note"><ListFilter size={13} />Limited to {c.addresses.length} selected resource{c.addresses.length === 1 ? '' : 's'}.</p>}
    {c.warnings.map(w => <div className="plan-warning" key={w} role="note"><TriangleAlert size={15} />{w}</div>)}
    <Disclosure summary="How this was compared">
     <p className="info-line"><ShieldCheck size={13} />Source: recorded state of each workspace, read with <code>show -json</code> through the local CLI. Not live cloud inventory. Values are redacted before they are compared. {c.notice}</p>
     <p className="info-line compare-facts"><span>{s.left.managed} managed / {s.left.data} data on the left</span><span>{s.right.managed} managed / {s.right.data} data on the right</span>{s.mapped > 0 && <span>{s.mapped} paired by mapping rules</span>}{s.ignoredAttributes > 0 && <span>{count(s.ignoredAttributes, 'value')} ignored by rules</span>}{s.normalized > 0 && <span>{count(s.normalized, 'value')} equal after substitution</span>}{s.notCompared > 0 && <span><LockKeyhole size={11} />{count(s.notCompared, 'redacted value')} not compared</span>}</p>
     {c.rules.length > 0 && <ul className="rule-hits" aria-label="Rule matches">{c.rules.map((r, i) => <li key={i}><span className="rule-number">{i + 1}</span>{ruleText(r)}<span className="muted">{count(s.ruleHits[i] ?? 0, r.kind === 'map' ? 'address' : 'value', r.kind === 'map' ? 'addresses' : 'values')}{r.kind === 'map' ? ' mapped' : ''}</span></li>)}</ul>}
     {counts(['only-left']) > 0 && counts(['only-right']) > 0 && <p className="info-line" role="note"><ListFilter size={13} />Addresses match exactly. If some of the {counts(['only-left'])} only-left and {counts(['only-right'])} only-right rows are the same object under a renamed address, add a <strong>Map renamed address</strong> rule and compare again; rule matches are counted above before you rely on them. <em>Unknown</em> rows are different: they matched, but redacted values were deliberately not compared.</p>}
    </Disclosure>
    <div className="compare-actions">
     <Button variant="outline" size="small" onClick={() => void exportReport('markdown')}><Download size={13} />Export Markdown</Button>
     <Button variant="outline" size="small" onClick={() => void exportReport('json')}><Download size={13} />Export JSON</Button>
    </div>
    {exportError && <p className="dialog-error" role="alert">{exportError}</p>}
    <GroupedMatches comparison={c} counts={counts} copy={copy} />
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

// shortcut: each group loads its first LIMIT rows (the server's page maximum) and the exports hold every row; add paging per group if that proves too few.
const LIMIT = 200;

function detail(m: ComparisonMatch) {
  if (m.status === 'only-left') return <span className="muted">Not in the right workspace</span>;
  if (m.status === 'only-right') return <span className="muted">Not in the left workspace</span>;
  if (m.status === 'attribute-different') return <>{m.differences} attribute{m.differences === 1 ? ' differs' : 's differ'}{m.paths.length > 0 && <span className="mono muted compare-paths">{m.paths.join(', ')}{m.differences > m.paths.length ? ', …' : ''}</span>}</>;
  if (m.status === 'unknown') return <span className="muted">{count(m.notCompared, 'redacted value')} not compared</span>;
  return <span className="muted">{m.reason || 'The type, mode or provider differs.'}</span>;
}

function GroupedMatches({ comparison, counts, copy }: { comparison: Comparison; counts: (list: MatchStatus[]) => number; copy: (value: string, label: string) => Promise<void> }) {
  const [selected, setSelected] = useState<string>();
  const shown = groups.filter(g => counts(g.statuses) > 0);
  const pages = useQueries({ queries: shown.map(g => ({
    queryKey: ['comparison-matches', comparison.id, g.key],
    queryFn: () => api<{ matches: ComparisonMatch[]; total: number }>(`comparisons/${comparison.id}/resources?status=${g.statuses.join(',')}&limit=${LIMIT}`)
  })) });
  if (!shown.length) return <p className="info-line" role="status"><Equal size={13} />{counts(['same']) ? 'Every compared resource is the same on both sides.' : 'Neither side records resources.'}</p>;
  if (pages.some(p => p.isPending)) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading…</p></div>;
  const failed = pages.find(p => p.error);
  if (failed?.error) return <ErrorState inline message={failed.error.message} />;
  const columns: Column<ComparisonMatch>[] = [
    { key: 'resource', header: 'Resource', cell: m => <button className="resource-button" aria-pressed={selected === m.id} onClick={() => setSelected(m.id)}><span className="resource-text"><strong className="mono change-address">{m.rightAddress || m.leftAddress}</strong><span className="resource-type">{m.type}{m.mode === 'data' && ' · data source'}{m.leftAddress && m.rightAddress && m.leftAddress !== m.rightAddress && <> · left <span className="mono">{m.leftAddress}</span></>}</span></span></button> },
    { key: 'detail', header: 'Detail', className: 'change-why', cell: m => <>{detail(m)}{m.rule ? <span className="flag">rule {m.rule}</span> : null}{m.ignored > 0 && <span className="flag">{m.ignored} ignored</span>}</> }
  ];
  return <>
   <DataTable label="Compared resources" columns={columns} rowKey={m => m.id} active={selected} groups={shown.map((g, i) => ({ key: g.key, label: g.label, count: counts(g.statuses), rows: pages[i].data?.matches ?? [] }))} />
   {shown.some(g => counts(g.statuses) > LIMIT) && <p className="muted">Each group shows its first {LIMIT} resources. Export the report for every row.</p>}
   {selected && <MatchInspector comparison={comparison} matchId={selected} close={() => setSelected(undefined)} copy={copy} />}
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
  return <Drawer label="Comparison details" backdrop={false} onClose={close}>
   <div className="inspector-header"><span><GitCompareArrows size={15} />Comparison details</span><Button variant="ghost" size="icon" aria-label="Close comparison details" onClick={close}><X size={16} /></Button></div>
   {detail.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Loading…</div> : detail.error ? <ErrorState inline message={detail.error.message} /> : d && <div className="inspector-body">
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
