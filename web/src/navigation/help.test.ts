import { afterEach, describe, expect, it, vi } from 'vitest';
import { hasUnseenRelease, markReleaseSeen, releaseUrl } from './help';

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe('Help menu release notes', () => {
  it('shows a release as unseen until its notes were opened in this browser', () => {
    expect(hasUnseenRelease('0.1.0-beta.5')).toBe(true);
    markReleaseSeen('0.1.0-beta.5');
    expect(hasUnseenRelease('0.1.0-beta.5')).toBe(false);
    // A newer build is unseen again.
    expect(hasUnseenRelease('0.1.0-beta.6')).toBe(true);
  });
  it('never marks development or unknown builds as new', () => {
    for (const version of [undefined, '', 'dev', 'test']) {
      expect(hasUnseenRelease(version)).toBe(false);
      markReleaseSeen(version);
    }
    expect(localStorage.getItem('hclaro.seenVersion')).toBeNull();
  });
  it('links to the release of the running build, or to the release list', () => {
    expect(releaseUrl('0.1.0-beta.5')).toBe('https://github.com/botopsy/hclaro/releases/tag/v0.1.0-beta.5');
    expect(releaseUrl('test')).toBe('https://github.com/botopsy/hclaro/releases');
  });
  it('tolerates unavailable storage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(hasUnseenRelease('0.1.0-beta.5')).toBe(true);
    expect(() => markReleaseSeen('0.1.0-beta.5')).not.toThrow();
  });
});
