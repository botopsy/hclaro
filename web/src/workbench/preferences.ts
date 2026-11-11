import { useEffect, useState } from 'react';
import { applyTheme } from '../brand/theme';
import type { EditorName } from '../types';

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
  try {
    const saved = JSON.parse(localStorage.getItem('hclaro.preferences') || '{}');
    return {
      theme: ['system', 'light', 'dark'].includes(saved.theme) ? saved.theme : defaults.theme,
      density: saved.density === 'compact' ? 'compact' : 'comfortable',
      grouping: saved.grouping === 'flat' ? 'flat' : 'module',
      editor: saved.editor === 'cursor' ? 'cursor' : 'vscode',
      sidebar: saved.sidebar === 'compact' ? 'compact' : 'expanded'
    };
  } catch {
    return defaults;
  }
}
// Validated display preferences, saved in this browser and applied as the theme.
export function usePreferences() {
  const [preferences, setPreferences] = useState(readPreferences);
  useEffect(() => {
    try {
      localStorage.setItem('hclaro.preferences', JSON.stringify(preferences));
    } catch {/* Preferences are optional. */}
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
