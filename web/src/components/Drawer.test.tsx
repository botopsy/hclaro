import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Drawer } from './Drawer';
import { navigate } from '../router';

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

describe('Drawer redesign behaviour', () => {
  it('opens at 520px over a backdrop that closes it', () => {
    localStorage.clear();
    const close = vi.fn();
    const { container } = render(<Drawer label="Details" onClose={close}>x</Drawer>);
    expect(container.querySelector('aside')).toHaveStyle({ width: '520px' });
    fireEvent.click(container.querySelector('.drawer-backdrop')!);
    expect(close).toHaveBeenCalledTimes(1);
  });

  it('closes on navigation', () => {
    const close = vi.fn();
    render(<Drawer label="Details" onClose={close}>x</Drawer>);
    navigate('/somewhere-else');
    expect(close).toHaveBeenCalledTimes(1);
    window.dispatchEvent(new PopStateEvent('popstate'));
    expect(close).toHaveBeenCalledTimes(2);
  });
});
