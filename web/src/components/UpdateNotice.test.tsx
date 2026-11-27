import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UpdateNotice } from './UpdateNotice';

vi.mock('../api', () => ({ api: vi.fn() }));
import { api } from '../api';

function show(status: unknown) {
  vi.mocked(api).mockResolvedValue(status);
  return render(<QueryClientProvider client={new QueryClient()}><UpdateNotice /></QueryClientProvider>);
}

beforeEach(() => localStorage.clear());
afterEach(cleanup);

describe('UpdateNotice', () => {
  it('shows a newer version with the upgrade command and release notes', async () => {
    show({ enabled: true, current: '1.0.0', latest: '1.2.0', available: true });
    expect(await screen.findByText('Hclaro 1.2.0 is available')).toBeInTheDocument();
    expect(screen.getByText(/You have 1\.0\.0/)).toBeInTheDocument();
    expect(screen.getByText('brew upgrade botopsy/tap/hclaro')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: "What's new" })).toHaveAttribute('href', 'https://github.com/botopsy/hclaro/releases/tag/v1.2.0');
  });

  it.each([{ enabled: false, available: false }, { enabled: true, current: '1.0.0', available: false }])('stays hidden for %o', async status => {
    show(status);
    await vi.waitFor(() => expect(api).toHaveBeenCalled());
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('dismisses one version and shows the next release again', async () => {
    const { unmount } = show({ enabled: true, current: '1.0.0', latest: '1.2.0', available: true });
    fireEvent.click(await screen.findByRole('button', { name: 'Dismiss update notice' }));
    expect(screen.queryByText('Hclaro 1.2.0 is available')).toBeNull();
    unmount();
    show({ enabled: true, current: '1.0.0', latest: '1.2.0', available: true });
    await vi.waitFor(() => expect(api).toHaveBeenCalledTimes(2));
    expect(screen.queryByText('Hclaro 1.2.0 is available')).toBeNull();
    cleanup();
    show({ enabled: true, current: '1.0.0', latest: '1.3.0', available: true });
    expect(await screen.findByText('Hclaro 1.3.0 is available')).toBeInTheDocument();
  });
});
