import { useState } from 'react';
import { Check, ChevronDown, Layers3, Settings2 } from 'lucide-react';
import { Button } from '../components/ui/button';
import { useSwitcherPanel } from './ProjectSwitcher';

// The workspace pill of the top bar (§2.1) for a Hub project and its menu: the project's
// workspaces, an inline New workspace name + Create, and Manage workspaces…. Choosing a
// workspace changes this browser tab only; Create opens the reviewed, confirmed dialog.
export function WorkspaceSwitcher({ project, workspace, workspaces, cliDefault, onSwitch, onCreate, createDisabled, onManage }: {
  project: string;
  workspace: string;
  workspaces: string[];
  cliDefault?: string;
  // Opens a workspace in this tab; the caller confirms first when that would discard work.
  onSwitch: (workspace: string) => void;
  // Starts the existing workspace review with this name; nothing is created here.
  onCreate: (name: string) => void;
  // Why a workspace cannot be created now, if it cannot.
  createDisabled?: string;
  onManage: () => void;
}) {
  const { open, setOpen, wrapper, trigger, panel, go, arrows } = useSwitcherPanel();
  const [name, setName] = useState('');
  return <div className="project-switcher" ref={wrapper}>
   <button ref={trigger} type="button" className="workspace-pill" aria-expanded={open} aria-controls="workspace-switcher-panel" title="Terraform/OpenTofu workspace this tab reads. Switching runs nothing and never selects a workspace in the CLI." onClick={() => {
      setName('');
      setOpen(o => !o);
    }}>
    <Layers3 size={14} aria-hidden="true" /><strong className="mono">{workspace}</strong><ChevronDown size={13} aria-hidden="true" /><span className="sr-only">Switch workspace</span>
   </button>
   {open && <div ref={panel} id="workspace-switcher-panel" className="switcher-popover" role="group" aria-label="Switch workspace" onKeyDown={arrows}>
    <p className="switcher-label">Workspaces in {project}</p>
    <ul aria-label="Workspaces">{workspaces.map(ws => <li key={ws}><button type="button" className="switcher-option" aria-current={ws === workspace ? 'page' : undefined} onClick={() => go(() => {
      if (ws !== workspace) onSwitch(ws);
    })}>
     <Layers3 size={14} aria-hidden="true" /><span><strong className="mono">{ws}</strong></span>{ws === cliDefault && <span className="hub-tag" title="Selected in this project's CLI data directory. Switching here does not change it.">CLI default</span>}{ws === workspace && <Check size={14} className="switcher-check" aria-label="Current workspace" />}
    </button></li>)}</ul>
    <form className="switcher-all" onSubmit={e => {
        e.preventDefault();
        if (name) go(() => onCreate(name));
      }}>
     <label className="switcher-search"><input className="mono" value={name} onChange={e => setName(e.target.value.trim())} placeholder="New workspace name" aria-label="New workspace name" maxLength={90} autoComplete="off" spellCheck={false} disabled={!!createDisabled} /><Button type="submit" size="small" variant="outline" disabled={!!createDisabled || !name} title={createDisabled}>Create</Button></label>
     {createDisabled && <p className="switcher-empty">{createDisabled}</p>}
    </form>
    <ul className="switcher-all">
     <li><button type="button" className="switcher-option" onClick={() => go(onManage)}><Settings2 size={14} aria-hidden="true" /><span><strong>Manage workspaces…</strong></span></button></li>
    </ul>
   </div>}
  </div>;
}
