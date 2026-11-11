import { Monitor, Moon, ShieldCheck, Sun } from 'lucide-react';
import { HclaroLogo } from '../brand/HclaroLogo';
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
      })}>{(Object.keys(editorLabels) as EditorName[]).map(name => <option key={name} value={name}>{editorLabels[name]}</option>)}</select></div><div className="settings-security"><ShieldCheck size={23} /><div><h2>Your machine. Your infrastructure.</h2><p>No telemetry, cloud accounts, or external services. Only display preferences are saved in this browser. Hub projects are saved as paths and names in your user configuration directory. To change engines, restart with <code>--engine terraform</code> or <code>--engine tofu</code>.</p></div></div></div>;
}
