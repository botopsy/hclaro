import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Menu, MenuItem, Popover } from './Menu';
import { Dialog } from './Dialog';

afterEach(cleanup);

function profiles(pick = vi.fn()) {
  return <Menu button="Profile ▾" label="Profile">
   <MenuItem checked onSelect={() => pick('none')}>None</MenuItem>
   <MenuItem checked={false} onSelect={() => pick('dev')}>dev</MenuItem>
   <MenuItem onSelect={() => pick('manage')}>Manage profiles…</MenuItem>
  </Menu>;
}

describe('Menu and Popover', () => {
  it('opens, moves with the arrow keys and closes on selection', () => {
    const pick = vi.fn();
    render(profiles(pick));
    const trigger = screen.getByRole('button', { name: 'Profile' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(trigger);
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    const items = [...screen.getAllByRole('menuitemradio'), screen.getByRole('menuitem')];
    expect(items[0]).toHaveFocus();
    expect(items[0]).toHaveAttribute('aria-checked', 'true');
    const menu = screen.getByRole('menu');
    fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(items[1]).toHaveFocus();
    fireEvent.keyDown(menu, { key: 'ArrowUp' });
    fireEvent.keyDown(menu, { key: 'ArrowUp' });
    expect(items[2]).toHaveFocus();
    fireEvent.keyDown(menu, { key: 'Home' });
    expect(items[0]).toHaveFocus();
    fireEvent.click(items[1]);
    expect(pick).toHaveBeenCalledWith('dev');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger).toHaveFocus();
  });

  it('Escape closes the menu, returns focus and leaves the enclosing dialog open', () => {
    const close = vi.fn();
    render(<Dialog title="Pick" onClose={close}>{profiles()}</Dialog>);
    const trigger = screen.getByRole('button', { name: 'Profile' });
    fireEvent.click(trigger);
    fireEvent.keyDown([...screen.getAllByRole('menuitemradio'), screen.getByRole('menuitem')][0], { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger).toHaveFocus();
    expect(close).not.toHaveBeenCalled();
  });

  it('a click outside closes a popover, which can hold an input', () => {
    render(<><Popover button="Workspace ▾" label="Workspaces"><input aria-label="New workspace name" /></Popover><p>outside</p></>);
    fireEvent.click(screen.getByRole('button', { name: 'Workspaces' }));
    expect(screen.getByRole('dialog', { name: 'Workspaces' })).toBeInTheDocument();
    expect(screen.getByLabelText('New workspace name')).toHaveFocus();
    fireEvent.pointerDown(screen.getByText('outside'));
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
