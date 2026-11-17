import { useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useQuery } from '@tanstack/react-query';
import { Ban, Copy, Download, LoaderCircle, Send, Sparkles, Square, TriangleAlert, X } from 'lucide-react';
import { api, ApiError, post } from '../api';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { scopeKey, scopedPath, type PlanScope } from '../plans/routes';
import type { ImportRequest } from '../types';

// AI handoff (AI-01.03) and headless requests (AI-01.04): the server builds a sanitized
// context package; the user previews it and only Copy, Download or Send hands it over.
// Nothing is shared by opening the preview. A reply is untrusted text: it is rendered as
// plain text only, with no links, command buttons or actions.

export type AssistDisclosure = { version: string; lines: string[] };
export type AssistProvider = { id: string; name: string; mode: 'headless-eligible' | 'handoff-only' | 'unsupported'; version?: string; path?: string; reason?: string; message?: string };
export type AssistSettings = {
  enabled: boolean; strictLocal: boolean; handoff: boolean; headless: boolean; disclosure: AssistDisclosure;
  disclosureAccepted?: boolean; providers?: AssistProvider[]; defaultProvider?: string;
};
type AssistRun = { id: string; provider: string; kind: string; sha256: string; status: 'running' | 'completed' | 'failed' | 'cancelled'; reply?: string; error?: { code: string; message: string }; durationMs: number };
export type AssistRequest = { kind: 'plan'; planId: string } | { kind: 'drift'; scanId: string } | { kind: 'resource'; resourceId: string } | { kind: 'import'; import: ImportRequest };
type AssistPackage = { kind: string; markdown: string; sha256: string; sections: string[]; dropped: string[]; disclosure: AssistDisclosure };
type Exported = { kind: string; sha256: string; markdown: string; filename: string };

export const assistSettingsQuery = { queryKey: ['ai-settings'], queryFn: () => api<AssistSettings>('ai/settings') };

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

export function AssistDrawer({ scope, request, title, onClose }: { scope: PlanScope; request: AssistRequest; title: string; onClose: () => void }) {
  const pkg = useQuery({
    queryKey: ['ai-package', scopeKey(scope), JSON.stringify(request)],
    queryFn: () => post<AssistPackage>(scopedPath(scope, 'ai/packages'), request),
    retry: false,
    gcTime: 0,
    staleTime: 0
  });
  const settings = useQuery(assistSettingsQuery).data;
  const eligible = settings?.headless ? (settings.providers ?? []).filter(p => p.mode === 'headless-eligible') : [];
  // Handoff-only assistants get their own Copy action; Hclaro never runs them (AI-01.05).
  const handoffs = (settings?.providers ?? []).filter(p => p.mode === 'handoff-only');
  const [picked, setPicked] = useState<string>();
  const provider = eligible.find(p => p.id === (picked ?? settings?.defaultProvider)) ?? eligible[0];
  const [runId, setRunId] = useState<string>();
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
      const run = await post<AssistRun>(scopedPath(scope, `ai/packages/${p.sha256}/run`), { provider: provider.id, disclosureVersion: settings.disclosure.version });
      setRunId(run.id);
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
      if (e.sha256 !== p.sha256 || e.markdown !== p.markdown) throw new Error('The package changed since the preview. Open the preview again.');
      if (how === 'copy') {
        try {
          await navigator.clipboard.writeText(e.markdown);
        } catch {
          throw new Error('The browser did not allow copying to the clipboard. Use Download .md instead.');
        }
      }
      else save(e.filename, e.markdown);
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
  return <Drawer label="AI context preview" onClose={onClose}>
   <div className="inspector-header"><span><Sparkles size={15} />{title}</span><Button variant="ghost" size="icon" aria-label="Close AI context preview" onClick={onClose}><X size={16} /></Button></div>
   <div className="inspector-body assist-preview">
    {pkg.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Building the context package on this machine…</p></div>
      : error?.code === 'package_blocked' ? <div className="empty-state error-state" role="alert"><Ban size={26} /><h2>Sharing blocked</h2><p>{error.message}</p>{error.fields.length > 0 && <><p>Fields that still look secret or local (values are not shown):</p><ul className="assist-fields">{error.fields.map(f => <li key={f}><code>{f}</code></li>)}</ul></>}</div>
      : pkg.error ? <div className="empty-state error-state" role="alert"><p>{pkg.error.message}</p></div>
      : p && p.sections.length === 0 ? <div className="empty-state"><h2>Nothing to share</h2><p>This context package has no content.</p></div>
      : p && <>
       {disclosure && <ul className="assist-disclosure" aria-label="Before you share">{disclosure.lines.map(l => <li key={l}>{l}</li>)}</ul>}
       <dl className="metadata">
        <div><dt>Included</dt><dd>{p.sections.join(', ')}</dd></div>
        <div><dt>Removed for size</dt><dd>{p.dropped.length ? p.dropped.join(', ') : 'Nothing'}</dd></div>
        <div><dt>Never included</dt><dd>State values, .tfvars values, saved plans, plan JSON, environment, credentials, backend settings and local paths. Values that look secret are shown as [redacted].</dd></div>
        <div><dt>SHA-256</dt><dd className="mono" title={p.sha256}>{p.sha256.slice(0, 16)}…</dd></div>
       </dl>
       <pre className="assist-text" aria-label="Context package text" tabIndex={0}>{p.markdown}</pre>
       {handed && <p className="info-line" role="status">Context {handed.how}{handed.to && <> for {handed.to}. Paste it into {handed.to} yourself: Hclaro did not open or run it</>}. Hclaro's protections end here: what happens to this text is now up to you and the assistant you give it to.</p>}
       {failed && <p className="dialog-error" role="alert">{failed}</p>}
       {runId && <RunPanel id={runId} />}
      </>}
    <div className="form-actions">
     <Button variant="ghost" onClick={onClose}>Cancel</Button>
     {p && p.sections.length > 0 && <>
      {handoffs.length ? handoffs.map(h => <Button key={h.id} variant="outline" disabled={busy} onClick={() => void handOver('copy', h.name)}><Copy size={14} />Copy for {h.name}</Button>)
        : <Button variant="outline" disabled={busy} onClick={() => void handOver('copy')}><Copy size={14} />Copy</Button>}
      <Button variant="outline" disabled={busy} onClick={() => void handOver('download')}><Download size={14} />Download .md</Button>
      {provider && settings && <>
       {eligible.length > 1 && <select aria-label="Assistant for this request" value={provider.id} onChange={e => setPicked(e.target.value)}>{eligible.map(e => <option key={e.id} value={e.id}>{e.name}</option>)}</select>}
       <Button disabled={busy || !settings.disclosureAccepted || !!runId} title={settings.disclosureAccepted ? undefined : 'Accept the AI disclosure in Settings first.'} onClick={() => void send()}><Send size={14} />Send to {provider.name}</Button>
      </>}
     </>}
    </div>
    {p && p.sections.length > 0 && handoffs.length > 0 && <p className="muted assist-note">{handoffs.map(h => h.name).join(' and ')} {handoffs.length > 1 ? 'are' : 'is'} handoff-only: copy or download the context and give it to the assistant yourself. Hclaro never runs {handoffs.length > 1 ? 'them' : 'it'}.</p>}
    {p && provider && settings && !settings.disclosureAccepted && <p className="muted assist-note">To send this to {provider.name}, accept the AI disclosure in Settings first.</p>}
   </div>
  </Drawer>;
}

// RunPanel polls the session's request until it ends. The reply is shown as plain text in a
// <pre>: React escapes it, so HTML, links and commands in it stay inert.
function RunPanel({ id }: { id: string }) {
  const run = useQuery({
    queryKey: ['ai-run', id],
    queryFn: () => api<AssistRun>('ai/run'),
    refetchInterval: q => q.state.data?.status === 'running' ? 1000 : false,
    retry: false,
    gcTime: 0
  });
  const [cancelling, setCancelling] = useState(false);
  const r = run.data?.id === id ? run.data : undefined;
  async function cancel() {
    setCancelling(true);
    try {
      await post('ai/run/cancel', {});
    } finally {
      setCancelling(false);
    }
  }
  return <section className="assist-run" aria-label="Assistant reply">
   <p className="plan-warning" role="note"><TriangleAlert size={14} /><span>AI output is unverified. Hclaro never runs, applies or approves anything it suggests; check every claim and command yourself.</span></p>
   {run.error ? <p className="dialog-error" role="alert">{run.error.message}</p>
     : !r ? (run.data ? <p className="muted">Another request replaced this one.</p> : <p className="muted" role="status"><LoaderCircle className="spin" size={14} /> Starting…</p>)
     : r.status === 'running' ? <div className="assist-running" role="status"><LoaderCircle className="spin" size={14} /><span>Waiting for the assistant…</span><Button variant="outline" size="small" disabled={cancelling} onClick={() => void cancel()}><Square size={12} />Cancel</Button></div>
     : r.status === 'completed' ? <pre className="assist-text" aria-label="Assistant reply text" tabIndex={0}>{r.reply || '(empty reply)'}</pre>
     : <p className={r.status === 'cancelled' ? 'muted' : 'dialog-error'} role="alert">{r.error?.message ?? 'The request did not finish.'}</p>}
  </section>;
}
