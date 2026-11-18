import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AssistDrawer, type AssistProvider } from './AskAI';
import { SettingsPanel } from '../workbench/SettingsPanel';
import type { PlanScope } from '../plans/routes';

// AI-01.05: the workflow does not need Claude Code. Handoff-only assistants get their own
// Copy action and are never run. Fake server only.
const hash = 'f'.repeat(64);
const markdown = '# Hclaro context: plan\n';
const disclosure = { version: '3', lines: ['Nothing is shared without your click.'] };
const review = '# Hclaro review request (review-plan@1)\n\n' + markdown + '\n## Review request\n\nReview this plan.\n';
const pkg = { kind: 'plan', markdown, sha256: hash, sections: ['context'], dropped: [], disclosure, prompt: 'review-plan@1', review };
const claudeMissing: AssistProvider = { id: 'claude', name: 'Claude Code', mode: 'unsupported', reason: 'cli_missing', message: 'The assistant\'s executable was not found on PATH.' };
const claude: AssistProvider = { id: 'claude', name: 'Claude Code', mode: 'headless-eligible', version: '2.1.326', path: '/synthetic/bin/claude' };
const codex: AssistProvider = { id: 'codex', name: 'OpenAI Codex', mode: 'handoff-only', version: '0.166.0', path: '/synthetic/bin/codex', reason: 'not_headless', message: 'This assistant is handoff-only.' };
const copilot: AssistProvider = { id: 'copilot', name: 'GitHub Copilot CLI', mode: 'handoff-only', version: '1.0.112', path: '/synthetic/bin/copilot', reason: 'not_headless', message: 'This assistant is handoff-only.' };

let posts: string[] = [];
function serve(providers: AssistProvider[]) {
  posts = [];
  const headless = providers.some(p => p.mode === 'headless-eligible');
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push(url);
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/ai/settings')) return Response.json({ enabled: true, strictLocal: false, handoff: true, headless, disclosure, disclosureAccepted: true, providers, defaultProvider: headless ? 'claude' : undefined });
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
    ['Codex only', [claudeMissing, codex], ['Copy for OpenAI Codex']],
    ['Copilot only', [claudeMissing, copilot], ['Copy for GitHub Copilot CLI']],
    ['Codex and Copilot', [claudeMissing, codex, copilot], ['Copy for OpenAI Codex', 'Copy for GitHub Copilot CLI']],
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
    expect(actions()).toEqual(['Close AI context preview', 'Cancel', 'Copy for OpenAI Codex', 'Download .md', 'Review with Claude Code']);
    await user.click(screen.getByRole('button', { name: 'Review with Claude Code' }));
    expect(posts).toEqual([`${prefix}/ai/packages`, `${prefix}/ai/conversations`]);
  });
});

describe('Settings assistants', () => {
  it('separates installed from what Hclaro can do, and never requires Claude Code', async () => {
    serve([claudeMissing, codex, copilot]);
    render(<QueryClientProvider client={client()}><SettingsPanel preferences={{ theme: 'system', density: 'comfortable', grouping: 'module', editor: 'vscode', sidebar: 'expanded' }} changePreference={() => {}} /></QueryClientProvider>);
    const rows = within(await screen.findByRole('list', { name: 'Assistants' })).getAllByRole('listitem').map(li => li.textContent);
    expect(rows[0]).toBe('Claude Code · Not installed');
    expect(rows[1]).toMatch(/^OpenAI Codex · Detected · Handoff only · 0\.166\.0 .*Use OpenAI Codex by copying or downloading Hclaro context\./);
    expect(rows[2]).toMatch(/^GitHub Copilot CLI · Detected · Handoff only .*Use GitHub Copilot CLI by copying or downloading Hclaro context\./);
    // The usage line is shown once; the long reason waits in a collapsed detail.
    expect(screen.getAllByText('Why handoff only?')).toHaveLength(2);
    expect(screen.getAllByText('This assistant is handoff-only.')[0]).not.toBeVisible();
    expect(screen.queryByText(/Anthropic/)).toBeNull();
    expect(screen.queryByLabelText('Default assistant')).toBeNull();
    expect(screen.queryByRole('button', { name: /Accept the disclosure/ })).toBeNull();
  });
});
