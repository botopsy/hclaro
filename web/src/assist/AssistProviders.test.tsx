import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AssistDrawer } from './AskAI';
import type { AssistProvider } from '../types';
import { SettingsPanel } from '../workbench/SettingsPanel';
import type { PlanScope } from '../lib/scope';

// AI-01.05, AI-01.14: the workflow does not need Claude Code. Copy-only assistants get their own
// Copy action and are never run. Fake server only.
const hash = 'f'.repeat(64);
const markdown = '# Hclaro context: plan\n';
const disclosure = { version: '3', lines: ['Nothing is shared without your click.'] };
const review = '# Hclaro review request (review-plan@1)\n\n' + markdown + '\n## Review request\n\nReview this plan.\n';
const pkg = { kind: 'plan', markdown, sha256: hash, sections: ['context'], dropped: [], disclosure, prompt: 'review-plan@1', review };
const claudeMissing: AssistProvider = { id: 'claude', name: 'Claude Code', mode: 'unsupported', send: false, usable: false, installed: false, reason: 'cli_missing', message: 'The assistant\'s executable was not found on PATH.' };
const claude: AssistProvider = { id: 'claude', name: 'Claude Code', mode: 'headless-eligible', send: true, usable: true, installed: true, version: '2.1.326', path: '/synthetic/bin/claude' };
// Codex is copy-only (AI-01.14 review): Hclaro cannot verify that all its tools are off.
const codex: AssistProvider = { id: 'codex', name: 'OpenAI Codex', mode: 'handoff-only', send: false, usable: true, installed: true, version: '0.166.0', path: '/synthetic/bin/codex', reason: 'not_headless', message: 'This assistant is handoff-only: Codex turns tools on by default.' };

let posts: string[] = [];
let bodies: unknown[] = [];
function serve(providers: AssistProvider[], extra: object = {}) {
  posts = [];
  bodies = [];
  const headless = providers.some(p => p.send);
  const first = providers.find(p => p.send) ?? providers.find(p => p.usable);
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      posts.push(url);
      bodies.push(JSON.parse(String(init.body)));
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: true, strictLocal: false, handoff: true, headless, disclosure, disclosureAccepted: true, checked: true, providers, defaultProvider: first?.id, ...extra });
    if (url.endsWith('/ai/packages')) return Response.json(pkg);
    if (url.endsWith(`/ai/packages/${hash}/export`)) return Response.json({ kind: 'plan', sha256: hash, markdown, prompt: 'review-plan@1', review, filename: `hclaro-plan-${hash.slice(0, 12)}.md` });
    if (url.endsWith('/ai/conversations')) return Response.json({ id: '1'.repeat(32), state: 'running', turns: [] }, { status: 202 });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
const client = () => new QueryClient({ defaultOptions: { queries: { retry: false } } });
function show(scope: PlanScope) {
  render(<QueryClientProvider client={client()}><AssistDrawer scope={scope} request={{ kind: 'plan', planId: 'a'.repeat(32) }} title="Ask AI" onClose={() => {}} /></QueryClientProvider>);
}
const actions = () => within(screen.getByLabelText('AI context preview')).getAllByRole('button').map(b => b.getAttribute('aria-label') ?? b.textContent);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const scopes: [string, PlanScope, string][] = [['single project', { kind: 'launch' }, '/api/v1'], ['Hub', { kind: 'context', contextId: '1'.repeat(32) }, `/api/v1/contexts/${'1'.repeat(32)}`]];

describe.each(scopes)('AI handoff without Claude Code (%s)', (_, scope, prefix) => {
  it.each([
    ['Codex copy-only', [claudeMissing, codex], ['Copy for OpenAI Codex']],
    ['no assistant', [], ['Copy']]
  ] as [string, AssistProvider[], string[]][])('%s: preview, provider Copy and Download, no Send', async (_, providers, copies) => {
    serve(providers);
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, 'writeText');
    show(scope);
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(actions()).toEqual(['Close AI context preview', 'Cancel', ...copies, 'Download .md']);
    expect(screen.queryByText(/Claude/)).toBeNull();
    expect(posts).toEqual([`${prefix}/ai/packages`]);
    await user.click(screen.getByRole('button', { name: copies[copies.length - 1] }));
    expect(await screen.findByText(/protections end here/)).toBeInTheDocument();
    // The handoff carries the review prompt too, so the user gets the same review elsewhere.
    expect(write).toHaveBeenCalledWith(review);
    // Handoff is an export of the previewed hash only: no run request starts an assistant.
    expect(posts).toEqual([`${prefix}/ai/packages`, `${prefix}/ai/packages/${hash}/export`]);
    if (providers.length) {
      const name = providers[providers.length - 1].name;
      expect(screen.getByText(new RegExp(`copied for ${name}\\. Paste it into ${name} yourself: Hclaro did not open or run it`))).toBeInTheDocument();
      expect(screen.getByText(/handoff-only: copy or download the context/)).toBeInTheDocument();
    }
  });

  it('Claude installed: Review with Claude Code stays, next to Copy for Codex', async () => {
    serve([claude, codex]);
    const user = userEvent.setup();
    show(scope);
    await screen.findByLabelText('Text to share: review prompt and context package');
    expect(actions()).toEqual(['Close AI context preview', 'Cancel', 'Copy for OpenAI Codex', 'Download .md', 'Send']);
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(posts).toEqual([`${prefix}/ai/packages`, `${prefix}/ai/conversations`]);
  });
});

const settingsPanel = () => render(<QueryClientProvider client={client()}><SettingsPanel preferences={{ theme: 'system', density: 'comfortable', grouping: 'module', editor: 'vscode', sidebar: 'expanded' }} changePreference={() => {}} /></QueryClientProvider>);
const aiRow = () => screen.getByRole('heading', { name: 'AI assistance' }).closest('.setting-row') as HTMLElement;

// AI-01.14: one sentence, a switch and a two-assistant picker; the rest waits in Details.
describe('Settings AI row', () => {
  it('on by default: one sentence, the picker and collapsed Details', async () => {
    serve([claude, codex]);
    settingsPanel();
    const picker = await screen.findByLabelText('Assistant') as HTMLSelectElement;
    expect(within(picker).getAllByRole('option').map(o => [o.textContent, (o as HTMLOptionElement).disabled])).toEqual([['Claude Code', false], ['OpenAI Codex (copy only)', false]]);
    expect(picker.value).toBe('claude');
    expect(within(aiRow()).getAllByRole('paragraph').map(p => p.textContent)[0]).toBe('Ask your AI assistant about plans and code. Nothing is shared until you click Send or Copy.');
    expect(within(aiRow()).getByRole('button', { name: 'On' })).toHaveAttribute('aria-pressed', 'true');
    // No disclosure list, provider list or Accept button in Settings any more.
    expect(screen.queryByText(disclosure.lines[0])).toBeNull();
    expect(screen.queryByRole('button', { name: /Accept/ })).toBeNull();
    expect(screen.getByText('/synthetic/bin/claude')).not.toBeVisible();
    expect(within(screen.getByRole('list', { name: 'Assistants' })).getAllByRole('listitem').map(li => li.textContent)).toEqual([
      'Claude Code · Send and copy · 2.1.326 · /synthetic/bin/claude', 'OpenAI Codex · Copy only · 0.166.0 · /synthetic/bin/codex · This assistant is handoff-only: Codex turns tools on by default.']);
  });

  it('marks a missing assistant disabled and an unverified one copy only, with the reason in Details', async () => {
    serve([claudeMissing, codex]);
    settingsPanel();
    const picker = await screen.findByLabelText('Assistant');
    expect(within(picker).getAllByRole('option').map(o => [o.textContent, (o as HTMLOptionElement).disabled])).toEqual([['Claude Code (not installed)', true], ['OpenAI Codex (copy only)', false]]);
    const rows = within(screen.getByRole('list', { name: 'Assistants' })).getAllByRole('listitem').map(li => li.textContent);
    expect(rows).toEqual(['Claude Code · Not installed', 'OpenAI Codex · Copy only · 0.166.0 · /synthetic/bin/codex · This assistant is handoff-only: Codex turns tools on by default.']);
    expect(screen.queryByText(/Anthropic/)).toBeNull();
  });

  it('picking Codex saves the choice', async () => {
    serve([claude, codex]);
    const user = userEvent.setup();
    settingsPanel();
    await user.selectOptions(await screen.findByLabelText('Assistant'), 'codex');
    expect(posts).toEqual(['/api/v1/ai/settings']);
    expect(bodies).toEqual([{ defaultProvider: 'codex' }]);
  });

  it.each([
    ['checking', { checking: true, providers: [] }, 'Checking…'],
    ['strict local-only', { strictLocal: true, headless: false, providers: [] }, 'Strict local-only: copy and download only.'],
    ['unreadable saved settings', { warning: 'Saved AI settings could not be read, so the defaults apply.' }, 'Saved AI settings could not be read, so the defaults apply.']
  ])('%s', async (_, extra, text) => {
    serve([claude], extra);
    settingsPanel();
    expect(await screen.findByText(text)).toBeInTheDocument();
    if (!('warning' in extra)) expect(screen.queryByLabelText('Assistant')).toBeNull();
  });

  it('starts nothing by itself: assistants are checked only on request', async () => {
    serve([], { checked: false, headless: false });
    const user = userEvent.setup();
    settingsPanel();
    const check = await screen.findByRole('button', { name: 'Check now' });
    expect(posts).toEqual([]);
    expect(screen.queryByLabelText('Assistant')).toBeNull();
    await user.click(check);
    expect(posts).toEqual(['/api/v1/ai/detect']);
  });

  it('off shows only the title, the sentence and the switch', async () => {
    serve([claude, codex], { enabled: false, handoff: false, headless: false, providers: [] });
    settingsPanel();
    await screen.findByRole('button', { name: 'Off', pressed: true });
    expect(within(aiRow()).getAllByRole('paragraph')).toHaveLength(1);
    expect(screen.queryByLabelText('Assistant')).toBeNull();
    expect(screen.queryByText('Details')).toBeNull();
  });
});
