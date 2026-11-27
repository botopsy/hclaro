import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowUpCircle, X } from 'lucide-react';
import { api } from '../api';
import { readStorage, storageKeys, writeStorage } from '../lib/storage';
import { releaseUrl } from '../navigation/help';
import { Button } from './ui/button';
import { CopyButton } from './CopyButton';

type UpdateStatus = { enabled: boolean; current?: string; latest?: string; available: boolean };

const upgradeCommand = 'brew upgrade botopsy/tap/hclaro';

export const useUpdateStatus = () => useQuery({ queryKey: ['update'], queryFn: () => api<UpdateStatus>('update'), staleTime: 60 * 60 * 1000, retry: false });

// UpdateNotice tells the user, bottom right, that a newer release exists. The server does the
// lookup (and can be told not to with --no-update-check); the page only reads the answer.
// Dismissing hides that one version, so the next release shows again.
export function UpdateNotice() {
  const update = useUpdateStatus();
  const [dismissed, setDismissed] = useState(() => readStorage('local', storageKeys.updateDismissed));
  const u = update.data;
  if (!u?.enabled || !u.available || !u.latest || dismissed === u.latest) return null;
  const latest = u.latest;
  return <aside className="update-notice" role="status" aria-label="Update available">
   <ArrowUpCircle size={18} aria-hidden="true" />
   <div>
    <strong>Hclaro {latest} is available</strong>
    <p className="muted">You have {u.current}. <a href={releaseUrl(latest)} target="_blank" rel="noopener noreferrer">What's new</a></p>
    <p className="update-command"><code>{upgradeCommand}</code><CopyButton value={upgradeCommand} label="Copy upgrade command" iconOnly /></p>
   </div>
   <Button type="button" variant="ghost" size="icon" aria-label="Dismiss update notice" onClick={() => { writeStorage('local', storageKeys.updateDismissed, latest); setDismissed(latest); }}><X size={14} aria-hidden="true" /></Button>
  </aside>;
}
