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
  acknowledgement_required: 'Confirm that planning may contact providers and the backend.',
  shutting_down: 'Hclaro is shutting down.',
  log_not_retained: 'The log for this operation is no longer kept. Only the most recent operation logs are retained.'
};
export class ApiError extends Error {
  constructor(public code: string, message: string) {
    super(message);
  }
}
async function failure(response: Response): Promise<never> {
  if (response.status === 401) throw new ApiError('unauthorized', 'This session is locked. Open the private URL printed by the running Hclaro CLI.');
  let code = '';
  try {
    code = (await response.json() as { error?: string }).error || '';
  } catch {/* Non-JSON errors use the generic message. */}
  throw new ApiError(code, messages[code] || 'Unable to reach the local Hclaro server. Check that it is still running, then retry.');
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
// Mutating requests carry the per-launch CSRF token in a header; the server also checks Origin.
export async function post<T>(path: string, body: unknown): Promise<T> {
  if (!csrf) csrf = (await api<{ csrfToken: string }>('session')).csrfToken;
  const response = await fetch(`/api/v1/${path}`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
      'X-Hclaro-CSRF': csrf
    },
    body: JSON.stringify(body),
    cache: 'no-store'
  });
  if (!response.ok) return failure(response);
  return response.json() as Promise<T>;
}
export type StreamHandlers = {
  log: (event: OperationEvent) => void;
  status: (operation: Operation) => void;
  truncated: () => void;
};
// Server-sent events are read with fetch so the session token stays in a header, never a URL.
// Resolves true when the server reports the end of the operation.
export async function streamEvents(id: string, after: number, handlers: StreamHandlers, signal: AbortSignal): Promise<boolean> {
  const response = await fetch(`/api/v1/operations/${id}/events?after=${after}`, {
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: 'text/event-stream'
    },
    cache: 'no-store',
    signal
  });
  if (!response.ok) return failure(response);
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
