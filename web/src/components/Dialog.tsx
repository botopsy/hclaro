import { useId, useRef, type ReactNode, type RefObject } from 'react';
import { X } from 'lucide-react';
import { Button } from './ui/button';
import { useModal } from './useModal';

// A modal with a fixed header and footer and a scrolling body. Focus trap, Escape and focus
// return come from useModal; `initial` names the control focused on open (default: ✕).
export function Dialog({ title, onClose, footer, initial, wide = false, children }: { title: string; onClose: () => void; footer?: ReactNode; initial?: RefObject<HTMLElement | null>; wide?: boolean; children: ReactNode }) {
  const dialog = useRef<HTMLDivElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  const id = useId();
  useModal(dialog, initial ?? close, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className={`dialog dialog-framed${wide ? ' dialog-wide' : ''}`} role="dialog" aria-modal="true" aria-labelledby={id}>
    <div className="dialog-header"><h2 id={id}>{title}</h2><Button ref={close} variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <div className="dialog-body">{children}</div>
    {footer && <div className="dialog-actions">{footer}</div>}
   </div>
  </div>;
}
