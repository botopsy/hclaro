import { useState } from 'react';
import { ChevronDown, UserCog } from 'lucide-react';
import { Menu, MenuItem } from '../components/Menu';
import type { ExecutionProfile } from '../types';
import { IdentityBadge } from './ContextSummary';
import { ProfilesDialog } from './ProfilesDialog';
import { AuthIndicator } from './AuthPanel';
import type { useProfileSelection } from './selection';

type Selection = ReturnType<typeof useProfileSelection>;

// ContextSelector is the Profile menu (§2.1): it chooses the execution profile this tab uses
// for a project. The choice is per tab; "Manage profiles…" edits the project's stored profiles.
export function ContextSelector({ projectId, selection, workspaces, onChosen }: {
  projectId: string;
  selection: Selection;
  workspaces?: string[];
  // Called after a choice, for example to open the workspace a profile selects.
  onChosen?: (profile?: ExecutionProfile) => void;
}) {
  const [managing, setManaging] = useState(false);
  const list = selection.profiles.data?.profiles || [];
  const current = list.find(p => p.id === selection.selected);
  function choose(id: string) {
    selection.select(id);
    onChosen?.(list.find(p => p.id === id));
  }
  const label = `Profile: ${current?.name ?? 'none'}`;
  // The dialog is a sibling, not a child: .context-item text does not wrap.
  return <><div className="context-item context-selector">
   <Menu className="profile-menu" align="end" label={label} button={<span className="profile-trigger" title="Execution profile for this browser tab only"><UserCog size={13} aria-hidden="true" />{label}<ChevronDown size={12} aria-hidden="true" /></span>}>
    <MenuItem checked={!current} disabled={selection.profiles.isPending} onSelect={() => choose('')}><span className="profile-option"><strong>None (inherited environment)</strong><small>Uses the environment Hclaro started with</small></span></MenuItem>
    {list.map(p => <MenuItem key={p.id} checked={p.id === selection.selected} onSelect={() => choose(p.id)}><span className="profile-option"><strong>{p.name}{p.default ? ' (default)' : ''}</strong><small>{[p.awsProfile ? `AWS_PROFILE=${p.awsProfile}` : 'No AWS profile', p.awsRegion, p.workspace && `workspace ${p.workspace}`].filter(Boolean).join(' · ')}</small></span></MenuItem>)}
    <span className="menu-separator" aria-hidden="true" />
    <MenuItem onSelect={() => setManaging(true)}>Manage profiles…</MenuItem>
   </Menu>
   {current && (current.awsProfile ? <AuthIndicator projectId={projectId} profileId={current.id} /> : <IdentityBadge />)}
   {selection.missing && <span className="issue-text" role="status">The profile chosen in this tab was removed.</span>}
  </div>
  {managing && <ProfilesDialog projectId={projectId} workspaces={workspaces} selected={selection.selected} onSelect={id => choose(id)} onClose={() => setManaging(false)} />}
  </>;
}
