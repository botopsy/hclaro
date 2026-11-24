import { useState, type FormEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, Check, CircleCheck, CircleHelp, Copy, Eye, EyeOff, FilePlus2, FileSearch, Layers3, LoaderCircle, Play, Radar, ShieldCheck, Terminal, TriangleAlert, Unlink } from 'lucide-react';
import { api, ApiError, send } from '../api';
import type { DiscoveryPrefill, ImportOptions, ImportPreview, ImportRequest, Operation, PlanOptions } from '../types';
import { ContextSummary, ScopeSummary } from '../profiles/ContextSummary';
import { useVarFileChoice, VarFileChoice } from '../profiles/VarFileChoice';
import { Button } from '../components/ui/button';
import { launchScope, scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { AskAI } from '../assist/AskAI';
import { engineLabel } from '../lib/format';
import { errorMessage } from '../lib/errorMessage';
import { ErrorState } from '../components/ErrorState';
import { Disclosure } from '../components/Disclosure';

// addressType returns the resource type of an instance address (the server parses it exactly).
function addressType(address: string) {
  const parts = address.trim().replace(/\[[^\]]*\]$/, '').split('.');
  return parts.length >= 2 ? parts[parts.length - 2] : '';
}

const blank: ImportRequest = { address: '', id: '', provider: '', file: 'imports.tf', generateConfig: false };
const stepLabels = ['Existing object', 'Review the new file', 'Written'];
// Preview issue codes that belong to one field (internal/imports/check.go); they show under it.
const fieldCodes: Record<'address' | 'id' | 'provider' | 'file', string[]> = { address: ['invalid_address'], id: ['invalid_id', 'id_credential'], provider: ['invalid_provider'], file: ['invalid_file', 'file_exists'] };
const allFieldCodes = Object.values(fieldCodes).flat();

// §4.11: a 3-step card. The wizard checks a request on the server (no side effects), shows the
// exact new file and only then offers to write it and run a full plan. Applying happens later,
// from the plan review, with the same confirmation as any other plan.
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
  // Opens the started import plan.
  onStarted: (operation: Operation) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const client = useQueryClient();
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
  // Step 3: the file is written and its plan is running.
  const [written, setWritten] = useState<{ file: string; operation: Operation }>();
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
  // Back to step 1. Any edit does this too: the server only accepts what was reviewed.
  function back() {
    setPreview(undefined);
    setConsent(false);
    setError('');
  }
  function change(update: Partial<ImportRequest>) {
    setRequest(r => ({ ...r, ...update }));
    back();
  }
  async function check(event?: FormEvent) {
    event?.preventDefault();
    setChecking(true);
    setError('');
    try {
      setPreview(await send<ImportPreview>('POST', scopedPath(scope, 'imports/preview'), { ...request, address: request.address.trim() }));
    } catch (e) {
      setError(errorMessage(e, 'The import could not be checked.'));
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
      // refuses it if anything in that scope changed since. It takes no profile; a discovery
      // link must come from this same workspace's discovery (M24.09).
      let operation: Operation;
      if (scoped) operation = await send<Operation>('POST', scopedPath(scope, 'imports'), { ...reviewed, contextFingerprint: o?.contextFingerprint ?? '', ...(linked && prefill ? { discoveryToken: prefill.token } : {}) });
      else {
        const bound = summary?.context.profileId ? { profileId, contextFingerprint: summary.context.fingerprint } : {};
        operation = await send<Operation>('POST', 'imports', { ...reviewed, ...bound, ...(linked && prefill ? { discoveryToken: prefill.token } : {}) });
      }
      setWritten({ file: preview.file, operation });
      // The app learns at once that a plan is running (its list polls only while one is active).
      void client.invalidateQueries({ queryKey: ['operations'] });
      setRequest(blank);
      back();
    } catch (e) {
      setError(errorMessage(e, 'The import could not be started.'));
      // The file may exist now or the context changed; check again before retrying.
      setPreview(undefined);
      setConsent(false);
    } finally {
      setStarting(false);
    }
  }
  // Clearing the prefill remounts this view (it is part of its key), so it waits until step 3 is left.
  function leave(open: boolean) {
    const operation = written?.operation;
    setWritten(undefined);
    clearPrefill?.();
    if (open && operation) onStarted(operation);
  }
  const issues = preview?.issues.filter(i => i.severity === 'error') ?? [];
  const fieldIssue = (field: keyof typeof fieldCodes) => issues.find(i => fieldCodes[field].includes(i.code));
  const fieldError = (field: keyof typeof fieldCodes) => {
    const issue = fieldIssue(field);
    return issue && <small id={`import-${field}-error`} className="field-error" role="alert">{issue.message}</small>;
  };
  const errors = issues.filter(i => !allFieldCodes.includes(i.code));
  const warnings = preview?.issues.filter(i => i.severity === 'warning') ?? [];
  const step = written ? 3 : preview && !preview.blocked ? 2 : 1;
  const ready = step === 2 && !!o?.canImport;
  // A Hub workspace whose context changed or closed is never retargeted: open it again.
  if (scoped && options.error instanceof ApiError && (options.error.code === 'context_closed' || options.error.code === 'scope_changed')) return <ErrorState icon={TriangleAlert} iconSize={26} title="Open this workspace again" message={options.error.message} retry={reopen} retryLabel="Open again" note="Nothing was written." />;
  return <div className="import-wizard">
   {workspaceStatus === 'loading' ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before imports can be checked…</div> : o && !o.canImport && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span>{o.reason || 'Importing through Hclaro is unavailable for this workspace.'} You can still check an import here and copy a manual recipe.</span></div>}
   {prefill && step < 3 && <section className={`discovery-link ${linked ? '' : 'unlinked'}`} aria-label="Discovered object">
    <div><Radar size={15} aria-hidden="true" /><span>{linked ? <>From Cloud discovery: <code>{prefill.type}</code> <code>{prefill.importId}</code> in account <strong>{prefill.account}</strong>{prefill.region && <>, {prefill.region}</>}. The address is only a suggestion: confirm it and the resource block, then review the plan.</> : <>No longer linked to the discovered object: the import ID or resource type was changed. This is now a manual import.</>}</span>
     <Button variant="ghost" size="small" onClick={() => { clearPrefill?.(); }}><Unlink size={12} />Clear</Button></div>
    {linked && prefill.warnings.length > 0 && <ul className="import-issues" aria-label="Discovery notes">{prefill.warnings.map(w => <li key={w} className="issue-warning"><CircleHelp size={14} aria-hidden="true" /><span>{w}</span></li>)}</ul>}
   </section>}

   <section className="surface import-card" aria-label="Import an existing object">
    <ol className="import-steps" aria-label="Import steps">
     {stepLabels.map((label, i) => <li key={label} className={i + 1 < step ? 'done' : i + 1 === step ? 'current' : ''} aria-current={i + 1 === step ? 'step' : undefined}><span>{i + 1 < step ? <Check size={12} aria-label="Done" /> : i + 1}</span>{label}</li>)}
    </ol>
    <div className="import-card-body">
     {step === 1 && <form onSubmit={e => void check(e)} aria-labelledby="import-target-title">
      <div className="import-card-title"><h2 id="import-target-title">Existing object</h2><p>The object must already exist outside {engine}. Importing records it in state; it doesn't create or change it, but the plan can propose updates to match your configuration.</p></div>
      <div className="form-grid">
       <label className="field wide"><span>Resource instance address</span><input className="mono" aria-label="Resource instance address" value={request.address} onChange={e => change({ address: e.target.value })} placeholder={'aws_s3_bucket.logs or module.net.aws_subnet.private["a"]'} spellCheck={false} autoComplete="off" required aria-invalid={!!fieldIssue('address')} aria-describedby="import-address-error address-hint" />{fieldError('address')}<small id="address-hint">Where the object will live in state. Managed resources only; include the count index or for_each key if the block uses one.</small></label>
       <label className="field wide"><span>Import ID</span><span className="input-with-action"><input className="mono" aria-label="Import ID" type={maskId ? 'password' : 'text'} value={request.id} onChange={e => change({ id: e.target.value })} placeholder="bucket name, ARN, resource ID…" spellCheck={false} autoComplete="off" required aria-invalid={!!fieldIssue('id')} aria-describedby="import-id-error id-hint" /><Button type="button" variant="ghost" size="icon" aria-label={maskId ? 'Show import ID' : 'Hide import ID'} aria-pressed={maskId} onClick={() => setMaskId(!maskId)}>{maskId ? <Eye size={14} /> : <EyeOff size={14} />}</Button></span>{fieldError('id')}<small id="id-hint">The provider-specific ID of the existing object. It's written into the new file in plain text; values that look like credentials are refused.</small></label>
       <label className="field"><span>Provider <span className="muted">(optional)</span></span><input className="mono" aria-label="Provider" value={request.provider} onChange={e => change({ provider: e.target.value })} placeholder="aws.west" spellCheck={false} autoComplete="off" aria-invalid={!!fieldIssue('provider')} aria-describedby="import-provider-error" />{fieldError('provider')}<small>Only for a non-default provider configuration.</small></label>
       <label className="field"><span>New file in the root module</span><input className="mono" aria-label="New file name" value={request.file} onChange={e => change({ file: e.target.value })} spellCheck={false} autoComplete="off" required aria-invalid={!!fieldIssue('file')} aria-describedby="import-file-error" />{fieldError('file')}<small>Must not exist yet. Hclaro never edits existing files.</small></label>
      </div>
      <label className="check generate-check"><input type="checkbox" checked={request.generateConfig} disabled={!o?.supportsGeneratedConfig} onChange={e => change({ generateConfig: e.target.checked })} /><span>Let {engine} generate the resource block during the plan <span className="muted">{o && !o.supportsGeneratedConfig ? '(not supported by this engine version)' : '(root module, no instance key, and no existing block only)'}</span></span></label>
      {errors.length > 0 && <ul className="import-issues" role="alert" aria-label="Problems to fix">{errors.map(i => <li key={i.code + i.message} className="issue-error"><TriangleAlert size={14} aria-hidden="true" /><span><strong>Cannot import yet.</strong> {i.message}</span></li>)}</ul>}
      <div className="form-actions"><Button type="submit" disabled={checking || !request.address || !request.id || !request.file}><FileSearch size={14} />{checking ? 'Checking…' : 'Check and preview'}</Button><span className="muted"><ShieldCheck size={13} />Reads configuration and the state snapshot. Nothing is written or run.</span></div>
     </form>}

     {step === 2 && preview && <>
      <div className="import-card-title"><h2>Review the new file</h2><p>Import into <code>{preview.address}</code>{o && <> in workspace <strong>{o.workspace}</strong></>}. This is exactly what will be written:</p></div>
      <AskAI scope={scope} request={{ kind: 'import', import: { ...request, address: request.address.trim() } }} label="Review with AI" />
      {warnings.length > 0 && <ul className="import-issues" aria-label="Warnings">{warnings.map(i => <li key={i.code + i.message} className="issue-warning"><CircleHelp size={14} aria-hidden="true" /><span>{i.message}</span></li>)}</ul>}
      <p className="config-status">{preview.config === 'found' ? <><CircleCheck size={14} />Resource block found in <code>{preview.configFile}</code>.</> : preview.config === 'missing' && preview.generatedFile ? <><FilePlus2 size={14} />No resource block yet. {engine} will write one to <code>{preview.generatedFile}</code> during the plan.</> : preview.config === 'missing' ? <><TriangleAlert size={14} />No resource block found.</> : <><CircleHelp size={14} />The resource block could not be verified; the plan will report it if it is missing.</>}</p>
      {preview.content && <div className="file-diff">
       <div className="file-diff-header"><span><FilePlus2 size={13} /><code>{preview.file}</code> (new)</span><Button variant="ghost" size="icon" aria-label="Copy import block" onClick={() => void copy(preview.content, 'Import block')}><Copy size={13} /></Button></div>
       <pre aria-label={`Contents of ${preview.file}`}>{preview.content.trimEnd().split('\n').map((line, i) => <span key={i} className="diff-line-added"><span aria-hidden="true">+ </span>{line}{'\n'}</span>)}</pre>
      </div>}
      {preview.generatedFile && <p className="info-line"><FilePlus2 size={13} /><span><code>{preview.generatedFile}</code> is a second new file, written by {engine} itself while planning. Its content depends on the live object, so it is shown in the plan review, before anything can be applied.</span></p>}
      {preview.recipe && <Disclosure open={!o?.canImport} summary={<><Terminal size={13} aria-hidden="true" /> Manual recipe</>}>
       <p>To import with your own CLI workflow instead: save the block above, then plan and apply a saved plan.</p>
       <pre className="recipe">{preview.recipe.commands.join('\n')}</pre>
       <p className="plan-warning"><TriangleAlert size={14} /><span>Only for engines without import blocks: <code>{preview.recipe.legacy}</code> writes state immediately, without a plan you can review first. Hclaro never runs it.</span></p>
      </Disclosure>}
      {ready && o && <section className="import-confirm" aria-label="Write the file and plan">
       <ul className="preflight-list">
        <li><FilePlus2 size={16} /><span><strong>Writes {preview.generatedFile ? <>two new files</> : <>one new file</>}</strong>: <code>{preview.file}</code>{preview.generatedFile && <> now, and <code>{preview.generatedFile}</code> during the plan</>}. Existing files are never changed. To back out later, delete {preview.generatedFile ? 'them' : 'it'}.</span></li>
        <li><Layers3 size={16} /><span><strong>Then runs a plan for workspace {o.workspace}.</strong> Every workspace of this project plans this import block until you remove the file. The plan contacts providers and the {o.backend} backend with your existing credentials and may lock the state.</span></li>
        <li><ShieldCheck size={16} /><span><strong>Does not change state or infrastructure.</strong> {o.canApply ? 'The import is recorded only if you apply the reviewed plan and confirm it.' : 'Applying is unavailable here; you can review the plan only.'}</span></li>
       </ul>
       {scoped ? o.scope && <ScopeSummary scope={o.scope} executionContext={o.executionContext} title="This import plan will use" /> : summary ? <ContextSummary summary={summary} title="This import plan will use" /> : context.data?.profileError && <p className="dialog-error" role="alert">{context.data.profileError}</p>}
       <VarFileChoice choice={choice} />
       <label className="check consent"><input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} /><span>Write <code>{preview.file}</code> to this project</span></label>
      </section>}
      <div className="form-actions"><Button variant="ghost" onClick={back}><ArrowLeft size={14} />Back</Button>{ready && <Button onClick={() => void start()} disabled={!consent || starting || contextBlocked} title={contextBlocked ? 'Resolve the execution context issues first.' : undefined}><Play size={14} />{starting ? 'Starting…' : 'Write file and plan'}</Button>}</div>
     </>}

     {step === 3 && written && <div className="import-written" role="status">
      <span className="import-written-mark" aria-hidden="true"><Check size={16} /></span>
      <div className="import-card-title"><h2><code>{written.file}</code> written</h2><p>A plan is running to show the import together with any changes it brings. Nothing is imported until you review and apply that plan.</p></div>
      <div className="form-actions"><Button variant="ghost" onClick={() => leave(false)}>Import another</Button><Button onClick={() => leave(true)}><Play size={14} />Open plan…</Button></div>
     </div>}

     {error && <p className="dialog-error" role="alert">{error}</p>}
    </div>
   </section>
  </div>;
}
