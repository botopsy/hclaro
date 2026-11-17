import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AssistDrawer } from './AskAI';

// AI-01.04: Send to an assistant and the untrusted reply. Fake server only.
const hash = 'f'.repeat(64);
const markdown = '# Hclaro context: plan\n';
const disclosure = { version: '2', lines: ['Send runs a detected assistant once.'] };
const pkg = { kind: 'plan', markdown, sha256: hash, sections: ['context'], dropped: [], disclosure };
const provider = { id: 'claude', name: 'Claude Code', mode: 'headless-eligible', version: '2.1.326', path: '/synthetic/bin/claude' };
const reply = 'Run `terraform apply -auto-approve` and rm -rf / <a href="javascript:alert(1)">click</a> <script>window.pwned=1</script> [x](javascript:alert(1))\nresource "aws_s3_bucket" "b" {}';
const run = (status: string, extra = {}) => ({ id: '1'.repeat(32), provider: 'claude', kind: 'plan', sha256: hash, status, durationMs: 5, ...extra });

let posts: { url: string; body: unknown }[] = [];
function serve(settings: object, current: () => object) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: true, strictLocal: false, handoff: true, headless: true, disclosure, disclosureAccepted: true, providers: [provider], defaultProvider: 'claude', ...settings });
    if (url.endsWith('/ai/packages')) return Response.json(pkg);
    if (url.endsWith(`/ai/packages/${hash}/run`)) return Response.json(run('running'), { status: 202 });
    if (url.endsWith('/ai/run/cancel')) return Response.json({ cancelled: true });
    if (url.endsWith('/ai/run')) return Response.json(current());
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function show() {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><AssistDrawer scope={{ kind: 'launch' }} request={{ kind: 'plan', planId: 'a'.repeat(32) }} title="Ask AI" onClose={() => {}} /></QueryClientProvider>);
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('AI headless request', () => {
  it('sends the previewed hash only on click and renders the reply inert', async () => {
    serve({}, () => run('completed', { reply }));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Context package text');
    expect(posts.filter(p => p.url.endsWith('/run'))).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Send to Claude Code' }));
    const text = await screen.findByLabelText('Assistant reply text');
    expect(posts.find(p => p.url.endsWith('/run'))).toEqual({ url: `/api/v1/ai/packages/${hash}/run`, body: { provider: 'claude', disclosureVersion: '2' } });
    expect(text.textContent).toBe(reply);
    const panel = screen.getByRole('region', { name: 'Assistant reply' });
    expect(within(panel).queryAllByRole('link')).toHaveLength(0);
    expect(panel.querySelector('script, a, iframe')).toBeNull();
    expect(within(panel).queryAllByRole('button')).toHaveLength(0);
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined();
    expect(within(panel).getByText(/AI output is unverified/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /apply|approve/i })).toBeNull();
  });

  it('cancels a running request', async () => {
    serve({}, () => run('running'));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Context package text');
    await user.click(screen.getByRole('button', { name: 'Send to Claude Code' }));
    // The drawer's own Cancel closes the preview; the request's Cancel is in the reply panel.
    const panel = await screen.findByRole('region', { name: 'Assistant reply' });
    await user.click(await within(panel).findByRole('button', { name: 'Cancel' }));
    expect(posts.some(p => p.url.endsWith('/ai/run/cancel'))).toBe(true);
    expect(screen.getByText(/AI output is unverified/)).toBeInTheDocument();
  });

  it('needs the disclosure accepted, and offers no Send in strict local-only mode', async () => {
    serve({ disclosureAccepted: false }, () => run('completed'));
    show();
    await screen.findByLabelText('Context package text');
    expect(screen.getByRole('button', { name: 'Send to Claude Code' })).toBeDisabled();
    expect(screen.getByText(/accept the AI disclosure in Settings/)).toBeInTheDocument();
    cleanup();

    serve({ strictLocal: true, headless: false }, () => run('completed'));
    show();
    await screen.findByLabelText('Context package text');
    expect(screen.queryByRole('button', { name: /Send to/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument();
  });

  it('shows a failed request without provider text', async () => {
    serve({}, () => run('failed', { error: { code: 'auth_required', message: 'The assistant is not signed in.' } }));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Context package text');
    await user.click(screen.getByRole('button', { name: 'Send to Claude Code' }));
    expect(await screen.findByText('The assistant is not signed in.')).toBeInTheDocument();
  });
});
