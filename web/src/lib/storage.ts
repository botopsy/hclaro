// Browser storage keys, and access that treats storage as optional: private windows may block
// it, so a read falls back to undefined and a write is dropped.
export const storageKeys = {
  session: 'hclaro.session',
  preferences: 'hclaro.preferences',
  drawerWidth: 'hclaro.drawerWidth',
  compareDraft: 'hclaro.compare.draft',
  compareId: 'hclaro.compare.id',
  // The build version whose release notes the Help menu last showed (unseen dot).
  seenVersion: 'hclaro.seenVersion',
  // The newer version whose update notice was dismissed (M35).
  updateDismissed: 'hclaro.updateDismissed',
  // Per tab: another tab of the same project can use a different profile.
  profile: (projectId: string) => `hclaro.profile.${projectId}`
};

type Area = 'local' | 'session';
const area = (where: Area) => where === 'local' ? localStorage : sessionStorage;

export function readStorage(where: Area, key: string): string | undefined {
  try {
    return area(where).getItem(key) ?? undefined;
  } catch {
    return undefined;
  }
}

export function writeStorage(where: Area, key: string, value: string) {
  try {
    area(where).setItem(key, value);
  } catch {/* Storage is a convenience. */}
}

// readJSON parses a stored JSON value; the caller validates its shape.
export function readJSON(where: Area, key: string): unknown {
  try {
    return JSON.parse(readStorage(where, key) ?? 'null');
  } catch {
    return undefined;
  }
}
