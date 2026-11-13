import type { Operation, OperationEvent } from './types';
let token = '';
let csrf = '';
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
const messages: Record<string, string> = {
  operation_running: 'Another operation is already running. Wait for it to finish or cancel it.',
  operation_not_running: 'This operation has already finished.',
  planning_unavailable: 'Planning is not available for this workspace.',
  workspace_loading: 'Hclaro is still reading the workspace. Try again in a moment.',
  invalid_var_files: 'The selected variable files are no longer available. Reopen the plan dialog and choose again.',
  acknowledgement_required: 'Confirm that this may contact providers and the backend.',
  shutting_down: 'Hclaro is shutting down.',
  log_not_retained: 'The log for this operation is no longer kept. Only the most recent operation logs are retained.',
  invalid_replace: 'The selected instances are no longer in the state snapshot, or include data sources. Reselect managed instances and try again.',
  approval_required: 'Confirm the plan again before applying it.',
  approval_invalid: 'The confirmation expired or was already used. Review the plan and confirm again.',
  confirmation_mismatch: 'The typed confirmation did not match. Nothing was applied.',
  preflight_required: 'Run the preflight checks again before applying.',
  preflight_expired: 'The preflight checks expired. Run them again.',
  preflight_blocked: 'A preflight check now blocks this apply. Nothing was applied.',
  preflight_changed: 'The preflight results changed. Review them again. Nothing was applied.',
  scan_not_found: 'This drift scan is no longer kept. Scan again.',
  scan_not_started: 'The drift scan could not be started.',
  invalid_request: 'The request was refused. Reload the page and try again.'
};
export class ApiError extends Error {
  constructor(public code: string, message: string) {
    super(message);
  }
}
// Server-side refusals, including the request guard's, carry a code and a specific reason.
async function failure(response: Response): Promise<ApiError> {
  if (response.status === 401) return new ApiError('unauthorized', 'This session is locked. Open the private URL printed by the running Hclaro CLI.');
  let code = '';
  let reason = '';
  try {
    const body = await response.json() as { error?: string; reason?: string };
    code = body.error || '';
    reason = body.reason || '';
  } catch {/* Non-JSON errors use the generic message. */}
  return new ApiError(code, reason || messages[code] || 'Unable to reach the local Hclaro server. Check that it is still running, then retry.');
}
export type Session = { csrfToken: string; capabilities?: { scopedExecution?: boolean } };
// The one /session read: it serves the capabilities query and the CSRF header of mutations.
// Concurrent callers share the request in flight; nothing is kept after it settles.
let pendingSession: Promise<Session> | undefined;
function fetchSession(): Promise<Session> {
  pendingSession ??= request<Session>('GET', 'session').then(session => {
    csrf = session.csrfToken;
    return session;
  }).finally(() => { pendingSession = undefined; });
  return pendingSession;
}
export const sessionQuery = { queryKey: ['session'], queryFn: fetchSession, staleTime: Infinity };
// Every API call goes through here and throws only ApiError for a refusal. Mutating requests
// carry the per-launch CSRF token in a header (the server also checks Origin); a CSRF refusal
// refetches the token and retries once, since the guard ran no handler.
async function request<T>(method: 'GET' | 'POST' | 'PATCH' | 'DELETE', path: string, body?: unknown, retried = false): Promise<T> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (method !== 'GET') {
    if (!csrf) await fetchSession();
    headers['Content-Type'] = 'application/json';
    headers['X-Hclaro-CSRF'] = csrf;
  }
  const response = await fetch(`/api/v1/${path}`, { method, headers, body: method === 'GET' ? undefined : JSON.stringify(body), cache: 'no-store' });
  if (response.ok) return response.json() as Promise<T>;
  const error = await failure(response);
  if (error.code === 'csrf_invalid' && !retried) {
    await fetchSession();
    return request<T>(method, path, body, true);
  }
  throw error;
}
export function api<T>(path: string): Promise<T> {
  return request<T>('GET', path);
}
export function post<T>(path: string, body: unknown): Promise<T> {
  return request<T>('POST', path, body);
}
export function send<T>(method: 'POST' | 'PATCH' | 'DELETE', path: string, body: unknown): Promise<T> {
  return request<T>(method, path, body);
}
// A new browser tab has its own sessionStorage, so the session travels in the URL fragment,
// which is never sent to the server and is removed from history on arrival.
export function openInNewTab(path: string) {
  window.open(`${path}#session=${token}`, '_blank', 'noopener');
}
export type StreamHandlers = {
  log: (event: OperationEvent) => void;
  status: (operation: Operation) => void;
  truncated: () => void;
};
// Server-sent events are read with fetch so the session token stays in a header, never a URL.
// Resolves true when the server reports the end of the operation. path is the operation's
// route, for example "operations/<id>" or a Hub workspace's "contexts/<cid>/operations/<id>".
export async function streamEvents(path: string, after: number, handlers: StreamHandlers, signal: AbortSignal): Promise<boolean> {
  const response = await fetch(`/api/v1/${path}/events?after=${after}`, {
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: 'text/event-stream'
    },
    cache: 'no-store',
    signal
  });
  if (!response.ok) throw await failure(response);
  if (!response.body) return false;
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let ended = false;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary;
    while ((boundary = buffer.indexOf('\n\n')) >= 0) {
      const block = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      let event = 'message';
      let data = '';
      for (const line of block.split('\n')) {
        if (line.startsWith('event: ')) event = line.slice(7);
        else if (line.startsWith('data: ')) data += line.slice(6);
      }
      if (!data) continue;
      const payload = JSON.parse(data);
      if (event === 'log') handlers.log(payload as OperationEvent);
      else if (event === 'status') handlers.status(payload as Operation);
      else if (event === 'truncated') handlers.truncated();
      else if (event === 'end') ended = true;
    }
  }
  return ended;
}
// download saves an authenticated GET response as a file. The bearer token stays in a header;
// the browser receives only a local object URL.
export async function download(path: string, filename: string) {
  const response = await fetch(`/api/v1/${path}`, {
    headers: {
      Authorization: `Bearer ${token}`
    },
    cache: 'no-store'
  });
  if (!response.ok) throw await failure(response);
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.rel = 'noopener';
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
