import { useRef } from 'react';
import { X } from 'lucide-react';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';

// Asked before a project or workspace switch would discard work kept only in this tab.
// Switching never starts a Terraform/OpenTofu command; it only changes what this tab reads.
export function ConfirmSwitch({ items, onConfirm, onCancel }: { items: string[]; onConfirm: () => void; onCancel: () => void }) {
  const dialog = useRef<HTMLDivElement>(null);
  const stay = useRef<HTMLButtonElement>(null);
  useModal(dialog, stay, onCancel);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="switch-title" aria-describedby="switch-summary">
    <div className="dialog-header"><h2 id="switch-title">Leave this project?</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onCancel}><X size={15} /></Button></div>
    <p id="switch-summary">Switching discards what this tab has not used yet:</p>
    <ul className="switch-losses">{items.map(item => <li key={item}>{item}</li>)}</ul>
    <p className="muted">Nothing runs when you switch. Operations already started keep running and stay in Operations and History.</p>
    <div className="dialog-actions"><Button ref={stay} variant="ghost" onClick={onCancel}>Stay here</Button><Button variant="outline" onClick={onConfirm}>Switch anyway</Button></div>
   </div>
  </div>;
}
