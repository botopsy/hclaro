import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Drawer } from './Drawer';

afterEach(cleanup);

describe('Drawer Escape', () => {
  it('a single drawer closes and the app still sees Escape', () => {
    const app = vi.fn();
    window.addEventListener('keydown', app);
    const close = vi.fn();
    render(<Drawer label="One" onClose={close}>x</Drawer>);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    window.removeEventListener('keydown', app);
    expect(close).toHaveBeenCalledTimes(1);
    expect(app).toHaveBeenCalledTimes(1);
  });

  it('a stacked drawer closes alone', () => {
    const app = vi.fn();
    window.addEventListener('keydown', app);
    const below = vi.fn(), top = vi.fn();
    render(<><Drawer label="Below" onClose={below}>x</Drawer><Drawer label="Top" onClose={top}>y</Drawer></>);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    window.removeEventListener('keydown', app);
    expect(top).toHaveBeenCalledTimes(1);
    expect(below).not.toHaveBeenCalled();
    expect(app).not.toHaveBeenCalled();
  });

  it('an open modal keeps Escape from stacked drawers', () => {
    const below = vi.fn(), top = vi.fn();
    render(<><Drawer label="Below" onClose={below}>x</Drawer><Drawer label="Top" onClose={top}>y</Drawer><div role="dialog" aria-modal="true" /></>);
    // The modal's own capture listener stops propagation; without it, bubble listeners run as before.
    const modal = (e: KeyboardEvent) => e.stopPropagation();
    window.addEventListener('keydown', modal, true);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    window.removeEventListener('keydown', modal, true);
    expect(top).not.toHaveBeenCalled();
    expect(below).not.toHaveBeenCalled();
  });
});
