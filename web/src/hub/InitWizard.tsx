import { useCallback, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { AlertTriangle, CheckCircle2, CloudDownload, FileLock2, HardDrive, LoaderCircle, Play, RefreshCcw, ShieldCheck, X } from 'lucide-react';
import { api, send } from '../api';
import { operationActive, type InitStatus, type Operation } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { OperationLog } from '../plans/OperationLog';

function engineLabel(name?: string) {
  return name === 'tofu' ? 'OpenTofu' : 'Terraform';
}
function message(error: unknown) {
  return error instanceof Error ? error.message : 'Unable to reach the local Hclaro server. Check that it is still running, then retry.';
}
const outcomeTitle: Record<string, string> = {
  succeeded: 'Initialized',
  failed: 'Initialization failed',
  cancelled: 'Initialization cancelled',
  unknown: 'Result could not be confirmed'
};

// Guided init for one Hub project: review the context and effects, confirm, then follow the
// sanitized log. The review is read-only; init runs only from the confirm button, and a
// failure is never retried automatically.
export function InitWizard({ projectId, onClose, onFinished }: {
  projectId: string;
  onClose: () => void;
  onFinished: () => void;
}) {
  const status = useQuery({
    queryKey: ['init-status', projectId],
    queryFn: () => api<InitStatus>(`projects/${projectId}/init-status`),
    gcTime: 0
  });
  const [acknowledged, setAcknowledged] = useState(false);
  const [started, setStarted] = useState<Operation>();
  // A finished run that the user chose to review again is not followed any more.
  const [dismissed, setDismissed] = useState('');
  const [result, setResult] = useState<Operation>();
  const [error, setError] = useState('');
  const [starting, setStarting] = useState(false);
  const dialog = useRef<HTMLDivElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  useModal(dialog, close, onClose);
  const s = status.data;
  // Reopening the wizard while init runs (for example after a reload) follows that run.
  const operation = started ?? (s?.operation && operationActive(s.operation) && s.operation.id !== dismissed ? s.operation : undefined);
  const finished = useCallback((op: Operation) => {
    setResult(op);
    onFinished();
  }, [onFinished]);
  async function start() {
    if (!s?.contextId) return;
    setStarting(true);
    setError('');
    try {
      setStarted(await send<Operation>('POST', `projects/${projectId}/operations/init`, { mode: 'standard', expectedContextId: s.contextId }));
    } catch (e) {
      setError(message(e));
      void status.refetch();
    } finally {
      setStarting(false);
    }
  }
  function reviewAgain() {
    setDismissed(operation?.id || '');
    setStarted(undefined);
    setResult(undefined);
    setAcknowledged(false);
    setError('');
    void status.refetch();
  }
  const engine = engineLabel(s?.engine);
  const running = operation && !result;
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog dialog-wide init-dialog" role="dialog" aria-modal="true" aria-labelledby="init-title">
    <div className="dialog-header"><h2 id="init-title">{operation ? `Initialize ${s?.alias || 'project'}` : `Review init for ${s?.alias || 'project'}`}</h2><Button ref={close} variant="ghost" size="icon" aria-label={running ? 'Close; init keeps running' : 'Close'} onClick={onClose}><X size={15} /></Button></div>
    {status.isPending ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Reading the project folder… No command runs.</p>
      : status.error || !s ? <><p className="dialog-error" role="alert">{message(status.error)}</p><div className="dialog-actions"><Button variant="outline" onClick={() => void status.refetch()}>Try again</Button></div></>
      : operation ? <>
       <OperationLog key={operation.id} operation={operation} onFinished={finished} />
       {running && <p className="muted">You can close this dialog; init keeps running and you can return to it from the project page.</p>}
       {result && <InitResult op={result} />}
       {result && <div className="dialog-actions">{result.status !== 'succeeded' && <Button variant="outline" onClick={reviewAgain}><RefreshCcw size={14} />Review again</Button>}<Button onClick={onClose}>{result.status === 'succeeded' ? 'Done' : 'Close'}</Button></div>}
      </>
      : <>
       <p>{engine} will run <code>init</code> in this folder. Nothing runs until you confirm. Review where it runs and what it changes.</p>
       <dl className="metadata init-facts">
        <div><dt>Project</dt><dd>{s.alias}</dd></div>
        <div><dt>Folder</dt><dd className="mono">{s.rootPath}</dd></div>
        <div><dt>Engine</dt><dd>{s.engine ? `${engine} ${s.engineVersion || '· version is checked before init runs'}` : 'Not found on PATH'}</dd></div>
        <div><dt>Backend</dt><dd>{s.initialized && s.backend !== 'unknown' ? s.backend : s.backendDeclared ? 'Declared in configuration; not initialized yet' : 'None declared: local state'}</dd></div>
        <div><dt>Workspace</dt><dd>CLI default{s.cliDefault ? ` (${s.cliDefault})` : ''}; <code>TF_WORKSPACE</code> is not passed</dd></div>
        <div><dt>Data directory</dt><dd className="mono">{s.dataDir}</dd></div>
        <div><dt>Lock file</dt><dd>{s.lockfile ? 'Present; init may update it' : 'Not present; init will create it'}</dd></div>
        <div><dt>Current status</dt><dd>{s.initialized ? 'Initialized; running init again is safe' : 'Not initialized'}</dd></div>
       </dl>
       <div className="init-command"><span className="muted">Command</span><code>{s.command.join(' ')}</code><span className="muted">Terraform 1.9 and later run with <code>-json</code> instead of <code>-no-color</code>, so progress can be read safely. No other flags are added.</span></div>
       <ul className="preflight-list">
        <li><HardDrive size={16} /><span><strong>Writes local files</strong> in this folder: the <code>{s.dataDir}</code> directory and <code>.terraform.lock.hcl</code>. Configuration files are not changed.</span></li>
        <li><CloudDownload size={16} /><span><strong>Contacts registries and the backend</strong> with your existing credentials to download providers and modules and to configure state storage. Credentials are never stored or shown.</span></li>
        <li><FileLock2 size={16} /><span><strong>Never upgrades or migrates.</strong> No <code>-upgrade</code>, <code>-migrate-state</code>, <code>-force-copy</code> or <code>-reconfigure</code>. If init needs one of them, or asks a question, it stops and Hclaro explains what to do.</span></li>
       </ul>
       {s.notices.length > 0 && <ul className="init-notices">{s.notices.map(n => <li key={n}><AlertTriangle size={13} />{n}</li>)}</ul>}
       {s.operation && !operationActive(s.operation) && s.operation.status !== 'succeeded' && <p className="muted">Last attempt: {outcomeTitle[s.operation.status] || s.operation.status}{s.operation.diagnostic ? ` · ${s.operation.diagnostic.code.replaceAll('_', ' ')}` : ''}.</p>}
       {!s.canInit ? <p className="dialog-error" role="alert">{s.reason}</p> : <label className="init-ack"><input type="checkbox" checked={acknowledged} onChange={e => setAcknowledged(e.target.checked)} />I understand init may contact registries and the backend and writes local files in this folder.</label>}
       {error && <p className="dialog-error" role="alert">{error}</p>}
       <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button onClick={() => void start()} disabled={!s.canInit || !acknowledged || starting}><Play size={14} />{starting ? 'Starting…' : 'Initialize'}</Button></div>
      </>}
   </div>
  </div>;
}

function InitResult({ op }: { op: Operation }) {
  const ok = op.status === 'succeeded';
  return <section className={`init-result ${ok ? 'init-result-ok' : 'init-result-problem'}`} role={ok ? 'status' : 'alert'} aria-label="Init result">
   <h3>{ok ? <CheckCircle2 size={16} /> : <AlertTriangle size={16} />}{outcomeTitle[op.status] || op.status}{op.exitCode !== undefined && <span className="muted"> · exit code {op.exitCode}</span>}</h3>
   {ok ? <p>Workspaces and recorded state can now be read. Nothing else was run.</p> : op.diagnostic && <p>{op.diagnostic.message}</p>}
   {op.lockfileChanged && <p><FileLock2 size={13} /> <code>.terraform.lock.hcl</code> was created or changed. Review it before committing.</p>}
   {!ok && op.engineDiagnostics.length > 0 && <ul className="init-diagnostics">{op.engineDiagnostics.slice(0, 5).map((d, i) => <li key={i}><strong>{d.summary}</strong>{d.detail && <span className="muted"> {d.detail}</span>}</li>)}</ul>}
   {!ok && <p className="safe-note"><ShieldCheck size={13} />Nothing was retried automatically.</p>}
  </section>;
}
