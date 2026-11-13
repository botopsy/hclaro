import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, initializeSession, post } from './api';

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

// M26.14: guard refusals are JSON; the client shows their reason, refreshes a refused CSRF
// token once, and throws ApiError for every refusal.
describe('request path', () => {
  type Call = { url: string; method: string; csrf?: string };
  let calls: Call[];
  const refusal = (status: number, error: string, reason: string) => Response.json({ error, reason }, { status });
  function stub(answer: (call: Call, n: number) => Response) {
    calls = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
      const call = { url, method: init.method ?? 'GET', csrf: (init.headers as Record<string, string>)['X-Hclaro-CSRF'] };
      calls.push(call);
      return answer(call, calls.length);
    }));
  }
  afterEach(() => vi.unstubAllGlobals());

  it('shows the reason of a guard refusal', async () => {
    stub(() => refusal(415, 'json_required', 'The request must send a JSON body.'));
    await expect(api('plans')).rejects.toMatchObject({ code: 'json_required', message: 'The request must send a JSON body.' });
  });

  it('throws ApiError for a locked session on reads', async () => {
    stub(() => new Response('', { status: 401 }));
    const error = await api('workspace').catch((e: unknown) => e) as ApiError;
    expect(error).toBeInstanceOf(ApiError);
    expect(error.code).toBe('unauthorized');
  });

  it('refetches the CSRF token once after a CSRF refusal', async () => {
    let token = 'old';
    stub(call => {
      if (call.url.endsWith('/session')) return Response.json({ csrfToken: token });
      return call.csrf === 'new' ? Response.json({ ok: true }) : refusal(403, 'csrf_invalid', 'Reload the page.');
    });
    await expect(post('plans', {})).rejects.toMatchObject({ code: 'csrf_invalid' }); // old token cached, refetch still old
    expect(calls.map(c => c.method + ' ' + c.url.slice(8))).toEqual(['GET session', 'POST plans', 'GET session', 'POST plans']);
    token = 'new';
    calls = [];
    await expect(post('plans', {})).resolves.toEqual({ ok: true });
    expect(calls.map(c => c.method + ' ' + (c.csrf ?? ''))).toEqual(['POST old', 'GET ', 'POST new']);
  });

  it('does not retry other refusals', async () => {
    stub(call => call.url.endsWith('/session') ? Response.json({ csrfToken: 'x' }) : refusal(403, 'forbidden_origin', 'Wrong page.'));
    await expect(post('plans', {})).rejects.toMatchObject({ code: 'forbidden_origin', message: 'Wrong page.' });
    expect(calls.filter(c => c.method === 'POST')).toHaveLength(1);
  });
});
