import { useState, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { LoaderCircle, Monitor, Moon, Plus, ShieldCheck, Sun } from 'lucide-react';
import { send, sessionQuery } from '../api';
import { Segmented } from '../components/Segmented';
import { helpLinks, releaseUrl } from '../navigation/help';
import { useUpdateStatus } from '../components/UpdateNotice';
import { blankProfile } from '../profiles/ProfilesDialog';
import { profilesQuery } from '../profiles/selection';
import { assistSettingsQuery } from '../assist/AskAI';
import type { AssistProvider, AssistSettings } from '../types';
import { HclaroLogo } from '../brand/HclaroLogo';
import { Button } from '../components/ui/button';
import type { EditorName } from '../types';
import type { Preferences } from './preferences';
import { editorLabels } from '../git/editor';
import { errorMessage } from '../lib/errorMessage';
export function SettingsPanel({
  preferences,
  changePreference,
  project
}: {
  preferences: Preferences;
  changePreference: (change: Partial<Preferences>) => void;
  // The open project: execution profiles are per project, so only its profiles are shown.
  project?: { id: string; name: string };
}) {
  const version = useQuery(sessionQuery).data?.version;
  const updates = useUpdateStatus().data;
  const row = (title: string, text: ReactNode, control: ReactNode) => <div className="setting-row"><div><h2>{title}</h2><p>{text}</p></div>{control}</div>;
  const link = (href: string, label: string) => <a href={href} target="_blank" rel="noopener noreferrer">{label}</a>;
  return <div className="settings-panel">
   <div className="setting-row about-row"><div><h2>About Hclaro</h2><p>A local workspace for Terraform and OpenTofu.</p><p className="settings-links">{link(releaseUrl(version), "What's new")}{link(helpLinks.bug, 'Report a bug')}{link(helpLinks.star, 'GitHub')}</p></div><HclaroLogo /></div>
   {updates && row('Update check', updates.enabled ? <>On: Hclaro asks GitHub once a day whether a newer release exists and sends no project data. Start with <code>--no-update-check</code> to turn it off.</> : 'Off: Hclaro does not look for new releases.', null)}
   {row('Appearance', 'Follow your system, or choose a theme.', <Segmented label="Theme" value={preferences.theme} onChange={theme => changePreference({ theme })} options={[{ value: 'system', label: 'System', icon: <Monitor size={14} /> }, { value: 'light', label: 'Light', icon: <Sun size={14} /> }, { value: 'dark', label: 'Dark', icon: <Moon size={14} /> }]} />)}
   {row('Resource density', 'Adjust the space between resource rows.', <select aria-label="Resource density" value={preferences.density} onChange={e => changePreference({ density: e.target.value as Preferences['density'] })}><option value="comfortable">Comfortable</option><option value="compact">Compact</option></select>)}
   {row('Default grouping', 'How Resources groups its rows when it opens.', <select aria-label="Default grouping" value={preferences.grouping} onChange={e => changePreference({ grouping: e.target.value as Preferences['grouping'] })}><option value="module">Module tree</option><option value="type">By type</option><option value="flat">Flat list</option></select>)}
   {row('Source editor', "Used by every Open in editor button. Hclaro asks the operating system to open the editor's link; it never runs a command you type.", <select aria-label="Source editor" value={preferences.editor} onChange={e => changePreference({ editor: e.target.value === 'cursor' ? 'cursor' : 'vscode' })}>{(Object.keys(editorLabels) as EditorName[]).map(name => <option key={name} value={name}>{editorLabels[name]}</option>)}</select>)}
   <ProfilesSetting project={project} />
   <AssistSetting />
   <div className="settings-security"><ShieldCheck size={23} /><div><h2>Your machine. Your infrastructure.</h2><p>No telemetry, cloud accounts, or external services. Only display preferences are saved in this browser. Hub projects are saved as paths and names in your user configuration directory. To change engines, restart with <code>--engine terraform</code> or <code>--engine tofu</code>.</p></div></div>
  </div>;
}

// ProfilesSetting lists the open project's execution profiles with Remove and an inline Add form
// (name, AWS profile, region) that posts to the project's endpoint. Profiles hold names only,
// never credentials; the other options stay in Manage profiles… of the Profile menu.
function ProfilesSetting({ project }: { project?: { id: string; name: string } }) {
  const client = useQueryClient();
  const profiles = useQuery(profilesQuery(project?.id));
  const [form, setForm] = useState({ name: '', awsProfile: '', awsRegion: '' });
  const [confirm, setConfirm] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  if (!project) return <div className="setting-row"><div><h2>Execution profiles</h2><p>Profiles belong to a project in Hclaro. Open a project first to manage its profiles.</p></div></div>;
  const id = project.id;
  const base = `projects/${id}/execution-profiles`;
  const list = profiles.data?.profiles ?? [];
  const readOnly = profiles.data?.status === 'unavailable';
  const name = form.name.trim();
  // Same rule as the server: unique within this project, ignoring case.
  const taken = list.some(p => p.name.toLowerCase() === name.toLowerCase());
  async function act(action: () => Promise<unknown>, fallback: string) {
    setBusy(true);
    setError('');
    try {
      await action();
      await client.invalidateQueries({ queryKey: ['profiles', id] });
      await client.invalidateQueries({ queryKey: ['execution-context'] });
      await client.invalidateQueries({ queryKey: ['plan-options'] });
    } catch (e) {
      setError(errorMessage(e, fallback));
    } finally {
      setBusy(false);
    }
  }
  return <div className="setting-row setting-stack"><div><h2>Execution profiles for {project.name}</h2><p>The AWS profile and region used for plans, drift checks and tools in this project. Hclaro stores names and settings, never secrets.</p></div>
   {profiles.data?.notice && <p className="plan-warning" role="note">{profiles.data.notice}</p>}
   {profiles.isPending ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Loading profiles…</p> : profiles.error ? <p className="dialog-error" role="alert">{errorMessage(profiles.error)}</p> : <ul className="settings-profiles" aria-label="Execution profiles">
    <li><strong>None (inherited environment)</strong><span className="muted">Uses the environment Hclaro started with</span></li>
    {list.map(p => <li key={p.id}><strong>{p.name}</strong><span className="muted mono">{[p.awsProfile ? `AWS_PROFILE=${p.awsProfile}` : 'No AWS profile', p.awsRegion].filter(Boolean).join(' · ')}</span>
     {confirm === p.id ? <Button size="small" variant="outline" disabled={busy} onClick={() => void act(async () => {
         await send('DELETE', `${base}/${p.id}`, { expectedRevision: p.revision });
         setConfirm(undefined);
       }, 'The profile could not be removed.')}>Remove {p.name}</Button> : <Button size="small" variant="ghost" aria-label={`Remove ${p.name}`} disabled={readOnly || busy} onClick={() => setConfirm(p.id)}>Remove</Button>}
    </li>)}
   </ul>}
   <form className="settings-profile-form" aria-label="Add a profile" onSubmit={e => {
       e.preventDefault();
       void act(async () => {
         await send('POST', base, { ...blankProfile, name, awsProfile: form.awsProfile.trim(), awsRegion: form.awsRegion.trim() });
         setForm({ name: '', awsProfile: '', awsRegion: '' });
       }, 'The profile could not be added.');
     }}>
    <label className="field"><span>Name</span><input value={form.name} maxLength={60} placeholder="prod-readonly" aria-invalid={taken} onChange={e => setForm({ ...form, name: e.target.value })} /></label>
    <label className="field"><span>AWS profile</span><input className="mono" value={form.awsProfile} maxLength={64} placeholder="from ~/.aws/config" autoComplete="off" onChange={e => setForm({ ...form, awsProfile: e.target.value })} /></label>
    <label className="field"><span>Region</span><input className="mono" value={form.awsRegion} maxLength={32} placeholder="optional" autoComplete="off" onChange={e => setForm({ ...form, awsRegion: e.target.value })} /></label>
    <Button type="submit" variant="outline" disabled={readOnly || busy || !name || !form.awsProfile.trim() || taken}><Plus size={14} />Add profile</Button>
   </form>
   {taken && <p className="dialog-error" role="alert">This project already has a profile named {name}.</p>}
   {error && <p className="dialog-error" role="alert">{error}</p>}
  </div>;
}

// AI assistance is on by default (OD-AI-13). The switch and the chosen assistant are saved by the
// server in the configuration folder; turning it off drops every package and conversation and
// cancels running turns. Being on shares nothing: Send, Copy and Download are separate clicks.
function AssistSetting() {
  const client = useQueryClient();
  const settings = useQuery(assistSettingsQuery);
  const [error, setError] = useState<string>();
  const s = settings.data;
  async function change(body: { enabled?: boolean; defaultProvider?: string }, path = 'ai/settings') {
    setError(undefined);
    try {
      client.setQueryData(assistSettingsQuery.queryKey, await send<AssistSettings>('POST', path, body));
    } catch (e) {
      setError(errorMessage(e, 'AI assistance could not be changed.'));
    }
  }
  const providers = s?.providers ?? [];
  return <div className="setting-row"><div><h2>AI assistance</h2><p>Ask your AI assistant about plans and code. Nothing is shared until you click Send or Copy.</p>
   {s?.enabled && (s.strictLocal ? <p className="muted">Strict local-only: copy and download only.</p>
     : s.checking ? <p className="muted" role="status">Checking…</p>
     : !s.checked ? <p className="muted">Installed assistants are checked when you open an AI preview or the AI Assistant. <Button variant="outline" size="small" onClick={() => void change({}, 'ai/detect')}>Check now</Button></p>
     : <>
      <p><label>Assistant <select aria-label="Assistant" value={s.defaultProvider ?? ''} onChange={e => void change({ defaultProvider: e.target.value })}>
       {!s.defaultProvider && <option value="">None installed</option>}
       {providers.map(p => <option key={p.id} value={p.id} disabled={!p.usable}>{p.name}{!p.usable ? (p.installed ? ' (not usable)' : ' (not installed)') : !p.send ? ' (copy only)' : ''}</option>)}
      </select></label></p>
      <details className="muted"><summary>Details</summary><ul className="assist-providers" aria-label="Assistants">{providers.map(p => <ProviderRow key={p.id} p={p} />)}</ul></details>
     </>)}
   {s?.warning && <p className="muted" role="alert">{s.warning}</p>}{error && <p className="dialog-error" role="alert">{error}</p>}</div><div className="segmented" role="group" aria-label="AI assistance">{([false, true] as const).map(on => <button key={String(on)} aria-pressed={!!s && s.enabled === on} disabled={!s} onClick={() => void change({ enabled: on })}>{on ? 'On' : 'Off'}</button>)}</div></div>;
}

// ProviderRow is one assistant's status, version, read-only path (OD-AI-3) and, when Hclaro
// cannot run it, the reason.
function ProviderRow({ p }: { p: AssistProvider }) {
  const status = p.send ? 'Send and copy' : p.usable ? 'Copy only' : p.installed ? 'Not usable' : 'Not installed';
  return <li><strong>{p.name}</strong> · {status}{p.version && <> · <span className="mono">{p.version}</span></>}{p.path && <> · <code title="Set with a command-line flag when starting Hclaro; it cannot be changed here.">{p.path}</code></>}
   {!p.send && p.message && p.installed && <> · {p.message}</>}</li>;
}
