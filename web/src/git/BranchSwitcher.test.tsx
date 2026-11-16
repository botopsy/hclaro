import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BranchSwitcher } from './BranchSwitcher';
import type { GitBranchList, GitSwitchPreview } from '../types';

const main = '0123456789abcdef0123456789abcdef01234567';
const dev = 'fedcba9876543210fedcba9876543210fedcba98';
const branches: GitBranchList = {
  state: 'repository',
  head: { commit: main, branch: 'main', detached: false, unborn: false, worktree: false },
  branches: [{ name: 'dev', commit: dev, current: false }, { name: 'main', commit: main, current: true }, { name: 'prod', commit: dev, current: false }],
  hidden: 1, incomplete: false, checkedAt: '2026-11-16T10:00:00Z'
};
const clean: GitSwitchPreview = {
  from: branches.head!, to: branches.branches[0], workingTree: { clean: true, staged: 0, modified: 0, untracked: 0 },
  sourceChanges: { files: 3, iac: 2, inProject: 1 }, canSwitch: true, blocked: [], warnings: [], checkedAt: '2026-11-16T10:00:00Z'
};
const dirty: GitSwitchPreview = {
  ...clean, workingTree: { clean: false, staged: 1, modified: 2, untracked: 0 }, canSwitch: false,
  blocked: [{ code: 'dirty_tree', message: 'The working tree has uncommitted changes. Commit or stash them with your preferred Git client.' }]
};

type Call = { path: string; body?: Record<string, unknown> };
let calls: Call[];
function stub(preview: GitSwitchPreview) {
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const path = url.replace('/api/v1/', '');
    calls.push({ path, body: init.body ? JSON.parse(init.body as string) : undefined });
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'git/branches') return Response.json(branches);
    if (path === 'git/switch/preview') return Response.json(preview);
    if (path === 'git/switch') return Response.json({ switch: { from: 'main', to: 'dev' }, git: { state: 'repository' } });
    return Response.json({ error: 'not_found' }, { status: 404 });
  }));
}
function renderSwitcher(notify = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><BranchSwitcher prefix="" workspace="default" notify={notify} /></QueryClientProvider>);
  return notify;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Git branch switcher', () => {
  it('lists local branches, confirms and switches with the reviewed HEAD', async () => {
    stub(clean);
    const notify = renderSwitcher();
    // Nothing runs Git until the selector is opened.
    expect(calls.filter(c => c.path.startsWith('git/'))).toEqual([]);
    await userEvent.click(screen.getByRole('button', { name: 'Switch Git branch' }));
    const list = await screen.findByRole('list', { name: 'Local branches' });
    expect(within(list).getByRole('button', { name: /main/ })).toHaveAttribute('aria-current', 'true');
    expect(within(list).getByRole('button', { name: /main/ })).toBeDisabled();
    expect(screen.getByText(/1 branch with unusual names not shown/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Filter branches'), 'pro');
    expect(within(list).queryByRole('button', { name: /dev/ })).toBeNull();
    await userEvent.clear(screen.getByLabelText('Filter branches'));
    await userEvent.click(within(list).getByRole('button', { name: /dev/ }));
    const dialog = await screen.findByRole('dialog', { name: 'Switch Git branch?' });
    await within(dialog).findByText('Clean');
    expect(within(dialog).getByText(/3 files differ · 2 IaC · 1 in this project/)).toBeInTheDocument();
    expect(within(dialog).getByText('default')).toBeInTheDocument();
    expect(calls.some(c => c.path === 'git/switch')).toBe(false);
    await userEvent.click(within(dialog).getByRole('button', { name: /Switch to dev/ }));
    await waitFor(() => expect(notify).toHaveBeenCalledWith(expect.stringMatching(/^Switched to dev\. No Terraform/)));
    expect(calls.find(c => c.path === 'git/switch')?.body).toEqual({ branch: 'dev', expectedBranch: 'main', expectedCommit: main });
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('blocks a switch with uncommitted changes and explains why', async () => {
    stub(dirty);
    renderSwitcher();
    await userEvent.click(screen.getByRole('button', { name: 'Switch Git branch' }));
    await userEvent.click(within(await screen.findByRole('list', { name: 'Local branches' })).getByRole('button', { name: /dev/ }));
    const dialog = await screen.findByRole('dialog');
    expect(await within(dialog).findByText(/Commit or stash them with your preferred Git client/)).toBeInTheDocument();
    expect(within(dialog).getByText('1 staged, 2 modified')).toBeInTheDocument();
    const button = within(dialog).getByRole('button', { name: /Switch to dev/ });
    expect(button).toBeDisabled();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(calls.some(c => c.path === 'git/switch')).toBe(false);
  });
});
