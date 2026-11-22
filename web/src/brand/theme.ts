import { readJSON, storageKeys } from '../lib/storage';
// Theme resolution shared by the first paint (main.tsx) and the Settings preference (App.tsx).
// The stylesheet already follows the system palette through color-scheme before any script runs;
// applying the saved preference before React renders keeps a pinned theme from flashing.
type ThemePreference = 'system' | 'light' | 'dark';


export function savedTheme(): ThemePreference {
  const theme = (readJSON('local', storageKeys.preferences) as { theme?: unknown } | null | undefined)?.theme;
  return theme === 'light' || theme === 'dark' ? theme : 'system';
}

export function resolveTheme(preference: ThemePreference): 'light' | 'dark' {
  if (preference !== 'system') return preference;
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function applyTheme(preference: ThemePreference) {
  const theme = resolveTheme(preference);
  document.documentElement.dataset.theme = theme;
  // The browser chrome colour of each theme is the page background. index.html carries it per
  // media query (filled from tokens.css at build); remember the originals before pinning one.
  const metas = [...document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')];
  for (const meta of metas) meta.dataset.colour ??= meta.content;
  const colour = metas.find(meta => meta.getAttribute('media')?.includes(theme))?.dataset.colour;
  if (colour) for (const meta of metas) meta.content = colour;
  return theme;
}
