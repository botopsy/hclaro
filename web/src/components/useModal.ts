import { useEffect, type RefObject } from 'react';

// Shared modal behavior: focus the initial control, close on Escape, keep Tab inside the
// dialog and restore focus when it closes.
export function useModal(dialog: RefObject<HTMLElement | null>, initial: RefObject<HTMLElement | null>, onClose: () => void) {
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    initial.current?.focus();
    function key(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
      if (event.key !== 'Tab' || !dialog.current) return;
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
  }, [dialog, initial, onClose]);
}
