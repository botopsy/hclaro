import { skipToken, useQuery } from '@tanstack/react-query';
import type { GitContext, Workspace } from '../types';

// Top bar warnings (§2.1): shown only when something is wrong. They use data already loaded:
// the workspace snapshot and the Git status the Git page read. Navigating never runs `git status`.
export function WarningChips({ workspace, gitPrefix, onOpen }: { workspace?: Workspace; gitPrefix?: string; onOpen: (view: 'Overview' | 'Git') => void }) {
  const git = useQuery<GitContext>({ queryKey: ['git-status', gitPrefix], queryFn: skipToken, enabled: gitPrefix !== undefined }).data;
  const unreachable = workspace?.status === 'error' && workspace.diagnostic?.code === 'state_unavailable';
  const uncommitted = gitPrefix !== undefined && git?.state === 'repository' ? git.counts.changed : 0;
  if (!unreachable && !uncommitted) return null;
  return <>
    {unreachable && <button type="button" className="chip chip-tone-delete topbar-warning" title="The state could not be read, so planning, applying and importing are off. Open Overview for the reason." onClick={() => onOpen('Overview')}><span className="status-dot" aria-hidden="true" />Backend unreachable</button>}
    {uncommitted > 0 && <button type="button" className="chip chip-tone-update topbar-warning" title="A plan or apply made now would not match a commit in the repository. Open Git to review the changes." onClick={() => onOpen('Git')}><span className="status-dot" aria-hidden="true" />{uncommitted} uncommitted IaC file{uncommitted === 1 ? '' : 's'}</button>}
  </>;
}
