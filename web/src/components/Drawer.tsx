import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from 'react';

const storageKey = 'hclaro.drawerWidth';
const minWidth = 360;
const defaultWidth = 720;
const step = 40;

function maxWidth() {
  return Math.max(minWidth, window.innerWidth - 80);
}

function clamp(width: number) {
  return Math.round(Math.min(maxWidth(), Math.max(minWidth, width)));
}

// The saved width is the user's preference; it is capped to the window only when shown,
// so a temporarily narrow window does not overwrite it.
function bound(width: number) {
  return Math.round(Math.min(Math.max(minWidth, width), 4000));
}

function savedWidth() {
  try {
    const value = Number(localStorage.getItem(storageKey));
    return Number.isFinite(value) && value > 0 ? value : defaultWidth;
  } catch {
    return defaultWidth;
  }
}

// Drawer is a non-modal details panel docked to the right edge of the window. The list
// behind it stays usable, so selecting another row updates the drawer in place.
export function Drawer({ label, onClose, children }: { label: string; onClose: () => void; children: ReactNode }) {
  const [preferred, setPreferred] = useState(() => bound(savedWidth()));
  const [, setViewport] = useState(() => window.innerWidth);
  const width = clamp(preferred);
  const drag = useRef<{ startX: number; startWidth: number } | null>(null);
  const panel = useRef<HTMLElement>(null);

  // Return focus to the last control used outside the drawer (normally the row that opened it)
  // when the drawer closes, unless the user has already moved focus elsewhere.
  useEffect(() => {
    let opener = document.activeElement as HTMLElement | null;
    function focusIn(event: FocusEvent) {
      if (!panel.current?.contains(event.target as Node)) opener = event.target as HTMLElement;
    }
    const aside = panel.current;
    document.addEventListener('focusin', focusIn);
    return () => {
      document.removeEventListener('focusin', focusIn);
      const active = document.activeElement;
      if ((!active || active === document.body || aside?.contains(active)) && opener?.isConnected) opener.focus();
    };
  }, []);

  useEffect(() => {
    try {
      localStorage.setItem(storageKey, String(preferred));
    } catch {
      // Width is a convenience; private windows may block storage.
    }
  }, [preferred]);

  useEffect(() => {
    function key(event: globalThis.KeyboardEvent) {
      if (event.key === 'Escape' && !event.defaultPrevented) onClose();
    }
    function resize() {
      setViewport(window.innerWidth);
    }
    window.addEventListener('keydown', key);
    window.addEventListener('resize', resize);
    return () => {
      window.removeEventListener('keydown', key);
      window.removeEventListener('resize', resize);
    };
  }, [onClose]);

  function pointerDown(event: PointerEvent<HTMLDivElement>) {
    event.currentTarget.setPointerCapture(event.pointerId);
    drag.current = { startX: event.clientX, startWidth: width };
  }
  function pointerMove(event: PointerEvent<HTMLDivElement>) {
    if (drag.current) setPreferred(clamp(drag.current.startWidth + drag.current.startX - event.clientX));
  }
  function pointerUp() {
    drag.current = null;
  }
  function keyDown(event: KeyboardEvent<HTMLDivElement>) {
    const change = { ArrowLeft: step, ArrowRight: -step, Home: maxWidth(), End: -maxWidth() }[event.key];
    if (change === undefined) return;
    event.preventDefault();
    setPreferred(clamp(width + change));
  }

  return <aside ref={panel} className="inspector" aria-label={label} style={{ width }}>
   <div className="drawer-resize" role="separator" aria-orientation="vertical" aria-label={`Resize ${label.toLowerCase()}`} aria-valuemin={minWidth} aria-valuemax={maxWidth()} aria-valuenow={width} tabIndex={0} onPointerDown={pointerDown} onPointerMove={pointerMove} onPointerUp={pointerUp} onPointerCancel={pointerUp} onKeyDown={keyDown} />
   {children}
  </aside>;
}
