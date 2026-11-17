import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Monitor, Moon, ShieldCheck, Sun } from 'lucide-react';
import { post } from '../api';
import { assistSettingsQuery, type AssistProvider, type AssistSettings } from '../assist/AskAI';
import { HclaroLogo } from '../brand/HclaroLogo';
import { Button } from '../components/ui/button';
import { editorLabels } from '../git/GitView';
import type { EditorName } from '../types';
import type { Preferences } from './preferences';
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

// The AI master switch lives on the server for this session only: it starts off at every
// launch, and turning it off drops every context package built so far.
function AssistSetting() {
  const client = useQueryClient();
  const settings = useQuery(assistSettingsQuery);
  const [error, setError] = useState<string>();
  const s = settings.data;
  async function change(body: { enabled?: boolean; defaultProvider?: string; acceptDisclosure?: string }) {
    setError(undefined);
    try {
      client.setQueryData(assistSettingsQuery.queryKey, await post<AssistSettings>('ai/settings', body));
    } catch (e) {
      setError(e instanceof Error ? e.message : 'AI assistance could not be changed.');
    }
  }
  const eligible = (s?.providers ?? []).filter(p => p.mode === 'headless-eligible');
  return <div className="setting-row"><div><h2>AI assistance</h2><p>Preview a sanitized context package for a plan, drift scan, import or resource, then copy or download it for the assistant you choose, or send it to a detected assistant. Off at every launch. Nothing is shared without your click.</p>
   {s?.enabled && <ul className="assist-disclosure">{s.disclosure.lines.map(l => <li key={l}>{l}</li>)}</ul>}
   {s?.enabled && !s.strictLocal && <>
    <ul className="assist-providers" aria-label="Assistants">{(s.providers ?? []).map(p => <ProviderRow key={p.id} p={p} />)}</ul>
    {eligible.length > 0 && <p><label>Default assistant <select aria-label="Default assistant" value={s.defaultProvider ?? ''} onChange={e => void change({ defaultProvider: e.target.value })}>{eligible.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label></p>}
    {eligible.length > 0 && (s.disclosureAccepted ? <p className="muted" role="status">Disclosure accepted for this session. Send is available in the context preview.</p>
      : <p><Button variant="outline" size="small" onClick={() => void change({ acceptDisclosure: s.disclosure.version })}>Accept the disclosure for this session</Button></p>)}
   </>}
   {s?.strictLocal && <p className="muted">Strict local-only mode: copy and download stay available; Hclaro never runs an assistant itself.</p>}{error && <p className="dialog-error" role="alert">{error}</p>}</div><div className="segmented" role="group" aria-label="AI assistance">{([false, true] as const).map(on => <button key={String(on)} aria-pressed={!!s && s.enabled === on} disabled={!s} onClick={() => void change({ enabled: on })}>{on ? 'On' : 'Off'}</button>)}</div></div>;
}

// ProviderRow separates whether an assistant is installed from what Hclaro can do with it.
function ProviderRow({ p }: { p: AssistProvider }) {
  const status = p.mode === 'headless-eligible' ? 'Detected · Send or handoff' : p.mode === 'handoff-only' ? 'Detected · Handoff only' : p.reason === 'cli_missing' ? 'Not installed' : 'Not usable';
  return <li><strong>{p.name}</strong> · {status}{p.version && <> · <span className="mono">{p.version}</span></>}{p.path && <> · <code title="Set with a command-line flag when starting Hclaro; it cannot be changed here.">{p.path}</code></>}
   {p.mode === 'handoff-only' ? <><br />Use {p.name} by copying or downloading Hclaro context.{p.message && <details className="muted"><summary>Why handoff only?</summary>{p.message}</details>}</>
     : p.message && p.reason !== 'cli_missing' && <span className="muted"> {p.message}</span>}</li>;
}
