import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Dialog } from './Dialog';

afterEach(cleanup);

describe('Dialog', () => {
  it('is a labelled modal with a body and footer, closing on Escape and ✕', () => {
    const close = vi.fn();
    render(<Dialog title="Run a plan" onClose={close} footer={<button>Run plan</button>}><p>Body</p></Dialog>);
    const dialog = screen.getByRole('dialog', { name: 'Run a plan' });
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(dialog.querySelector('.dialog-body')).toHaveTextContent('Body');
    expect(dialog.querySelector('.dialog-actions')).toHaveTextContent('Run plan');
    expect(screen.getByRole('button', { name: 'Close' })).toHaveFocus();
    fireEvent.keyDown(document.body, { key: 'Escape' });
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(close).toHaveBeenCalledTimes(2);
  });

  it('returns focus to the opener when it closes', () => {
    const { rerender } = render(<button>Open</button>);
    screen.getByRole('button', { name: 'Open' }).focus();
    rerender(<><button>Open</button><Dialog title="T" onClose={() => {}}>x</Dialog></>);
    expect(screen.getByRole('button', { name: 'Close' })).toHaveFocus();
    rerender(<button>Open</button>);
    expect(screen.getByRole('button', { name: 'Open' })).toHaveFocus();
  });
});
