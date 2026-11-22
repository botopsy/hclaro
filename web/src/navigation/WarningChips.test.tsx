import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { GitContext, Workspace } from '../types';
import { WarningChips } from './WarningChips';

afterEach(cleanup);

const ws = (extra: Partial<Workspace> = {}) => ({ status: 'ready', info: {}, ...extra }) as Workspace;
const git = (changed: number, state: GitContext['state'] = 'repository') => ({ state, clean: changed === 0, files: [], counts: { changed, untracked: 0, outside: 0, other: 0 }, warnings: [], incomplete: false, checkedAt: '' }) as GitContext;

function show(workspace: Workspace | undefined, cached?: GitContext, prefixOrNone: string | null = '') {
  const prefix = prefixOrNone ?? undefined;
  const client = new QueryClient();
  if (cached) client.setQueryData(['git-status', prefix], cached);
  const onOpen = vi.fn();
  const fetch = vi.spyOn(globalThis, 'fetch');
  render(<QueryClientProvider client={client}><span data-testid="bar"><WarningChips workspace={workspace} gitPrefix={prefix} onOpen={onOpen} /></span></QueryClientProvider>);
  return { onOpen, fetch };
}

describe('WarningChips', () => {
  it('shows nothing when all is fine, and never runs git status itself', () => {
    const { fetch } = show(ws(), git(0));
    expect(screen.getByTestId('bar')).toBeEmptyDOMElement();
    show(ws());
    expect(fetch).not.toHaveBeenCalled();
  });

  it('shows Backend unreachable only for an unreadable state, and opens Overview', async () => {
    const { onOpen } = show(ws({ status: 'error', diagnostic: { code: 'state_unavailable', message: '' } }));
    await userEvent.click(screen.getByRole('button', { name: 'Backend unreachable' }));
    expect(onOpen).toHaveBeenCalledWith('Overview');
    cleanup();
    show(ws({ status: 'error', diagnostic: { code: 'state_locked', message: '' } }));
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('counts uncommitted IaC files from the cached Git status, and opens Git', async () => {
    const { onOpen } = show(ws(), git(3), 'projects/x/');
    await userEvent.click(screen.getByRole('button', { name: '3 uncommitted IaC files' }));
    expect(onOpen).toHaveBeenCalledWith('Git');
    cleanup();
    show(ws(), git(1));
    expect(screen.getByRole('button', { name: '1 uncommitted IaC file' })).toBeTruthy();
    cleanup();
    show(ws(), git(2), null);
    expect(screen.queryByRole('button')).toBeNull();
  });
});
