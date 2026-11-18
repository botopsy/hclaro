import { useState } from 'react';
import { createPortal } from 'react-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { FileCode2, LoaderCircle, MessageSquarePlus, Send, Sparkles } from 'lucide-react';
import { api, post } from '../api';
import { Button } from '../components/ui/button';
import { scopeKey, scopedPath, type PlanScope } from '../plans/routes';
import { ErrorState } from '../workbench/ErrorState';
import { AssistDrawer, ConversationPanel, assistSettingsQuery, type Approve, type Conversation } from './AskAI';

// The AI Assistant page (AI-01.09): conversations that are not tied to a plan or drift result.
// A conversation starts with no project data, or with a project package the user previewed in
// the existing drawer. The project and workspace are the ones the context selector shows; the
// assistant is fixed per conversation. Conversations of another project or workspace stay
// listed, read-only.

type Listed = { id: string; provider: string; kind: string; state: string; title: string; project: string; workspace: string; here: boolean; startedAt: string };
type Mode = 'chat' | 'project';
// Mirrors assist.MaxMessage; the server enforces it.
const maxMessage = 8000;

export function AssistantPage({ scope, project, workspace }: { scope?: PlanScope; project: string; workspace: string }) {
  const settings = useQuery(assistSettingsQuery).data;
  const client = useQueryClient();
  const list = useQuery({
    queryKey: ['ai-conversations', scope ? scopeKey(scope) : 'none'],
    queryFn: () => api<{ conversations: Listed[] }>(scopedPath(scope!, 'ai/conversations')),
    enabled: !!scope && settings?.enabled === true,
    refetchInterval: q => q.state.data?.conversations.some(c => c.state === 'running') ? 2000 : false
  });
  const providers = settings?.providers ?? [];
  const eligible = settings?.headless ? providers.filter(p => p.mode === 'headless-eligible') : [];
  const [picked, setPicked] = useState<string>();
  const provider = eligible.find(p => p.id === (picked ?? settings?.defaultProvider)) ?? eligible[0];
  const [mode, setMode] = useState<Mode>('chat');
  const [text, setText] = useState('');
  const [selected, setSelected] = useState<string>();
  const [preview, setPreview] = useState<'start' | 'attach'>();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<string>();
  if (!settings) return <div className="empty-state" role="status"><LoaderCircle className="spin" /></div>;
  if (!settings.enabled) return <ErrorState title="AI assistance is off" message="Turn it on in Settings to use the AI Assistant." />;
  if (!scope) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Opening this workspace…</p></div>;
  const conversations = list.data?.conversations ?? [];
  const current = conversations.find(c => c.id === selected);
  const nameOf = (id: string) => providers.find(p => p.id === id)?.name ?? id;

  async function start(packageSha256?: string) {
    if (!provider || !settings) return;
    const c = await post<Conversation>(scopedPath(scope!, 'ai/conversations'), { provider: provider.id, disclosureVersion: settings.disclosure.version, message: text, ...(packageSha256 ? { packageSha256 } : {}) });
    setText('');
    setSelected(c.id);
    setPreview(undefined);
    await list.refetch();
  }
  async function startChat() {
    setBusy(true);
    setFailed(undefined);
    try {
      await start();
    } catch (err) {
      setFailed(err instanceof Error ? err.message : 'The conversation could not be started.');
    } finally {
      setBusy(false);
    }
  }
  // Only the drawer's approve button attaches a package: to a new conversation, or as the next
  // turn of the open one.
  const approve: Approve | undefined = preview === 'start' ? { label: 'Start conversation with this context', run: p => start(p.sha256) }
    : preview === 'attach' && selected ? {
      label: 'Attach to conversation', run: async p => {
        await post(scopedPath(scope, `ai/conversations/${selected}/context`), { packageSha256: p.sha256 });
        setPreview(undefined);
        // The open panel polls only while a turn runs: show the attached turn and the cleared stale state now.
        await client.invalidateQueries({ queryKey: ['ai-conversation', scopeKey(scope), selected] });
        await list.refetch();
      }
    } : undefined;
  const tooLong = new TextEncoder().encode(text).length > maxMessage;
  const blocked = providers.filter(p => p.mode !== 'headless-eligible' || !settings.headless);

  return <div className="assistant-page">
   <nav className="assistant-list" aria-label="Conversations in this session">
    <Button variant={selected ? 'outline' : 'default'} onClick={() => setSelected(undefined)}><MessageSquarePlus size={14} />New conversation</Button>
    {conversations.length === 0 ? <p className="muted">No conversations in this session yet. They are kept in memory until Hclaro exits or AI is turned off.</p>
      : <ul>{conversations.map(c => <li key={c.id}><button type="button" className={`assistant-item ${c.id === selected ? 'active' : ''} ${c.here ? '' : 'elsewhere'}`} aria-current={c.id === selected ? 'true' : undefined} onClick={() => setSelected(c.id)}>
        <strong>{c.title || 'Conversation'}</strong>
        <span>{nameOf(c.provider)} · {c.kind === 'conversation' ? 'no project data' : 'project context'} · {c.state}</span>
        {!c.here && <span className="muted">Belongs to {c.project} / {c.workspace}</span>}
       </button></li>)}</ul>}
   </nav>
   <section className="assistant-main" aria-label={selected ? 'Selected conversation' : 'New conversation'}>
    {current && !current.here ? <div className="empty-state" role="note"><h2>Read-only here</h2><p>This conversation belongs to {current.project} / {current.workspace}. Open that project and workspace to continue it; nothing is sent from here.</p></div>
      : selected ? <ConversationPanel key={selected} scope={scope} id={selected} name={nameOf(current?.provider ?? provider?.id ?? '')} onClosed={() => {
        setSelected(undefined);
        void list.refetch();
      }} staleHint="If the project files changed, attach the current project context to continue; otherwise close it and start a new conversation." actions={<Button variant="outline" onClick={() => setPreview('attach')}><FileCode2 size={14} />Attach project context</Button>} />
      : <div className="assistant-new">
       <p className="muted">Project <strong>{project}</strong> · workspace <strong>{workspace}</strong>, from the context selector.</p>
       <label>Assistant
        <select value={provider?.id ?? ''} disabled={eligible.length === 0} onChange={e => setPicked(e.target.value)}>
         {eligible.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
         {blocked.map(p => <option key={p.id} value="" disabled>{p.name}: {settings.strictLocal ? 'strict local-only mode' : p.mode === 'handoff-only' ? 'handoff-only, copy or download instead' : p.message || 'not available'}</option>)}
         {eligible.length === 0 && blocked.length === 0 && <option value="">No assistant detected</option>}
        </select>
       </label>
       <p className="muted assist-note">The assistant is fixed for each conversation. Changing it here affects only new conversations; open conversations keep theirs.</p>
       <fieldset className="assistant-mode">
        <legend>Context</legend>
        <label><input type="radio" name="assistant-mode" checked={mode === 'chat'} onChange={() => setMode('chat')} />Conversation only: no project data is attached</label>
        <label><input type="radio" name="assistant-mode" checked={mode === 'project'} onChange={() => setMode('project')} />Attach project context: preview the package first</label>
       </fieldset>
       <label className="assist-compose">{mode === 'chat' ? 'Message' : 'Message (optional)'}
        <textarea value={text} rows={4} onChange={e => setText(e.target.value)} />
       </label>
       {tooLong && <p className="dialog-error" role="alert">This message is longer than {maxMessage} bytes. Shorten it to send it.</p>}
       {failed && <p className="dialog-error" role="alert">{failed}</p>}
       {!settings.disclosureAccepted && eligible.length > 0 && <p className="muted assist-note">To send anything, accept the AI disclosure in Settings first.</p>}
       {eligible.length === 0 && <p className="muted assist-note">No assistant can run here. Preview the project context to copy or download it for an assistant you run yourself.</p>}
       <div className="form-actions">
        {mode === 'chat' ? <Button disabled={busy || !provider || !settings.disclosureAccepted || !text.trim() || tooLong} onClick={() => void startChat()}><Send size={14} />Start conversation</Button>
          : <Button disabled={busy || tooLong} onClick={() => setPreview('start')}><Sparkles size={14} />Preview project context</Button>}
       </div>
      </div>}
   </section>
   {preview && createPortal(<AssistDrawer scope={scope} request={{ kind: 'project' }} title={preview === 'attach' ? 'Attach project context' : 'Project context'} approve={approve} onClose={() => setPreview(undefined)} />, document.body)}
  </div>;
}
