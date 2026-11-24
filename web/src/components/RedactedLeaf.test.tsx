import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api';
import { JsonTree } from './JsonTree';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

const attributes = { name: 'db', password: '[redacted]', env: [{ value: '[redacted]' }] };

describe('Reveal a redacted value', () => {
  it('reveals on one click without a warning, shows the value and can copy and hide it', async () => {
    const user = userEvent.setup();
    const reveal = vi.fn().mockResolvedValue('hunter2');
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    render(<JsonTree value={attributes} reveal={reveal} />);
    await user.click(screen.getAllByRole('button', { name: /Reveal/ })[0]);
    expect(screen.queryByText(/never saved or sent anywhere/)).toBeNull();
    expect(reveal).toHaveBeenCalledWith(['password']);
    expect((await screen.findByTestId('revealed-value')).textContent).toBe('hunter2');
    await user.click(screen.getByRole('button', { name: 'Copy value of password' }));
    expect(writeText).toHaveBeenCalledWith('hunter2');
    await user.click(screen.getByRole('button', { name: /Hide/ }));
    expect(screen.queryByText('hunter2')).toBeNull();
    // Nothing was stored.
    expect(JSON.stringify({ ...localStorage })).not.toContain('hunter2');
    expect(JSON.stringify({ ...sessionStorage })).not.toContain('hunter2');
  });

  it('drops the value when it unmounts', async () => {
    const user = userEvent.setup();
    const reveal = vi.fn().mockResolvedValue('hunter2');
    const { unmount } = render(<JsonTree value={attributes} reveal={reveal} />);
    await user.click(screen.getAllByRole('button', { name: /Reveal/ })[0]);
    await screen.findByText('hunter2');
    unmount();
    expect(document.body.textContent).not.toContain('hunter2');
  });

  it('says the backend denied access and offers Try again', async () => {
    const user = userEvent.setup();
    const reveal = vi.fn().mockRejectedValueOnce(new ApiError('reveal_denied', 'x')).mockResolvedValueOnce('later');
    render(<JsonTree value={attributes} reveal={reveal} />);
    await user.click(screen.getAllByRole('button', { name: /Reveal/ })[0]);
    expect((await screen.findByRole('alert')).textContent).toContain('Your credentials can’t read this value');
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect((await screen.findByTestId('revealed-value')).textContent).toBe('later');
  });

  it('offers no Reveal button without a reveal function', () => {
    render(<JsonTree value={attributes} />);
    expect(screen.queryByRole('button', { name: /Reveal/ })).toBeNull();
    expect(screen.getAllByText('redacted').length).toBe(2);
  });
});
