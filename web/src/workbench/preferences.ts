import { useEffect, useState } from 'react';
import { applyTheme } from '../brand/theme';
import type { EditorName } from '../types';
import { readJSON, storageKeys, writeStorage } from '../lib/storage';

export type Preferences = {
  theme: 'system' | 'light' | 'dark';
  density: 'comfortable' | 'compact';
  grouping: 'module' | 'flat';
  // The editor that "Open in editor" links use; only these names exist on the server.
  editor: EditorName;
  // Desktop sidebar: labelled (default) or icon-only. Narrow screens always use a drawer.
  sidebar: 'expanded' | 'compact';
};
const defaults: Preferences = {
  theme: 'system',
  density: 'comfortable',
  grouping: 'module',
  editor: 'vscode',
  sidebar: 'expanded'
};
function readPreferences(): Preferences {
  const saved = (readJSON('local', storageKeys.preferences) ?? {}) as Partial<Record<keyof Preferences, unknown>>;
  if (typeof saved !== 'object') return defaults;
  return {
    theme: saved.theme === 'light' || saved.theme === 'dark' ? saved.theme : defaults.theme,
    density: saved.density === 'compact' ? 'compact' : 'comfortable',
    grouping: saved.grouping === 'flat' ? 'flat' : 'module',
    editor: saved.editor === 'cursor' ? 'cursor' : 'vscode',
    sidebar: saved.sidebar === 'compact' ? 'compact' : 'expanded'
  };
}
// Validated display preferences, saved in this browser and applied as the theme.
export function usePreferences() {
  const [preferences, setPreferences] = useState(readPreferences);
  useEffect(() => {
    writeStorage('local', storageKeys.preferences, JSON.stringify(preferences));
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const apply = () => applyTheme(preferences.theme);
    apply();
    media.addEventListener('change', apply);
    return () => media.removeEventListener('change', apply);
  }, [preferences]);
  const changePreference = (change: Partial<Preferences>) => setPreferences(p => ({
    ...p,
    ...change
  }));
  return { preferences, changePreference };
}
// Whether a CSS media query currently matches; follows changes.
export function useMedia(query: string) {
  const [matches, setMatches] = useState(() => typeof window.matchMedia === 'function' && window.matchMedia(query).matches);
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return;
    const media = window.matchMedia(query);
    const update = () => setMatches(media.matches);
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, [query]);
  return matches;
}
