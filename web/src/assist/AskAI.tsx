import { useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useQuery } from '@tanstack/react-query';
import { Ban, Copy, Download, LoaderCircle, Sparkles, TriangleAlert, X } from 'lucide-react';
import { api, ApiError, post } from '../api';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';
import { scopeKey, scopedPath, type PlanScope } from '../plans/routes';
import type { ImportRequest } from '../types';

// AI handoff (AI-01.03): the server builds a sanitized context package; the user previews
// it and only Copy or Download hands it over. Nothing is shared by opening the preview.

export type AssistDisclosure = { version: string; lines: string[] };
export type AssistSettings = { enabled: boolean; strictLocal: boolean; handoff: boolean; headless: boolean; disclosure: AssistDisclosure };
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
  const [busy, setBusy] = useState(false);
  const [handed, setHanded] = useState<'copied' | 'downloaded'>();
  const [failed, setFailed] = useState<string>();
  // Export names the previewed hash; the server returns those exact bytes or refuses.
  async function handOver(how: 'copy' | 'download') {
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
      setHanded(how === 'copy' ? 'copied' : 'downloaded');
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
       {handed && <p className="info-line" role="status">Context {handed}. Hclaro's protections end here: what happens to this text is now up to you and the assistant you give it to.</p>}
       {failed && <p className="dialog-error" role="alert">{failed}</p>}
      </>}
    <div className="form-actions">
     <Button variant="ghost" onClick={onClose}>Cancel</Button>
     {p && p.sections.length > 0 && <>
      <Button variant="outline" disabled={busy} onClick={() => void handOver('copy')}><Copy size={14} />Copy</Button>
      <Button variant="outline" disabled={busy} onClick={() => void handOver('download')}><Download size={14} />Download .md</Button>
     </>}
    </div>
   </div>
  </Drawer>;
}
