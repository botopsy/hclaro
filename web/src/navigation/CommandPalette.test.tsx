import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CommandPalette } from './CommandPalette';
import type { NavContext } from './config';
import type { SwitchTarget } from './ProjectSwitcher';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const launch: NavContext = { route: { kind: 'launch' }, mode: 'launch', view: 'Overview' };
const targets: SwitchTarget[] = [
  { key: 'launch', label: 'launch-stack', detail: 'Opened from terminal', path: '/', current: true, launch: true },
  { key: 'n', label: 'network', detail: 'Workspace dev', path: '/projects/n/workspaces/dev' }
];

function setup(context = launch, extra: Partial<Parameters<typeof CommandPalette>[0]> = {}) {
  const props = { onClose: vi.fn(), onAction: vi.fn(), onPage: vi.fn(), onResource: vi.fn(), onSwitch: vi.fn() };
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><button>Search</button><CommandPalette context={context} targets={targets} {...props} {...extra} /></QueryClientProvider>);
  return props;
}
const options = () => screen.getAllByRole('option').map(o => o.getAttribute('aria-label'));

describe('CommandPalette', () => {
  it('lists actions, enabled pages and other projects, filters them, and says when nothing matches', async () => {
    setup();
    const input = screen.getByRole('combobox', { name: /Search pages/ });
    expect(input).toHaveFocus();
    expect(options()).toEqual(expect.arrayContaining(['Run plan… (Actions)', 'Check drift… (Actions)', 'Generate dependency graph… (Actions)', 'Add project… (Actions)', 'Plans (Pages)', 'network · Workspace dev (Projects)']));
    // The current project is not offered; AI Assistant is hidden while AI is off.
    expect(options()).not.toContain('launch-stack · Opened from terminal (Projects)');
    expect(options().some(o => o?.startsWith('AI Assistant'))).toBe(false);
    await userEvent.type(input, 'drift');
    expect(options()).toEqual(['Check drift… (Actions)', 'Drift detection (Pages)']);
    await userEvent.clear(input);
    await userEvent.type(input, 'zzz');
    expect(screen.queryByRole('listbox')).toBeNull();
    expect(screen.getByText('Nothing matches')).toBeInTheDocument();
  });

  it('hides actions and pages that are unavailable, and shows AI Assistant when AI is on', () => {
    setup({ route: { kind: 'context', projectId: 'n', workspace: 'dev', view: 'Overview' }, mode: 'hub-workspace', view: 'Overview', ai: true }, { addDisabled: true });
    const shown = options();
    expect(shown).not.toContain('Run plan… (Actions)');
    expect(shown).not.toContain('Add project… (Actions)');
    expect(shown).not.toContain('Plans (Pages)');
    expect(shown).toContain('Resources (Pages)');
    expect(shown).toContain('AI Assistant (Pages)');
  });

  it('moves with the arrow keys and runs the highlighted entry on Enter, which only reports the choice', async () => {
    const props = setup();
    const input = screen.getByRole('combobox');
    await userEvent.type(input, 'run plan');
    expect(screen.getByRole('option', { name: 'Run plan… (Actions)' })).toHaveAttribute('aria-selected', 'true');
    await userEvent.clear(input);
    await userEvent.type(input, 'drift{ArrowDown}');
    expect(screen.getByRole('option', { name: 'Drift detection (Pages)' })).toHaveAttribute('aria-selected', 'true');
    expect(input).toHaveAttribute('aria-activedescendant', screen.getByRole('option', { name: 'Drift detection (Pages)' }).id);
    await userEvent.keyboard('{ArrowDown}');
    expect(screen.getByRole('option', { name: 'Check drift… (Actions)' })).toHaveAttribute('aria-selected', 'true');
    await userEvent.keyboard('{Enter}');
    expect(props.onClose).toHaveBeenCalled();
    expect(props.onAction).toHaveBeenCalledWith('drift');
    expect(props.onPage).not.toHaveBeenCalled();
  });

  it('switches projects through the caller and closes on Escape', async () => {
    const props = setup();
    await userEvent.click(screen.getByRole('option', { name: /network/ }));
    expect(props.onSwitch).toHaveBeenCalledWith('/projects/n/workspaces/dev');
    await userEvent.keyboard('{Escape}');
    expect(props.onClose).toHaveBeenCalledTimes(2);
  });

  it('searches resources with the existing resource API and opens the chosen one', async () => {
    const fetch = vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('query=logs') ? { total: 1, resources: [{ id: 'r1', address: 'aws_s3_bucket.logs' }] } : { total: 0, resources: [] }), { headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', fetch);
    const props = setup(launch, { resourceScope: { kind: 'launch' } });
    await userEvent.type(screen.getByRole('combobox'), 'logs');
    const option = await screen.findByRole('option', { name: 'aws_s3_bucket.logs (Resources)' });
    expect(fetch.mock.calls.map(c => String(c[0])).every(u => u.includes('/resources?'))).toBe(true);
    await userEvent.click(option);
    expect(props.onResource).toHaveBeenCalledWith('r1');
    await waitFor(() => expect(within(screen.getByRole('dialog')).queryByText('Nothing matches')).toBeNull());
  });
});
