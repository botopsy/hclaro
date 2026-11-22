import { createContext, useContext, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Check } from 'lucide-react';

const CloseContext = createContext<() => void>(() => {});

// Popover: a trigger button and a panel under it. Escape or Tab-out closes it and returns focus
// to the trigger; a click outside closes it. With `menu`, the panel is role="menu" and the
// arrow keys, Home and End move between its items. Other content (a search box, an inline
// form) can sit in a plain popover.
export function Popover({ button, label, menu = false, align = 'start', className = '', children }: { button: ReactNode; label?: string; menu?: boolean; align?: 'start' | 'end'; className?: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const id = useId();

  function close(focusTrigger = true) {
    setOpen(false);
    if (focusTrigger) trigger.current?.focus();
  }

  useEffect(() => {
    if (!open) return;
    const items = panel.current?.querySelectorAll<HTMLElement>(menu ? '[role^="menuitem"]:not(:disabled)' : 'input, button:not(:disabled), a[href]');
    items?.[0]?.focus();
    function outside(event: PointerEvent) {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    }
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open, menu]);

  function keyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape') {
      // Keep an enclosing drawer open (useModal skips Escape from inside a popover).
      event.stopPropagation();
      close();
      return;
    }
    if (event.key === 'Tab' && menu) {
      close(false);
      return;
    }
    if (!menu) return;
    const items = Array.from(panel.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]:not(:disabled)') ?? []);
    const at = items.indexOf(document.activeElement as HTMLElement);
    const next = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: items.length - 1 }[event.key];
    if (next === undefined || !items.length) return;
    event.preventDefault();
    items[(next + items.length) % items.length].focus();
  }

  return <span ref={root} className={`popover-root ${className}`}>
   <button ref={trigger} type="button" className="popover-trigger" aria-label={label} aria-haspopup={menu ? 'menu' : 'dialog'} aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(!open)}>{button}</button>
   {open && <div ref={panel} id={id} className={`popover popover-${align}`} role={menu ? 'menu' : 'dialog'} aria-label={label} onKeyDown={keyDown}>
    <CloseContext.Provider value={close}>{children}</CloseContext.Provider>
   </div>}
  </span>;
}

export function Menu(props: Omit<Parameters<typeof Popover>[0], 'menu'>) {
  return <Popover {...props} menu />;
}

// A menu entry. `checked` makes it a radio item (the current project, profile, workspace).
export function MenuItem({ onSelect, checked, disabled, children }: { onSelect: () => void; checked?: boolean; disabled?: boolean; children: ReactNode }) {
  const close = useContext(CloseContext);
  return <button type="button" className="menu-item" role={checked === undefined ? 'menuitem' : 'menuitemradio'} aria-checked={checked} disabled={disabled} tabIndex={-1} onClick={() => {
    close();
    onSelect();
  }}><span className="menu-check" aria-hidden="true">{checked && <Check size={13} />}</span>{children}</button>;
}
