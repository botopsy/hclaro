import { useState } from 'react';
import { SlidersHorizontal, UserCog } from 'lucide-react';
import { Button } from '../components/ui/button';
import type { ExecutionProfile } from '../types';
import { IdentityBadge } from './ContextSummary';
import { ProfilesDialog } from './ProfilesDialog';
import type { useProfileSelection } from './selection';

type Selection = ReturnType<typeof useProfileSelection>;

// ContextSelector chooses the execution profile this tab uses for a project. The choice is
// per tab; "Manage" edits the project's stored profiles.
export function ContextSelector({ projectId, selection, workspaces, onChosen, compact }: {
  projectId: string;
  selection: Selection;
  workspaces?: string[];
  // Called after a choice, for example to open the workspace a profile selects.
  onChosen?: (profile?: ExecutionProfile) => void;
  compact?: boolean;
}) {
  const [managing, setManaging] = useState(false);
  const list = selection.profiles.data?.profiles || [];
  const current = list.find(p => p.id === selection.selected);
  function choose(id: string) {
    selection.select(id);
    onChosen?.(list.find(p => p.id === id));
  }
  // The dialog is a sibling, not a child: .context-item text does not wrap.
  return <><div className={`context-item context-selector ${compact ? 'compact' : ''}`}>
   <UserCog size={13} aria-hidden="true" />
   <label htmlFor={`profile-${projectId}`}>Profile</label>
   <select id={`profile-${projectId}`} className="workspace-switch" value={selection.selected} disabled={selection.profiles.isPending} onChange={e => choose(e.target.value)} title="Execution profile for this browser tab only">
    <option value="">None (inherited environment)</option>
    {list.map(p => <option key={p.id} value={p.id}>{p.name}{p.default ? ' (default)' : ''}{p.workspace ? ` · ${p.workspace}` : ''}</option>)}
   </select>
   {current && <IdentityBadge />}
   {selection.missing && <span className="issue-text" role="status">The profile chosen in this tab was removed.</span>}
   <Button variant="ghost" size="small" onClick={() => setManaging(true)} title="Create, edit, clone, remove or set the default profile"><SlidersHorizontal size={12} />Manage</Button>
  </div>
  {managing && <ProfilesDialog projectId={projectId} workspaces={workspaces} selected={selection.selected} onSelect={id => choose(id)} onClose={() => setManaging(false)} />}
  </>;
}
