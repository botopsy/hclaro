// Theme resolution shared by the first paint (main.tsx) and the Settings preference (App.tsx).
// The stylesheet already follows the system palette through color-scheme before any script runs;
// applying the saved preference before React renders keeps a pinned theme from flashing.
export type ThemePreference = 'system' | 'light' | 'dark';

// The browser chrome colour of each theme is the page background: navy or ivory.
const chrome = { dark: '#0B1220', light: '#F4F4F0' } as const;

export function savedTheme(): ThemePreference {
  try {
    const theme = JSON.parse(localStorage.getItem('hclaro.preferences') || '{}').theme;
    return theme === 'light' || theme === 'dark' ? theme : 'system';
  } catch {
    return 'system';
  }
}

export function resolveTheme(preference: ThemePreference): 'light' | 'dark' {
  if (preference !== 'system') return preference;
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function applyTheme(preference: ThemePreference) {
  const theme = resolveTheme(preference);
  document.documentElement.dataset.theme = theme;
  document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]').forEach(meta => {
    meta.content = chrome[theme];
  });
  return theme;
}
