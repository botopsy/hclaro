import { useRef } from 'react';
import { CloudCog, LockKeyhole, Play, RefreshCcw, ShieldCheck, X } from 'lucide-react';
import type { ExecutionContext, PlanOptions, ReplaceTarget, ScopeView } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { engineLabel } from './labels';
import { ContextSummary, RecordedContext } from '../profiles/ContextSummary';
import { useVarFileChoice, VarFileChoice } from '../profiles/VarFileChoice';

// Shown before every plan: planning is not a local-only read.
export function PreflightDialog({
  options,
  project,
  replace = [],
  busy,
  error,
  onConfirm,
  onClose
}: {
  options: PlanOptions;
  project: string;
  replace?: ReplaceTarget[];
  busy: boolean;
  error?: string;
  onConfirm: (varFiles: string[], contextFingerprint?: string) => void;
  onClose: () => void;
}) {
  const choice = useVarFileChoice(options);
  const summary = options.context;
  const blocked = !!summary && summary.issues.length > 0;
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  const engine = engineLabel(options.engine);
  const replacing = replace.length > 0;
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="preflight-title" aria-describedby="preflight-summary">
    <div className="dialog-header"><h2 id="preflight-title">{replacing ? 'Plan a replacement' : 'Run a plan'}</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="preflight-summary">{engine} will run <code>plan</code> in <strong>{project}</strong> for workspace <strong>{options.workspace}</strong>{replacing && <> with one <code>-replace</code> per selected instance</>}. Review what this does before continuing.</p>
    {replacing && <div className="replace-targets"><span className="muted">Instances to replace ({replace.length})</span><ul>{replace.map(r => <li key={r.id}><RefreshCcw size={12} aria-hidden="true" /><code>{r.address}</code></li>)}</ul><p>This is a full plan, not a targeted one: Hclaro never uses -target, so every other change the engine proposes is included, shown for review and applied together. Up to 50 instances per plan.</p></div>}
    <ul className="preflight-list">
     <li><CloudCog size={16} /><span><strong>Contacts providers and the {options.backend} backend</strong> using your existing credentials. Data sources are read and resources are refreshed to compare with configuration.</span></li>
     <li><LockKeyhole size={16} /><span><strong>May lock the state</strong> while planning, if the backend supports locking. Other operations wait until the plan finishes or is cancelled.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not change infrastructure.</strong> {options.canApply ? 'You review the saved plan first; it is applied only if you confirm it explicitly.' : 'The saved plan is deleted after Hclaro extracts redacted review data. Applying is unavailable here.'}</span></li>
    </ul>
    {summary ? <ContextSummary summary={summary} title="This plan will use" /> : options.scope ? <ScopeSummary scope={options.scope} executionContext={options.executionContext} title="This plan will use" /> : options.profileError ? <p className="dialog-error" role="alert">{options.profileError}</p> : <p className="muted">No execution profile: the plan uses the environment Hclaro was started with. Save this project to the Hub to choose a profile.</p>}
    <VarFileChoice choice={choice} />
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(choice.chosen, summary?.context.profileId ? summary.context.fingerprint : undefined)} disabled={busy || blocked || !!options.profileError} title={blocked ? 'Resolve the execution context issues first.' : undefined}><Play size={14} />{busy ? 'Starting…' : replacing ? 'Plan replacement' : 'Run plan'}</Button></div>
   </div>
  </div>;
}

// ScopeSummary shows the identity of a Hub workspace's execution scope, as the server pinned it.
export function ScopeSummary({ scope, executionContext, title }: { scope: ScopeView; executionContext?: ExecutionContext | null; title: string }) {
  return <section className="context-summary" aria-label={title}><div className="context-summary-heading"><h3>{title}</h3></div><dl className="metadata compact-metadata">
   <div><dt>Project</dt><dd><strong>{scope.alias}</strong></dd></div>
   <div><dt>Root folder</dt><dd className="mono">{scope.rootPath}</dd></div>
   <div><dt>Workspace</dt><dd><strong>{scope.workspace}</strong> <span className="muted">(this tab; the CLI default is not changed)</span></dd></div>
   <div><dt>Engine</dt><dd>{engineLabel(scope.engine)} {scope.version}</dd></div>
   <div><dt>Backend · mode</dt><dd>{scope.backend} · {scope.executionMode}</dd></div>
   <div><dt>Profile</dt><dd><RecordedContext context={executionContext ?? undefined} /></dd></div>
  </dl></section>;
}
