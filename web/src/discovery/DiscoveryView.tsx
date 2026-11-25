import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, Check, ChevronDown, CircleCheck, CircleDashed, CircleHelp, CircleSlash, CircleX, Cloud, CloudCog, Copy, History, Import, LoaderCircle, Play, Radar, Search, ShieldCheck, Square, TriangleAlert, X, type LucideIcon } from 'lucide-react';
import { api, ApiError, send } from '../api';
import type { CloudIdentity, CloudResourceCandidate, DiscoveryMatchStatus, DiscoveryOptions, DiscoveryPrefill, DiscoveryProvider, DiscoveryScan, DiscoveryScope } from '../types';
import { Button } from '../components/ui/button';
import { Dialog } from '../components/Dialog';
import { Popover } from '../components/Menu';
import { Disclosure } from '../components/Disclosure';
import { EmptyState } from '../components/EmptyState';
import { Segmented } from '../components/Segmented';
import { Chip, type Tone } from '../components/Chip';
import { ContextSummary, ScopeSummary } from '../profiles/ContextSummary';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { clock } from '../lib/format';
import { errorMessage } from '../lib/errorMessage';
import { ErrorState } from '../components/ErrorState';

// Labels are distinguished by icon and text, never by color alone.
const labels: Record<DiscoveryMatchStatus, { label: string; icon: LucideIcon; tone: string; chip: Tone; hint: string }> = {
  possibly_untracked: { label: 'Possibly untracked', icon: CircleDashed, tone: 'create', chip: 'create', hint: 'Not in this workspace’s state. Another workspace, project or tool may still manage it.' },
  already_tracked: { label: 'Already tracked', icon: CircleCheck, tone: 'neutral', chip: 'ok', hint: 'Recorded in this workspace’s state with the same identifier.' },
  unknown: { label: 'Unknown', icon: CircleHelp, tone: 'warn', chip: 'update', hint: 'The mapping could not be decided. Never imported without your review.' },
  unresolved: { label: 'Unresolved', icon: CircleSlash, tone: 'muted', chip: 'neutral', hint: 'No verified import ID, so it cannot be compared or imported.' }
};
const order: DiscoveryMatchStatus[] = ['possibly_untracked', 'unknown', 'already_tracked', 'unresolved'];
// §4.11: concurrent reads are 1, 2 or 4 (up to the server's limit).
const concurrencyChoices = ['1', '2', '4'];

function MatchBadge({ value }: { value: DiscoveryMatchStatus }) {
  const { label, icon: Icon, chip, hint } = labels[value] ?? labels.unknown;
  return <Chip tone={chip} title={hint} icon={<Icon size={12} aria-hidden="true" />}>{label}</Chip>;
}

function query(profileId: string, provider: DiscoveryProvider) {
  const parts = [profileId && `profileId=${profileId}`, provider !== 'aws' && `provider=${provider}`].filter(Boolean);
  return parts.length ? `?${parts.join('&')}` : '';
}

// AWS is the default: its requests carry no provider, as before Google Cloud existed.
function withProvider(provider: DiscoveryProvider) {
  return provider === 'aws' ? {} : { provider };
}

// What differs by cloud in the page's wording; the same screens serve every provider.
const clouds: Record<DiscoveryProvider, { name: string; cli: string; account: string; ops: string; via: string; verify: string; check: string; api: string }> = {
  aws: { name: 'AWS', cli: 'AWS CLI', account: 'account', ops: 'Describe and List', via: 'STS', verify: 'STS GetCallerIdentity', check: 'one STS check', api: 'AWS APIs' },
  gcp: { name: 'Google Cloud', cli: 'Google Cloud CLI (gcloud)', account: 'project', ops: 'list', via: 'gcloud', verify: 'gcloud config and project lookup', check: 'one project check', api: 'Google Cloud APIs' }
};

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
  const [provider, setProvider] = useState<DiscoveryProvider>('aws');
  const q = scoped ? query('', provider) : query(profileId, provider);
  const options = useQuery({ queryKey: ['discovery-options', key, profileId, workspaceStatus, provider], queryFn: () => api<DiscoveryOptions>(at(scope, 'options') + q) });
  const scans = useQuery({ queryKey: ['discovery-scans', key], queryFn: () => api<{ scans: DiscoveryScan[] }>(at(scope, 'scans')), enabled: !!options.data?.canScan });
  const [scanId, setScanId] = useState<string>();
  const shownId = scanId ?? scans.data?.scans[0]?.id;
  const scan = useQuery({
    queryKey: ['discovery-scan', key, shownId, profileId, workspaceStatus, provider],
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
      await send<CloudIdentity>('POST', at(scope, 'identity'), { ...bound(o, profileId, scope), ...withProvider(provider) });
      await options.refetch();
    } catch (e) {
      setVerifyError(errorMessage(e, `The ${clouds[provider].name} context could not be verified.`));
    } finally {
      setVerifying(false);
    }
  }
  async function cancel(id: string) {
    try {
      await send('POST', at(scope, `scans/${id}/cancel`), {});
    } finally {
      void scan.refetch();
    }
  }
  if (scoped && options.error instanceof ApiError && (options.error.code === 'context_closed' || options.error.code === 'scope_changed')) return <ErrorState icon={TriangleAlert} iconSize={26} title="Open this workspace again" message={options.error.message} retry={reopen} retryLabel="Open again" note="Nothing ran." />;
  if (options.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading cloud discovery…</p></div>;
  if (options.error || !o) return <ErrorState inline message={options.error?.message || 'Cloud discovery is unavailable.'} />;
  const cloud = clouds[o.provider];
  // Only providers whose CLI is on PATH are offered; AWS stays so its missing CLI is explained.
  const offered = (o.providers ?? []).filter(p => p.detected || p.name === provider);
  return <div className="plans discovery">
   {offered.length > 1 && <Segmented label="Cloud provider" options={offered.map(p => ({ value: p.name, label: p.label }))} value={provider} onChange={p => { setProvider(p); setScanId(undefined); setVerifyError(''); }} />}
   <Disclosure summary={<>Finds {cloud.name} objects this workspace doesn’t record yet, in {o.types.length} types. <span className="discovery-covered">What’s covered</span></>}>
    <p>Cloud discovery lists existing {cloud.name} objects of {o.types.length} supported types only ({o.types.map((t, i) => <Fragment key={t.type}>{i ? ', ' : ''}<code>{t.type}</code></Fragment>)}) in the {cloud.account} your credentials belong to, at most {o.limits.maxRegions} regions, {o.limits.maxPerType.toLocaleString()} objects per type and region and {o.limits.maxObjects.toLocaleString()} objects per scan. Other {cloud.name} types and other clouds are not discovered. It compares them with <strong>this workspace’s recorded state</strong>. It never creates, changes or deletes cloud objects, and never writes state. Nothing is contacted until you ask.</p>
   </Disclosure>
   {!o.canScan && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span><strong>Cloud discovery is unavailable here.</strong> {o.reason}</span></div>}
   {o.canScan && <AccountCard options={o} identity={identity} busy={verifying} blocked={blocked} error={verifyError} onVerify={() => void verify()} onScan={() => setPreview(true)} running={running} scanned={!!shownId} />}
   {scans.data && scans.data.scans.length > 0 && <div className="plans-toolbar">
    <label className="history-select"><History size={14} /><span className="sr-only">Scan history</span><select aria-label="Scan history" value={shownId} onChange={e => setScanId(e.target.value)}>{scans.data.scans.map((s, i) => <option key={s.id} value={s.id}>{i === 0 ? 'Latest · ' : ''}{clock(s.startedAt)} · {s.identity.account}{s.status === 'completed' ? '' : ` · ${s.status}`}</option>)}</select></label>
    {running && scan.data && <Button variant="outline" size="small" onClick={() => void cancel(scan.data!.id)}><Square size={12} />Cancel scan</Button>}
   </div>}
   {scan.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading scan…</p></div> : scan.error ? <ErrorState inline message={scan.error.message} /> : scan.data ? <ScanResult key={scan.data.id} scope={scope} scan={scan.data} options={o} profileId={profileId} onPrepared={onPrepared} openResource={openResource} copy={copy} /> : identity && o.canScan && <EmptyState icon={<Radar size={28} />} title="No discovery scan yet">Choose regions and resource types to list. Each scan calls {cloud.name} list APIs with your local credentials, only when you ask.</EmptyState>}
   {o.canScan && (o.scope ? <Disclosure summary="Execution context"><ScopeSummary scope={o.scope} executionContext={o.executionContext} title="Discovery uses" /></Disclosure> : o.context ? <Disclosure summary="Execution context"><ContextSummary summary={o.context} title="Discovery uses" /></Disclosure> : o.profileError ? <p className="dialog-error" role="alert">{o.profileError}</p> : null)}
   {preview && identity && <ScanPreview target={scope} options={o} identity={identity} profileId={profileId} onClose={() => setPreview(false)} onStarted={s => {
     setPreview(false);
     setScanId(s.id);
     void client.invalidateQueries({ queryKey: ['discovery-scans'] });
   }} />}
  </div>;
}

// §4.11: the account card. Unverified, it explains and offers Verify; verified, it shows the
// account, principal, default region, AWS CLI and permissions (the facts fold away once a scan exists).
function AccountCard({ options, identity, busy, blocked, error, onVerify, onScan, running, scanned }: { options: DiscoveryOptions; identity?: CloudIdentity; busy: boolean; blocked: boolean; error: string; onVerify: () => void; onScan: () => void; running: boolean; scanned: boolean }) {
  const profile = options.context?.context.profileName;
  const cloud = clouds[options.provider];
  const awsProfile = options.provider === 'aws' ? options.context?.context.awsProfile : options.context?.context.gcpConfiguration;
  const facts = identity && <dl className="metadata compact-metadata discovery-account-facts">
   <div><dt>Principal</dt><dd className="mono">{identity.arn} <span className="muted">({identity.principalType})</span></dd></div>
   <div><dt>Default region</dt><dd>{identity.region ? <code>{identity.region}</code> : <span className="muted">none</span>} <span className="muted">({identity.regionSource === 'profile' ? 'from the execution profile' : identity.regionSource === 'environment' ? 'from the environment' : identity.regionSource === 'gcloud' ? 'from the gcloud configuration' : options.provider === 'gcp' ? 'no region configured' : 'no region configured; us-east-1'})</span></dd></div>
   <div><dt>{cloud.cli}</dt><dd className="mono">{identity.cliVersion}</dd></div>
   <div><dt>Permissions</dt><dd className="muted">Checked per service when you scan. A denied service shows as missing, not empty.</dd></div>
  </dl>;
  return <section className="surface discovery-account" aria-labelledby="discovery-account-title">
   <div className="discovery-account-head">
    <span className={`discovery-account-mark${identity ? ' verified' : ''}`} aria-hidden="true">{identity ? <Check size={15} /> : <Cloud size={18} />}</span>
    <div>
     <h2 id="discovery-account-title">{identity ? <>{cloud.name} {cloud.account} {identity.account}</> : `${cloud.name} context not verified`}</h2>
     <p>{identity ? <>Verified {clock(identity.verifiedAt)} with {cloud.via} for workspace <strong>{options.workspace}</strong>{profile ? <> and profile <strong>{profile}</strong></> : ' and the inherited environment'} · valid until {clock(identity.expiresAt)}. Each scan and import preparation checks the {cloud.account} again.</> : <>Hclaro never infers the {cloud.account} from a profile name. Verify asks {options.provider === 'aws' ? 'AWS ' : ''}<code>{cloud.verify}</code> (read-only{options.provider === 'aws' ? ', contacts AWS' : '; the project lookup contacts Google Cloud'}) to learn which {cloud.account} and principal the {awsProfile ? <>{options.provider === 'aws' ? 'profile' : 'configuration'} <code>{awsProfile}</code></> : 'inherited credentials'} belong to.</>}</p>
    </div>
    <Button variant={identity ? 'outline' : 'default'} onClick={onVerify} disabled={busy || blocked}><ShieldCheck size={14} />{busy ? 'Verifying…' : identity ? 'Verify again' : `Verify ${cloud.name} context`}</Button>
    {identity && <Button onClick={onScan} disabled={blocked || running}><Radar size={14} />{scanned ? 'Scan again…' : 'Scan…'}</Button>}
   </div>
   {identity && (scanned ? <Disclosure summary="Account details">{facts}</Disclosure> : facts)}
   {blocked && <p className="dialog-error" role="alert">Resolve the execution context issues before verifying.</p>}
   {error && <p className="dialog-error" role="alert">{error}</p>}
  </section>;
}

// The scan preview states the scope, the request bounds and the possible cost before anything runs.
function ScanPreview({ target, options, identity, profileId, onClose, onStarted }: { target: PlanScope; options: DiscoveryOptions; identity: CloudIdentity; profileId: string; onClose: () => void; onStarted: (scan: DiscoveryScan) => void }) {
  const cloud = clouds[options.provider];
  const [scope, setScope] = useState<DiscoveryScope>({ regions: [identity.region || options.defaultRegion], types: options.types.map(t => t.type), maxPerType: options.limits.defaultPerType, concurrency: options.limits.defaultConcurrency });
  const [consent, setConsent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
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
      onStarted(await send<DiscoveryScan>('POST', at(target, 'scans'), { identityId: identity.id, scope, acknowledgeApiCalls: true, ...withProvider(options.provider), ...bound(options, profileId, target) }));
    } catch (e) {
      setError(errorMessage(e, 'The scan could not be started.'));
    } finally {
      setBusy(false);
    }
  }
  return <Dialog title={`Scan ${cloud.name} ${cloud.account} ${identity.account}`} wide onClose={onClose} footer={<>
    <Button variant="ghost" onClick={onClose}>Cancel</Button>
    <Button onClick={() => void start()} disabled={!valid || !consent || busy}><Play size={14} />{busy ? 'Starting…' : 'Start scan'}</Button>
   </>}>
    <div className="discovery-scan">
    <p>Choose what to list. Only this {cloud.account} is scanned, never an organization or other {cloud.account}s.</p>
    <div className="discovery-types-field"><span className="discovery-types-title">Resource types</span>
     <Popover label="Resource types" className="discovery-types" button={<>{scope.types.length === options.types.length ? `All ${options.types.length} types` : `${scope.types.length} of ${options.types.length} types`}<ChevronDown size={13} aria-hidden="true" /></>}>
      <div className="discovery-types-actions"><Button variant="ghost" size="small" onClick={() => { setScope(s => ({ ...s, types: options.types.map(t => t.type) })); setConsent(false); }}>Select all</Button><Button variant="ghost" size="small" onClick={() => { setScope(s => ({ ...s, types: [] })); setConsent(false); }}>Clear</Button></div>
      <div className="discovery-types-list">{options.types.map(t => <label key={t.type} className="check"><input type="checkbox" checked={scope.types.includes(t.type)} onChange={() => toggle('types', t.type)} /><span><strong>{t.label}</strong> <code>{t.type}</code> {t.global && <span className="muted">· listed once for the {cloud.account}</span>}</span></label>)}</div>
     </Popover>
     <small className="muted">Each type needs its own read permission; a denied one is shown as missing, not empty.</small>
    </div>
    <fieldset className="discovery-fieldset"><legend>Regions <span className="muted">({scope.regions.length} of up to {options.limits.maxRegions})</span></legend><div className="discovery-regions">{options.regions.map(r => <label key={r} className="check"><input type="checkbox" checked={scope.regions.includes(r)} disabled={!scope.regions.includes(r) && scope.regions.length >= options.limits.maxRegions} onChange={() => toggle('regions', r)} /><span className="mono">{r}</span></label>)}</div></fieldset>
    <div className="form-grid">
     <label className="field"><span>Objects per type and region</span><input type="number" min={1} max={options.limits.maxPerType} value={scope.maxPerType} onChange={e => { setScope(s => ({ ...s, maxPerType: Number(e.target.value) || 0 })); setConsent(false); }} /><small>Listing stops at this limit; a truncated list is marked incomplete.</small></label>
     <div className="field"><span>Concurrent reads</span><Segmented label="Concurrent reads" options={concurrencyChoices.filter(n => Number(n) <= options.limits.maxConcurrency).map(n => ({ value: n, label: n }))} value={String(scope.concurrency)} onChange={n => setScope(s => ({ ...s, concurrency: Number(n) }))} /><small>Fewer reads at once reduce throttling. Throttled reads are retried with backoff.</small></div>
    </div>
    <ul className="preflight-list">
     <li><CloudCog size={16} /><span><strong>Calls {cloud.name} list APIs {estimate.min === estimate.max ? estimate.min : `${estimate.min}–${estimate.max}`} times</strong> ({estimate.tasks} list task{estimate.tasks === 1 ? '' : 's'} plus {cloud.check}) with your local credentials. Some APIs can incur charges or count against rate limits shared with other tools.{options.provider === 'gcp' && ' Each type is listed for the whole project in one request; the regions you choose only filter what is kept.'}</span></li>
     <li><ShieldCheck size={16} /><span><strong>Read-only.</strong> Only {cloud.ops} operations run; Hclaro cannot create, change or delete cloud objects, and writes no state or files.</span></li>
     <li><CircleHelp size={16} /><span>Tags and names are untrusted text: credential-like values are hidden. Rules, policies, endpoints, instance metadata and database configuration are never read into Hclaro.</span></li>
    </ul>
    <label className="check consent"><input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} /><span>Call the {cloud.api} listed above for {cloud.account} {identity.account}</span></label>
    {error && <p className="dialog-error" role="alert">{error}</p>}
    </div>
  </Dialog>;
}

function typeLabel(options: DiscoveryOptions, type: string) {
  return options.types.find(t => t.type === type)?.label ?? type;
}

function Outcome({ scan, options }: { scan: DiscoveryScan; options: DiscoveryOptions }) {
  const failed = scan.tasks.filter(t => t.status === 'failed' || (scan.truncated && t.status === 'cancelled'));
  const truncated = scan.tasks.filter(t => t.truncated);
  if (scan.status === 'running') {
    const done = scan.tasks.filter(t => t.status === 'done' || t.status === 'failed').length;
    return <section className="discovery-progress" role="status"><h2><LoaderCircle className="spin" size={16} />Scanning… {done} of {scan.tasks.length} lists done · {scan.calls} {clouds[options.provider].name} requests</h2></section>;
  }
  if (scan.status === 'completed') return <p className="discovery-complete" role="status"><span className="discovery-account-mark verified" aria-hidden="true"><Check size={13} /></span><span><strong>Scan complete.</strong> {scan.results?.length ?? 0} object{scan.results?.length === 1 ? '' : 's'} found with {scan.calls} {clouds[options.provider].name} requests{scan.duplicatesMerged ? `; ${scan.duplicatesMerged} duplicate report${scan.duplicatesMerged === 1 ? '' : 's'} merged` : ''}.</span></p>;
  const title = scan.status === 'partial' ? 'Scan incomplete' : scan.status === 'cancelled' ? 'Scan cancelled' : 'Scan failed';
  return <section className={scan.status === 'partial' ? 'drift-outcome' : 'operation-failed'} role="alert">
   <h2>{scan.status === 'partial' ? <TriangleAlert size={17} /> : <CircleX size={17} />}{title}</h2>
   {scan.message && <p>{scan.message}</p>}
   {(failed.length > 0 || truncated.length > 0 || scan.truncated) && <ul className="discovery-task-issues">
    {scan.truncated && <li><strong>All types</strong>: stopped at {options.limits.maxObjects.toLocaleString()} objects per scan; more exist. Scan fewer regions or types.</li>}
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
      onPrepared(await send<DiscoveryPrefill>('POST', at(scope, `scans/${scan.id}/candidates/${c.id}/prepare`), { ...bound(options, profileId, scope), acknowledgeUnknownMapping, ...withProvider(options.provider) }));
    } catch (e) {
      setError(errorMessage(e, 'The import could not be prepared.'));
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
  return <Dialog title="State mapping unknown" onClose={onClose} initial={confirm} footer={<>
    <Button variant="ghost" onClick={onClose}>Cancel</Button>
    <Button ref={confirm} onClick={onConfirm} disabled={busy}><Import size={14} />I reviewed it, prepare import</Button>
   </>}>
   <p><code>{candidate.resourceId}</code> may already be managed: {candidate.match.reason}</p>
   {candidate.match.addresses.length > 0 && <p>Matching addresses: {candidate.match.addresses.map(a => <code key={a}>{a} </code>)}</p>}
   <p className="muted">Preparing does not import anything. The wizard still asks for the address and a reviewed plan, and the plan fails if the object is already in state at another address.</p>
  </Dialog>;
}
