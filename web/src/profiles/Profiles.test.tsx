import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PlansView } from '../plans/PlansView';
import { ContextSelector } from './ContextSelector';
import { AuthPanel } from './AuthPanel';
import { useProfileSelection } from './selection';
import type { AuthStatus, ExecutionProfile, ExecutionSummary, PlanOptions } from '../types';

const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const projectId = '1'.repeat(32);
const prodId = '2'.repeat(32);
const devId = '3'.repeat(32);
const profile = (id: string, name: string, extra: Partial<ExecutionProfile> = {}): ExecutionProfile => ({ id, projectId, name, engine: '', workspace: '', varFiles: [], parallelism: 0, lockTimeoutSeconds: 0, awsProfile: '', awsRegion: '', awsAuth: '', awsExpectedAccount: '', awsExpectedRole: '', envNames: [], revision: 1, createdAt: '2026-11-05T09:00:00Z', updatedAt: '2026-11-05T09:00:00Z', default: false, ...extra });
const profiles = [profile(prodId, 'Production', { default: true, awsProfile: 'prod-sso', varFiles: ['env/prod.tfvars'] }), profile(devId, 'Development', { awsProfile: 'dev-sso' })];
const summary = (fingerprint: string, issues: ExecutionSummary['issues'] = []): ExecutionSummary => ({
  projectId, alias: 'network', rootPath: '/work/network', status: 'ready', backend: 's3', executionMode: 'local-cli', engine: 'terraform', engineFound: true, engineVersion: '1.9.8', workspace: 'demo', workspaceSource: 'launch', dataDir: '.terraform',
  context: { projectId, profileId: prodId, profileName: 'Production', profileRevision: 1, varFiles: [{ ref: 'env/prod.tfvars', path: 'env/prod.tfvars', size: 12, modifiedAt: '2026-11-05T09:00:00Z', linked: false }], options: { parallelism: 4 }, awsProfile: 'prod-sso', awsRegion: 'eu-west-1', env: [{ name: 'TF_VAR_db_password', present: true }], identity: 'not_verified', notices: [], issues, fingerprint },
  issues
});
const options = (s?: ExecutionSummary): PlanOptions => ({ canPlan: true, canApply: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 's3', varFiles: ['prod.tfvars'], projectId, context: s });

const tools = { aws: { available: true, version: '2.17.0', supported: true }, awsVault: { available: false, supported: false }, ssoLogin: true, login: false, detectedAt: '2026-11-16T00:00:00Z' };
const authStatus = (extra: Partial<AuthStatus> = {}): AuthStatus => ({ projectId, profileId: prodId, profileName: 'Production', profileRevision: 1, method: 'aws-cli', awsProfile: 'prod-sso', region: 'eu-west-1', expectedAccount: '111122223333', profileKind: 'sso', state: 'not_checked', message: 'Not checked.', canSsoLogin: true, canLogin: false, commands: [{ label: 'Sign in with IAM Identity Center', command: 'aws sso login --profile prod-sso' }], disclosure: 'CLI execution identity.', tools, ...extra });
let auth = authStatus();
let posts: { url: string; body: Record<string, unknown> }[] = [];
let planOptions = options(summary('f'.repeat(64)));
function serve() {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method && init.method !== 'GET') posts.push({ url, body: JSON.parse(String(init.body)) });
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.includes('/plans/options')) return Response.json(planOptions);
    if (url.endsWith('/aws/verify')) return Response.json(auth = authStatus({ state: 'verified', account: '111122223333', arn: 'arn:aws:sts::111122223333:assumed-role/Deploy/me', checkedAt: '2026-11-16T10:00:00Z', message: 'Verified when last checked.' }));
    if (url.endsWith('/aws/login')) return Response.json(auth = authStatus({ login: { id: 'l', mode: 'sso', state: 'completed', message: 'Login process completed — verify identity.', startedAt: '2026-11-16T10:00:00Z' } }), { status: 202 });
    if (url.endsWith('/aws')) return Response.json(auth);
    if (url.endsWith('/execution-profiles')) return init?.method === 'POST' ? Response.json(profile('4'.repeat(32), 'New'), { status: 201 }) : Response.json({ profiles, defaultId: prodId, status: 'ready', notice: '' });
    if (url.endsWith('/var-files')) return Response.json({ files: [{ ref: 'env/prod.tfvars', path: 'env/prod.tfvars', size: 12, modifiedAt: '2026-11-05T09:00:00Z', linked: false }], truncated: false });
    if (url.endsWith('/plans')) return init?.method === 'POST' ? Response.json({ id: 'a'.repeat(32) }, { status: 202 }) : Response.json({ plans: [] });
    if (url.endsWith('/operations')) return Response.json({ operations: [] });
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function client() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}
function Selector() {
  const selection = useProfileSelection(projectId);
  return <ContextSelector projectId={projectId} selection={selection} />;
}
beforeEach(() => {
  sessionStorage.clear();
  planOptions = options(summary('f'.repeat(64)));
  auth = authStatus();
  serve();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Execution profiles', () => {
  it('starts from the project default and keeps the choice per tab', async () => {
    render(<QueryClientProvider client={client()}><Selector /></QueryClientProvider>);
    const select = await screen.findByLabelText('Profile') as HTMLSelectElement;
    await waitFor(() => expect(select.value).toBe(prodId));
    // The header shows the profile's AWS state in words; reading it never contacts AWS.
    expect(await screen.findByText('Not checked')).toBeInTheDocument();
    await userEvent.selectOptions(select, devId);
    expect(sessionStorage.getItem(`hclaro.profile.${projectId}`)).toBe(devId);
    // Choosing a profile is a tab-local choice: nothing is sent to the server.
    expect(posts).toEqual([]);
    await userEvent.selectOptions(select, '');
    expect(sessionStorage.getItem(`hclaro.profile.${projectId}`)).toBe('none');
    expect(screen.queryByText('Not checked')).not.toBeInTheDocument();
  });

  it('reviews the full context before planning and binds its fingerprint', async () => {
    sessionStorage.setItem(`hclaro.profile.${projectId}`, prodId);
    render(<QueryClientProvider client={client()}><PlansView project="network" profileId={prodId} choosePlan={() => {}} copy={async () => {}} /></QueryClientProvider>);
    await userEvent.click(await screen.findByRole('button', { name: 'Run plan' }));
    const dialog = await screen.findByRole('dialog');
    const review = within(dialog).getByRole('region', { name: 'This plan will use' });
    // A profile with an AWS profile shows its AWS state (read locally, never polled).
    expect(await within(review).findByText('Not checked')).toBeInTheDocument();
    for (const text of ['Production', 'prod-sso', 'eu-west-1', '/work/network', 'TF_VAR_db_password', 'set (value not shown)']) {
      expect(within(review).getAllByText(new RegExp(text.replace(/[/()]/g, '\\$&'))).length).toBeGreaterThan(0);
    }
    expect(within(review).getByText(/not an AWS profile or a cloud account/)).toBeInTheDocument();
    // The profile's approved file starts checked; the root file is optional.
    expect(within(dialog).getByRole('checkbox', { name: /env\/prod\.tfvars/ })).toBeChecked();
    expect(within(dialog).getByRole('checkbox', { name: /^prod\.tfvars/ })).not.toBeChecked();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Run plan' }));
    await waitFor(() => expect(posts.some(p => p.url.endsWith('/plans'))).toBe(true));
    expect(posts.find(p => p.url.endsWith('/plans'))!.body).toEqual({ acknowledgeBackendAccess: true, varFiles: ['env/prod.tfvars'], replace: [], profileId: prodId, contextFingerprint: 'f'.repeat(64) });
    expect(document.body.textContent).not.toContain(canary);
  });

  it('blocks planning while the context has issues', async () => {
    planOptions = options(summary('f'.repeat(64), [{ code: 'var_file_missing', message: 'env/prod.tfvars was not found in the project root. Restore it, or remove it from the profile.' }]));
    render(<QueryClientProvider client={client()}><PlansView project="network" profileId={prodId} choosePlan={() => {}} copy={async () => {}} /></QueryClientProvider>);
    await userEvent.click(await screen.findByRole('button', { name: 'Run plan' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/was not found in the project root/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Run plan' })).toBeDisabled();
  });

  it('creates a profile with names only', async () => {
    render(<QueryClientProvider client={client()}><Selector /></QueryClientProvider>);
    await userEvent.click(await screen.findByRole('button', { name: 'Manage' }));
    const dialog = await screen.findByRole('dialog', { name: 'Execution profiles' });
    await userEvent.click(within(dialog).getByRole('button', { name: 'New profile' }));
    await userEvent.type(within(dialog).getByLabelText(/Profile name/), 'Staging');
    await userEvent.type(within(dialog).getByLabelText(/Workspace selector/), 'staging');
    await userEvent.click(await within(dialog).findByRole('checkbox', { name: /env\/prod\.tfvars/ }));
    await userEvent.type(within(dialog).getByLabelText(/^AWS profile/), 'staging-sso');
    await userEvent.type(within(dialog).getByLabelText(/^AWS region/), 'eu-west-1');
    await userEvent.type(within(dialog).getByLabelText(/Expected account ID/), '111122223333');
    await userEvent.type(within(dialog).getByLabelText(/Expected environment variable names/), 'VAULT_ADDR, TF_VAR_x');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create profile' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].url).toMatch(/\/projects\/1{32}\/execution-profiles$/);
    expect(posts[0].body).toEqual({ name: 'Staging', engine: '', workspace: 'staging', varFiles: ['env/prod.tfvars'], parallelism: 0, lockTimeoutSeconds: 0, awsProfile: 'staging-sso', awsRegion: 'eu-west-1', awsAuth: '', awsExpectedAccount: '111122223333', awsExpectedRole: '', envNames: ['VAULT_ADDR', 'TF_VAR_x'] });
    // The new profile becomes this tab's choice.
    await waitFor(() => expect(sessionStorage.getItem(`hclaro.profile.${projectId}`)).toBe('4'.repeat(32)));
  });

  it('verifies and signs in only on explicit actions, and never claims a sign-in verified', async () => {
    render(<QueryClientProvider client={client()}><AuthPanel projectId={projectId} profileId={prodId} /></QueryClientProvider>);
    const panel = await screen.findByRole('region', { name: 'AWS authentication' });
    expect(within(panel).getByText('Not checked')).toBeInTheDocument();
    expect(within(panel).getByText('111122223333')).toBeInTheDocument();
    expect(posts).toEqual([]);
    await userEvent.click(within(panel).getByRole('button', { name: /Sign in with AWS \(SSO\)/ }));
    await waitFor(() => expect(posts.map(p => p.body)).toEqual([{ mode: 'sso' }]));
    expect(await within(panel).findByText(/Login process completed — verify identity/)).toBeInTheDocument();
    expect(within(panel).queryByText('Verified')).not.toBeInTheDocument();
    await userEvent.click(within(panel).getByRole('button', { name: /Verify identity/ }));
    await waitFor(() => expect(posts.at(-1)).toEqual({ url: expect.stringMatching(/\/execution-profiles\/2{32}\/aws\/verify$/), body: {} }));
    expect(await within(panel).findByText('Verified')).toBeInTheDocument();
    expect(within(panel).getByText('arn:aws:sts::111122223333:assumed-role/Deploy/me')).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(canary);
  });
});
