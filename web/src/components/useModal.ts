import { useEffect, useRef, type RefObject } from 'react';

const focusable = 'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary, [tabindex]:not([tabindex="-1"])';

// Shared modal behavior: focus the initial control, close on Escape, keep Tab inside the
// dialog and restore focus when it closes.
export function useModal(dialog: RefObject<HTMLElement | null>, initial: RefObject<HTMLElement | null>, onClose: () => void) {
  // Callers pass inline closures; a ref keeps parent re-renders from re-running the focus effect.
  const close = useRef(onClose);
  useEffect(() => {
    close.current = onClose;
  }, [onClose]);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    initial.current?.focus();
    function key(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation();
        close.current();
      }
      if (event.key !== 'Tab' || !dialog.current) return;
      // Filter '*' rather than querySelectorAll(focusable): some DOMs return selector-list matches out of document order.
      const items = Array.from(dialog.current.querySelectorAll<HTMLElement>('*')).filter(e => e.matches(focusable) && !e.closest('[inert], details:not([open]) > :not(summary)'));
      const first = items[0], last = items.at(-1);
      if (!dialog.current.contains(document.activeElement)) {
        event.preventDefault();
        (event.shiftKey ? last : first)?.focus();
      } else if (event.shiftKey && document.activeElement === first) {
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
  }, [dialog, initial]);
}
