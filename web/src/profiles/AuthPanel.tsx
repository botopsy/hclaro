import { useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleCheck, CircleHelp, CircleX, KeyRound, LoaderCircle, LogIn, RefreshCw, ShieldAlert, Terminal, UserCheck, X } from 'lucide-react';
import { api, send } from '../api';
import type { AuthState, AuthStatus } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { errorMessage } from '../lib/errorMessage';

const labels: Record<AuthState, string> = {
  not_configured: 'No AWS profile',
  tool_missing: 'Missing tool',
  not_checked: 'Not checked',
  checking: 'Checking…',
  verified: 'Verified',
  auth_required: 'Authentication required',
  access_denied_or_error: 'Error',
  identity_mismatch: 'Mismatch',
  unknown: 'Unknown'
};

function tone(state: AuthState) {
  if (state === 'verified') return 'ok';
  if (state === 'identity_mismatch' || state === 'access_denied_or_error' || state === 'tool_missing') return 'bad';
  return 'warn';
}

function StateIcon({ state }: { state: AuthState }) {
  if (state === 'checking') return <LoaderCircle className="spin" size={12} aria-hidden="true" />;
  if (state === 'verified') return <CircleCheck size={12} aria-hidden="true" />;
  if (tone(state) === 'bad') return <CircleX size={12} aria-hidden="true" />;
  if (state === 'not_configured') return <ShieldAlert size={12} aria-hidden="true" />;
  return <CircleHelp size={12} aria-hidden="true" />;
}

const errorText = (e: unknown) => (errorMessage(e, 'The request failed.'));

const authQuery = (projectId: string, profileId: string) => ({
  queryKey: ['aws-auth', projectId, profileId],
  // Reading the status is local: it never contacts AWS. While a sign-in runs, the status is
  // read again so its end shows; STS is never polled.
  queryFn: () => api<AuthStatus>(`projects/${projectId}/execution-profiles/${profileId}/aws`),
  refetchInterval: (q: { state: { data?: AuthStatus } }) => (q.state.data?.login?.state === 'running' ? 2000 : false),
  staleTime: 30_000
});

// AuthStateBadge says the state in words, never by color alone.
function AuthStateBadge({ status }: { status: AuthStatus }) {
  return <span className={`auth-badge auth-${tone(status.state)}`}><StateIcon state={status.state} />{labels[status.state]}</span>;
}

// AuthPanel is the AWS authentication of one execution profile: the selected method and
// profile, the expected-account guard, the last STS check and explicit verify and sign-in
// actions. Sign-in runs the AWS CLI's own browser flow; this page never sees a sign-in URL,
// code or credential, and nothing that was waiting resumes after a sign-in.
export function AuthPanel({ projectId, profileId }: { projectId: string; profileId: string }) {
  const client = useQueryClient();
  const status = useQuery(authQuery(projectId, profileId));
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const base = `projects/${projectId}/execution-profiles/${profileId}/aws`;
  async function act(name: string, run: () => Promise<unknown>) {
    setBusy(name);
    setError('');
    try {
      const next = await run();
      if (next && typeof next === 'object' && 'state' in next) client.setQueryData(['aws-auth', projectId, profileId], next);
      else await client.invalidateQueries({ queryKey: ['aws-auth', projectId, profileId] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy('');
    }
  }
  if (status.isPending) return <p role="status" className="muted"><LoaderCircle className="spin" size={12} /> Detecting AWS tools…</p>;
  if (status.error) return <p className="dialog-error" role="alert">{errorText(status.error)}</p>;
  const s = status.data;
  const running = s.login?.state === 'running';
  const method = s.method === 'aws-vault' ? 'aws-vault' : 'AWS CLI';
  return <section className="auth-panel" aria-label="AWS authentication">
   <div className="auth-heading"><h3>AWS authentication</h3><AuthStateBadge status={s} /></div>
   <dl className="metadata compact-metadata">
    <div><dt>Method</dt><dd>{s.awsProfile ? method : <span className="muted">Inherited environment</span>}{s.awsProfile && <span className="muted"> · {s.method === 'aws-vault' ? (s.tools.awsVault.available ? `aws-vault ${s.tools.awsVault.version || ''}` : 'aws-vault not found') : s.tools.aws.available ? `aws ${s.tools.aws.version || ''}` : 'aws not found'}</span>}</dd></div>
    <div><dt>AWS profile</dt><dd>{s.awsProfile ? <code>{s.awsProfile}</code> : <span className="muted">None</span>}{s.profileKind === 'sso' && <span className="muted"> · IAM Identity Center</span>}</dd></div>
    <div><dt>Region</dt><dd>{s.region ? <code>{s.region}</code> : <span className="muted">Inherited</span>}</dd></div>
    <div><dt>Expected account</dt><dd>{s.expectedAccount ? <code>{s.expectedAccount}</code> : <span className="muted">No guard</span>}{s.expectedRole && <> · role <code>{s.expectedRole}</code></>}</dd></div>
    <div><dt>CLI execution identity</dt><dd>{s.account ? <><code>{s.account}</code> · <code className="auth-arn">{s.arn}</code></> : <span className="muted">—</span>}</dd></div>
    <div><dt>Last checked</dt><dd>{s.checkedAt ? new Date(s.checkedAt).toLocaleString() : <span className="muted">Never in this session</span>}</dd></div>
   </dl>
   <p className={`auth-message auth-${tone(s.state)}`} role="status">{s.message}</p>
   {s.login && <p className="auth-message" role="status"><LogIn size={12} aria-hidden="true" /> {s.login.mode === 'sso' ? 'aws sso login' : 'aws login'}: {s.login.message}</p>}
   {s.awsProfile && <div className="auth-actions">
    <Button variant="outline" size="small" onClick={() => void act('verify', () => send('POST', `${base}/verify`, {}))} disabled={!!busy || running || s.state === 'tool_missing'}><UserCheck size={13} />{busy === 'verify' ? 'Verifying…' : s.checkedAt ? 'Verify again' : 'Verify identity'}</Button>
    {s.canSsoLogin && <Button size="small" onClick={() => void act('sso', () => send('POST', `${base}/login`, { mode: 'sso' }))} disabled={!!busy || running}><LogIn size={13} />Sign in with AWS (SSO)</Button>}
    {s.canLogin && <Button variant="outline" size="small" onClick={() => void act('login', () => send('POST', `${base}/login`, { mode: 'login' }))} disabled={!!busy || running}><KeyRound size={13} />Sign in with aws login</Button>}
    {running && <Button variant="ghost" size="small" onClick={() => void act('cancel', () => send('POST', `${base}/login/cancel`, {}))} disabled={busy === 'cancel'}><X size={13} />Cancel sign-in</Button>}
    <Button variant="ghost" size="small" onClick={() => void act('tools', async () => { await api(`auth/aws/tools?refresh=1`); })} disabled={!!busy} title="Detect aws and aws-vault again (local only)"><RefreshCw size={13} />Detect tools</Button>
   </div>}
   {s.awsProfile && s.tools.aws.available && !s.tools.aws.supported && <p className="muted">{s.tools.aws.message} Verify identity still works; sign-in needs AWS CLI v2.</p>}
   {s.tools.loginMessage && s.awsProfile && s.method !== 'aws-vault' && <p className="muted">{s.tools.loginMessage}</p>}
   {s.commands.length > 0 && <details className="auth-commands"><summary>Terminal commands</summary><ul className="plain-list">{s.commands.map(c => <li key={c.command}><span className="muted">{c.label}</span><span className="auth-command"><code>{c.command}</code><Button size="small" variant="ghost" aria-label={`Copy: ${c.command}`} onClick={() => void navigator.clipboard.writeText(c.command).then(() => setNote('Command copied'), () => setNote('Clipboard is unavailable. Select and copy the command manually.'))}><Terminal size={12} />Copy</Button></span></li>)}</ul><span className="muted" role="status">{note}</span><p className="muted">After running one, choose Verify again. Hclaro never asks for passwords or MFA codes.</p></details>}
   <p className="muted auth-disclosure">{s.disclosure} Verify contacts AWS and may run a configured credential_process; Hclaro never checks automatically.</p>
   {error && <p className="dialog-error" role="alert">{error}</p>}
  </section>;
}

// AuthIndicator is the header's authentication state for the profile chosen in this tab. It
// reads only the local status; the panel's actions are explicit.
export function AuthIndicator({ projectId, profileId }: { projectId: string; profileId: string }) {
  const status = useQuery(authQuery(projectId, profileId));
  const [open, setOpen] = useState(false);
  if (!status.data || status.data.state === 'not_configured') return null;
  return <>
   <Button variant="ghost" size="small" className="auth-indicator" onClick={() => setOpen(true)} title="AWS authentication of this profile">
    <AuthStateBadge status={status.data} />{status.data.awsProfile && <code>{status.data.awsProfile}</code>}
   </Button>
   {open && <AuthDialog projectId={projectId} profileId={profileId} onClose={() => setOpen(false)} />}
  </>;
}

function AuthDialog({ projectId, profileId, onClose }: { projectId: string; profileId: string; onClose: () => void }) {
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, close, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="auth-title">
    <div className="dialog-header"><h2 id="auth-title">AWS sign-in</h2><Button ref={close} variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <AuthPanel projectId={projectId} profileId={profileId} />
   </div>
  </div>;
}
