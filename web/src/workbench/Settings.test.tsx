import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SettingsPanel } from './SettingsPanel';
import type { ExecutionProfile } from '../types';

const projectId = '1'.repeat(32);
const prodId = '2'.repeat(32);
const profile = (id: string, name: string, extra: Partial<ExecutionProfile> = {}): ExecutionProfile => ({ id, projectId, name, engine: '', workspace: '', varFiles: [], parallelism: 0, lockTimeoutSeconds: 0, awsProfile: '', awsRegion: '', awsAuth: '', awsExpectedAccount: '', awsExpectedRole: '', envNames: [], revision: 3, createdAt: '2026-11-05T09:00:00Z', updatedAt: '2026-11-05T09:00:00Z', default: false, ...extra });
let status = 'ready';
let calls: { method: string; url: string; body: Record<string, unknown> }[] = [];

beforeEach(() => {
  status = 'ready';
  calls = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method && init.method !== 'GET') calls.push({ method: init.method, url, body: JSON.parse(String(init.body)) });
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64), version: '0.1.0' });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: false, providers: [] });
    if (url.endsWith(`/projects/${projectId}/execution-profiles`)) return init?.method === 'POST' ? Response.json(profile('4'.repeat(32), 'Dev'), { status: 201 }) : Response.json({ profiles: [profile(prodId, 'Production', { awsProfile: 'prod-sso', awsRegion: 'eu-west-1' })], status, notice: '' });
    if (url.endsWith(`/execution-profiles/${prodId}`)) return new Response(null, { status: 204 });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const change = vi.fn();
function settings(project?: { id: string; name: string }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><SettingsPanel preferences={{ theme: 'system', density: 'comfortable', grouping: 'module', editor: 'vscode', sidebar: 'expanded' }} changePreference={change} project={project} /></QueryClientProvider>);
}

describe('Settings (§4.7)', () => {
  it('asks to open a project first when none is open', () => {
    settings();
    expect(screen.getByText(/Open a project first/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Add profile/ })).toBeNull();
  });

  it('lists the open project profiles and adds one through the project endpoint', async () => {
    const user = userEvent.setup();
    settings({ id: projectId, name: 'network' });
    expect(screen.getByRole('heading', { name: 'Execution profiles for network' })).toBeInTheDocument();
    const list = await screen.findByRole('list', { name: 'Execution profiles' });
    expect(within(list).getAllByRole('listitem').map(li => li.textContent)).toEqual(['None (inherited environment)Uses the environment Hclaro started with', 'ProductionAWS_PROFILE=prod-sso · eu-west-1Remove']);
    const add = screen.getByRole('button', { name: 'Add profile' });
    expect(add).toBeDisabled();
    await user.type(screen.getByLabelText('Name'), 'production');
    await user.type(screen.getByLabelText('AWS profile'), 'dev-sso');
    // Unique within this project, ignoring case, like the server.
    expect(add).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('already has a profile named production');
    await user.clear(screen.getByLabelText('Name'));
    await user.type(screen.getByLabelText('Name'), ' Dev ');
    await user.type(screen.getByLabelText('Region'), 'eu-central-1');
    expect(add).toBeEnabled();
    await user.click(add);
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({ method: 'POST', url: expect.stringMatching(new RegExp(`/projects/${projectId}/execution-profiles$`)), body: { name: 'Dev', awsProfile: 'dev-sso', awsRegion: 'eu-central-1', varFiles: [], envNames: [] } });
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue(''));
  });

  it('removes a profile after a second click, with its revision', async () => {
    const user = userEvent.setup();
    settings({ id: projectId, name: 'network' });
    await user.click(await screen.findByRole('button', { name: 'Remove Production' }));
    expect(calls).toEqual([]);
    await user.click(screen.getByRole('button', { name: 'Remove Production' }));
    await waitFor(() => expect(calls).toEqual([{ method: 'DELETE', url: expect.stringMatching(new RegExp(`/execution-profiles/${prodId}$`)), body: { expectedRevision: 3 } }]));
  });

  it('disables changes when the profile store is read-only', async () => {
    status = 'unavailable';
    settings({ id: projectId, name: 'network' });
    expect(await screen.findByRole('button', { name: 'Remove Production' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Add profile' })).toBeDisabled();
  });

  it('offers By type as a default grouping and About links', async () => {
    const user = userEvent.setup();
    settings();
    await user.selectOptions(screen.getByLabelText('Default grouping'), 'type');
    expect(change).toHaveBeenCalledWith({ grouping: 'type' });
    expect(screen.getByRole('link', { name: 'Report a bug' })).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getByRole('group', { name: 'Theme' })).toBeInTheDocument();
  });
});
