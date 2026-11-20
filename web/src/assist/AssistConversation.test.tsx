import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AssistDrawer } from './AskAI';
import type { PlanScope } from '../plans/routes';

// AI-01.04 / AI-01.07 / AI-01.08: Review with <assistant> starts a conversation; replies are untrusted and inert; follow-ups,
// cancel, failure and close. Fake server only.
const hash = 'f'.repeat(64);
const id = '1'.repeat(32);
const markdown = '# Hclaro context: plan\n';
const disclosure = { version: '4', lines: ['Every follow-up resends the whole conversation.'] };
const pkg = { kind: 'plan', markdown, sha256: hash, sections: ['context'], dropped: [], disclosure, prompt: 'review-plan@1', review: 'Review this plan.\n' + markdown };
const provider = { id: 'claude', name: 'Claude Code', mode: 'headless-eligible', send: true, usable: true, installed: true, version: '2.1.326', path: '/synthetic/bin/claude' };
const reply = 'Run `terraform apply -auto-approve` and rm -rf / <a href="javascript:alert(1)">click</a> <script>window.pwned=1</script> [x](javascript:alert(1))\nresource "aws_s3_bucket" "b" {}';
type Turn = { role: string; text: string; at: string; prompt?: string };
const at = '2026-11-18T09:00:00Z';
const chat = (state: string, turns: Turn[], extra = {}) => ({ id, provider: 'claude', kind: 'plan', packageSha256: hash, state, turns, usedBytes: 2048, limitBytes: 262144, startedAt: at, ...extra });
const first = [{ role: 'user', text: 'Review this plan.', at, prompt: 'review-plan@1' }, { role: 'assistant', text: reply, at }];

let posts: { url: string; body: unknown }[] = [];
function serve(settings: object, current: () => object, answers: Record<string, () => Response> = {}) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body)) });
    for (const [suffix, answer] of Object.entries(answers)) if (url.endsWith(suffix)) return answer();
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: true, strictLocal: false, handoff: true, headless: true, disclosure, disclosureAccepted: true, providers: [provider], defaultProvider: 'claude', maxMessage: 8000, ...settings });
    if (url.endsWith('/ai/packages')) return Response.json(pkg);
    if (url.endsWith('/ai/conversations')) return Response.json(chat('running', [{ role: 'user', text: '', at }]), { status: 202 });
    if (url.endsWith(`/ai/conversations/${id}/messages`)) return Response.json(chat('running', first), { status: 202 });
    if (url.endsWith(`/ai/conversations/${id}/cancel`)) return Response.json({ cancelled: true });
    if (url.endsWith(`/ai/conversations/${id}/close`)) return Response.json({ closed: true });
    if (url.endsWith(`/ai/conversations/${id}/save`)) return Response.json(current());
    if (url.endsWith(`/ai/conversations/${id}`)) return Response.json(current());
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function show(scope: PlanScope = { kind: 'launch' }) {
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><AssistDrawer scope={scope} request={{ kind: 'plan', planId: 'a'.repeat(32) }} title="Ask AI" onClose={() => {}} /></QueryClientProvider>);
}
const started = () => posts.filter(p => p.url.endsWith('/ai/conversations'));

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('AI conversation', () => {
  it('starts only on click with the previewed hash and renders replies inert', async () => {
    serve({}, () => chat('ready', first));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(started()).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    const text = await screen.findByLabelText('Assistant reply text');
    expect(started()).toEqual([{ url: '/api/v1/ai/conversations', body: { packageSha256: hash, provider: 'claude', disclosureVersion: '4', prompt: 'review-plan@1' } }]);
    expect(screen.getByText(/Asked for a review of the context package/)).toBeInTheDocument();
    expect(text.textContent).toBe(reply);
    const panel = screen.getByRole('region', { name: 'Conversation' });
    expect(within(panel).queryAllByRole('link')).toHaveLength(0);
    expect(panel.querySelector('script, a, iframe')).toBeNull();
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined();
    expect(within(panel).getByText(/AI output is unverified/)).toBeInTheDocument();
    expect(within(panel).getByText(/Every message resends the whole conversation/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /apply|approve/i })).toBeNull();
    // Only conversation controls: no command buttons derived from the reply.
    expect(within(panel).getAllByRole('button').map(b => b.textContent)).toEqual(['Close conversation', 'Send message']);
    expect(screen.getByRole('button', { name: 'Review with Claude Code' })).toBeDisabled();
  });

  it.each([['single project', { kind: 'launch' }, '/api/v1'], ['Hub', { kind: 'context', contextId: '2'.repeat(32) }, `/api/v1/contexts/${'2'.repeat(32)}`]] as [string, PlanScope, string][])(
    'sends a follow-up in its own scope (%s)', async (_, scope, prefix) => {
      let turns = first;
      serve({}, () => chat('ready', turns));
      const user = userEvent.setup();
      show(scope);
      await screen.findByLabelText('Text to share: review prompt and context package');
      await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
      await screen.findByLabelText('Assistant reply text');
      turns = [...first, { role: 'user', text: 'Which change is riskiest?', at }, { role: 'assistant', text: 'The replacement.', at }];
      await user.type(screen.getByLabelText('Follow-up message'), 'Which change is riskiest?');
      await user.click(screen.getByRole('button', { name: 'Send message' }));
      expect(await screen.findByText('The replacement.')).toBeInTheDocument();
      expect(posts.find(p => p.url.endsWith('/messages'))).toEqual({ url: `${prefix}/ai/conversations/${id}/messages`, body: { text: 'Which change is riskiest?' } });
      expect(screen.getByText('Which change is riskiest?')).toBeInTheDocument();
      expect(screen.getByLabelText('Follow-up message')).toHaveValue('');
    });

  it('cancels a running turn and puts the message back', async () => {
    let state = chat('running', [...first, { role: 'user', text: 'slow one', at }]);
    serve({}, () => state);
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    const panel = await screen.findByRole('region', { name: 'Conversation' });
    // The drawer's own Cancel closes the preview; the turn's Cancel is in the panel.
    state = chat('cancelled', first, { draft: 'slow one', error: { code: 'cancelled', message: 'The request was cancelled.' } });
    await user.click(await within(panel).findByRole('button', { name: 'Cancel' }));
    expect(posts.some(p => p.url.endsWith(`/ai/conversations/${id}/cancel`))).toBe(true);
    expect(await screen.findByText(/The request was cancelled\. Your message is back/)).toBeInTheDocument();
    expect(screen.getByText('slow one', { selector: 'textarea' })).toBeInTheDocument();
  });

  it('keeps earlier turns on a failure and returns the refused message', async () => {
    let state: object = chat('ready', first);
    serve({}, () => state, { [`/ai/conversations/${id}/messages`]: () => Response.json({ error: 'message_blocked', reason: 'This message looks like it contains a secret.' }, { status: 422 }) });
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    await screen.findByLabelText('Assistant reply text');
    await user.type(screen.getByLabelText('Follow-up message'), 'password=hunter2');
    await user.click(screen.getByRole('button', { name: 'Send message' }));
    expect(await screen.findByText('This message looks like it contains a secret.')).toBeInTheDocument();
    // Refused before sending: the text stays for the user to fix.
    expect(screen.getByLabelText('Follow-up message')).toHaveValue('password=hunter2');
    expect(screen.getByLabelText('Assistant reply text')).toBeInTheDocument();
    state = chat('failed', first, { draft: '', error: { code: 'auth_required', message: 'The assistant is not signed in.' } });
    cleanup();
    serve({}, () => state);
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    expect(await screen.findByText(/The assistant is not signed in\./)).toBeInTheDocument();
    expect(screen.getByLabelText('Assistant reply text')).toBeInTheDocument();
  });

  it('refuses an over-long message in the browser and explains the size limit', async () => {
    serve({}, () => chat('ready', first), { [`/ai/conversations/${id}/messages`]: () => Response.json({ error: 'conversation_too_long', reason: 'This conversation reached its size limit (256 KiB, resent in full every turn). Nothing was sent. Start a new conversation from the preview.' }, { status: 413 }) });
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    const box = await screen.findByLabelText('Follow-up message');
    await user.click(box);
    await user.paste('x'.repeat(8001));
    expect(screen.getByText(/longer than 8000 bytes/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled();
    await user.clear(box);
    await user.type(box, 'one more');
    await user.click(screen.getByRole('button', { name: 'Send message' }));
    expect(await screen.findByText(/reached its size limit/)).toBeInTheDocument();
    expect(box).toHaveValue('one more');
  });

  it('closes the conversation and allows a new one', async () => {
    serve({}, () => chat('ready', first));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    await screen.findByLabelText('Assistant reply text');
    await user.click(screen.getByRole('button', { name: 'Close conversation' }));
    expect(posts.some(p => p.url.endsWith(`/ai/conversations/${id}/close`))).toBe(true);
    expect(screen.queryByRole('region', { name: 'Conversation' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Review with Claude Code' })).toBeEnabled();
  });

  it('opening the preview asks for detection, never with AI off or strict local-only (AI-01.14)', async () => {
    serve({ checked: false, headless: false }, () => chat('ready', first));
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(posts.map(p => p.url)).toContain('/api/v1/ai/detect');
    cleanup();
    serve({ checked: false, headless: false, strictLocal: true }, () => chat('ready', first));
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(posts.map(p => p.url)).not.toContain('/api/v1/ai/detect');
  });

  it('accepts the disclosure inline on the first Send, and offers no Send in strict local-only mode', async () => {
    serve({ disclosureAccepted: false }, () => chat('ready', first));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(screen.queryByRole('button', { name: 'Review with Claude Code' })).toBeNull();
    expect(within(screen.getByLabelText('Before you share')).getByText(disclosure.lines[0])).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Accept and send' }));
    const sent = posts.filter(p => !p.url.endsWith('/ai/packages'));
    expect(sent.map(p => p.url)).toEqual(['/api/v1/ai/settings', '/api/v1/ai/conversations']);
    expect(sent[0].body).toEqual({ acceptDisclosure: '4' });
    // The request names the assistant shown, so the server refuses if its choice changed.
    expect(sent[1].body).toMatchObject({ provider: 'claude', disclosureVersion: '4' });
    cleanup();

    serve({ strictLocal: true, headless: false }, () => chat('ready', first));
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(screen.queryByRole('button', { name: /Review with/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument();
  });

  it('shows a stale-context banner and refuses new messages', async () => {
    serve({}, () => chat('ready', first, { stale: true }));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    expect(await screen.findByText(/Context is stale/)).toBeInTheDocument();
    expect(screen.getByLabelText('Follow-up message')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled();
    expect(posts.filter(p => p.url.endsWith('/messages'))).toHaveLength(0);
  });

  // Found in real-browser verification: the panel only polls while a turn runs, so a context that
  // went stale while idle surfaced as a refusal but left the input enabled and no banner.
  it('shows the stale banner after a context_changed refusal', async () => {
    let stale = false;
    serve({}, () => chat('ready', first, { stale }), {
      [`/ai/conversations/${id}/messages`]: () => { stale = true; return Response.json({ error: 'context_changed', reason: 'The workspace changed since this conversation started.' }, { status: 409 }); }
    });
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    await screen.findByLabelText('Assistant reply text');
    await user.type(screen.getByLabelText('Follow-up message'), 'Still safe?');
    await user.click(screen.getByRole('button', { name: 'Send message' }));
    expect(await screen.findByText(/Context is stale/)).toBeInTheDocument();
    expect(screen.getByText('The workspace changed since this conversation started.')).toBeInTheDocument();
    expect(screen.getByLabelText('Follow-up message')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled();
  });

  // AI-01.11: Save is opt-in per conversation; a reopened read-only conversation says why and
  // sends nothing; a hidden turn shows no text.
  it('saves only on request', async () => {
    let saved = false;
    serve({}, () => chat('ready', first, { saved }));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    const box = await screen.findByRole('checkbox', { name: /Save conversation/ });
    expect(box).not.toBeChecked();
    expect(posts.filter(p => p.url.endsWith('/save'))).toHaveLength(0);
    saved = true;
    await user.click(box);
    expect(posts.filter(p => p.url.endsWith('/save')).map(p => p.body)).toEqual([{ saved: true }]);
    expect(await screen.findByRole('checkbox', { name: /Save conversation/ })).toBeChecked();
  });

  it('shows a read-only reopened conversation and hides flagged turns', async () => {
    const turns = [{ role: 'user', text: 'hello', at }, { role: 'assistant', text: '', at, hidden: true }];
    serve({}, () => chat('ready', turns, { saved: true, needsReview: true, readOnly: 'Some saved text now looks like a secret.' }));
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    expect(await screen.findByText(/Read-only: Some saved text now looks like a secret/)).toBeInTheDocument();
    expect(screen.getByText(/Hidden: this saved message now looks like/)).toBeInTheDocument();
    expect(screen.queryByLabelText('Assistant reply text')).toBeNull();
    expect(screen.getByLabelText('Follow-up message')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Send message' })).toBeDisabled();
  });
  it('shows Hclaro-computed proposals and applies or rejects each one on its own', async () => {
    const proposed = 'Here is a fix.\n```hclaro-change path=main.tf base=' + 'a'.repeat(64) + '\nlocals {}\n```\n```hclaro-change path=../x.tf base=new\nlocals {}\n```\n';
    const turns = [first[0], { role: 'assistant', text: proposed, at }];
    const ready = { turn: 1, index: 0, path: 'main.tf', status: 'ready', created: false, baseSha256: 'a'.repeat(64), newSha256: 'b'.repeat(64), diff: '--- a/main.tf\n+++ b/main.tf\n@@ -1,1 +1,1 @@\n-locals { a = 1 }\n+locals {}\n', added: 1, removed: 1 };
    const bad = { turn: 1, index: 1, path: '../x.tf', status: 'invalid', code: 'change_path', reason: 'Hclaro writes only .tf and .tofu files inside the project folder.', created: true, added: 0, removed: 0 };
    serve({}, () => chat('ready', turns), {
      [`/ai/conversations/${id}/changes`]: () => Response.json({ changes: [ready, bad] }),
      [`/ai/conversations/${id}/changes/apply`]: () => Response.json({ ...ready, status: 'applied', canUndo: true })
    });
    const user = userEvent.setup();
    show();
    await screen.findByLabelText('Text to share: review prompt and context package');
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    const list = await screen.findByRole('list', { name: 'Proposed changes' });
    expect(within(list).getByLabelText('Diff computed by Hclaro').textContent).toContain('+locals {}');
    expect(within(list).getByText(/Cannot be applied: Hclaro writes only/)).toBeInTheDocument();
    // One Apply per valid proposal, no "apply all"; nothing is posted before the click.
    expect(within(list).getAllByRole('button', { name: /^Apply/ })).toHaveLength(1);
    expect(screen.queryByRole('button', { name: /apply all/i })).toBeNull();
    expect(posts.filter(p => p.url.includes('/changes/'))).toHaveLength(0);
    await user.click(within(list).getByRole('button', { name: 'Apply to main.tf' }));
    expect(posts.filter(p => p.url.includes('/changes/'))).toEqual([{ url: `/api/v1/ai/conversations/${id}/changes/apply`, body: { turn: 1, index: 0, baseSha256: 'a'.repeat(64), newSha256: 'b'.repeat(64) } }]);
    expect(await within(list).findByText(/Nothing was run: run a plan to review the effect/)).toBeInTheDocument();
    expect(within(list).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
  });
});
