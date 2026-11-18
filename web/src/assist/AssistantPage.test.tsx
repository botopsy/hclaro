import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AssistantPage } from './AssistantPage';
import type { PlanScope } from '../plans/routes';

// AI-01.09: the AI Assistant page. Fake server only.
const hash = 'a'.repeat(64);
const id = '1'.repeat(32);
const other = '2'.repeat(32);
const at = '2026-11-18T09:00:00Z';
const markdown = '# Hclaro context: project\n\nresource "null_resource" "a" {}\n';
const disclosure = { version: '5', lines: ['Hclaro builds this context on your machine.'] };
const claude = { id: 'claude', name: 'Claude Code', mode: 'headless-eligible' };
const codex = { id: 'codex', name: 'Codex CLI', mode: 'handoff-only' };
const pkg = { kind: 'project', markdown, sha256: hash, sections: ['context', 'files'], dropped: [], disclosure, prompt: '', review: 'x' };
const chat = (turns: object[], extra = {}) => ({ id, provider: 'claude', kind: 'conversation', packageSha256: '', state: 'ready', turns, usedBytes: 1024, limitBytes: 262144, startedAt: at, ...extra });
const listed = (extra = {}) => ({ id, provider: 'claude', kind: 'conversation', state: 'ready', title: 'What is a moved block?', project: 'net', workspace: 'default', here: true, startedAt: at, ...extra });

let posts: { url: string; body: unknown }[] = [];
let gets: string[] = [];
function serve(settings: object = {}, conversations: object[] = [], current: () => object = () => chat([])) {
  posts = [];
  gets = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    else gets.push(url);
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: true, strictLocal: false, handoff: true, headless: true, disclosure, disclosureAccepted: true, providers: [claude, codex], defaultProvider: 'claude', ...settings });
    if (url.endsWith('/ai/packages')) return Response.json(pkg);
    if (url.endsWith('/ai/conversations') && init?.method === 'POST') return Response.json(chat([{ role: 'user', text: 'hi', at }], { state: 'running' }), { status: 202 });
    if (url.endsWith('/ai/conversations')) return Response.json({ conversations });
    if (url.endsWith(`/ai/conversations/${id}/context`)) return Response.json(chat([]));
    if (url.endsWith(`/ai/conversations/${id}`)) return Response.json(current());
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function show(scope: PlanScope = { kind: 'launch' }) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><AssistantPage scope={scope} project="net" workspace="default" /></QueryClientProvider>);
}
const started = () => posts.filter(p => p.url.endsWith('/ai/conversations'));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('AI Assistant page', () => {
  it('says AI is off and sends nothing', async () => {
    serve({ enabled: false });
    show();
    expect(await screen.findByText('AI assistance is off')).toBeInTheDocument();
    expect(gets.some(u => u.includes('/ai/conversations'))).toBe(false);
  });

  it('starts a conversation without project data', async () => {
    serve();
    const user = userEvent.setup();
    show();
    expect(await screen.findByText(/Project/)).toHaveTextContent('Project net · workspace default, from the context selector.');
    const start = screen.getByRole('button', { name: 'Start conversation' });
    expect(start).toBeDisabled();
    await user.type(screen.getByLabelText('Message'), 'What is a moved block?');
    await user.click(start);
    expect(started()).toEqual([{ url: '/api/v1/ai/conversations', body: { provider: 'claude', disclosureVersion: '5', message: 'What is a moved block?' } }]);
    expect(posts.some(p => p.url.endsWith('/ai/packages'))).toBe(false);
    expect(await screen.findByRole('region', { name: 'Conversation' })).toBeInTheDocument();
  });

  it('lists handoff-only assistants as unavailable and explains that the assistant is fixed per conversation', async () => {
    serve();
    show();
    const select = await screen.findByLabelText('Assistant');
    const options = within(select).getAllByRole('option') as HTMLOptionElement[];
    expect(options.map(o => [o.textContent, o.disabled])).toEqual([['Claude Code', false], ['Codex CLI: handoff-only, copy or download instead', true]]);
    expect(screen.getByText(/Changing it here affects only new conversations/)).toBeInTheDocument();
  });

  it('attaches project context only after the preview is approved (Hub scope)', async () => {
    serve();
    const user = userEvent.setup();
    const ctx = '3'.repeat(32);
    show({ kind: 'context', contextId: ctx });
    await user.click(await screen.findByLabelText(/Attach project context: preview the package first/));
    await user.type(screen.getByLabelText('Message (optional)'), 'Summarize');
    await user.click(screen.getByRole('button', { name: 'Preview project context' }));
    const text = await screen.findByLabelText('Text to share: context package');
    expect(text.textContent).toBe(markdown);
    expect(posts).toEqual([{ url: `/api/v1/contexts/${ctx}/ai/packages`, body: { kind: 'project' } }]);
    await user.click(screen.getByRole('button', { name: 'Start conversation with this context' }));
    expect(started()).toEqual([{ url: `/api/v1/contexts/${ctx}/ai/conversations`, body: { provider: 'claude', disclosureVersion: '5', message: 'Summarize', packageSha256: hash } }]);
  });

  it('attaches project context to an open conversation as a turn', async () => {
    let turns: object[] = [{ role: 'user', text: 'What is a moved block?', at }, { role: 'assistant', text: 'A refactor.', at }];
    serve({}, [listed()], () => chat(turns));
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: /What is a moved block\?/ }));
    await screen.findByText('A refactor.');
    turns = [...turns, { role: 'context', text: '', at, sha256: hash }];
    await user.click(screen.getByRole('button', { name: 'Attach project context' }));
    await screen.findByLabelText('Text to share: context package');
    expect(posts.some(p => p.url.endsWith('/context'))).toBe(false);
    await user.click(screen.getByRole('button', { name: 'Attach to conversation' }));
    expect(posts.find(p => p.url.endsWith('/context'))).toEqual({ url: `/api/v1/ai/conversations/${id}/context`, body: { packageSha256: hash } });
    // The open conversation shows the attached turn without waiting for a poll.
    expect(await screen.findByText(/Project context attached \(hash/)).toBeInTheDocument();
  });

  it('offers re-attaching project context when the files changed', async () => {
    serve({}, [listed({ kind: 'project' })], () => chat([{ role: 'user', text: 'What is a moved block?', at }, { role: 'assistant', text: 'A refactor.', at }], { kind: 'project', stale: true }));
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: /What is a moved block\?/ }));
    expect(await screen.findByText(/attach the current project context to continue/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Attach project context' }));
    await user.click(await screen.findByRole('button', { name: 'Attach to conversation' }));
    expect(posts.find(p => p.url.endsWith('/context'))).toEqual({ url: `/api/v1/ai/conversations/${id}/context`, body: { packageSha256: hash } });
  });

  it('shows another workspace\'s conversation read-only', async () => {
    serve({}, [listed({ id: other, here: false, project: 'app', workspace: 'prod', title: 'Elsewhere' })]);
    const user = userEvent.setup();
    show();
    expect(await screen.findByText('Belongs to app / prod')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Elsewhere/ }));
    expect(screen.getByText(/This conversation belongs to app \/ prod/)).toBeInTheDocument();
    expect(screen.queryByLabelText('Follow-up message')).toBeNull();
    expect(gets.some(u => u.endsWith(`/ai/conversations/${other}`))).toBe(false);
  });
});
