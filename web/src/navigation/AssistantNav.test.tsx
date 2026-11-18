import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import { parseRoute } from '../router';
import type { Workspace } from '../types';
import { itemsIn, viewTrail } from './config';

// AI-01.09: the AI Assistant rail entry exists only while AI is on.
const hubId = 'a'.repeat(32);
const ws: Workspace = {
  status: 'ready', capturedAt: '2026-11-18T12:00:00Z',
  info: { project: 'launch-stack', engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 'local', executionMode: 'local-cli', initialized: true, providerLock: true, warnings: [], capabilities: { canReadState: true, canPlan: true, canApplySavedPlan: true, supportsImportBlocks: true, supportsGeneratedConfig: true, canImport: true } },
  managed: 0, data: 0, modules: [], providers: {}, types: {}
};
let ai = false;
beforeEach(() => {
  vi.stubGlobal('matchMedia', vi.fn().mockImplementation((query: string) => ({ matches: !query.includes('width'), media: query, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const path = url.replace('/api/v1/', '').split('?')[0];
    if (path === 'session') return Response.json({ csrfToken: 'c'.repeat(64) });
    if (path === 'ai/settings') return Response.json({ enabled: ai, strictLocal: false, handoff: ai, headless: false, disclosure: { version: '5', lines: [] }, providers: [] });
    if (path === 'ai/conversations') return Response.json({ conversations: [] });
    if (path === 'workspace') return Response.json(ws);
    if (path === 'resources') return Response.json({ resources: [], total: 0 });
    if (path === 'operations') return Response.json({ operations: [] });
    if (path === 'projects') return Response.json({ projects: [], registry: { status: 'ready', notice: '', path: '' }, environment: {}, launchRoot: '/infra/launch-stack' });
    return Response.json({}, { status: 404 });
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.pushState(null, '', '/');
});
function app() {
  window.history.pushState(null, '', '/');
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><App /></QueryClientProvider>);
}

describe('AI Assistant navigation', () => {
  it('adds the entry only with AI on and names it in breadcrumbs and Hub URLs', () => {
    expect(itemsIn('analyze').map(i => i.label)).toEqual(['Plans', 'Drift detection', 'Dependency graph', 'Compare']);
    expect(itemsIn('analyze', true).map(i => i.label)).toEqual(['Plans', 'Drift detection', 'Dependency graph', 'Compare', 'AI Assistant']);
    expect(viewTrail('Assistant')).toEqual(['AI Assistant']);
    expect(parseRoute(`/projects/${hubId}/workspaces/dev/assistant`)).toEqual({ kind: 'context', projectId: hubId, workspace: 'dev', view: 'Assistant' });
  });

  it('hides the rail entry while AI is off', async () => {
    ai = false;
    app();
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    await screen.findByRole('button', { name: 'Plans' });
    expect(within(nav).queryByRole('button', { name: 'AI Assistant' })).toBeNull();
  });

  it('opens the AI Assistant page from the rail while AI is on', async () => {
    ai = true;
    app();
    const user = userEvent.setup();
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    await user.click(await within(nav).findByRole('button', { name: 'AI Assistant' }));
    expect(await screen.findByRole('heading', { name: 'AI Assistant' })).toBeInTheDocument();
    expect(await screen.findByText(/Conversation only: no project data is attached/)).toBeInTheDocument();
  });
});
