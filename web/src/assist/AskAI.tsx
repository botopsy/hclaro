import { useEffect, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Ban, Copy, Download, LoaderCircle, MessageSquareX, Send, Sparkles, Square, TriangleAlert, X } from 'lucide-react';
import { api, ApiError, post } from '../api';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { scopeKey, scopedPath, type PlanScope } from '../plans/routes';
import type { ImportRequest } from '../types';
import { hasProposal, ProposedChanges, useChanges } from './ProposedChanges';

// AI handoff (AI-01.03) and conversations (AI-01.04, AI-01.07): the server builds a sanitized
// context package; the user previews it and only Copy, Download or Review with an assistant hands it over.
// Nothing is shared by opening the preview. "Review with <assistant>" starts a conversation whose
// first turn is the previewed text (review prompt + package, AI-01.08); every message is one
// headless run of the whole conversation. Replies are untrusted text: rendered as plain text
// only, with no links, command buttons or actions.

export type AssistDisclosure = { version: string; lines: string[] };
// send: Hclaro can run it; usable: it can at least be copied to; installed: found on PATH.
export type AssistProvider = { id: string; name: string; mode: string; send: boolean; usable: boolean; installed: boolean; version?: string; path?: string; reason?: string; message?: string };
export type AssistSettings = {
  enabled: boolean; strictLocal: boolean; handoff: boolean; headless: boolean; disclosure: AssistDisclosure;
  disclosureAccepted?: boolean; providers?: AssistProvider[]; defaultProvider?: string;
  // checked/checking: whether the assistants were detected (AI-01.14), which happens only on
  // request; warning: saved settings were unreadable.
  checked?: boolean; checking?: boolean; warning?: string;
  // The server's byte limit for one typed message.
  maxMessage: number;
};
type Turn = { role: 'user' | 'assistant' | 'context'; text: string; at: string; prompt?: string; sha256?: string; hidden?: boolean };
export type Conversation = {
  id: string; provider: string; kind: string; packageSha256: string; state: 'ready' | 'running' | 'failed' | 'cancelled'; stale?: boolean;
  turns: Turn[]; draft?: string; error?: { code: string; message: string }; usedBytes: number; limitBytes: number; startedAt: string;
  // Saved conversations (AI-01.11): the file is kept; readOnly says why a reopened one cannot continue.
  saved: boolean; saveError?: string; readOnly?: string; needsReview?: boolean;
};
// tooLong: the message exceeds the server's limit (it enforces it too).
export const tooLong = (text: string, settings?: AssistSettings) => !!settings && new TextEncoder().encode(text).length > settings.maxMessage;
// action is a quick action prompt (AI-01.10); cost attaches the session's existing cost estimate.
export type AssistRequest = { kind: 'project'; action?: string; cost?: boolean } | { kind: 'plan'; planId: string } | { kind: 'drift'; scanId: string } | { kind: 'resource'; resourceId: string } | { kind: 'import'; import: ImportRequest };
// review is the first turn (AI-01.08): the review prompt and the package, byte for byte what
// Send submits and what Copy and Download hand over.
export type AssistPackage = { kind: string; markdown: string; sha256: string; sections: string[]; dropped: string[]; disclosure: AssistDisclosure; prompt: string; review: string; costAvailable?: boolean; costHidden?: number };
type Exported = { kind: string; sha256: string; markdown: string; filename: string; prompt: string; review: string };

export const assistSettingsQuery = {
  queryKey: ['ai-settings'],
  queryFn: () => api<AssistSettings>('ai/settings'),
  refetchInterval: (q: { state: { data?: AssistSettings } }) => q.state.data?.checking ? 1000 : false
};

// chosenAssistant is the assistant picked in Settings when Hclaro can run it (AI-01.14): it
// drives every Review with, quick action and the AI Assistant page.
export function chosenAssistant(settings?: AssistSettings) {
  return settings?.headless ? settings.providers?.find(p => p.id === settings.defaultProvider && p.send) : undefined;
}

// useDetectAssistants asks the server to detect the assistants (their --version only) when the
// user opens an AI surface and that has not happened yet. Nothing is detected at startup.
export function useDetectAssistants(settings?: AssistSettings) {
  const client = useQueryClient();
  const due = !!settings?.enabled && !settings.strictLocal && settings.checked === false && !settings.checking;
  useEffect(() => {
    if (due) void post<AssistSettings>('ai/detect', {}).then(s => client.setQueryData(assistSettingsQuery.queryKey, s), () => undefined);
  }, [due, client]);
}

// useAcceptThen runs send after accepting the disclosure the user was shown, when this session
// has not accepted it yet: the first Send is "Accept and send" (AI-01.14). The server checks the version.
export function useAcceptThen() {
  const client = useQueryClient();
  return async (settings: AssistSettings, send: () => Promise<void>) => {
    if (!settings.disclosureAccepted) client.setQueryData(assistSettingsQuery.queryKey, await post<AssistSettings>('ai/settings', { acceptDisclosure: settings.disclosure.version }));
    await send();
  };
}

// useAssistEnabled is false until the server says AI is on; entry points render nothing then.
export function useAssistEnabled() {
  return useQuery(assistSettingsQuery).data?.enabled === true;
}

// AskAI is an entry point: a button that opens the preview drawer, or nothing while AI is
// off. With a note it renders as a warning line that carries the button.
export function AskAI({ scope, request, label, note }: { scope?: PlanScope; request: AssistRequest; label: string; note?: ReactNode }) {
  const enabled = useAssistEnabled();
  const [open, setOpen] = useState(false);
  if (!enabled || !scope) return null;
  const button = <Button variant="outline" size="small" onClick={() => setOpen(true)}><Sparkles size={13} />{label}</Button>;
  return <>
   {note ? <p className="plan-warning" role="note"><TriangleAlert size={14} /><span>{note}</span>{button}</p> : <div className="assist-entry">{button}</div>}
   {open && createPortal(<AssistDrawer scope={scope} request={request} title={label} onClose={() => setOpen(false)} />, document.body)}
  </>;
}

function save(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/markdown;charset=utf-8' }));
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.rel = 'noopener';
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// approve replaces "Review with <assistant>" (AI-01.09): the previewed package is attached to a
// new or open conversation only when the user presses it.
export type Approve = { label: string; run: (p: AssistPackage, provider: AssistProvider) => Promise<void> };

// onCost (quick actions) shows the opt-in to attach an existing cost estimate when the server offers one.
export function AssistDrawer({ scope, request, title, onClose, approve, onCost }: { scope: PlanScope; request: AssistRequest; title: string; onClose: () => void; approve?: Approve; onCost?: (attach: boolean) => void }) {
  const pkg = useQuery({
    queryKey: ['ai-package', scopeKey(scope), JSON.stringify(request)],
    queryFn: () => post<AssistPackage>(scopedPath(scope, 'ai/packages'), request),
    retry: false,
    gcTime: 0,
    staleTime: 0
  });
  const settings = useQuery(assistSettingsQuery).data;
  // Handoff-only assistants get their own Copy action; Hclaro never runs them (AI-01.05).
  const handoffs = (settings?.providers ?? []).filter(p => p.usable && !p.send);
  const provider = chosenAssistant(settings);
  const acceptThen = useAcceptThen();
  useDetectAssistants(settings);
  const [chatId, setChatId] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [handed, setHanded] = useState<{ how: 'copied' | 'downloaded'; to?: string }>();
  const [failed, setFailed] = useState<string>();
  // Send names the previewed hash, the assistant and the disclosure version the user accepted.
  async function send() {
    const p = pkg.data;
    if (!p || !provider || !settings) return;
    setBusy(true);
    setFailed(undefined);
    try {
      await acceptThen(settings, async () => {
        if (approve) return approve.run(p, provider);
        const chat = await post<Conversation>(scopedPath(scope, 'ai/conversations'), { packageSha256: p.sha256, provider: provider.id, disclosureVersion: settings.disclosure.version, prompt: p.prompt });
        setChatId(chat.id);
      });
    } catch (err) {
      setFailed(err instanceof Error ? err.message : 'The request could not be sent.');
    } finally {
      setBusy(false);
    }
  }
  // Export names the previewed hash; the server returns those exact bytes or refuses.
  async function handOver(how: 'copy' | 'download', to?: string) {
    const p = pkg.data;
    if (!p) return;
    setBusy(true);
    setFailed(undefined);
    try {
      const e = await post<Exported>(scopedPath(scope, `ai/packages/${p.sha256}/export`), {});
      if (e.sha256 !== p.sha256 || e.review !== p.review || e.prompt !== p.prompt) throw new Error('The package changed since the preview. Open the preview again.');
      if (how === 'copy') {
        try {
          await navigator.clipboard.writeText(e.review);
        } catch {
          throw new Error('The browser did not allow copying to the clipboard. Use Download .md instead.');
        }
      }
      else save(e.filename, e.review);
      setHanded({ how: how === 'copy' ? 'copied' : 'downloaded', to });
    } catch (err) {
      setFailed(err instanceof Error ? err.message : 'The package could not be exported.');
    } finally {
      setBusy(false);
    }
  }
  const error = pkg.error instanceof ApiError ? pkg.error : undefined;
  const p = pkg.data;
  const disclosure = p?.disclosure;
  // A quick action's first turn exists only in the preview; export returns plain packages and reviews.
  const quick = 'action' in request && !!request.action;
  const attached = 'cost' in request && !!request.cost;
  return <Drawer label="AI context preview" onClose={onClose}>
   <div className="inspector-header"><span><Sparkles size={15} />{title}</span><Button variant="ghost" size="icon" aria-label="Close AI context preview" onClick={onClose}><X size={16} /></Button></div>
   <div className="inspector-body assist-preview">
    {pkg.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Building the context package on this machine…</p></div>
      : error?.code === 'package_blocked' ? <div className="empty-state error-state" role="alert"><Ban size={26} /><h2>Sharing blocked</h2><p>{error.message}</p>{error.fields.length > 0 && <><p>Fields that still look secret or local (values are not shown):</p><ul className="assist-fields">{error.fields.map(f => <li key={f}><code>{f}</code></li>)}</ul></>}</div>
      : pkg.error ? <div className="empty-state error-state" role="alert"><p>{pkg.error.message}</p>{onCost && attached && <Button variant="outline" onClick={() => onCost(false)}>Continue without cost data</Button>}</div>
      : p && p.sections.length === 0 ? <div className="empty-state"><h2>Nothing to share</h2><p>This context package has no content.</p></div>
      : p && <>
       {disclosure && <ul className="assist-disclosure" aria-label="Before you share">{disclosure.lines.map(l => <li key={l}>{l}</li>)}</ul>}
       <dl className="metadata">
        <div><dt>Included</dt><dd>{p.sections.join(', ')}</dd></div>
        <div><dt>Removed for size</dt><dd>{p.dropped.length ? p.dropped.join(', ') : 'Nothing'}</dd></div>
        <div><dt>Never included</dt><dd>State values, .tfvars values, saved plans, plan JSON, environment, backend settings and local paths. Values that look secret are shown as [redacted] or their file is left out; detection is heuristic, so read the text below before you share it.</dd></div>
        {p.prompt && <div><dt>{quick ? 'Quick action prompt' : 'Review prompt'}</dt><dd className="mono">{p.prompt}</dd></div>}
        <div><dt>Package SHA-256</dt><dd className="mono" title={p.sha256}>{p.sha256.slice(0, 16)}…</dd></div>
       </dl>
       {onCost && (p.costAvailable || attached) && <label className="assist-cost"><input type="checkbox" checked={attached} onChange={e => onCost(e.target.checked)} />
        <span>Attach this session's latest Infracost estimate: workspace, Infracost version, pricing basis and finish time, totals, coverage, per-type counts and monthly change, and caveats. Individual cost line items are never shared. Hclaro reuses this completed estimate and does not start a new Infracost run.</span></label>}
       {attached && !!p.costHidden && <p className="plan-warning" role="alert"><TriangleAlert size={14} /><span>{p.costHidden} cost {p.costHidden === 1 ? 'line was' : 'lines were'} hidden because {p.costHidden === 1 ? 'it looked' : 'they looked'} secret or local. The attached cost summary is incomplete; the package says so too.</span></p>}
       {onCost && !p.costAvailable && !attached && <p className="muted assist-note">No cost estimate from this session is attached, so the assistant gets no pricing data.</p>}
       {approve && !p.prompt ? <pre className="assist-text" aria-label="Text to share: context package" tabIndex={0}>{p.markdown}</pre>
         : <pre className="assist-text" aria-label="Text to share: review prompt and context package" tabIndex={0}>{p.review}</pre>}
       {handed && <p className="info-line" role="status">Context {handed.how}{handed.to && <> for {handed.to}. Paste it into {handed.to} yourself: Hclaro did not open or run it</>}. Hclaro's protections end here: what happens to this text is now up to you and the assistant you give it to.</p>}
       {failed && <p className="dialog-error" role="alert">{failed}</p>}
       {chatId && <ConversationPanel scope={scope} id={chatId} name={provider?.name ?? 'the assistant'} onClosed={() => setChatId(undefined)} />}
      </>}
    <div className="form-actions">
     <Button variant="ghost" onClick={onClose}>Cancel</Button>
     {p && p.sections.length > 0 && <>
      {!quick && <>{handoffs.length ? handoffs.map(h => <Button key={h.id} variant="outline" disabled={busy} onClick={() => void handOver('copy', h.name)}><Copy size={14} />Copy for {h.name}</Button>)
        : <Button variant="outline" disabled={busy} onClick={() => void handOver('copy')}><Copy size={14} />Copy</Button>}
      <Button variant="outline" disabled={busy} onClick={() => void handOver('download')}><Download size={14} />Download .md</Button></>}
      {provider && settings && <Button disabled={busy || !!chatId} onClick={() => void send()}><Send size={14} />{!settings.disclosureAccepted ? 'Accept and send' : approve ? approve.label : `Review with ${provider.name}`}</Button>}
     </>}
    </div>
    {p && p.sections.length > 0 && handoffs.length > 0 && !quick && <p className="muted assist-note">{handoffs.map(h => h.name).join(' and ')} {handoffs.length > 1 ? 'are' : 'is'} handoff-only: copy or download the context and give it to the assistant yourself. Hclaro never runs {handoffs.length > 1 ? 'them' : 'it'}.</p>}
    {p && provider && settings && !settings.disclosureAccepted && <p className="muted assist-note">Accept and send accepts the notes above for this session and sends this to {provider.name}.</p>}
   </div>
  </Drawer>;
}

// ConversationPanel polls one conversation while a turn runs. Replies are shown in <pre>: React
// escapes them, so HTML, links and commands stay inert. A failed or cancelled message comes
// back as the server's draft and is put back into the input; nothing is dropped silently.
// actions are extra buttons (Attach project context); staleHint replaces the stale banner's advice.
export function ConversationPanel({ scope, id, name, onClosed, actions, staleHint }: { scope: PlanScope; id: string; name: string; onClosed: () => void; actions?: ReactNode; staleHint?: string }) {
  const path = scopedPath(scope, `ai/conversations/${id}`);
  const chat = useQuery({
    queryKey: ['ai-conversation', scopeKey(scope), id],
    queryFn: () => api<Conversation>(path),
    refetchInterval: q => q.state.data?.state === 'running' ? 1000 : false,
    retry: false,
    gcTime: 0
  });
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [refused, setRefused] = useState<string>();
  const [seen, setSeen] = useState(0);
  const client = useQueryClient();
  const settings = useQuery(assistSettingsQuery).data;
  const acceptThen = useAcceptThen();
  // A conversation reopened after a restart needs this session's acceptance before its next message.
  const accept = !!settings && !settings.disclosureAccepted;
  const c = chat.data;
  // A failed or cancelled message comes back as the draft: put it into an empty box, once per
  // server answer. It stays the draft until the next message is sent.
  if (c && c.state !== 'running' && c.draft && seen !== chat.dataUpdatedAt) {
    setSeen(chat.dataUpdatedAt);
    if (!text) setText(c.draft);
  }
  async function act(action: 'messages' | 'cancel' | 'close' | 'save', saved?: boolean) {
    setBusy(true);
    setRefused(undefined);
    try {
      const request = () => post(`${path}/${action}`, action === 'messages' ? { text } : action === 'save' ? { saved } : {}).then(() => undefined);
      await (settings && action === 'messages' ? acceptThen(settings, request) : request());
      if (action === 'close') return onClosed();
      if (action === 'messages') setText('');
      if (action === 'save') void Promise.all([client.invalidateQueries({ queryKey: ['ai-saved'] }), client.invalidateQueries({ queryKey: ['ai-conversations'] })]);
    } catch (err) {
      setRefused(err instanceof Error ? err.message : 'The request was refused.');
    } finally {
      setBusy(false);
    }
    // Also after a refusal: a context_changed refusal must show the stale banner and lock the input.
    await chat.refetch();
  }
  const proposing = !!c?.turns.some(t => t.role === 'assistant' && !t.hidden && hasProposal(t.text));
  const changes = useChanges(scope, id, c?.turns.length ?? 0, proposing);
  const long = tooLong(text, settings);
  const running = c?.state === 'running';
  const locked = running || c?.stale || !!c?.readOnly;
  return <section className="assist-run" aria-label="Conversation">
   <p className="plan-warning" role="note"><TriangleAlert size={14} /><span>AI output is unverified. Hclaro never runs, applies or approves anything it suggests, and writes a proposed file change only when you apply it; check every claim and command yourself.</span></p>
   {chat.error ? <p className="dialog-error" role="alert">{chat.error.message}</p> : !c ? <p className="muted" role="status"><LoaderCircle className="spin" size={14} /> Starting…</p> : <>
    <p className="muted assist-usage">{name} · {running ? 'waiting for a reply' : c.state} · {Math.ceil(c.usedBytes / 1024)} of {c.limitBytes / 1024} KiB used. Every message resends the whole conversation.</p>
    <ol className="assist-turns" aria-label="Messages">
     {c.turns.map((t, i) => t.hidden ? <li key={i} className="assist-turn context" role="note">Hidden: this saved {t.role === 'context' ? 'project context' : 'message'} now looks like it contains a secret, a credential or a local path.</li>
       : t.role === 'context' ? <li key={i} className="assist-turn context" role="note">Project context attached (hash <code title={t.sha256}>{t.sha256?.slice(0, 12)}…</code>)</li> : <li key={i} className={`assist-turn ${t.role}`}><strong>{t.role === 'user' ? 'You' : name}</strong>
      {t.role === 'assistant' ? <><pre className="assist-text" aria-label="Assistant reply text" tabIndex={0}>{t.text || '(empty reply)'}</pre>
        {changes.data && changes.data.changes.some(v => v.turn === i) && <ProposedChanges scope={scope} id={id} changes={changes.data.changes.filter(v => v.turn === i)} />}</>
        : t.prompt ? <p>{t.prompt.startsWith('qa-') ? 'Ran a quick action on the context package' : 'Asked for a review of the context package'} (<code>{t.prompt}</code>).</p>
        : <p>{t.text || 'Shared the context package.'}</p>}</li>)}
    </ol>
    {running && <div className="assist-running" role="status"><LoaderCircle className="spin" size={14} /><span>Waiting for {name}…</span><Button variant="outline" size="small" disabled={busy} onClick={() => void act('cancel')}><Square size={12} />Cancel</Button></div>}
    {c.readOnly && <p className="plan-warning" role="alert"><TriangleAlert size={14} /><span>Read-only: {c.readOnly}</span></p>}
    {c.saveError && <p className="dialog-error" role="alert">{c.saveError}</p>}
    {c.stale && !c.readOnly && <p className="plan-warning" role="alert"><TriangleAlert size={14} /><span>Context is stale: the workspace or the shared context changed since this conversation started, so no more messages can be sent. {staleHint ?? 'Close it and start a new conversation.'}</span></p>}
    {c.error && !running && <p className={c.state === 'cancelled' ? 'muted' : 'dialog-error'} role="alert">{c.error.message}{c.draft !== undefined && ' Your message is back in the box below.'}</p>}
    <label className="assist-compose">Follow-up message
     <textarea value={text} rows={3} disabled={locked} onChange={e => setText(e.target.value)} />
    </label>
    <label className="assist-cost"><input type="checkbox" checked={c.saved} disabled={busy || (!c.saved && c.needsReview)} onChange={e => void act('save', e.target.checked)} />
     <span>Save conversation: keep its context, messages and replies as plain text in Hclaro's configuration folder on this machine, to reopen after Hclaro restarts. Unticking deletes the saved copy.</span></label>
    {long && <p className="dialog-error" role="alert">This message is longer than {settings?.maxMessage} bytes. Shorten it to send it.</p>}
    {refused && <p className="dialog-error" role="alert">{refused}</p>}
    {accept && !locked && <ul className="assist-disclosure" aria-label="Before you send">{settings.disclosure.lines.map(l => <li key={l}>{l}</li>)}</ul>}
    <div className="form-actions">
     <Button variant="ghost" disabled={busy} onClick={() => void act('close')}><MessageSquareX size={14} />Close conversation</Button>
     {!running && !c.readOnly && actions}
     <Button disabled={busy || locked || long || !text.trim()} onClick={() => void act('messages')}><Send size={14} />{accept ? 'Accept and send' : 'Send message'}</Button>
    </div>
   </>}
  </section>;
}
