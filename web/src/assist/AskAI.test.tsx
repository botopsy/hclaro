import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import planGolden from '../../../fixtures/plan/all-actions.golden.json';
import { PlanReview } from '../plans/PlanReview';
import { AskAI } from './AskAI';
import type { AssistRequest } from '../types';
import type { PlanScope } from '../plans/routes';
import type { ImportRequest, Plan } from '../types';

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
// The review is the first turn: the review prompt and the package. Copy and Download hand it over.
const reviewText = '# Hclaro review request (review-plan@1)\n\n' + markdown + '\n## Review request\n\nReview this plan.\n';
const pkg = { kind: 'plan', markdown, sha256: hash, sections: ['context', 'changes'], dropped: ['configuration'], disclosure, prompt: 'review-plan@1', review: reviewText };
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
    if (url.endsWith(`/ai/packages/${hash}/export`)) return Response.json({ kind: 'plan', sha256: hash, markdown, prompt: 'review-plan@1', review: reviewText, filename: `hclaro-plan-${hash.slice(0, 12)}.md` });
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
    // A destructive plan has one entry point, on its warning line.
    expect(await screen.findByText(/destroys or replaces objects/)).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /Review with AI/ })).toHaveLength(1);
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    expect(await screen.findByText(/Building the context package/)).toBeInTheDocument();
    release();
    const text = await screen.findByLabelText('Text to share: review prompt and context package');
    expect(text.textContent).toBe(reviewText);
    expect(screen.getByText('review-plan@1')).toBeInTheDocument();
    expect(screen.getByText('Data may reach a cloud model and bills your own account.')).toBeInTheDocument();
    expect(screen.getByText('configuration')).toBeInTheDocument();
    expect(write).not.toHaveBeenCalled();
    expect(exports()).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await screen.findByText(/protections end here/)).toBeInTheDocument();
    expect(write).toHaveBeenCalledWith(reviewText);
    expect(exports()).toEqual([`/api/v1/ai/packages/${hash}/export`]);
  });

  it('downloads exactly the previewed bytes as .md', async () => {
    serve(true);
    const user = userEvent.setup();
    const blobs: Blob[] = [];
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => { blobs.push(b); return 'blob:x'; }), revokeObjectURL: vi.fn() }));
    const clicked = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    show({ kind: 'context', contextId: '1'.repeat(32) });
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(blobs).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Download .md' }));
    await screen.findByText(/protections end here/);
    expect(clicked).toHaveBeenCalledTimes(1);
    expect(await blobs[0].text()).toBe(reviewText);
    expect(posts).toEqual([`/api/v1/contexts/${'1'.repeat(32)}/ai/packages`, `/api/v1/contexts/${'1'.repeat(32)}/ai/packages/${hash}/export`]);
  });

  it('Cancel closes the preview and sends nothing', async () => {
    serve(true);
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, 'writeText');
    show();
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByLabelText('Text to share: review prompt and context package')).toBeNull();
    expect(exports()).toHaveLength(0);
    expect(write).not.toHaveBeenCalled();
  });

  it('explains a refused clipboard and does not claim the text was handed over', async () => {
    serve(true);
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new DOMException('Write permission denied.', 'NotAllowedError'));
    show();
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(await screen.findByText(/did not allow copying.*Download \.md/)).toBeInTheDocument();
    expect(screen.queryByText(/protections end here/)).toBeNull();
  });

  it('shows blocked field paths without Copy or Download', async () => {
    serve(true, url => url.endsWith('/ai/packages') ? Response.json({ error: 'package_blocked', reason: 'Some values still look secret.', blockedFields: ['changes[0].attributes[1].after'] }, { status: 422 }) : undefined);
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    expect(await screen.findByText('Sharing blocked')).toBeInTheDocument();
    expect(screen.getByText('changes[0].attributes[1].after')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Download .md' })).toBeNull();
  });

  it('shows error and empty states', async () => {
    serve(true, url => url.endsWith('/ai/packages') ? Response.json({ error: 'context_changed', reason: 'The project context changed since this plan was made.' }, { status: 409 }) : undefined);
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    expect(await screen.findByText('The project context changed since this plan was made.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
    cleanup();

    serve(true, url => url.endsWith('/ai/packages') ? Response.json({ ...pkg, sections: [] }) : undefined);
    show();
    await user.click(await screen.findByRole('button', { name: /Review with AI/ }));
    expect(await screen.findByText('Nothing to share')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull();
  });
});

// AI-01.06 no-AI baseline. AI off: every entry point (plan, drift, import, resource) goes
// through AskAI, which renders nothing and sends nothing, whether or not any assistant is
// installed. AI on with no assistant installed: every entry point stays available for handoff
// (AI-01.05); only Send and the Anthropic line depend on a detected Claude Code.
const requests: AssistRequest[] = [
  { kind: 'plan', planId }, { kind: 'drift', scanId: 'a'.repeat(32) }, { kind: 'resource', resourceId: 'a'.repeat(32) },
  { kind: 'import', import: { address: 'aws_s3_bucket.x', id: 'x', file: 'imports.tf' } as ImportRequest }
];
function entryPoints(scope: PlanScope) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    {requests.map(r => <AskAI key={r.kind} scope={scope} request={r} label={`Ask AI ${r.kind}`} />)}
  </QueryClientProvider>);
}

describe('No-AI baseline: AI off', () => {
  // What the server sends with no assistant session at all (assist.Settings{}).
  const absent = { enabled: false, strictLocal: false, handoff: false, headless: false, disclosure: { version: '', lines: null }, disclosureAccepted: false, providers: null };
  const offWithAssistants = { ...settings(false), providers: [{ id: 'codex', name: 'OpenAI Codex', mode: 'handoff-only', send: false, usable: true, installed: true }] };
  const baselines: [string, Route][] = [
    ['no assistant session', u => (u.endsWith('/ai/settings') ? Response.json(absent) : undefined)],
    ['switched off', () => undefined],
    ['switched off with an assistant installed', u => (u.endsWith('/ai/settings') ? Response.json(offWithAssistants) : undefined)],
    ['no AI routes', u => (u.endsWith('/ai/settings') ? Response.json({ error: 'route_not_found' }, { status: 404 }) : undefined)]
  ];
  for (const [name, route] of baselines) {
    it(`renders no AI element for any entry point: ${name}`, async () => {
      serve(false, route);
      const { container } = entryPoints({ kind: 'launch' });
      await vi.waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).endsWith('/ai/settings'))).toBe(true));
      await new Promise(r => setTimeout(r, 50));
      expect(container.innerHTML).toBe('');
      expect(screen.queryByText(/AI/)).toBeNull();
      expect(posts).toHaveLength(0);
    });
  }
});

describe.each([['single project', { kind: 'launch' }, '/api/v1'], ['Hub', { kind: 'context', contextId: '1'.repeat(32) }, `/api/v1/contexts/${'1'.repeat(32)}`]] as [string, PlanScope, string][])(
  'AI on, no assistant installed (%s)', (_, scope, prefix) => {
    const none = { enabled: true, strictLocal: false, handoff: true, headless: false, disclosure: { version: '3', lines: ['Hclaro builds this context on your machine and shares nothing by itself.'] }, disclosureAccepted: false, providers: [] };
    it.each(requests.map(r => [r.kind, r] as [string, AssistRequest]))('%s: preview, Copy and Download; no Send, no Anthropic line', async (kind, request) => {
      serve(true, u => (u.endsWith('/ai/settings') ? Response.json(none) : undefined));
      const user = userEvent.setup();
      const write = vi.spyOn(navigator.clipboard, 'writeText');
      const blobs: Blob[] = [];
      vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => { blobs.push(b); return 'blob:x'; }), revokeObjectURL: vi.fn() }));
      vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
      entryPoints(scope);
      for (const r of requests) expect(await screen.findByRole('button', { name: `Ask AI ${r.kind}` })).toBeInTheDocument();
      expect(posts).toHaveLength(0);
      await user.click(screen.getByRole('button', { name: `Ask AI ${kind}` }));
      expect((await screen.findByLabelText('Text to share: review prompt and context package')).textContent).toBe(reviewText);
      const body = JSON.parse(String(vi.mocked(fetch).mock.calls.find(([u]) => String(u) === `${prefix}/ai/packages`)?.[1]?.body));
      expect(body.kind).toBe(request.kind);
      expect(screen.queryByRole('button', { name: /Send/ })).toBeNull();
      expect(screen.queryByText(/Anthropic|Claude/)).toBeNull();
      await user.click(screen.getByRole('button', { name: 'Copy' }));
      await screen.findByText(/protections end here/);
      expect(write).toHaveBeenCalledWith(reviewText);
      await user.click(screen.getByRole('button', { name: 'Download .md' }));
      await vi.waitFor(() => expect(blobs).toHaveLength(1));
      expect(await blobs[0].text()).toBe(reviewText);
      expect(posts.filter(u => u.endsWith('/run'))).toHaveLength(0);
      expect(posts.every(u => u.startsWith(prefix + '/ai/packages'))).toBe(true);
    });
  });
