import { useCallback, useEffect, useRef, useState } from 'react';
import { AlertTriangle, ArrowRight, CheckCircle2, LoaderCircle, Plus, RefreshCcw, ShieldCheck, Terminal, Trash2, X } from 'lucide-react';
import { send } from '../api';
import type { Operation, WorkspaceAction, WorkspaceReview } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { RecordedContext } from '../profiles/ContextSummary';
import { OperationLog } from '../plans/OperationLog';

// The names Hclaro passes to a workspace command: never starting with "-", so a name can
// never be read as a flag. The server applies the same rule.
export const workspaceNamePattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,89}$/;

function message(error: unknown) {
  return error instanceof Error ? error.message : 'Unable to reach the local Hclaro server. Check that it is still running, then retry.';
}
function engineLabel(name?: string) {
  return name === 'tofu' ? 'OpenTofu' : 'Terraform';
}
const titles: Record<WorkspaceAction, (name: string) => string> = {
  new: () => 'Create a workspace',
  select: name => `Set ${name} as CLI default`,
  delete: name => `Delete workspace ${name}`
};
const confirmLabel: Record<WorkspaceAction, string> = {
  new: 'Create workspace',
  select: 'Set as CLI default',
  delete: 'Delete workspace'
};
const outcomeTitle: Record<string, string> = {
  succeeded: 'Done',
  failed: 'Command failed',
  cancelled: 'Cancelled',
  unknown: 'Result not confirmed'
};

// Creates a workspace, sets the CLI default or deletes an empty workspace. Each runs only
// after a review that lists workspaces with the CLI, and an explicit confirmation; a
// failure is never retried automatically.
export function WorkspaceDialog({ projectId, profileId = '', action, initialName = '', onClose, onFinished, onOpen }: {
  projectId: string;
  // The execution profile this tab uses ("" for none); the command runs with its environment.
  profileId?: string;
  action: WorkspaceAction;
  initialName?: string;
  onClose: () => void;
  onFinished: () => void;
  onOpen: (workspace: string) => void;
}) {
  const [name, setName] = useState(initialName);
  const [review, setReview] = useState<WorkspaceReview>();
  const [reviewing, setReviewing] = useState(false);
  const [acknowledged, setAcknowledged] = useState(false);
  const [typed, setTyped] = useState('');
  const [operation, setOperation] = useState<Operation>();
  const [result, setResult] = useState<Operation>();
  const [error, setError] = useState('');
  const [starting, setStarting] = useState(false);
  const dialog = useRef<HTMLDivElement>(null);
  const first = useRef<HTMLInputElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  useModal(dialog, action === 'new' ? first : close, onClose);
  const nameValid = workspaceNamePattern.test(name);
  async function runReview(target = name) {
    setReviewing(true);
    setError('');
    setAcknowledged(false);
    setTyped('');
    try {
      setReview(await send<WorkspaceReview>('POST', `projects/${projectId}/workspaces/review`, { action, name: target, ...(profileId ? { profileId } : {}) }));
    } catch (e) {
      setError(message(e));
    } finally {
      setReviewing(false);
    }
  }
  // Set-default and delete review immediately: the name was chosen from the list.
  const reviewed = useRef(false);
  useEffect(() => {
    if (action === 'new' || reviewed.current) return;
    reviewed.current = true;
    void runReview(initialName);
    // runReview only reads state setters and props fixed for this dialog.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  async function start() {
    if (!review?.contextId) return;
    setStarting(true);
    setError('');
    try {
      setOperation(await send<Operation>('POST', `projects/${projectId}/operations/workspace`, { action, name: review.name, expectedContextId: review.contextId, ...(profileId ? { profileId } : {}) }));
    } catch (e) {
      setError(message(e));
      setReview(undefined);
    } finally {
      setStarting(false);
    }
  }
  const finished = useCallback((op: Operation) => {
    setResult(op);
    onFinished();
  }, [onFinished]);
  const confirmed = action === 'delete' ? typed === review?.name : acknowledged;
  const engine = engineLabel(review?.engine);
  const running = operation && !result;
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog dialog-wide init-dialog" role="dialog" aria-modal="true" aria-labelledby="workspace-dialog-title">
    <div className="dialog-header"><h2 id="workspace-dialog-title">{titles[action](review?.name || name)}</h2><Button ref={close} variant="ghost" size="icon" aria-label={running ? 'Close; the command keeps running' : 'Close'} onClick={onClose}><X size={15} /></Button></div>
    {operation ? <>
      <OperationLog key={operation.id} operation={operation} onFinished={finished} />
      {running && <p className="muted">You can close this dialog; the command keeps running and the project page shows when it ends.</p>}
      {result && <WorkspaceResultPanel op={result} />}
      {result && <div className="dialog-actions">
       {result.status === 'succeeded' && action !== 'delete' && <Button variant="outline" onClick={() => onOpen(result.workspace?.name || name)}>Open {result.workspace?.name} in this tab<ArrowRight size={14} /></Button>}
       <Button onClick={onClose}>{result.status === 'succeeded' ? 'Done' : 'Close'}</Button>
      </div>}
     </>
     : !review ? <>
      {action === 'new' ? <form className="hub-form" onSubmit={e => {
          e.preventDefault();
          if (nameValid) void runReview();
        }}>
        <p>A new workspace is a separate, empty state in this project's backend. Hclaro first lists the existing workspaces with the CLI, then shows exactly what will run. Nothing is created until you confirm.</p>
        <label>Workspace name<input ref={first} className="mono" value={name} onChange={e => setName(e.target.value.trim())} maxLength={90} spellCheck={false} autoComplete="off" aria-invalid={!!name && !nameValid} aria-describedby="workspace-name-rules" required /></label>
        <p id="workspace-name-rules" className={name && !nameValid ? 'dialog-error' : 'muted'}>Letters, digits, <code>-</code> and <code>_</code>, starting with a letter or digit; up to 90 characters.</p>
        {error && <p className="dialog-error" role="alert">{error}</p>}
        <div className="dialog-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" disabled={!nameValid || reviewing}>{reviewing ? <><LoaderCircle className="spin" size={14} />Listing workspaces…</> : <>Review<ArrowRight size={14} /></>}</Button></div>
       </form>
       : reviewing || !error ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Listing workspaces{action === 'delete' ? ' and reading this workspace\'s state' : ''} with the CLI. This may contact the backend. Nothing is changed.</p>
       : <><p className="dialog-error" role="alert">{error}</p><div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Close</Button><Button variant="outline" onClick={() => void runReview(initialName)}><RefreshCcw size={14} />Try again</Button></div></>}
     </>
     : <>
      <dl className="metadata init-facts">
       <div><dt>Project</dt><dd>{review.alias}</dd></div>
       <div><dt>Folder</dt><dd className="mono">{review.rootPath}</dd></div>
       <div><dt>Engine</dt><dd>{review.engine ? `${engine} ${review.engineVersion || ''}` : 'Not found on PATH'}</dd></div>
       <div><dt>Backend</dt><dd>{review.backend}</dd></div>
       <div><dt>Data directory</dt><dd className="mono">{review.dataDir}</dd></div>
       <div><dt>CLI default now</dt><dd>{review.cliDefault || 'Not listed'}</dd></div>
      </dl>
      <div className="init-command"><span className="muted">Command</span><code>{review.command.join(' ')}</code><span className="muted">Run in this folder without <code>TF_WORKSPACE</code>. No other flags are added{action === 'delete' ? '; never -force' : ''}.</span></div>
      {review.canRun && <Effects review={review} />}
      <p className="context-line"><strong>Execution profile:</strong> <RecordedContext context={review.executionContext} /></p>
      {review.notices.length > 0 && <ul className="init-notices">{review.notices.map(n => <li key={n}><AlertTriangle size={13} />{n}</li>)}</ul>}
      {!review.canRun ? <p className="dialog-error" role="alert">{review.reason}</p>
       : action === 'delete' ? <label className="workspace-confirm"><span>Type <strong className="mono">{review.name}</strong> to confirm</span><input className="mono" value={typed} onChange={e => setTyped(e.target.value)} spellCheck={false} autoComplete="off" aria-label={`Type ${review.name} to confirm`} /></label>
       : <label className="init-ack"><input type="checkbox" checked={acknowledged} onChange={e => setAcknowledged(e.target.checked)} />{action === 'new' ? `I understand this also makes ${review.name} the CLI default for this folder.` : `I understand terminal commands in this folder will use ${review.name}'s state.`}</label>}
      {error && <p className="dialog-error" role="alert">{error}</p>}
      <div className="dialog-actions">
       <Button variant="ghost" onClick={onClose}>Cancel</Button>
       {action === 'new' && <Button variant="outline" onClick={() => setReview(undefined)}>Change name</Button>}
       {!review.canRun && <Button variant="outline" onClick={() => void runReview(review.name)}><RefreshCcw size={14} />Review again</Button>}
       <Button variant={action === 'delete' ? 'destructive' : 'default'} onClick={() => void start()} disabled={!review.canRun || !confirmed || starting}>{action === 'delete' ? <Trash2 size={14} /> : action === 'new' ? <Plus size={14} /> : <Terminal size={14} />}{starting ? 'Starting…' : confirmLabel[action]}</Button>
      </div>
     </>}
   </div>
  </div>;
}

function Effects({ review: r }: { review: WorkspaceReview }) {
  const from = r.cliDefault || 'the current workspace';
  if (r.action === 'new') return <ul className="preflight-list">
   <li><Plus size={16} /><span><strong>Creates an empty workspace</strong> named <code>{r.name}</code> in the {r.backend} backend. It starts with no resources; nothing in your infrastructure changes.</span></li>
   <li><Terminal size={16} /><span><strong>Also changes the CLI default</strong> from <code>{from}</code> to <code>{r.name}</code>. The engine always selects a new workspace. Afterwards, <code>{r.engine || 'terraform'} plan</code> in a terminal in this folder targets <code>{r.name}</code> unless that terminal sets <code>TF_WORKSPACE</code>.</span></li>
   <li><ShieldCheck size={16} /><span><strong>Hclaro tabs keep their own workspaces.</strong> Nothing switches them back automatically; select another CLI default later if you want one.</span></li>
  </ul>;
  if (r.action === 'select') return <ul className="preflight-list">
   <li><Terminal size={16} /><span><strong>Changes the CLI default</strong> from <code>{from}</code> to <code>{r.name}</code> for this folder's data directory. Commands you run in other terminals here will read and change <code>{r.name}</code>'s state.</span></li>
   <li><ShieldCheck size={16} /><span><strong>No state or infrastructure changes.</strong> Hclaro tabs are not affected: each reads its own workspace.</span></li>
  </ul>;
  return <ul className="preflight-list">
   <li><Trash2 size={16} /><span><strong>Permanently deletes</strong> the workspace's state from the {r.backend} backend. This cannot be undone.</span></li>
   <li><CheckCircle2 size={16} /><span><strong>Checked empty:</strong> its recorded state has no resources or outputs. The engine checks again and refuses if it is not empty; Hclaro never uses <code>-force</code>.</span></li>
   <li><ShieldCheck size={16} /><span><strong>No cloud resources are deleted.</strong> The CLI default stays <code>{from}</code>.</span></li>
  </ul>;
}

function WorkspaceResultPanel({ op }: { op: Operation }) {
  const ok = op.status === 'succeeded';
  const w = op.workspace;
  const changed = w && w.cliDefaultAfter && w.cliDefaultBefore !== w.cliDefaultAfter;
  return <section className={`init-result ${ok ? 'init-result-ok' : 'init-result-problem'}`} role={ok ? 'status' : 'alert'} aria-label="Workspace command result">
   <h3>{ok ? <CheckCircle2 size={16} /> : <AlertTriangle size={16} />}{outcomeTitle[op.status] || op.status}{op.exitCode !== undefined && <span className="muted"> · exit code {op.exitCode}</span>}</h3>
   <p>{ok ? op.diagnostic?.message || (w?.action === 'delete' ? `Deleted the empty workspace ${w.name}. No infrastructure was changed.` : w?.action === 'new' ? `Created ${w?.name}. The engine also made it the CLI default for this folder.` : `${w?.name} is now the CLI default for this folder. Hclaro tabs keep their own workspaces.`) : op.diagnostic?.message}</p>
   {w?.cliDefaultAfter ? <p>CLI default for this folder: <strong>{w.cliDefaultAfter}</strong>{changed ? <> (was {w.cliDefaultBefore})</> : ' (unchanged)'}.</p> : w?.ran && <p><strong>The CLI default could not be confirmed.</strong> List workspaces again before doing anything else.</p>}
   {!ok && op.engineDiagnostics.length > 0 && <ul className="init-diagnostics">{op.engineDiagnostics.slice(0, 5).map((d, i) => <li key={i}><strong>{d.summary}</strong>{d.detail && <span className="muted"> {d.detail}</span>}</li>)}</ul>}
   {!ok && <p className="safe-note"><ShieldCheck size={13} />Nothing was retried or undone automatically.</p>}
  </section>;
}
