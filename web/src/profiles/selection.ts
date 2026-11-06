import { useCallback, useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import type { ProfileList } from '../types';

const id = /^[0-9a-f]{32}$/;
const listeners = new Set<() => void>();

// The selected profile is kept per browser tab (sessionStorage is per tab), so another tab
// of the same project can use a different profile. "none" means the inherited environment;
// nothing stored means "use the project's default profile".
function read(projectId: string): string | undefined {
  try {
    const value = sessionStorage.getItem(`hclaro.profile.${projectId}`) || undefined;
    return value === 'none' || value && id.test(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

function write(projectId: string, value: string) {
  try {
    sessionStorage.setItem(`hclaro.profile.${projectId}`, value);
  } catch {/* Without storage the choice lasts until the page reloads. */}
}

export function profilesQuery(projectId?: string) {
  return {
    queryKey: ['profiles', projectId],
    queryFn: () => api<ProfileList>(`projects/${projectId}/execution-profiles`),
    enabled: !!projectId
  };
}

// useProfileSelection returns the profile this tab uses for a project ("" for none) and a
// setter. A stored choice for a profile that no longer exists falls back to none, never to
// another profile.
export function useProfileSelection(projectId?: string) {
  const profiles = useQuery(profilesQuery(projectId));
  const [stored, setStored] = useState(() => projectId ? read(projectId) : undefined);
  const [memory, setMemory] = useState<string>();
  useEffect(() => {
    const update = () => setStored(projectId ? read(projectId) : undefined);
    update();
    listeners.add(update);
    return () => {
      listeners.delete(update);
    };
  }, [projectId]);
  const list = profiles.data?.profiles || [];
  const choice = stored ?? memory;
  let selected = '';
  if (choice && choice !== 'none') selected = list.some(p => p.id === choice) ? choice : '';
  else if (!choice && profiles.data?.defaultId) selected = profiles.data.defaultId;
  const select = useCallback((value: string) => {
    if (!projectId) return;
    write(projectId, value || 'none');
    setMemory(value || 'none');
    listeners.forEach(l => l());
  }, [projectId]);
  const missing = !!choice && choice !== 'none' && profiles.isSuccess && !list.some(p => p.id === choice);
  return { selected, select, profiles, missing };
}
