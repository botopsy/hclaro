let token = '';
export function initializeSession() {
  const params = new URLSearchParams(window.location.hash.slice(1));
  const incoming = params.get('session');
  try {
    if (incoming && /^[a-f0-9]{64}$/.test(incoming)) sessionStorage.setItem('hclaro.session', incoming);
    token = sessionStorage.getItem('hclaro.session') || '';
  } catch {
    token = incoming || '';
  }
  // The credential never enters a request URL, referrer, or state cache.
  window.history.replaceState(null, '', window.location.pathname + window.location.search);
}
export async function api<T>(path: string): Promise<T> {
  const response = await fetch(`/api/v1/${path}`, {
    headers: {
      Authorization: `Bearer ${token}`
    },
    cache: 'no-store'
  });
  if (!response.ok) {
    if (response.status === 401) throw new Error('This session is locked. Open the private URL printed by the running Hclaro CLI.');
    throw new Error('Unable to read the local snapshot. Check that Hclaro is still running, then retry.');
  }
  return response.json() as Promise<T>;
}
