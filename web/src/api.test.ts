import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, initializeSession } from './api';

// M24.12: the bearer token arrives only in the private URL's fragment. It must leave the
// address bar and history at once and never travel in a request URL.
describe('session fragment', () => {
  const token = 'a'.repeat(64);
  afterEach(() => {
    vi.unstubAllGlobals();
    sessionStorage.clear();
    window.history.replaceState(null, '', '/');
  });

  it('moves the token out of the URL into this tab and sends it only as a header', async () => {
    window.history.pushState(null, '', `/projects?view=list#session=${token}`);
    initializeSession();
    expect(window.location.href).not.toContain(token);
    expect(window.location.hash).toBe('');
    expect(window.location.search).toBe('?view=list');
    expect(sessionStorage.getItem('hclaro.session')).toBe(token);
    const fetch = vi.fn(async () => new Response('{}', { status: 200 }));
    vi.stubGlobal('fetch', fetch);
    await api('session');
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/v1/session');
    expect((init.headers as Record<string, string>).Authorization).toBe(`Bearer ${token}`);
  });

  it('ignores a malformed token but still clears the fragment', () => {
    window.history.pushState(null, '', '/#session=not-a-token');
    initializeSession();
    expect(window.location.hash).toBe('');
    expect(sessionStorage.getItem('hclaro.session')).toBeNull();
  });
});
