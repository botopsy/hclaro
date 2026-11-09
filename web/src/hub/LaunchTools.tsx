import { ArrowRight, ExternalLink, FolderPlus, Info } from 'lucide-react';
import { Button } from '../components/ui/button';
import { tools } from './ToolsView';

// Tools for the project opened from the terminal. The tools run only in a Hub workspace
// context, so this page explains that first and moves the tab only when asked.
export function LaunchTools({ workspace, registered, onOpen, onOpenNewTab, onSave }: {
  workspace: string;
  // Whether this folder is saved in the Hub (only then does a Hub context exist for it).
  registered: boolean;
  onOpen: () => void;
  onOpenNewTab: () => void;
  onSave: () => void;
}) {
  return <div className="launch-tools">
   <section className="surface" aria-labelledby="launch-tools-title">
    <div className="surface-heading"><div><h2 id="launch-tools-title">{registered ? 'Tools run in this folder’s Hub entry' : 'Save this project to the Hub first'}</h2>
     <p>{registered ? <>Project tools run in a separate, read-only Hub context for this folder, in workspace <strong>{workspace}</strong>. Opening them switches this tab to that context, where plans, drift scans and imports are not available; use <strong>Open in new tab</strong> to keep this view.</> : 'Project tools run in a Hub context for a saved project. Save this folder to the Hub, then open Tools again. Nothing runs until you choose a tool.'}</p></div><Info size={18} aria-hidden="true" /></div>
    <div className="hub-actions">{registered ? <><Button onClick={onOpen}>Open tools for workspace {workspace}<ArrowRight size={14} /></Button><Button variant="outline" onClick={onOpenNewTab}><ExternalLink size={14} />Open in new tab</Button></> : <Button onClick={onSave}><FolderPlus size={14} />Go to Save to Hub</Button>}</div>
   </section>
   <ul className="launch-tools-list" aria-label="Available tools">{tools.map(({ name, title, icon: Icon, description }) => <li key={name}><Icon size={16} aria-hidden="true" /><span><strong>{title}</strong><span className="muted">{description}</span></span></li>)}</ul>
  </div>;
}
