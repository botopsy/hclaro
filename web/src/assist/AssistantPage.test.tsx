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
let blockedPackage = false;
let costRefused = false;
let gets: string[] = [];
let savedList: object[] | undefined;
function serve(settings: object = {}, conversations: object[] = [], current: () => object = () => chat([])) {
  posts = [];
  gets = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    else gets.push(url);
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: true, strictLocal: false, handoff: true, headless: true, disclosure, disclosureAccepted: true, providers: [claude, codex], defaultProvider: 'claude', ...settings });
    if (url.endsWith('/ai/packages')) {
      const req = JSON.parse(String(init?.body)) as { action?: string; cost?: boolean };
      if (costRefused && req.cost) return Response.json({ error: 'cost_unavailable', reason: 'There is no current cost estimate in this session to attach.' }, { status: 409 });
      if (blockedPackage) return Response.json({ error: 'package_blocked', reason: 'Some values still look secret.', blockedFields: ['files[0]'] }, { status: 422 });
      // A quick action's preview is its first turn: the prompt and the package (AI-01.10).
      return Response.json(req.action ? { ...pkg, prompt: req.action, review: `FIRST TURN ${req.action}${req.cost ? ' + cost' : ''}`, costAvailable: req.action === 'qa-cost@1', costHidden: req.cost ? 1 : undefined } : pkg);
    }
    if (url.includes('/ai/saved/') && init?.method === 'POST') return Response.json(url.endsWith('/open') ? chat([{ role: 'user', text: 'saved question', at }], { id: other, saved: true }) : { deleted: 1 });
    if (url.endsWith('/ai/saved') && savedList) return Response.json({ conversations: savedList, available: true, retentionDays: 30 });
    if (url.endsWith('/ai/conversations') && init?.method === 'POST') return Response.json(chat([{ role: 'user', text: 'hi', at }], { state: 'running' }), { status: 202 });
    if (url.endsWith('/ai/conversations')) return Response.json({ conversations });
    if (url.endsWith(`/ai/conversations/${id}/context`)) return Response.json(chat([]));
    if (url.endsWith(`/ai/conversations/${id}`)) return Response.json(current());
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function show(scope: PlanScope = { kind: 'launch' }, noProject = false) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><AssistantPage scope={scope} project="net" workspace="default" noProject={noProject} /></QueryClientProvider>);
}
const started = () => posts.filter(p => p.url.endsWith('/ai/conversations'));

afterEach(() => {
  savedList = undefined;
  blockedPackage = false;
  costRefused = false;
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

  it('uses the assistant chosen in Settings, fixed per conversation', async () => {
    serve();
    show();
    expect(await screen.findByText(/^Assistant:/)).toHaveTextContent('Assistant: Claude Code. Change it in Settings.');
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByText(/Changing it in Settings affects only new conversations/)).toBeInTheDocument();
  });

  it('opening the page asks for detection once, only if it has not run (AI-01.14)', async () => {
    serve({ checked: false, headless: false, providers: [] });
    show();
    await screen.findByText(/^Assistant:/);
    await vi.waitFor(() => expect(posts.map(p => p.url)).toEqual(['/api/v1/ai/detect']));
    cleanup();
    serve({ checked: true });
    show();
    await screen.findByText(/^Assistant:/);
    expect(posts).toEqual([]);
  });

  it('a chosen copy-only assistant offers no Send', async () => {
    serve({ headless: false, providers: [{ ...claude, mode: 'unsupported', reason: 'cli_missing' }, codex], defaultProvider: 'codex' });
    show();
    expect(await screen.findByText(/^Assistant:/)).toHaveTextContent('Assistant: Codex CLI · copy or download only.');
    expect(screen.getByRole('button', { name: 'Start conversation' })).toBeDisabled();
  });

  it('shows the disclosure inline and gates the first message behind Accept and send', async () => {
    serve({ disclosureAccepted: false });
    const user = userEvent.setup();
    show();
    expect(within(await screen.findByLabelText('Before you send')).getByText(disclosure.lines[0])).toBeInTheDocument();
    await user.type(screen.getByLabelText('Message'), 'hi');
    await user.click(screen.getByRole('button', { name: 'Accept and send' }));
    expect(posts.map(p => p.url)).toEqual(['/api/v1/ai/settings', '/api/v1/ai/conversations']);
    expect(posts[0].body).toEqual({ acceptDisclosure: '5' });
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

  const actions = [['Describe project', 'qa-describe@1'], ['Architecture review', 'qa-architecture@1'], ['Security review', 'qa-security@2'], ['Cost analysis', 'qa-cost@1'], ['Find issues', 'qa-issues@2'], ['Suggest improvements', 'qa-improve@2']];

  it.each(actions)('%s previews prompt and package, then starts with that prompt', async (label, prompt) => {
    serve();
    const user = userEvent.setup();
    show();
    await user.type(await screen.findByLabelText('Message'), 'typed text stays');
    await user.click(screen.getByRole('button', { name: label }));
    // The preview shows the first turn (prompt + package); nothing is sent yet.
    expect((await screen.findByLabelText('Text to share: review prompt and context package')).textContent).toBe(`FIRST TURN ${prompt}`);
    expect(screen.getByText('Quick action prompt')).toBeInTheDocument();
    expect(posts).toEqual([{ url: '/api/v1/ai/packages', body: { kind: 'project', action: prompt } }]);
    expect(started()).toEqual([]);
    // The prompt is only in the preview, so no Copy or Download of it.
    expect(screen.queryByRole('button', { name: /Copy/ })).toBeNull();
    await user.click(screen.getByRole('button', { name: `Start ${label.toLowerCase()}` }));
    expect(started()).toEqual([{ url: '/api/v1/ai/conversations', body: { provider: 'claude', disclosureVersion: '5', prompt, packageSha256: hash } }]);
    expect(await screen.findByLabelText('Follow-up message')).toBeInTheDocument();
  });

  it('attaches the existing cost estimate only when the user ticks it', async () => {
    serve();
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: 'Cost analysis' }));
    const attach = await screen.findByLabelText(/Attach this session's latest Infracost estimate/);
    expect(attach).not.toBeChecked();
    expect(attach.closest('label')).toHaveTextContent(/totals, coverage, per-type counts and monthly change, and caveats\. Individual cost line items are never shared\. .*does not start a new Infracost run/);
    expect(screen.queryByText(/cost summary is incomplete/)).toBeNull();
    await user.click(attach);
    expect((await screen.findByLabelText('Text to share: review prompt and context package')).textContent).toBe('FIRST TURN qa-cost@1 + cost');
    // A hidden line is called out: the attached summary is incomplete.
    expect(screen.getByRole('alert')).toHaveTextContent('1 cost line was hidden because it looked secret or local. The attached cost summary is incomplete');
    expect(posts.filter(p => p.url.endsWith('/ai/packages')).map(p => p.body)).toEqual([{ kind: 'project', action: 'qa-cost@1' }, { kind: 'project', action: 'qa-cost@1', cost: true }]);
    await user.click(screen.getByRole('button', { name: 'Start cost analysis' }));
    expect(started()[0].body).toEqual({ provider: 'claude', disclosureVersion: '5', prompt: 'qa-cost@1', packageSha256: hash });
    // Other actions never offer cost data.
    await user.click(screen.getByRole('button', { name: 'New conversation' }));
    await user.click(screen.getByRole('button', { name: 'Security review' }));
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(screen.queryByLabelText(/existing cost estimate/)).toBeNull();
  });

  it.each([
    ['no project', {}, true, /Open a project to use quick actions/],
    ['handoff-only assistant', { providers: [codex] }, false, /need an assistant Hclaro can run/],
    ['strict local-only', { headless: false }, false, /need an assistant Hclaro can run/]
  ])('disables quick actions with a reason: %s', async (_, settings, noProject, reason) => {
    serve(settings);
    show({ kind: 'launch' }, noProject);
    expect(await screen.findByText(reason)).toBeInTheDocument();
    for (const [label] of actions) expect(screen.getByRole('button', { name: label })).toBeDisabled();
  });

  it('shows a blocked package and offers no start', async () => {
    serve();
    blockedPackage = true;
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: 'Find issues' }));
    expect(await screen.findByText('Sharing blocked')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start find issues' })).toBeNull();
    expect(started()).toEqual([]);
  });

  it('lets the user continue without cost data when attaching is refused', async () => {
    serve();
    costRefused = true;
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: 'Cost analysis' }));
    await user.click(await screen.findByLabelText(/Attach this session's latest Infracost estimate/));
    expect(await screen.findByText(/no current cost estimate/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start cost analysis' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Continue without cost data' }));
    expect((await screen.findByLabelText('Text to share: review prompt and context package')).textContent).toBe('FIRST TURN qa-cost@1');
    expect(screen.getByLabelText(/Attach this session's latest Infracost estimate/)).not.toBeChecked();
    expect(started()).toEqual([]);
  });

  it('resets the cost checkbox when the action is opened again', async () => {
    serve();
    const user = userEvent.setup();
    show();
    await user.click(await screen.findByRole('button', { name: 'Cost analysis' }));
    await user.click(await screen.findByLabelText(/latest Infracost estimate/));
    await screen.findByText(/cost summary is incomplete/);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: 'Cost analysis' }));
    expect(await screen.findByLabelText(/latest Infracost estimate/)).not.toBeChecked();
    expect(posts.filter(p => p.url.endsWith('/ai/packages')).map(p => p.body).at(-1)).toEqual({ kind: 'project', action: 'qa-cost@1' });
  });

  // AI-01.11: saved conversations of this project are listed; Open reopens one by ID, Delete
  // removes its file, and Delete all asks first.
  it('lists, reopens and deletes saved conversations', async () => {
    savedList = [{ id: other, title: 'saved question', provider: 'claude', providerVersion: '2.1.327', kind: 'conversation', workspace: 'default', createdAt: at, lastActivity: at, open: false }];
    serve({}, [], () => chat([{ role: 'user', text: 'saved question', at }], { id: other, saved: true }));
    const user = userEvent.setup();
    show();
    const section = await screen.findByRole('region', { name: 'Saved conversations of this project' });
    expect(within(section).getByText('saved question')).toBeInTheDocument();
    expect(within(section).getByText(/Claude Code 2\.1\.327 · workspace default/)).toBeInTheDocument();
    await user.click(within(section).getByRole('button', { name: 'Open' }));
    expect(posts.map(p => p.url)).toContain(`/api/v1/ai/saved/${other}/open`);
    await user.click(within(section).getByRole('button', { name: 'Delete saved conversation saved question' }));
    expect(posts.map(p => p.url)).toContain(`/api/v1/ai/saved/${other}/delete`);
    await user.click(within(section).getByRole('button', { name: /Delete all saved for this project/ }));
    expect(posts.filter(p => p.url.endsWith('/delete-all'))).toHaveLength(0);
    await user.click(within(section).getByRole('button', { name: 'Delete all' }));
    expect(posts.filter(p => p.url.endsWith('/ai/saved/delete-all'))).toHaveLength(1);
  });

  it('lists Hub saved conversations under the context scope', async () => {
    savedList = [];
    serve();
    show({ kind: 'context', contextId: '3'.repeat(32) });
    expect(await screen.findByText(/No other saved conversations/)).toBeInTheDocument();
    expect(gets).toContain(`/api/v1/contexts/${'3'.repeat(32)}/ai/saved`);
  });
});
