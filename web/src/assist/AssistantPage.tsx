import { useState } from 'react';
import { createPortal } from 'react-dom';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, FileCode2, FolderOpen, LoaderCircle, MessageSquarePlus, Send, Sparkles, Trash2, X, Zap } from 'lucide-react';
import { api, send } from '../api';
import { Button } from '../components/ui/button';
import { scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import { ErrorState } from '../components/ErrorState';
import { Disclosure } from '../components/Disclosure';
import { Segmented } from '../components/Segmented';
import { AssistDrawer, ConversationPanel, assistSettingsQuery, chosenAssistant, tooLong, useAcceptThen, useDetectAssistants, type Approve } from './AskAI';
import type { AssistListed, AssistSaved, Conversation } from '../types';
import { errorMessage } from '../lib/errorMessage';

// The AI Assistant page (AI-01.09): conversations that are not tied to a plan or drift result.
// A conversation starts with no project data, or with a project package the user previewed in
// the existing drawer. The project and workspace are the ones the context selector shows; the
// assistant is the one chosen in Settings (AI-01.14) and fixed per conversation. Conversations of another project or workspace stay
// listed, read-only. Saved conversations (AI-01.11) of this project are listed below them and
// can be reopened or deleted.

type Mode = 'chat' | 'project';
// Quick actions (AI-01.10): a project package plus a fixed, versioned prompt from the server's
// registry. Each goes through the same preview drawer; the IDs are what turn metadata records.
const quickActions = [
  { id: 'qa-describe@1', label: 'Describe project' },
  { id: 'qa-architecture@1', label: 'Architecture review' },
  { id: 'qa-security@2', label: 'Security review' },
  { id: 'qa-cost@1', label: 'Cost analysis' },
  { id: 'qa-issues@2', label: 'Find issues' },
  { id: 'qa-improve@2', label: 'Suggest improvements' }
];

export function AssistantPage({ scope, project, workspace, noProject, onSettings }: { scope?: PlanScope; project: string; workspace: string; noProject?: boolean; onSettings?: () => void }) {
  const settings = useQuery(assistSettingsQuery).data;
  const client = useQueryClient();
  const list = useQuery({
    queryKey: ['ai-conversations', scope ? scopeKey(scope) : 'none'],
    queryFn: () => api<{ conversations: AssistListed[] }>(scopedPath(scope!, 'ai/conversations')),
    enabled: !!scope && settings?.enabled === true,
    refetchInterval: q => q.state.data?.conversations.some(c => c.state === 'running') ? 2000 : false
  });
  const saved = useQuery({
    queryKey: ['ai-saved', scope ? scopeKey(scope) : 'none'],
    queryFn: () => api<{ conversations: AssistSaved[]; available: boolean; retentionDays?: number }>(scopedPath(scope!, 'ai/saved')),
    enabled: !!scope && settings?.enabled === true && !noProject
  });
  const [confirmAll, setConfirmAll] = useState(false);
  const providers = settings?.providers ?? [];
  const provider = chosenAssistant(settings);
  const acceptThen = useAcceptThen();
  useDetectAssistants(settings);
  const [mode, setMode] = useState<Mode>('chat');
  const [text, setText] = useState('');
  const [selected, setSelected] = useState<string>();
  const [preview, setPreview] = useState<'start' | 'attach' | 'action'>();
  const [action, setAction] = useState<{ id: string; label: string; cost: boolean }>();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<string>();
  if (!settings) return <div className="empty-state" role="status"><LoaderCircle className="spin" /></div>;
  if (!settings.enabled) return <ErrorState title="AI assistance is off" message="Turn it on in Settings to use the AI Assistant." />;
  if (!scope) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Opening this workspace…</p></div>;
  const conversations = list.data?.conversations ?? [];
  const current = conversations.find(c => c.id === selected);
  const nameOf = (id: string) => providers.find(p => p.id === id)?.name ?? id;
  async function onSaved(action: string, id?: string) {
    setFailed(undefined);
    try {
      const c = await send<{ id?: string }>('POST', scopedPath(scope!, id ? `ai/saved/${id}/${action}` : `ai/saved/${action}`), {});
      if (action === 'open' && c.id) setSelected(c.id);
      setConfirmAll(false);
    } catch (err) {
      setFailed(errorMessage(err, 'The saved conversation could not be changed.'));
    }
    await Promise.all([list.refetch(), saved.refetch(), client.invalidateQueries({ queryKey: ['ai-conversation'] })]);
  }
  const closed = (saved.data?.conversations ?? []).filter(c => !c.open);

  async function start(packageSha256?: string, prompt?: string) {
    if (!provider || !settings) return;
    // A quick action's first message is its prompt; the typed text stays in the box.
    const c = await send<Conversation>('POST', scopedPath(scope!, 'ai/conversations'), { provider: provider.id, disclosureVersion: settings.disclosure.version, ...(prompt ? { prompt } : { message: text }), ...(packageSha256 ? { packageSha256 } : {}) });
    if (!prompt) setText('');
    setSelected(c.id);
    setPreview(undefined);
    await list.refetch();
  }
  async function startChat() {
    setBusy(true);
    setFailed(undefined);
    try {
      await acceptThen(settings!, () => start());
    } catch (err) {
      setFailed(errorMessage(err, 'The conversation could not be started.'));
    } finally {
      setBusy(false);
    }
  }
  // Only the drawer's approve button attaches a package: to a new conversation, or as the next
  // turn of the open one.
  const approve: Approve | undefined = preview === 'start' ? { label: 'Start conversation with this context', run: p => start(p.sha256) }
    : preview === 'action' && action ? { label: `Start ${action.label.toLowerCase()}`, run: p => start(p.sha256, p.prompt) }
    : preview === 'attach' && selected ? {
      label: 'Attach to conversation', run: async p => {
        await send('POST', scopedPath(scope, `ai/conversations/${selected}/context`), { packageSha256: p.sha256 });
        setPreview(undefined);
        // The open panel polls only while a turn runs: show the attached turn and the cleared stale state now.
        await client.invalidateQueries({ queryKey: ['ai-conversation', scopeKey(scope), selected] });
        await list.refetch();
      }
    } : undefined;
  const long = tooLong(text, settings);
  const chosen = providers.find(p => p.id === settings.defaultProvider);
  const actionsOff = noProject ? 'Open a project to use quick actions: they share its configuration.'
    : !provider ? 'Quick actions need an assistant Hclaro can run. A handoff-only assistant can use Preview project context, then Copy or Download.' : undefined;

  const conv = (c: AssistListed) => <li key={c.id}><button type="button" className={`assistant-item ${c.id === selected ? 'active' : ''} ${c.here ? '' : 'elsewhere'}`} aria-current={c.id === selected ? 'true' : undefined} onClick={() => setSelected(c.id)}>
    <strong>{c.title || 'Conversation'}</strong>
    <span>{nameOf(c.provider)} · {c.kind === 'conversation' ? 'no project data' : 'project context'} · {c.state}{c.saved ? ' · saved' : ''}</span>
    {!c.here && <span className="muted">Belongs to {c.project} / {c.workspace}</span>}
   </button></li>;
  return <>
   <div className="assistant-page">
   <nav className="assistant-list" aria-label="Conversations in this session">
    <Button variant={selected ? 'outline' : 'default'} onClick={() => setSelected(undefined)}><MessageSquarePlus size={14} />New conversation</Button>
    <h2 className="assistant-eyebrow">This session</h2>
    {conversations.length === 0 ? <p className="muted">None yet. Conversations stay in memory until Hclaro exits or AI is turned off, unless you save them.</p>
      : <ul>{conversations.map(conv)}</ul>}
    {saved.data?.available && <section aria-label="Saved conversations of this project">
     <h2 className="assistant-eyebrow">Saved</h2>
     {closed.length === 0 ? <p className="muted">None. Saved conversations are kept for {saved.data.retentionDays ?? 30} days after their last use.</p>
       : <ul>{closed.map(c => <li key={c.id} className="assistant-item elsewhere">
        <strong>{c.title || 'Conversation'}</strong>
        <span>{nameOf(c.provider)} {c.providerVersion} · workspace {c.workspace} · last used {new Date(c.lastActivity).toLocaleString()}</span>
        <span><Button variant="outline" size="small" onClick={() => void onSaved('open', c.id)}><FolderOpen size={12} />Open</Button> <Button variant="ghost" size="small" aria-label={`Delete saved conversation ${c.title}`} onClick={() => void onSaved('delete', c.id)}><Trash2 size={12} />Delete</Button></span>
       </li>)}</ul>}
     {(saved.data.conversations.length > 0) && (confirmAll
       ? <p role="alert">Delete every saved conversation of this project from disk? <Button variant="outline" size="small" onClick={() => void onSaved('delete-all')}>Delete all</Button> <Button variant="ghost" size="small" onClick={() => setConfirmAll(false)}>Keep</Button></p>
       : <Button variant="ghost" size="small" onClick={() => setConfirmAll(true)}><Trash2 size={12} />Delete all saved for this project</Button>)}
    </section>}
   </nav>
   <section className="assistant-main" aria-label={selected ? 'Selected conversation' : 'New conversation'}>
    <div className="assistant-bar">
     <span title="The assistant is fixed for each conversation. Changing it in Settings affects only new conversations; open conversations keep theirs.">Assistant <strong>{provider?.name ?? chosen?.name ?? 'none detected'}</strong>{settings.checking ? ' · checking…' : settings.strictLocal ? ' · strict local-only mode' : !provider && chosen ? ' · copy or download only' : ''}</span>
     {onSettings && <button type="button" className="link-button" onClick={onSettings}>Change in Settings</button>}
    </div>
    <div className="assistant-body">
    {current && !current.here ? <div className="empty-state" role="note"><h2>Read-only here</h2><p>This conversation belongs to {current.project} / {current.workspace}. Open that project and workspace to continue it; nothing is sent from here.</p></div>
      : selected ? <ConversationPanel key={selected} scope={scope} id={selected} name={nameOf(current?.provider ?? provider?.id ?? '')} onClosed={() => {
        setSelected(undefined);
        void list.refetch();
        void saved.refetch();
      }} staleHint="If the project files changed, attach the current project context to continue; otherwise close it and start a new conversation." actions={<Button variant="outline" onClick={() => setPreview('attach')}><FileCode2 size={14} />Attach project context</Button>} />
      : <div className="assistant-new">
       <p className="muted">Project <strong>{project}</strong> · workspace <strong>{workspace}</strong>, from the context selector.</p>
       <section className="assistant-actions" aria-label="Quick actions">
        <h2>Quick actions</h2>
        <p className="muted assist-note">Each one previews the project context and a fixed prompt first. It uses only the configuration Hclaro reads: no state, live cloud data or pricing.</p>
        <div className="assistant-action-grid">{quickActions.map(a => <button type="button" key={a.id} className="assistant-action" disabled={busy || !!actionsOff} title={actionsOff} onClick={() => {
          setAction({ ...a, cost: false });
          setPreview('action');
        }}><Zap size={16} aria-hidden="true" />{a.label}</button>)}</div>
        {actionsOff && <p className="muted assist-note" role="note">{actionsOff}</p>}
       </section>
       <p className="muted assist-note">The assistant is fixed for each conversation. Changing it in Settings affects only new conversations; open conversations keep theirs.</p>
       <div className="assistant-mode">
        <span className="muted">Context</span>
        <Segmented<Mode> label="Context" value={mode} onChange={setMode} options={[{ value: 'chat', label: 'Conversation only' }, { value: 'project', label: 'Attach project context' }]} />
       </div>
       {mode === 'project' && <ul className="assistant-attached" aria-label="What is attached">
        <li><Check size={12} aria-hidden="true" /> Configuration files (.tf) and the module tree</li>
        <li><Check size={12} aria-hidden="true" /> Provider requirements</li>
        <li><X size={12} aria-hidden="true" /> State, live cloud data, pricing</li>
        <li><X size={12} aria-hidden="true" /> Sensitive values</li>
       </ul>}
       <label className="assist-compose">{mode === 'chat' ? 'Message' : 'Message (optional)'}
        <textarea value={text} rows={4} onChange={e => setText(e.target.value)} />
       </label>
       {long && <p className="dialog-error" role="alert">This message is longer than {settings.maxMessage} bytes. Shorten it to send it.</p>}
       {failed && <p className="dialog-error" role="alert">{failed}</p>}
       {!settings.disclosureAccepted && provider && mode === 'chat' && <Disclosure summary="Before you send: what this shares and costs"><ul className="assist-disclosure" aria-label="Before you send">{settings.disclosure.lines.map(l => <li key={l}>{l}</li>)}</ul></Disclosure>}
       {!provider && <p className="muted assist-note">No assistant can run here. Preview the project context to copy or download it for an assistant you run yourself.</p>}
       <div className="form-actions">
        {mode === 'chat' ? <Button disabled={busy || !provider || !text.trim() || long} onClick={() => void startChat()}><Send size={14} />{settings.disclosureAccepted || !provider ? 'Start conversation' : 'Accept and send'}</Button>
          : <Button disabled={busy || long} onClick={() => setPreview('start')}><Sparkles size={14} />Preview project context</Button>}
       </div>
      </div>}
    </div>
   </section>
   {preview && createPortal(<AssistDrawer scope={scope} request={preview === 'action' && action ? { kind: 'project', action: action.id, ...(action.cost ? { cost: true } : {}) } : { kind: 'project' }}
     title={preview === 'attach' ? 'Attach project context' : preview === 'action' && action ? action.label : 'Project context'} approve={approve}
     onCost={action?.id === 'qa-cost@1' && preview === 'action' ? cost => setAction({ ...action, cost }) : undefined} onClose={() => setPreview(undefined)} />, document.body)}
  </div>
  </>;
}
