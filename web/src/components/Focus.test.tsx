import { useRef, useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { Drawer } from './Drawer';
import { useModal } from './useModal';

function Modal({ onClose }: { onClose: () => void }) {
  const dialog = useRef<HTMLDivElement>(null);
  const first = useRef<HTMLButtonElement>(null);
  useModal(dialog, first, onClose);
  return <div ref={dialog} role="dialog" aria-label="Test dialog">
   <button ref={first}>First</button>
   <button disabled>Disabled</button>
   <textarea aria-label="Notes" />
   <select aria-label="Last"><option>a</option></select>
  </div>;
}

function Harness() {
  const [open, setOpen] = useState<'modal' | 'drawer'>();
  return <>
   <button onClick={() => setOpen('modal')}>Open dialog</button>
   <button onClick={() => setOpen('drawer')}>Row one</button>
   <button>Row two</button>
   {open === 'modal' && <Modal onClose={() => setOpen(undefined)} />}
   {open === 'drawer' && <Drawer label="Details" onClose={() => setOpen(undefined)}><button onClick={() => setOpen(undefined)}>Close details</button></Drawer>}
  </>;
}

// A parent that re-renders with fresh props, like the views behind the apply dialog while operations poll.
function PollingParent({ tick }: { tick: number }) {
  const [open, setOpen] = useState(true);
  return <>
   <button>Poll {tick}</button>
   {open && <Modal onClose={() => setOpen(false)} />}
  </>;
}

describe('keyboard focus', () => {
  it('keeps focus and typed text in a dialog input when the parent re-renders', async () => {
    const user = userEvent.setup();
    const { rerender } = render(<PollingParent tick={0} />);
    await user.type(screen.getByLabelText('Notes'), 'apply');
    rerender(<PollingParent tick={1} />);
    expect(screen.getByRole('button', { name: 'Poll 1' })).toBeInTheDocument();
    expect(screen.getByLabelText('Notes')).toHaveFocus();
    await user.type(screen.getByLabelText('Notes'), ' now');
    expect(screen.getByLabelText('Notes')).toHaveValue('apply now');
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('keeps Tab inside a dialog across every focusable control and restores focus on Escape', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Open dialog' }));
    expect(screen.getByRole('button', { name: 'First' })).toHaveFocus();
    await user.tab();
    expect(screen.getByLabelText('Notes')).toHaveFocus();
    await user.tab();
    expect(screen.getByLabelText('Last')).toHaveFocus();
    await user.tab();
    expect(screen.getByRole('button', { name: 'First' })).toHaveFocus();
    await user.tab({ shift: true });
    expect(screen.getByLabelText('Last')).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByRole('button', { name: 'Open dialog' })).toHaveFocus();
  });

  it('returns focus to the row that opened a drawer when it closes', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Row one' }));
    await user.click(screen.getByRole('button', { name: 'Close details' }));
    expect(screen.queryByLabelText('Details')).toBeNull();
    expect(screen.getByRole('button', { name: 'Row one' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: 'Row one' }));
    await user.keyboard('{Escape}');
    expect(screen.getByRole('button', { name: 'Row one' })).toHaveFocus();
  });

  it('leaves focus alone when the user moved it elsewhere before the drawer closed', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Row one' }));
    await user.click(screen.getByRole('button', { name: 'Row two' }));
    await user.click(screen.getByRole('button', { name: 'Open dialog' }));
    expect(screen.getByRole('button', { name: 'First' })).toHaveFocus();
  });
});
