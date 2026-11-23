import { useEffect, useRef, useState, type ReactNode } from 'react';
import { AlertTriangle, CircleCheck, CircleStop, CircleX, LoaderCircle, Play, RotateCcw, type LucideIcon } from 'lucide-react';
import { operationActive, type ExecutionContext, type ExecutionSummary, type Operation, type ScopeView } from '../types';
import { Button } from '../components/ui/button';
import { Dialog } from '../components/Dialog';
import { ContextSummary, workspaceSources } from '../profiles/ContextSummary';
import { useOperationStream } from './OperationLog';
import { launchScope, type PlanScope } from '../lib/scope';
import { clock, duration, engineLabel, timeLabel } from '../lib/format';

export type RunFact = { icon: LucideIcon; title: string; detail: ReactNode };

// What a run will use. Plan, drift and graph options all carry these fields; a Hub workspace
// adds its scope, a launch project with a saved profile its resolved summary.
export type RunUses = {
  project: string;
  engine: string;
  version?: string;
  workspace: string;
  backend: string;
  executionMode?: string;
  context?: ExecutionSummary;
  scope?: ScopeView;
  executionContext?: ExecutionContext | null;
};

// The "Run a plan" / "Check drift" / "Generate dependency graph" dialog (redesign §2.4).
// Nothing runs until the primary button is pressed; a context with issues cannot be run.
export function RunDialog({ title, sentence, facts, noun, uses, action, busy = false, error, blockedReason, onRun, onClose, children }: {
  title: string;
  sentence: ReactNode;
  facts: RunFact[];
  noun: string;
  uses: RunUses;
  action: string;
  busy?: boolean;
  error?: string;
  blockedReason?: string;
  onRun: () => void;
  onClose: () => void;
  children?: ReactNode;
}) {
  const confirm = useRef<HTMLButtonElement>(null);
  const issues = uses.context?.issues ?? [];
  const blocked = blockedReason ?? (issues.length ? 'Resolve the execution context issues first.' : undefined);
  return <Dialog title={title} onClose={onClose} initial={confirm} footer={<>
    <Button variant="ghost" onClick={onClose}>Cancel</Button>
    <Button ref={confirm} onClick={onRun} disabled={busy || !!blocked} title={blocked}><Play size={14} />{busy ? 'Starting…' : action}</Button>
   </>}>
   <p className="run-sentence">{sentence}</p>
   <ul className="preflight-list">{facts.map(f => <li key={f.title}><f.icon size={16} aria-hidden="true" /><span><strong>{f.title}</strong> {f.detail}</span></li>)}</ul>
   <RunUsesBox noun={noun} uses={uses} />
   {/* A launch profile's full summary lists its own issues. */}
   {uses.scope && issues.length > 0 && <ul className="context-issues" role="alert">{issues.map(i => <li key={i.code + i.message}><AlertTriangle size={13} aria-hidden="true" />{i.message}</li>)}</ul>}
   {blockedReason && <p className="dialog-error" role="alert">{blockedReason}</p>}
   {children}
   {error && <p className="dialog-error" role="alert">{error}</p>}
  </Dialog>;
}

function RunUsesBox({ noun, uses }: { noun: string; uses: RunUses }) {
  const { scope, context } = uses;
  const title = `This ${noun} will use`;
  // A launch project's saved profile gets the full review: AWS sign-in, expected variables, var files.
  if (context && !scope) return <ContextSummary summary={context} title={title} />;
  const workspace = scope?.workspace ?? context?.workspace ?? uses.workspace;
  const where = scope ? 'this tab; the CLI default is not changed' : context ? workspaceSources[context.workspaceSource] : undefined;
  const rootPath = scope?.rootPath ?? context?.rootPath;
  const engine = scope?.engine ?? context?.engine ?? uses.engine;
  const version = scope?.version ?? context?.engineVersion ?? uses.version;
  const mode = scope?.executionMode ?? context?.executionMode ?? uses.executionMode;
  const profile = scope ? uses.executionContext : context?.context;
  return <section className="context-summary" aria-label={title}>
   <div className="context-summary-heading"><h3>{title}</h3></div>
   <dl className="metadata compact-metadata">
    <div><dt>Project</dt><dd><strong>{scope?.alias ?? context?.alias ?? uses.project}</strong></dd></div>
    {rootPath && <div><dt>Root folder</dt><dd className="mono">{rootPath}</dd></div>}
    <div><dt>Workspace</dt><dd><strong className="mono">{workspace}</strong>{where && <span className="muted"> ({where})</span>}</dd></div>
    <div><dt>Engine</dt><dd>{engineLabel(engine)}{version ? ` ${version}` : ''}</dd></div>
    <div><dt>Backend · mode</dt><dd>{scope?.backend ?? context?.backend ?? uses.backend}{mode ? ` · ${mode}` : ''}</dd></div>
    <div><dt>Profile</dt><dd>{profile?.profileName ? <><strong>{profile.profileName}</strong>{profile.awsProfile && <span className="muted mono"> AWS_PROFILE={profile.awsProfile}{profile.awsRegion ? ` · ${profile.awsRegion}` : ''}</span>}</> : <span className="muted">None (inherited environment)</span>}</dd></div>
   </dl>
  </section>;
}

// runFailure turns a finished, unsuccessful operation into a title and one sentence (§2.5).
// The lock holder is never named: the engine does not report it (Q3).
export function runFailure(op: Operation): { title: string; text: string } {
  const code = op.diagnostic?.code;
  if (op.status === 'cancelled') return { title: 'Cancelled', text: op.diagnostic?.message ?? 'The run was cancelled before it finished.' };
  if (code === 'state_locked') return { title: 'State is locked by another process', text: 'Wait for the other operation to finish, or check the lock with your CLI. Hclaro never force-unlocks state.' };
  if (code === 'network') return { title: 'Backend unreachable', text: 'A backend or provider endpoint did not answer. Check VPN and network access, then try again.' };
  if (code === 'credentials') {
    const profile = op.executionContext?.awsProfile ?? op.executionContext?.profileName;
    return { title: 'Credentials missing or expired', text: `The engine could not authenticate${profile ? ` with profile ${profile}` : ''}. Sign in again in your terminal, then try again.` };
  }
  const line = op.engineDiagnostics.find(d => d.severity === 'error')?.summary ?? op.diagnostic?.message ?? 'The engine failed without a readable error.';
  return { title: `Engine error${op.exitCode !== undefined ? ` (exit ${op.exitCode})` : ''}`, text: line };
}

// The running, failed and succeeded states of a plan, drift scan or graph run (§2.5). The
// log lines are the server's sanitized operation events.
export function RunState({ operation: initial, scope = launchScope, running, succeeded, onFinished, onRetry, children }: {
  operation: Operation;
  scope?: PlanScope;
  running: string;
  succeeded: string;
  onFinished?: (operation: Operation) => void;
  onRetry: () => void;
  // Extra content of the failed card (what was kept, how to recover).
  children?: ReactNode;
}) {
  const { events, operation, error, cancelling, cancel } = useOperationStream(initial, scope);
  const [showLog, setShowLog] = useState(false);
  const finished = useRef(onFinished);
  const done = !operationActive(operation);
  useEffect(() => {
    finished.current = onFinished;
  }, [onFinished]);
  useEffect(() => {
    if (done) finished.current?.(operation);
  }, [done, operation]);
  const log = (lines: typeof events) => <ol className="log-lines" aria-label="Output">{lines.map(e => <li key={e.seq} className={`log-${e.level}`}><time>{clock(e.time)}</time><span>{e.text}</span></li>)}{!lines.length && <li className="log-info"><span>Waiting for output…</span></li>}</ol>;
  if (!done) {
    return <section className="run-state" aria-label={running}>
     <header>
      <LoaderCircle className="spin" size={16} aria-hidden="true" />
      <strong role="status">{operation.cancelRequested ? 'Cancelling…' : running}</strong>
      <span className="muted">started {timeLabel(operation.startedAt)}</span>
      <Button variant="outline" size="small" onClick={() => void cancel()} disabled={cancelling || operation.cancelRequested}><CircleStop size={13} />Cancel</Button>
     </header>
     {log(events.slice(-4))}
     {error && <p className="log-note" role="alert">{error}</p>}
    </section>;
  }
  if (operation.status === 'succeeded') {
    return <section className="operation-applied" role="status"><h2><CircleCheck size={17} />{succeeded}</h2><p className="muted">Finished in {duration(operation.durationMs)}.</p></section>;
  }
  const failure = runFailure(operation);
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{failure.title}</h2>
   <p>{failure.text}</p>
   {children}
   <div className="run-actions">
    <Button variant="outline" size="small" aria-expanded={showLog} onClick={() => setShowLog(v => !v)}>{showLog ? 'Hide log' : 'View log'}</Button>
    <Button size="small" onClick={onRetry}><RotateCcw size={13} />Try again</Button>
   </div>
   {showLog && log(events)}
  </section>;
}
