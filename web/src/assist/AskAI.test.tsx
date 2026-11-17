import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import planGolden from '../../../fixtures/plan/all-actions.golden.json';
import { PlanReview } from '../plans/PlanReview';
import type { PlanScope } from '../plans/routes';
import type { Plan } from '../types';

const planId = 'a'.repeat(32);
const review = Object.fromEntries(Object.entries(planGolden).filter(([key]) => key !== 'details'));
const plan = {
  ...review, id: planId, operationId: 'b'.repeat(32), createdAt: '2026-11-09T09:00:00Z', engine: 'terraform', currentEngineVersion: '1.9.8',
  workspace: 'demo', varFiles: [], kind: 'plan', replace: [], destructive: true, artifactSha256: 'c'.repeat(64), fingerprint: 'd'.repeat(64),
  contextChanged: false, apply: { available: true, artifactRetained: true, expiresAt: '2026-11-09T10:00:00Z' }
} as unknown as Plan;
const hash = 'f'.repeat(64);
const markdown = '# Hclaro context: plan\n\n## Changes\n\n```text\ndelete aws_instance.old\n```\n';
const disclosure = { version: '1', lines: ['Data may reach a cloud model and bills your own account.'] };
const pkg = { kind: 'plan', markdown, sha256: hash, sections: ['context', 'changes'], dropped: ['configuration'], disclosure };
const settings = (enabled: boolean) => ({ enabled, strictLocal: false, handoff: enabled, headless: false, disclosure });

type Route = (url: string) => Response | Promise<Response> | undefined;
let posts: string[] = [];
function serve(enabled: boolean, ...routes: Route[]) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push(url);
    for (const route of routes) {
      const response = route(url);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json(settings(enabled));
    if (url.endsWith('/ai/packages')) return Response.json(pkg);
    if (url.endsWith(`/ai/packages/${hash}/export`)) return Response.json({ kind: 'plan', sha256: hash, markdown, filename: `hclaro-plan-${hash.slice(0, 12)}.md` });
    if (url.includes('/cost/')) return Response.json({ error: 'cost_unavailable' }, { status: 409 });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function show(scope?: PlanScope) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlanReview plan={plan} copy={async () => {}} scope={scope} /></QueryClientProvider>);
}
const exports = () => posts.filter(u => u.endsWith('/export'));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('AI handoff', () => {
  it('renders no entry point and sends nothing while AI is off', async () => {
    serve(false);
    show();
    await screen.findByText(/Ready to apply/);
    await new Promise(r => setTimeout(r, 50));
    expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).endsWith('/ai/settings'))).toBe(true);
    expect(screen.queryByRole('button', { name: /AI/ })).toBeNull();
    expect(screen.queryByText(/destroys or replaces objects/)).toBeNull();
    expect(posts).toHaveLength(0);
  });

  it('previews the package and copies exactly the previewed bytes only after a click', async () => {
    let release!: () => void;
    const gate = new Promise<void>(r => { release = r; });
    serve(true, url => url.endsWith('/ai/packages') ? gate.then(() => Response.json(pkg)) : undefined);
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, 'writeText');
    show();
    expect(await screen.findByRole('button', { name: /Review with AI/ })).toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: /Ask AI to analyze/ }));
    expect(await screen.findByText(/Building the context package/)).toBeInTheDocument();
    release();
    const text = await screen.findByLabelText('Context package text');
    expect(text.textContent).toBe(markdown);
    expect(screen.getByText('Data may reach a cloud model and bills your own account.')).toBeInTheDocument();
    expect(screen.getByText('configuration')).toBeInTheDocument();
    expect(write).not.toHaveBeenCalled();
    expect(exports()).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await screen.findByText(/protections end here/)).toBeInTheDocument();
    expect(write).toHaveBeenCalledWith(markdown);
    expect(exports()).toEqual([`/api/v1/ai/packages/${hash}/export`]);
  });

  it('downloads exactly the previewed bytes as .md', async () => {
    serve(true);
    const user = userEvent.setup();
    const blobs: Blob[] = [];
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => { blobs.push(b); return 'blob:x'; }), revokeObjectURL: vi.fn() }));
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    show({ kind: 'context', contextId: '1'.repeat(32) });
    await user.click(await screen.findByRole('button', { name: /Ask AI to analyze/ }));
    await screen.findByLabelText('Context package text');
    expect(blobs).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Download .md' }));
    await screen.findByText(/protections end here/);
    expect(clicked).toHaveBeenCalledTimes(1);
    expect(await blobs[0].text()).toBe(markdown);
    expect(posts).toEqual([`/api/v1/contexts/${'1'.repeat(32)}/ai/packages`, `/api/v1/contexts/${'1'.repeat(32)}/ai/packages/${hash}/export`]);
  });

  it('Cancel closes the preview and sends nothing', async () => {
    serve(true);
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, 'writeText');
    show();
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    await screen.findByLabelText('Context package text');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByLabelText('Context package text')).toBeNull();
    expect(exports()).toHaveLength(0);
    expect(write).not.toHaveBeenCalled();
  });

  it('explains a refused clipboard and does not claim the text was handed over', async () => {
    serve(true);
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new DOMException('Write permission denied.', 'NotAllowedError'));
    show();
    await user.click(await screen.findByRole('button', { name: /Ask AI to analyze/ }));
    await screen.findByLabelText('Context package text');
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await screen.findByText(/did not allow copying.*Download \.md/)).toBeInTheDocument();
    expect(screen.queryByText(/protections end here/)).toBeNull();
  });

  it('shows blocked field paths without Copy or Download', async () => {
    serve(true, url => url.endsWith('/ai/packages') ? Response.json({ error: 'package_blocked', reason: 'Some values still look secret.', blockedFields: ['changes[0].attributes[1].after'] }, { status: 422 }) : undefined);
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: /Ask AI to analyze/ }));
    expect(await screen.findByText('Sharing blocked')).toBeInTheDocument();
    expect(screen.getByText('changes[0].attributes[1].after')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Download .md' })).toBeNull();
  });

  it('shows error and empty states', async () => {
    serve(true, url => url.endsWith('/ai/packages') ? Response.json({ error: 'context_changed', reason: 'The project context changed since this plan was made.' }, { status: 409 }) : undefined);
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: /Ask AI to analyze/ }));
    expect(await screen.findByText('The project context changed since this plan was made.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
    cleanup();

    serve(true, url => url.endsWith('/ai/packages') ? Response.json({ ...pkg, sections: [] }) : undefined);
    show();
    await user.click(await screen.findByRole('button', { name: /Ask AI to analyze/ }));
    expect(await screen.findByText('Nothing to share')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
  });
});
