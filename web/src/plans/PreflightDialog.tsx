import { useEffect, useRef, useState } from 'react';
import { CloudCog, LockKeyhole, Play, ShieldCheck, X } from 'lucide-react';
import type { PlanOptions } from '../types';
import { Button } from '../components/ui/button';
import { engineLabel } from './labels';

// Shown before every plan: planning is not a local-only read.
export function PreflightDialog({
  options,
  project,
  busy,
  error,
  onConfirm,
  onClose
}: {
  options: PlanOptions;
  project: string;
  busy: boolean;
  error?: string;
  onConfirm: (varFiles: string[]) => void;
  onClose: () => void;
}) {
  const [varFiles, setVarFiles] = useState<string[]>([]);
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    confirm.current?.focus();
    function key(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
      if (event.key !== 'Tab' || !dialog.current) return;
      // Keep focus inside the modal.
      const items = Array.from(dialog.current.querySelectorAll<HTMLElement>('button:not(:disabled), input'));
      const first = items[0], last = items.at(-1);
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    }
    window.addEventListener('keydown', key, true);
    return () => {
      window.removeEventListener('keydown', key, true);
      previous?.focus();
    };
  }, [onClose]);
  const engine = engineLabel(options.engine);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="preflight-title" aria-describedby="preflight-summary">
    <div className="dialog-header"><h2 id="preflight-title">Run a plan</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="preflight-summary">{engine} will run <code>plan</code> in <strong>{project}</strong> for workspace <strong>{options.workspace}</strong>. Review what this does before continuing.</p>
    <ul className="preflight-list">
     <li><CloudCog size={16} /><span><strong>Contacts providers and the {options.backend} backend</strong> using your existing credentials. Data sources are read and resources are refreshed to compare with configuration.</span></li>
     <li><LockKeyhole size={16} /><span><strong>May lock the state</strong> while planning, if the backend supports locking. Other operations wait until the plan finishes or is cancelled.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not change infrastructure.</strong> The saved plan is deleted after Hclaro extracts redacted review data. This version cannot apply plans.</span></li>
    </ul>
    {options.varFiles.length > 0 && <fieldset className="var-files"><legend>Variable files <span className="muted">(optional; *.auto.tfvars and terraform.tfvars load automatically)</span></legend>{options.varFiles.map(name => <label key={name}><input type="checkbox" checked={varFiles.includes(name)} onChange={e => setVarFiles(e.target.checked ? [...varFiles, name] : varFiles.filter(v => v !== name))} /><code>{name}</code></label>)}</fieldset>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(varFiles)} disabled={busy}><Play size={14} />{busy ? 'Starting…' : 'Run plan'}</Button></div>
   </div>
  </div>;
}
