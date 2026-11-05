import { useRef, useState } from 'react';
import { CloudCog, LockKeyhole, Play, RefreshCcw, ShieldCheck, X } from 'lucide-react';
import type { PlanOptions, ReplaceTarget } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { engineLabel } from './labels';

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
  onConfirm: (varFiles: string[]) => void;
  onClose: () => void;
}) {
  const [varFiles, setVarFiles] = useState<string[]>([]);
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  const engine = engineLabel(options.engine);
  const replacing = replace.length > 0;
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="preflight-title" aria-describedby="preflight-summary">
    <div className="dialog-header"><h2 id="preflight-title">{replacing ? 'Plan a replacement' : 'Run a plan'}</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="preflight-summary">{engine} will run <code>plan</code> in <strong>{project}</strong> for workspace <strong>{options.workspace}</strong>{replacing && <> with one <code>-replace</code> per selected instance</>}. Review what this does before continuing.</p>
    {replacing && <div className="replace-targets"><span className="muted">Instances to replace ({replace.length})</span><ul>{replace.map(r => <li key={r.id}><RefreshCcw size={12} aria-hidden="true" /><code>{r.address}</code></li>)}</ul><p>This is a full plan, not a targeted one: every other change the engine proposes is included and shown for review.</p></div>}
    <ul className="preflight-list">
     <li><CloudCog size={16} /><span><strong>Contacts providers and the {options.backend} backend</strong> using your existing credentials. Data sources are read and resources are refreshed to compare with configuration.</span></li>
     <li><LockKeyhole size={16} /><span><strong>May lock the state</strong> while planning, if the backend supports locking. Other operations wait until the plan finishes or is cancelled.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not change infrastructure.</strong> {options.canApply ? 'You review the saved plan first; it is applied only if you confirm it explicitly.' : 'The saved plan is deleted after Hclaro extracts redacted review data. Applying is unavailable here.'}</span></li>
    </ul>
    {options.varFiles.length > 0 && <fieldset className="var-files"><legend>Variable files <span className="muted">(optional; *.auto.tfvars and terraform.tfvars load automatically)</span></legend>{options.varFiles.map(name => <label key={name}><input type="checkbox" checked={varFiles.includes(name)} onChange={e => setVarFiles(e.target.checked ? [...varFiles, name] : varFiles.filter(v => v !== name))} /><code>{name}</code></label>)}</fieldset>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(varFiles)} disabled={busy}><Play size={14} />{busy ? 'Starting…' : replacing ? 'Plan replacement' : 'Run plan'}</Button></div>
   </div>
  </div>;
}
