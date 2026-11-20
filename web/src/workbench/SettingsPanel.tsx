import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Monitor, Moon, ShieldCheck, Sun } from 'lucide-react';
import { post } from '../api';
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
  changePreference
}: {
  preferences: Preferences;
  changePreference: (change: Partial<Preferences>) => void;
}) {
  return <div className="settings-panel"><div className="setting-row about-row"><div><h2>About Hclaro</h2><p>A local workspace for Terraform and OpenTofu. Public beta.</p></div><HclaroLogo /></div><div className="setting-row"><div><h2>Appearance</h2><p>Follow your system, or choose a theme.</p></div><div className="segmented">{(['system', 'light', 'dark'] as const).map((theme, i) => {
          const Icon = [Monitor, Sun, Moon][i];
          return <button aria-pressed={preferences.theme === theme} key={theme} onClick={() => changePreference({
            theme
          })}><Icon size={14} />{theme}</button>;
        })}</div></div><div className="setting-row"><div><h2>Resource density</h2><p>Adjust the space between resource rows.</p></div><select aria-label="Resource density" value={preferences.density} onChange={e => changePreference({
        density: e.target.value as Preferences['density']
      })}><option value="comfortable">Comfortable</option><option value="compact">Compact</option></select></div><div className="setting-row"><div><h2>Default grouping</h2><p>Browse by module or use a flat resource list.</p></div><select aria-label="Default grouping" value={preferences.grouping} onChange={e => changePreference({
        grouping: e.target.value as Preferences['grouping']
      })}><option value="module">Module tree</option><option value="flat">Flat list</option></select></div><div className="setting-row"><div><h2>Source editor</h2><p>Used by Open in editor links. Hclaro asks the operating system to open the editor's link; it never runs a command you type.</p></div><select aria-label="Source editor" value={preferences.editor} onChange={e => changePreference({
        editor: e.target.value === 'cursor' ? 'cursor' : 'vscode'
      })}>{(Object.keys(editorLabels) as EditorName[]).map(name => <option key={name} value={name}>{editorLabels[name]}</option>)}</select></div><AssistSetting /><div className="settings-security"><ShieldCheck size={23} /><div><h2>Your machine. Your infrastructure.</h2><p>No telemetry, cloud accounts, or external services. Only display preferences are saved in this browser. Hub projects are saved as paths and names in your user configuration directory. To change engines, restart with <code>--engine terraform</code> or <code>--engine tofu</code>.</p></div></div></div>;
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
      client.setQueryData(assistSettingsQuery.queryKey, await post<AssistSettings>(path, body));
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
