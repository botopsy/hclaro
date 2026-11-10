import { useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { CircleCheck, CircleHelp, CloudCog, Radar, Unlink, Copy, Eye, EyeOff, FilePlus2, FileSearch, Import, Layers3, LoaderCircle, LockKeyhole, Play, ShieldCheck, Terminal, TriangleAlert } from 'lucide-react';
import { api, ApiError, post } from '../api';
import type { DiscoveryPrefill, ImportOptions, ImportPreview, ImportRequest, Operation, PlanOptions } from '../types';
import { ContextSummary } from '../profiles/ContextSummary';
import { useVarFileChoice, VarFileChoice } from '../profiles/VarFileChoice';
import { Button } from '../components/ui/button';
import { engineLabel } from '../plans/labels';
import { ScopeSummary } from '../plans/PreflightDialog';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../plans/routes';

// addressType returns the resource type of an instance address (the server parses it exactly).
function addressType(address: string) {
  const parts = address.trim().replace(/\[[^\]]*\]$/, '').split('.');
  return parts.length >= 2 ? parts[parts.length - 2] : '';
}

const blank: ImportRequest = { address: '', id: '', provider: '', file: 'imports.tf', generateConfig: false };

// The wizard checks a request on the server (no side effects), shows the exact new file and
// only then offers to write it and run a full plan. Applying happens later, from the plan
// review, with the same confirmation as any other plan.
export function ImportView({
  scope = launchScope,
  reopen,
  workspaceStatus,
  profileId = '',
  onStarted,
  copy,
  prefill,
  clearPrefill
}: {
  // The project or Hub workspace the import is written to and planned in. Every request goes
  // to it only; there is no fallback from a Hub workspace to the launch project.
  scope?: PlanScope;
  // Opens a closed Hub workspace again (after its context changed).
  reopen?: () => void;
  workspaceStatus?: string;
  // An object prepared in Cloud discovery: it fills the ID and suggests an address. The
  // address and resource block remain the user's decision.
  prefill?: DiscoveryPrefill;
  clearPrefill?: () => void;
  // The execution profile this tab uses ("" for none).
  profileId?: string;
  onStarted: (operation: Operation) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const [request, setRequest] = useState<ImportRequest>(() => prefill ? { ...blank, address: prefill.suggestedAddress, id: prefill.importId } : blank);
  // The import stays linked to the discovered object only while its ID and type are unchanged;
  // the server checks the link again before planning.
  const linked = !!prefill && request.id === prefill.importId && addressType(request.address) === prefill.type;
  const [maskId, setMaskId] = useState(false);
  const [preview, setPreview] = useState<ImportPreview>();
  const [checking, setChecking] = useState(false);
  const [consent, setConsent] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState('');
  const scoped = scope.kind === 'context';
  const options = useQuery({ queryKey: ['import-options', scopeKey(scope), workspaceStatus], queryFn: () => api<ImportOptions>(scopedPath(scope, 'imports/options')) });
  const o = options.data;
  // The execution context summary comes from the plan options: an import is a plan. A Hub
  // workspace's import options carry its scope and recorded context themselves.
  const context = useQuery({ queryKey: ['plan-options', workspaceStatus, profileId], queryFn: () => api<PlanOptions>(`plans/options${profileId ? `?profileId=${profileId}` : ''}`), enabled: !scoped });
  const choice = useVarFileChoice(scoped ? o && { varFiles: o.varFiles, executionContext: o.executionContext ?? undefined, contextFingerprint: o.contextFingerprint } : context.data);
  const summary = context.data?.context;
  const contextBlocked = !!summary && summary.issues.length > 0 || !!context.data?.profileError;
  const engine = engineLabel(o?.engine ?? 'terraform');
  // Any edit invalidates the preview: the server only accepts what was reviewed.
  function change(update: Partial<ImportRequest>) {
    setRequest(r => ({ ...r, ...update }));
    setPreview(undefined);
    setConsent(false);
    setError('');
  }
  async function check(event?: FormEvent) {
    event?.preventDefault();
    setChecking(true);
    setError('');
    try {
      setPreview(await post<ImportPreview>(scopedPath(scope, 'imports/preview'), { ...request, address: request.address.trim() }));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The import could not be checked.');
    } finally {
      setChecking(false);
    }
  }
  async function start() {
    if (!preview) return;
    setStarting(true);
    setError('');
    try {
      const reviewed = { ...request, address: request.address.trim(), varFiles: choice.chosen, previewSha256: preview.sha256, acknowledgeWrite: true, acknowledgeBackendAccess: true };
      // A Hub workspace's import is bound to the scope its options described; the server
      // refuses it if anything in that scope changed since. It takes no profile or discovery link.
      let operation: Operation;
      if (scoped) operation = await post<Operation>(scopedPath(scope, 'imports'), { ...reviewed, contextFingerprint: o?.contextFingerprint ?? '' });
      else {
        const bound = summary?.context.profileId ? { profileId, contextFingerprint: summary.context.fingerprint } : {};
        operation = await post<Operation>('imports', { ...reviewed, ...bound, ...(linked && prefill ? { discoveryToken: prefill.token } : {}) });
      }
      setRequest(blank);
      clearPrefill?.();
      setPreview(undefined);
      setConsent(false);
      onStarted(operation);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The import could not be started.');
      // The file may exist now or the context changed; check again before retrying.
      setPreview(undefined);
      setConsent(false);
    } finally {
      setStarting(false);
    }
  }
  const errors = preview?.issues.filter(i => i.severity === 'error') ?? [];
  const warnings = preview?.issues.filter(i => i.severity === 'warning') ?? [];
  const ready = !!preview && !preview.blocked && !!o?.canImport;
  // A Hub workspace whose context changed or closed is never retargeted: open it again.
  if (scoped && options.error instanceof ApiError && (options.error.code === 'context_closed' || options.error.code === 'scope_changed')) return <div className="empty-state error-state" role="alert"><TriangleAlert size={26} /><h2>Open this workspace again</h2><p>{options.error.message}</p>{reopen && <Button variant="outline" onClick={reopen}>Open again</Button>}<span className="safe-note"><ShieldCheck size={13} />Nothing was written.</span></div>;
  return <div className="import-wizard">
   {workspaceStatus === 'loading' ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before imports can be checked…</div> : o && !o.canImport && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span>{o.reason || 'Importing through Hclaro is unavailable for this workspace.'} You can still check an import here and copy a manual recipe.</span></div>}
   {prefill && <section className={`discovery-link ${linked ? '' : 'unlinked'}`} aria-label="Discovered object">
    <div><Radar size={15} aria-hidden="true" /><span>{linked ? <>From Cloud discovery: <code>{prefill.type}</code> <code>{prefill.importId}</code> in account <strong>{prefill.account}</strong>{prefill.region && <>, {prefill.region}</>}. The address is only a suggestion: confirm it and the resource block, then review the plan.</> : <>No longer linked to the discovered object: the import ID or resource type was changed. This is now a manual import.</>}</span>
     <Button variant="ghost" size="small" onClick={() => { clearPrefill?.(); }}><Unlink size={12} />Clear</Button></div>
    {linked && prefill.warnings.length > 0 && <ul className="import-issues" aria-label="Discovery notes">{prefill.warnings.map(w => <li key={w} className="issue-warning"><CircleHelp size={14} aria-hidden="true" /><span>{w}</span></li>)}</ul>}
   </section>}
   <ol className="import-steps" aria-label="Import steps">
    <li className={preview ? 'done' : 'current'}><span>1</span>Describe the object</li>
    <li className={ready ? 'done' : preview ? 'current' : ''}><span>2</span>Review the new file</li>
    <li className={ready ? 'current' : ''}><span>3</span>Write and plan</li>
    <li><span>4</span>Review and apply</li>
   </ol>

   <form className="surface import-form" onSubmit={e => void check(e)} aria-labelledby="import-target-title">
    <div className="surface-heading"><div><h2 id="import-target-title">Existing object</h2><p>The object must already exist outside {engine}. Importing records it in state; it does not create or change it, but the plan can propose updates to match your configuration.</p></div><Import size={18} /></div>
    <div className="form-grid">
     <label className="field wide"><span>Resource instance address</span><input className="mono" aria-label="Resource instance address" value={request.address} onChange={e => change({ address: e.target.value })} placeholder={'aws_s3_bucket.logs or module.net.aws_subnet.private["a"]'} spellCheck={false} autoComplete="off" required aria-describedby="address-hint" /><small id="address-hint">Where the object will live in state. Managed resources only; include the count index or for_each key if the block uses one.</small></label>
     <label className="field wide"><span>Import ID</span><span className="input-with-action"><input className="mono" aria-label="Import ID" type={maskId ? 'password' : 'text'} value={request.id} onChange={e => change({ id: e.target.value })} placeholder="bucket name, ARN, resource ID…" spellCheck={false} autoComplete="off" required aria-describedby="id-hint" /><Button type="button" variant="ghost" size="icon" aria-label={maskId ? 'Show import ID' : 'Hide import ID'} aria-pressed={maskId} onClick={() => setMaskId(!maskId)}>{maskId ? <Eye size={14} /> : <EyeOff size={14} />}</Button></span><small id="id-hint">The provider-specific ID of the existing object. It is written into the new file in plain text; values that look like credentials are refused.</small></label>
     <label className="field"><span>Provider <span className="muted">(optional)</span></span><input className="mono" aria-label="Provider" value={request.provider} onChange={e => change({ provider: e.target.value })} placeholder="aws.west" spellCheck={false} autoComplete="off" /><small>Only for a non-default provider configuration.</small></label>
     <label className="field"><span>New file in the root module</span><input className="mono" aria-label="New file name" value={request.file} onChange={e => change({ file: e.target.value })} spellCheck={false} autoComplete="off" required /><small>Must not exist yet. Hclaro never edits existing files.</small></label>
    </div>
    <label className="check generate-check"><input type="checkbox" checked={request.generateConfig} disabled={!o?.supportsGeneratedConfig} onChange={e => change({ generateConfig: e.target.checked })} /><span>Let {engine} generate the resource block during the plan <span className="muted">{o && !o.supportsGeneratedConfig ? '(not supported by this engine version)' : '(root module, no instance key, and no existing block only)'}</span></span></label>
    <div className="form-actions"><Button type="submit" variant={preview ? 'outline' : 'default'} disabled={checking || !request.address || !request.id || !request.file}><FileSearch size={14} />{checking ? 'Checking…' : preview ? 'Check again' : 'Check and preview'}</Button><span className="muted"><ShieldCheck size={13} />Checking reads configuration and the state snapshot. Nothing is written or run.</span></div>
   </form>

   {error && <p className="dialog-error" role="alert">{error}</p>}

   {preview && <section className="surface import-review" aria-labelledby="import-review-title">
    <div className="surface-heading"><div><h2 id="import-review-title">Review</h2><p>{preview.address ? <>Import into <code>{preview.address}</code>{o && <> in workspace <strong>{o.workspace}</strong></>}.</> : 'Fix the issues below to see the import block.'}</p></div><FileSearch size={18} /></div>
    {errors.length > 0 && <ul className="import-issues" role="alert" aria-label="Problems to fix">{errors.map(i => <li key={i.code + i.message} className="issue-error"><TriangleAlert size={14} aria-hidden="true" /><span><strong>Cannot import yet.</strong> {i.message}</span></li>)}</ul>}
    {warnings.length > 0 && <ul className="import-issues" aria-label="Warnings">{warnings.map(i => <li key={i.code + i.message} className="issue-warning"><CircleHelp size={14} aria-hidden="true" /><span>{i.message}</span></li>)}</ul>}
    {preview.address && <p className="config-status">{preview.config === 'found' ? <><CircleCheck size={14} />Resource block found in <code>{preview.configFile}</code>.</> : preview.config === 'missing' && preview.generatedFile ? <><FilePlus2 size={14} />No resource block yet. {engine} will write one to <code>{preview.generatedFile}</code> during the plan.</> : preview.config === 'missing' ? <><TriangleAlert size={14} />No resource block found.</> : <><CircleHelp size={14} />The resource block could not be verified; the plan will report it if it is missing.</>}</p>}
    {preview.content && <div className="file-diff">
     <div className="file-diff-header"><span><FilePlus2 size={13} />New file <code>{preview.file}</code></span><Button variant="ghost" size="icon" aria-label="Copy import block" onClick={() => void copy(preview.content, 'Import block')}><Copy size={13} /></Button></div>
     <pre aria-label={`Contents of ${preview.file}`}>{preview.content.trimEnd().split('\n').map((line, i) => <span key={i} className="diff-line-added"><span aria-hidden="true">+ </span>{line}{'\n'}</span>)}</pre>
    </div>}
    {preview.generatedFile && !preview.blocked && <p className="info-line"><FilePlus2 size={13} /><span><code>{preview.generatedFile}</code> is a second new file, written by {engine} itself while planning. Its content depends on the live object, so it is shown in the plan review, before anything can be applied.</span></p>}
    {preview.recipe && <details className="recipe" open={!o?.canImport}>
     <summary><Terminal size={13} />Manual recipe</summary>
     <p className="muted">To import with your own CLI workflow instead: save the block above, then plan and apply a saved plan.</p>
     <pre>{preview.recipe.commands.join('\n')}</pre>
     <p className="plan-warning"><TriangleAlert size={14} /><span>Only for engines without import blocks: <code>{preview.recipe.legacy}</code> writes state immediately, without a plan you can review first. Hclaro never runs it.</span></p>
    </details>}
   </section>}

   {ready && o && <section className="surface import-confirm" aria-labelledby="import-confirm-title">
    <div className="surface-heading"><div><h2 id="import-confirm-title">Write the file and plan</h2><p>{engine} runs a full plan for workspace <strong>{o.workspace}</strong>. You review every change it proposes before anything is applied.</p></div><Play size={18} /></div>
    <ul className="preflight-list">
     <li><FilePlus2 size={16} /><span><strong>Writes {preview.generatedFile ? <>two new files</> : <>one new file</>}</strong>: <code>{preview.file}</code>{preview.generatedFile && <> now, and <code>{preview.generatedFile}</code> during the plan</>}. Existing files are never changed. To back out later, delete {preview.generatedFile ? 'them' : 'it'}.</span></li>
     <li><Layers3 size={16} /><span><strong>Becomes part of the project's configuration.</strong> Every workspace of this project plans this import block until you remove the file; only the plan of workspace <strong>{o.workspace}</strong> is run now.</span></li>
     <li><CloudCog size={16} /><span><strong>Contacts providers and the {o.backend} backend</strong> using your existing credentials, to read the existing object and refresh other resources.</span></li>
     <li><LockKeyhole size={16} /><span><strong>May lock the state</strong> while planning, if the backend supports locking.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not change state or infrastructure.</strong> {o.canApply ? 'The import is recorded only if you apply the reviewed plan and confirm it.' : 'Applying is unavailable here; you can review the plan only.'}</span></li>
    </ul>
    {scoped ? o.scope && <ScopeSummary scope={o.scope} executionContext={o.executionContext} title="This import plan will use" /> : summary ? <ContextSummary summary={summary} title="This import plan will use" /> : context.data?.profileError && <p className="dialog-error" role="alert">{context.data.profileError}</p>}
    <VarFileChoice choice={choice} />
    <label className="check consent"><input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} /><span>Write <code>{preview.file}</code> to this project</span></label>
    <div className="form-actions"><Button onClick={() => void start()} disabled={!consent || starting || contextBlocked} title={contextBlocked ? 'Resolve the execution context issues first.' : undefined}><Play size={14} />{starting ? 'Starting…' : 'Write file and plan import'}</Button></div>
   </section>}
  </div>;
}
