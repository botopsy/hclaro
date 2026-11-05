import { useState, type FormEvent } from 'react';
import { useQuery } from '@tanstack/react-query';
import { CircleCheck, CircleHelp, CloudCog, Copy, Eye, EyeOff, FilePlus2, FileSearch, Import, LoaderCircle, LockKeyhole, Play, ShieldCheck, Terminal, TriangleAlert } from 'lucide-react';
import { api, post } from '../api';
import type { ImportOptions, ImportPreview, ImportRequest, Operation } from '../types';
import { Button } from '../components/ui/button';
import { engineLabel } from '../plans/labels';

const blank: ImportRequest = { address: '', id: '', provider: '', file: 'imports.tf', generateConfig: false };

// The wizard checks a request on the server (no side effects), shows the exact new file and
// only then offers to write it and run a full plan. Applying happens later, from the plan
// review, with the same confirmation as any other plan.
export function ImportView({
  workspaceStatus,
  onStarted,
  copy
}: {
  workspaceStatus?: string;
  onStarted: (operation: Operation) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const [request, setRequest] = useState<ImportRequest>(blank);
  const [maskId, setMaskId] = useState(false);
  const [preview, setPreview] = useState<ImportPreview>();
  const [checking, setChecking] = useState(false);
  const [consent, setConsent] = useState(false);
  const [varFiles, setVarFiles] = useState<string[]>([]);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState('');
  const options = useQuery({ queryKey: ['import-options', workspaceStatus], queryFn: () => api<ImportOptions>('imports/options') });
  const o = options.data;
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
      setPreview(await post<ImportPreview>('imports/preview', { ...request, address: request.address.trim() }));
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
      const operation = await post<Operation>('imports', { ...request, address: request.address.trim(), varFiles, previewSha256: preview.sha256, acknowledgeWrite: true, acknowledgeBackendAccess: true });
      setRequest(blank);
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
  return <div className="import-wizard">
   {workspaceStatus === 'loading' ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace before imports can be checked…</div> : o && !o.canImport && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span>{o.reason || 'Importing through Hclaro is unavailable for this workspace.'} You can still check an import here and copy a manual recipe.</span></div>}
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
     <li><CloudCog size={16} /><span><strong>Contacts providers and the {o.backend} backend</strong> using your existing credentials, to read the existing object and refresh other resources.</span></li>
     <li><LockKeyhole size={16} /><span><strong>May lock the state</strong> while planning, if the backend supports locking.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not change state or infrastructure.</strong> {o.canApply ? 'The import is recorded only if you apply the reviewed plan and confirm it.' : 'Applying is unavailable here; you can review the plan only.'}</span></li>
    </ul>
    {o.varFiles.length > 0 && <fieldset className="var-files"><legend>Variable files <span className="muted">(optional)</span></legend>{o.varFiles.map(name => <label key={name}><input type="checkbox" checked={varFiles.includes(name)} onChange={e => setVarFiles(e.target.checked ? [...varFiles, name] : varFiles.filter(v => v !== name))} /><code>{name}</code></label>)}</fieldset>}
    <label className="check consent"><input type="checkbox" checked={consent} onChange={e => setConsent(e.target.checked)} /><span>Write <code>{preview.file}</code> to this project</span></label>
    <div className="form-actions"><Button onClick={() => void start()} disabled={!consent || starting}><Play size={14} />{starting ? 'Starting…' : 'Write file and plan import'}</Button></div>
   </section>}
  </div>;
}
