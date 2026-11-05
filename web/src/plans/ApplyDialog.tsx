import { useEffect, useRef, useState } from 'react';
import { Fingerprint, LoaderCircle, Rocket, Terminal, TriangleAlert, X } from 'lucide-react';
import { post } from '../api';
import type { Approval, Operation, Plan } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { actionOrder, engineLabel, meta } from './labels';

// Final confirmation before a saved-plan apply. The server issues a one-time token bound to
// this plan when the dialog opens; closing the dialog declines and nothing is applied.
export function ApplyDialog({ plan, onStarted, onClose }: { plan: Plan; onStarted: (operation: Operation) => void; onClose: () => void }) {
  const [approval, setApproval] = useState<Approval>();
  const [typed, setTyped] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const cancel = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  // Cancel has initial focus so Enter never applies by accident.
  useModal(dialog, cancel, onClose);
  useEffect(() => {
    let current = true;
    post<Approval>(`plans/${plan.id}/approval`, {}).then(a => current && setApproval(a), e => current && setError(e instanceof Error ? e.message : 'This plan cannot be applied.'));
    return () => {
      current = false;
    };
  }, [plan.id]);
  const matches = !approval?.phrase || typed === approval.phrase;
  async function apply() {
    if (!approval || !matches) return;
    setBusy(true);
    setError('');
    try {
      onStarted(await post<Operation>(`plans/${plan.id}/apply`, { confirmationToken: approval.confirmationToken, confirmation: typed }));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The apply could not be started.');
      setBusy(false);
      setApproval(undefined);
    }
  }
  const engine = engineLabel(plan.engine);
  const counts = approval ? actionOrder.filter(a => a !== 'no-op' && approval.summary[a]) : [];
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog apply-dialog" role="dialog" aria-modal="true" aria-labelledby="apply-title" aria-describedby="apply-summary">
    <div className="dialog-header"><h2 id="apply-title">Apply this plan</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="apply-summary">{engine} will apply the saved plan you reviewed to workspace <strong>{plan.workspace}</strong>. This changes real infrastructure. A saved plan does not prompt again, so this is the final confirmation.</p>
    {!approval && !error && <p role="status"><LoaderCircle className="spin" size={13} /> Checking that the plan can still be applied…</p>}
    {approval && <>
     <div className="apply-counts" role="list" aria-label="Changes to apply">{counts.map(a => {
      const { label, icon: Icon, tone } = meta(a);
      return <span role="listitem" key={a} className={`apply-count tone-${tone}`}><Icon size={13} aria-hidden="true" />{approval.summary[a]} {label.toLowerCase()}</span>;
     })}</div>
     {approval.imports > 0 && <p>Imports <strong>{approval.imports}</strong> existing object{approval.imports === 1 ? '' : 's'} into state{approval.import && <>, including <code>{approval.import.address}</code></>}. Import writes state: type the phrase to confirm.</p>}
     {approval.replace.length > 0 && <p>Replacement was requested for <strong>{approval.replace.length}</strong> instance{approval.replace.length === 1 ? '' : 's'}; the counts above include every other change in the plan.</p>}
     {approval.destructive && <p className="plan-warning" role="alert"><TriangleAlert size={15} /><span>This plan destroys or replaces objects. Data stored in them can be lost, and dependent services can be unavailable while they are recreated. Hclaro cannot guarantee zero downtime.</span></p>}
     <div className="command-preview"><span className="muted"><Terminal size={12} /> Command</span><code>{approval.command.join(' ')}</code><span className="muted"><Fingerprint size={12} /> Saved plan SHA-256 <code>{approval.artifactSha256.slice(0, 16)}…</code></span></div>
     {approval.phrase && <label className="phrase-field"><span>Type <code>{approval.phrase}</code> to confirm</span><input value={typed} onChange={e => setTyped(e.target.value)} autoComplete="off" spellCheck={false} aria-label="Confirmation phrase" /></label>}
    </>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button ref={cancel} variant="ghost" onClick={onClose}>Cancel</Button><Button variant={approval?.destructive ? 'destructive' : 'default'} onClick={() => void apply()} disabled={!approval || !matches || busy}><Rocket size={14} />{busy ? 'Starting…' : 'Apply plan'}</Button></div>
   </div>
  </div>;
}
