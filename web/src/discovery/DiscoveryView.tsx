import { useEffect, useMemo, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, BadgeCheck, CircleCheck, CircleDashed, CircleHelp, CircleSlash, CircleX, Cloud, CloudCog, Copy, History, Import, LoaderCircle, Play, Radar, Search, ShieldCheck, Square, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, ApiError, post } from '../api';
import type { CloudIdentity, CloudResourceCandidate, DiscoveryMatchStatus, DiscoveryOptions, DiscoveryPrefill, DiscoveryScan, DiscoveryScope } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { clock } from '../plans/labels';
import { ContextSummary } from '../profiles/ContextSummary';
import { ScopeSummary } from '../plans/PreflightDialog';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../plans/routes';

// Labels are distinguished by icon and text, never by color alone.
const labels: Record<DiscoveryMatchStatus, { label: string; icon: LucideIcon; tone: string; hint: string }> = {
  possibly_untracked: { label: 'Possibly untracked', icon: CircleDashed, tone: 'create', hint: 'Not in this workspace’s state. Another workspace, project or tool may still manage it.' },
  already_tracked: { label: 'Already tracked', icon: CircleCheck, tone: 'neutral', hint: 'Recorded in this workspace’s state with the same identifier.' },
  unknown: { label: 'Unknown', icon: CircleHelp, tone: 'warn', hint: 'The mapping could not be decided. Never imported without your review.' },
  unresolved: { label: 'Unresolved', icon: CircleSlash, tone: 'muted', hint: 'No verified import ID, so it cannot be compared or imported.' }
};
const order: DiscoveryMatchStatus[] = ['possibly_untracked', 'unknown', 'already_tracked', 'unresolved'];

export function MatchBadge({ value }: { value: DiscoveryMatchStatus }) {
  const { label, icon: Icon, tone, hint } = labels[value] ?? labels.unknown;
  return <span className={`action-badge tone-${tone}`} title={hint}><Icon size={12} aria-hidden="true" />{label}</span>;
}

function query(profileId: string) {
  return profileId ? `?profileId=${profileId}` : '';
}

// A request path: a Hub workspace uses only its own context's routes (M24.09).
function at(scope: PlanScope, path: string) {
  return scopedPath(scope, `discovery/${path}`);
}

// The reviewed context: a Hub workspace's request carries the fingerprint its options
// described and no profile; a launch request only when it is profile-bound.
function bound(options: DiscoveryOptions | undefined, profileId: string, scope: PlanScope) {
  if (scope.kind === 'context') return { contextFingerprint: options?.contextFingerprint ?? '' };
  const fingerprint = options?.context?.context.profileId ? options.context.context.fingerprint : undefined;
  return fingerprint ? { profileId, contextFingerprint: fingerprint } : {};
}

export function DiscoveryView({
  scope = launchScope,
  reopen,
  profileId = '',
  workspaceStatus,
  onPrepared,
  openResource,
  copy
}: {
  // A Hub workspace discovers in its own context only (M24.09); there is no launch fallback.
  scope?: PlanScope;
  // Opens a closed Hub workspace again.
  reopen?: () => void;
  profileId?: string;
  workspaceStatus?: string;
  // Opens the import wizard with a prepared candidate.
  onPrepared: (prefill: DiscoveryPrefill) => void;
  openResource: (id: string) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const client = useQueryClient();
  // Opening this page reads options and the in-memory scan list only. AWS is contacted only
  // by Verify, Scan and Prepare import.
  const scoped = scope.kind === 'context';
  const key = scopeKey(scope);
  const q = scoped ? '' : query(profileId);
  const options = useQuery({ queryKey: ['discovery-options', key, profileId, workspaceStatus], queryFn: () => api<DiscoveryOptions>(at(scope, 'options') + q) });
  const scans = useQuery({ queryKey: ['discovery-scans', key], queryFn: () => api<{ scans: DiscoveryScan[] }>(at(scope, 'scans')), enabled: !!options.data?.canScan });
  const [scanId, setScanId] = useState<string>();
  const shownId = scanId ?? scans.data?.scans[0]?.id;
  const scan = useQuery({
    queryKey: ['discovery-scan', key, shownId, profileId, workspaceStatus],
    queryFn: () => api<DiscoveryScan>(at(scope, `scans/${shownId}`) + q),
    enabled: !!shownId,
    refetchInterval: q => q.state.data?.status === 'running' ? 800 : false
  });
  const [verifying, setVerifying] = useState(false);
  const [verifyError, setVerifyError] = useState('');
  const [preview, setPreview] = useState(false);
  const o = options.data;
  const identity = o?.identity;
  const blocked = !!o?.profileError || !!o?.context && o.context.issues.length > 0;
  const running = scan.data?.status === 'running';
  useEffect(() => {
    if (scan.data && scan.data.status !== 'running') void client.invalidateQueries({ queryKey: ['discovery-scans'] });
  }, [scan.data?.status, client]); // eslint-disable-line react-hooks/exhaustive-deps
  async function verify() {
    setVerifying(true);
    setVerifyError('');
    try {
      await post<CloudIdentity>(at(scope, 'identity'), bound(o, profileId, scope));
      await options.refetch();
    } catch (e) {
      setVerifyError(e instanceof Error ? e.message : 'The AWS context could not be verified.');
    } finally {
      setVerifying(false);
    }
  }
  async function cancel(id: string) {
    try {
      await post(at(scope, `scans/${id}/cancel`), {});
    } finally {
      void scan.refetch();
    }
  }
  if (scoped && options.error instanceof ApiError && (options.error.code === 'context_closed' || options.error.code === 'scope_changed')) return <div className="empty-state error-state" role="alert"><TriangleAlert size={26} /><h2>Open this workspace again</h2><p>{options.error.message}</p>{reopen && <Button variant="outline" onClick={reopen}>Open again</Button>}<span className="safe-note"><ShieldCheck size={13} />Nothing ran.</span></div>;
  if (options.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading cloud discovery…</p></div>;
  if (options.error || !o) return <div className="empty-state error-state" role="alert"><p>{options.error?.message || 'Cloud discovery is unavailable.'}</p></div>;
  return <div className="plans discovery">
   <p className="info-line drift-explainer"><ShieldCheck size={13} />Cloud discovery lists existing AWS objects of {o.types.length} supported types in the account your credentials belong to, and compares them with <strong>this workspace’s recorded state</strong>. It never creates, changes or deletes cloud objects, and never writes state. Nothing is contacted until you ask.</p>
   {!o.canScan && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span><strong>Cloud discovery is unavailable here.</strong> {o.reason}</span></div>}
   {o.canScan && <IdentityBanner options={o} identity={identity} busy={verifying} blocked={blocked} error={verifyError} onVerify={() => void verify()} onScan={() => setPreview(true)} running={running} />}
   {o.canScan && (o.scope ? <details className="discovery-context"><summary>Execution context</summary><ScopeSummary scope={o.scope} executionContext={o.executionContext} title="Discovery uses" /></details> : o.context ? <details className="discovery-context"><summary>Execution context</summary><ContextSummary summary={o.context} title="Discovery uses" /></details> : o.profileError ? <p className="dialog-error" role="alert">{o.profileError}</p> : null)}
   {scans.data && scans.data.scans.length > 0 && <div className="plans-toolbar">
    <label className="history-select"><History size={14} /><span className="sr-only">Scan history</span><select aria-label="Scan history" value={shownId} onChange={e => setScanId(e.target.value)}>{scans.data.scans.map((s, i) => <option key={s.id} value={s.id}>{i === 0 ? 'Latest · ' : ''}{clock(s.startedAt)} · {s.identity.account} · {s.scope.regions.join(', ')} · {s.status}</option>)}</select></label>
    {running && scan.data && <Button variant="outline" size="small" onClick={() => void cancel(scan.data!.id)}><Square size={12} />Cancel scan</Button>}
   </div>}
   {scan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading scan…</p></div> : scan.error ? <div className="empty-state error-state" role="alert"><p>{scan.error.message}</p></div> : scan.data ? <ScanResult key={scan.data.id} scope={scope} scan={scan.data} options={o} profileId={profileId} onPrepared={onPrepared} openResource={openResource} copy={copy} /> : o.canScan && <div className="empty-state"><Radar size={30} /><h2>No discovery scan yet</h2><p>Verify the AWS context, then choose regions and resource types to scan. Each scan calls AWS list APIs with your local credentials, only when you ask.</p></div>}
   {preview && identity && <ScanPreview target={scope} options={o} identity={identity} profileId={profileId} onClose={() => setPreview(false)} onStarted={s => {
     setPreview(false);
     setScanId(s.id);
     void client.invalidateQueries({ queryKey: ['discovery-scans'] });
   }} />}
  </div>;
}

function IdentityBanner({ options, identity, busy, blocked, error, onVerify, onScan, running }: { options: DiscoveryOptions; identity?: CloudIdentity; busy: boolean; blocked: boolean; error: string; onVerify: () => void; onScan: () => void; running: boolean }) {
  const profile = options.context?.context.profileName;
  const awsProfile = options.context?.context.awsProfile;
  return <section className={`surface discovery-identity ${identity ? 'verified' : ''}`} aria-labelledby="discovery-identity-title">
   <div className="surface-heading">
    <div>
     <h2 id="discovery-identity-title">{identity ? <><BadgeCheck size={16} aria-hidden="true" />AWS account {identity.account}</> : <><Cloud size={16} aria-hidden="true" />AWS context not verified</>}</h2>
     <p>{identity ? <>Verified {clock(identity.verifiedAt)} with STS for workspace <strong>{options.workspace}</strong>{profile ? <> and profile <strong>{profile}</strong></> : ' and the inherited environment'}. Valid until {clock(identity.expiresAt)}; every scan and import preparation checks the account again.</> : <>Hclaro never infers the account from a profile name. Verify asks AWS STS <code>GetCallerIdentity</code> (read-only, contacts AWS) which account and principal the {awsProfile ? <>profile <code>{awsProfile}</code></> : 'inherited credentials'} belong to.</>}</p>
    </div>
    <div className="discovery-actions">
     <Button variant={identity ? 'outline' : 'default'} onClick={onVerify} disabled={busy || blocked}><ShieldCheck size={14} />{busy ? 'Verifying…' : identity ? 'Verify again' : 'Verify AWS context'}</Button>
     {identity && <Button onClick={onScan} disabled={blocked || running}><Radar size={14} />Scan…</Button>}
    </div>
   </div>
   {identity && <dl className="metadata compact-metadata discovery-identity-facts">
    <div><dt>Account</dt><dd className="mono">{identity.account}</dd></div>
    <div><dt>Principal</dt><dd className="mono">{identity.arn} <span className="muted">({identity.principalType})</span></dd></div>
    <div><dt>Default region</dt><dd><code>{identity.region}</code> <span className="muted">({identity.regionSource === 'profile' ? 'from the execution profile' : identity.regionSource === 'environment' ? 'from the environment' : 'no region configured; us-east-1'})</span></dd></div>
    <div><dt>AWS CLI</dt><dd className="mono">{identity.cliVersion}</dd></div>
    <div><dt>Permissions</dt><dd className="muted">Checked per service when you scan. A denied service shows as missing, not empty.</dd></div>
   </dl>}
   {blocked && <p className="dialog-error" role="alert">Resolve the execution context issues before verifying.</p>}
   {error && <p className="dialog-error" role="alert">{error}</p>}
  </section>;
}

// The scan preview states the scope, the request bounds and the possible cost before anything runs.
function ScanPreview({ target, options, identity, profileId, onClose, onStarted }: { target: PlanScope; options: DiscoveryOptions; identity: CloudIdentity; profileId: string; onClose: () => void; onStarted: (scan: DiscoveryScan) => void }) {
  const [scope, setScope] = useState<DiscoveryScope>({ regions: [identity.region], types: options.types.map(t => t.type), maxPerType: options.limits.defaultPerType, concurrency: options.limits.defaultConcurrency });
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  const estimate = useMemo(() => {
    const pages = Math.ceil(scope.maxPerType / Math.min(options.limits.pageSize, Math.max(1, scope.maxPerType)));
    let tasks = 0;
    for (const t of options.types) if (scope.types.includes(t.type)) tasks += t.global ? 1 : scope.regions.length;
    return { tasks, min: 1 + tasks, max: 1 + tasks * pages };
  }, [scope, options]);
  const valid = scope.regions.length > 0 && scope.regions.length <= options.limits.maxRegions && scope.types.length > 0 && scope.maxPerType >= 1 && scope.maxPerType <= options.limits.maxPerType;
  function toggle<K extends 'regions' | 'types'>(key: K, value: string) {
    setScope(s => ({ ...s, [key]: s[key].includes(value) ? s[key].filter(v => v !== value) : [...s[key], value] }));
    setConsent(false);
  }
  async function start() {
    setBusy(true);
    setError('');
    try {
      onStarted(await post<DiscoveryScan>(at(target, 'scans'), { identityId: identity.id, scope, acknowledgeApiCalls: true, ...bound(options, profileId, target) }));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The scan could not be started.');
    } finally {
      setBusy(false);
    }
  }
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog wide-dialog" role="dialog" aria-modal="true" aria-labelledby="discovery-scan-title" aria-describedby="discovery-scan-summary">
    <div className="dialog-header"><h2 id="discovery-scan-title">Scan AWS account {identity.account}</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="discovery-scan-summary">Choose what to list. Only this account is scanned, never an organization or other accounts.</p>
    <fieldset className="discovery-fieldset"><legend>Resource types</legend>{options.types.map(t => <label key={t.type} className="check"><input type="checkbox" checked={scope.types.includes(t.type)} onChange={() => toggle('types', t.type)} /><span><strong>{t.label}</strong> <code>{t.type}</code> <span className="muted">· needs {t.permission}{t.global ? ' · listed once for the account' : ''}</span></span></label>)}</fieldset>
    <fieldset className="discovery-fieldset"><legend>Regions <span className="muted">(up to {options.limits.maxRegions})</span></legend><div className="discovery-regions">{options.regions.map(r => <label key={r} className="check"><input type="checkbox" checked={scope.regions.includes(r)} disabled={!scope.regions.includes(r) && scope.regions.length >= options.limits.maxRegions} onChange={() => toggle('regions', r)} /><span className="mono">{r}</span></label>)}</div></fieldset>
    <div className="form-grid">
     <label className="field"><span>Objects per type and region</span><input type="number" min={1} max={options.limits.maxPerType} value={scope.maxPerType} onChange={e => { setScope(s => ({ ...s, maxPerType: Number(e.target.value) || 0 })); setConsent(false); }} /><small>Listing stops at this limit; a truncated list is marked incomplete.</small></label>
     <label className="field"><span>Concurrent reads</span><select value={scope.concurrency} onChange={e => setScope(s => ({ ...s, concurrency: Number(e.target.value) }))}>{Array.from({ length: options.limits.maxConcurrency }, (_, i) => i + 1).map(n => <option key={n} value={n}>{n}</option>)}</select><small>Fewer reads at once reduce throttling. Throttled reads are retried with backoff.</small></label>
    </div>
    <ul className="preflight-list">
     <li><CloudCog size={16} /><span><strong>Calls AWS list APIs {estimate.min === estimate.max ? estimate.min : `${estimate.min}–${estimate.max}`} times</strong> ({estimate.tasks} list task{estimate.tasks === 1 ? '' : 's'} plus one STS check) with your local credentials. Some AWS APIs can incur charges or count against API rate limits shared with other tools.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Read-only.</strong> Only Describe and List operations run; Hclaro cannot create, change or delete cloud objects, and writes no state or files.</span></li>
     <li><CircleHelp size={16} /><span>Tags and names are untrusted text: credential-like values are hidden. Rules, policies, endpoints and database configuration are never read into Hclaro.</span></li>
    </ul>
    <label className="check consent"><input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} /><span>Call the AWS APIs listed above for account {identity.account}</span></label>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => void start()} disabled={!valid || !consent || busy}><Play size={14} />{busy ? 'Starting…' : 'Start scan'}</Button></div>
   </div>
  </div>;
}

function typeLabel(options: DiscoveryOptions, type: string) {
  return options.types.find(t => t.type === type)?.label ?? type;
}

function Outcome({ scan, options }: { scan: DiscoveryScan; options: DiscoveryOptions }) {
  const failed = scan.tasks.filter(t => t.status === 'failed');
  const truncated = scan.tasks.filter(t => t.truncated);
  if (scan.status === 'running') {
    const done = scan.tasks.filter(t => t.status === 'done' || t.status === 'failed').length;
    return <section className="discovery-progress" role="status"><h2><LoaderCircle className="spin" size={16} />Scanning… {done} of {scan.tasks.length} lists done · {scan.calls} AWS requests</h2></section>;
  }
  if (scan.status === 'completed') return <section className="operation-applied" role="status"><h2><CircleCheck size={17} />Scan complete</h2><p>{scan.results?.length ?? 0} object{scan.results?.length === 1 ? '' : 's'} found with {scan.calls} AWS requests{scan.duplicatesMerged ? `; ${scan.duplicatesMerged} duplicate report${scan.duplicatesMerged === 1 ? '' : 's'} merged` : ''}.</p></section>;
  const title = scan.status === 'partial' ? 'Scan incomplete' : scan.status === 'cancelled' ? 'Scan cancelled' : 'Scan failed';
  return <section className={scan.status === 'partial' ? 'drift-outcome' : 'operation-failed'} role="alert">
   <h2>{scan.status === 'partial' ? <TriangleAlert size={17} /> : <CircleX size={17} />}{title}</h2>
   {scan.message && <p>{scan.message}</p>}
   {(failed.length > 0 || truncated.length > 0) && <ul className="discovery-task-issues">
    {failed.map(t => <li key={t.type + t.region}><strong>{typeLabel(options, t.type)} · {t.region}</strong>: {t.message}</li>)}
    {truncated.map(t => <li key={'t' + t.type + t.region}><strong>{typeLabel(options, t.type)} · {t.region}</strong>: stopped at {t.count} objects; more exist.</li>)}
   </ul>}
   <p className="muted">{scan.status === 'failed' ? 'No objects were listed. This is not the same as no objects.' : 'Objects of the services and regions above may be missing; missing is not the same as none.'}</p>
  </section>;
}

function ScanResult({ scope, scan, options, profileId, onPrepared, openResource, copy }: { scope: PlanScope; scan: DiscoveryScan; options: DiscoveryOptions; profileId: string; onPrepared: (p: DiscoveryPrefill) => void; openResource: (id: string) => void; copy: (value: string, label: string) => Promise<void> }) {
  const [filter, setFilter] = useState<DiscoveryMatchStatus[]>([]);
  const [service, setService] = useState('');
  const [search, setSearch] = useState('');
  const [confirming, setConfirming] = useState<CloudResourceCandidate>();
  const [preparing, setPreparing] = useState<string>();
  const [error, setError] = useState('');
  const results = useMemo(() => scan.results ?? [], [scan.results]);
  const visible = useMemo(() => results.filter(c => (!filter.length || filter.includes(c.match.status)) && (!service || c.type === service) && (!search || `${c.resourceId} ${c.name ?? ''} ${c.type} ${c.region} ${c.tags.map(t => `${t.key}=${t.value}`).join(' ')}`.toLowerCase().includes(search.toLowerCase()))), [results, filter, service, search]);
  async function prepare(c: CloudResourceCandidate, acknowledgeUnknownMapping = false) {
    setPreparing(c.id);
    setError('');
    try {
      onPrepared(await post<DiscoveryPrefill>(at(scope, `scans/${scan.id}/candidates/${c.id}/prepare`), { ...bound(options, profileId, scope), acknowledgeUnknownMapping }));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The import could not be prepared.');
    } finally {
      setPreparing(undefined);
      setConfirming(undefined);
    }
  }
  return <div className="plan-review discovery-result">
   <div className="plan-meta">
    <div><span className="muted">Started</span><strong>{clock(scan.startedAt)}</strong></div>
    <div><span className="muted">Account</span><strong className="mono">{scan.identity.account}</strong></div>
    <div><span className="muted">Regions</span><strong>{scan.scope.regions.join(', ')}</strong></div>
    <div><span className="muted">Workspace</span><strong>{scan.context.workspace}</strong></div>
    <div><span className="muted">Profile</span><strong>{scan.context.profileName || 'None (inherited)'}</strong></div>
   </div>
   {scan.current === false && <div className="plan-warning" role="alert"><TriangleAlert size={15} /><span><strong>This scan belongs to another context.</strong> {scan.staleReason}</span></div>}
   <Outcome scan={scan} options={options} />
   {scan.current && results.length > 0 && <>
    <div className="summary-grid" role="group" aria-label="Filter by state mapping">{order.map(s => {
     const { label, icon: Icon, tone } = labels[s];
     const n = scan.summary?.[s] ?? 0;
     return <button key={s} className={`summary-card tone-${tone} ${filter.includes(s) ? 'chosen' : ''}`} aria-pressed={filter.includes(s)} disabled={!n} onClick={() => setFilter(f => f.includes(s) ? f.filter(v => v !== s) : [...f, s])}><span><Icon size={14} aria-hidden="true" />{label}</span><strong>{n}</strong></button>;
    })}</div>
    <p className="muted discovery-note">“Possibly untracked” means not in <strong>this</strong> workspace’s state. Other workspaces, projects or tools may manage the object. Identifiers are compared exactly; a different format is labelled unknown, never untracked.</p>
    <div className="filter-row plan-filters">
     <div className="search-field compact"><Search size={14} /><input value={search} onChange={e => setSearch(e.target.value)} placeholder="Filter by ID, name, region or tag…" aria-label="Filter discovered objects" />{search && <button className="clear-search" aria-label="Clear filter" onClick={() => setSearch('')}><X size={12} /></button>}</div>
     <label className="history-select"><span className="muted">Service</span><select aria-label="Service filter" value={service} onChange={e => setService(e.target.value)}><option value="">All types</option>{options.types.filter(t => scan.scope.types.includes(t.type)).map(t => <option key={t.type} value={t.type}>{t.label}</option>)}</select></label>
     {(filter.length > 0 || search || service) && <Button variant="ghost" size="small" onClick={() => { setFilter([]); setSearch(''); setService(''); }}><X size={12} />Reset</Button>}
     <span className="filter-count">{visible.length} shown</span>
    </div>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="table-scroll"><table className="resources-table changes-table discovery-table">
     <thead><tr><th scope="col">State mapping</th><th scope="col">Object</th><th scope="col">Region</th><th scope="col">Tags</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
     <tbody>{visible.map(c => <tr key={c.id}>
      <td><MatchBadge value={c.match.status} /></td>
      <td><span className="resource-text"><strong className="mono">{c.resourceId}</strong>{c.name && c.name !== c.resourceId && <span>{c.name}</span>}<span className="resource-type">{c.type}{Object.entries(c.details).map(([k, v]) => ` · ${k} ${v}`).join('')}</span>
       <span className="muted discovery-reason">{c.match.reason}</span>
       {c.match.addresses.length > 0 && <span className="discovery-addresses">{c.match.addresses.map((a, i) => <button key={a} className="link-button mono" onClick={() => openResource(c.match.resourceIds[i])}>{a}<ArrowRight size={11} /></button>)}</span>}
      </span></td>
      <td className="mono">{c.region || 'unknown'}{c.seenInRegions.length > 1 && <span className="muted"> (seen in {c.seenInRegions.join(', ')})</span>}</td>
      <td>{c.tags.length ? <ul className="tag-list">{c.tags.map(t => <li key={t.key}><code>{t.key}</code>={t.value === '[redacted]' ? <span className="redacted">redacted</span> : <span>{t.value}</span>}</li>)}{c.tagsOmitted ? <li className="muted">+{c.tagsOmitted} hidden</li> : null}</ul> : <span className="muted">none</span>}</td>
      <td><div className="discovery-row-actions">
       {c.importId && <Button variant="ghost" size="icon" aria-label={`Copy import ID of ${c.resourceId}`} onClick={() => void copy(c.importId!, 'Import ID')}><Copy size={13} /></Button>}
       {(c.match.status === 'possibly_untracked' || c.match.status === 'unknown') && <Button variant="outline" size="small" disabled={!!preparing} onClick={() => c.match.status === 'unknown' ? setConfirming(c) : void prepare(c)}><Import size={13} />{preparing === c.id ? 'Checking…' : 'Prepare import'}</Button>}
      </div></td>
     </tr>)}</tbody>
    </table></div>
   </>}
   {scan.current && scan.status !== 'running' && results.length === 0 && scan.status !== 'failed' && <div className="empty-state"><Radar size={26} /><h2>No objects listed</h2><p>{scan.status === 'completed' ? 'Every selected list returned no objects in the scanned regions.' : 'Some lists failed, so this is not proof that no objects exist.'}</p></div>}
   {confirming && <UnknownConfirm candidate={confirming} busy={!!preparing} onClose={() => setConfirming(undefined)} onConfirm={() => void prepare(confirming, true)} />}
  </div>;
}

function UnknownConfirm({ candidate, busy, onClose, onConfirm }: { candidate: CloudResourceCandidate; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="alertdialog" aria-modal="true" aria-labelledby="unknown-title" aria-describedby="unknown-reason">
    <div className="dialog-header"><h2 id="unknown-title">State mapping unknown</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="unknown-reason"><code>{candidate.resourceId}</code> may already be managed: {candidate.match.reason}</p>
    {candidate.match.addresses.length > 0 && <p>Matching addresses: {candidate.match.addresses.map(a => <code key={a}>{a} </code>)}</p>}
    <p className="muted">Preparing does not import anything. The wizard still asks for the address and a reviewed plan, and the plan fails if the object is already in state at another address.</p>
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={onConfirm} disabled={busy}><Import size={14} />I reviewed it, prepare import</Button></div>
   </div>
  </div>;
}
