import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go parser's golden output keeps the frontend honest about the API contract.
import golden from '../../../fixtures/plan/all-actions.golden.json';
import unsupportedGolden from '../../../fixtures/plan/unknown-actions.golden.json';
import { PlansView } from './PlansView';
import { ApplyDialog } from './ApplyDialog';
import { planRoute } from '../lib/scope';
import type { Approval, Operation, Plan, PlanOptions, PreflightCheck, PreflightReport } from '../types';

const canary = 'DO_NOT_LEAK_HCLARO_TEST_SECRET';
const planId = 'a'.repeat(32);
const { details, ...review } = golden as typeof golden & { details: Record<string, unknown> };
const plan = {
  ...review,
  id: planId,
  operationId: 'b'.repeat(32),
  createdAt: '2026-11-05T09:00:00Z',
  engine: 'terraform',
  currentEngineVersion: '1.9.8',
  workspace: 'demo',
  varFiles: [],
  kind: 'plan',
  replace: [],
  destructive: true,
  artifactSha256: 'c'.repeat(64),
  fingerprint: 'd'.repeat(64),
  contextChanged: false,
  apply: { available: false, artifactRetained: false, reason: 'Remote execution backends are read-only in Hclaro.' }
} as unknown as Plan;
const applicable: Plan = { ...plan, apply: { available: true, artifactRetained: true, expiresAt: '2099-01-01T10:00:00Z' } };
const options: PlanOptions = { canPlan: true, canApply: true, engine: 'terraform', version: '1.9.8', workspace: 'demo', backend: 's3', varFiles: ['prod.tfvars'] };
const operation: Operation = { id: 'b'.repeat(32), kind: 'plan', status: 'succeeded', stage: 'Finished', startedAt: '2026-11-05T09:00:00Z', durationMs: 4200, exitCode: 0, command: ['terraform', 'plan', '-input=false', '-json', '-out=<private temporary file>'], varFiles: [], replace: [], planId, stateReloaded: false, engineDiagnostics: [], cancelRequested: false, logDropped: 0, logRetained: true };
const approval: Approval = { confirmationToken: 'f'.repeat(64), expiresAt: '2026-11-05T09:05:00Z', planId, artifactSha256: 'c'.repeat(64), engine: 'terraform', workspace: 'demo', summary: { create: 1, replace: 2, delete: 2 }, replace: [], imports: 0, destructive: true, phrase: 'apply demo', command: ['terraform', 'apply', '-input=false', '-json', '<reviewed saved plan cccccccccccc>'], acknowledged: ['git.worktree'] };
const check = (id: string, severity: PreflightCheck['severity'], status: PreflightCheck['status'], title: string, evidence: string, extra: Partial<PreflightCheck> = {}): PreflightCheck => ({ id, title, severity, status, evidence, source: 'built-in', ...extra });
const report: PreflightReport = {
  planId, generatedAt: '2026-11-05T09:01:00Z', expiresAt: '2026-11-05T09:11:00Z',
  context: { project: 'platform-infra', engine: 'terraform', engineVersion: '1.9.8', workspace: 'demo', backend: 's3', executionMode: 'local-cli', planKind: 'replace', planCreatedAt: '2026-11-05T09:00:00Z', artifactSha256: 'c'.repeat(64), fingerprint: 'd'.repeat(16), varFiles: [] },
  checks: [
    check('context.identity', 'block', 'pass', 'Engine, version and workspace match the plan', 'Terraform 1.9.8, workspace demo.'),
    check('plan.artifact', 'block', 'pass', 'Reviewed saved plan is intact', 'SHA-256 cccccccccccccccc… matches the reviewed plan.'),
    check('git.worktree', 'warn', 'fail', 'Git working tree is clean', '1 modified and 0 untracked file(s), including main.tf.', { source: 'policy', remediation: 'Commit or stash the changes.' }),
    check('plan.scope', 'warn', 'fail', 'Plan changes only what was requested', 'Besides the 1 requested replacement(s), the plan also changes 2 other resource instance(s).'),
    check('cloud.aws.identity', 'info', 'unknown', 'AWS caller identity verified', 'Not verified. Hclaro does not infer the account from an AWS profile name.')
  ],
  blocked: false, blockers: 0, acknowledge: ['git.worktree', 'plan.scope'],
  impact: {
    summary: { create: 1, replace: 2, delete: 2 }, total: 5, destructive: 4, imports: 0, requested: ['module.compute.aws_instance.main[0]'], extra: 2,
    highRisk: [{ address: 'module.compute.aws_instance.main[0]', action: 'replace', requested: true }, { address: 'aws_lb_target_group_attachment.web[2]', action: 'delete' }],
    changes: [{ address: 'module.compute.aws_instance.main[0]', action: 'replace', requested: true }, { address: 'aws_lb_target_group_attachment.web[2]', action: 'delete' }, { address: 'aws_route53_record.www', action: 'create' }],
    truncated: false
  },
  policy: { source: 'default', version: 1, valid: true, rules: { gitDirty: 'warn', productionLabels: { severity: 'warn', patterns: [] } } }, identity: { state: 'not_requested' },
  binding: 'b'.repeat(64), phrase: 'apply demo', command: ['terraform', 'apply', '-input=false', '-json', '<reviewed saved plan cccccccccccc>']
};

const applyButton = /^Apply (plan|\d+ changes?)$/;
type Route = (url: string, init?: RequestInit) => Response | undefined;
let posts: { url: string; init: RequestInit }[] = [];
function serve(...routes: Route[]) {
  posts = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === 'POST') posts.push({ url, init });
    for (const route of routes) {
      const response = route(url, init);
      if (response) return response;
    }
    if (url.endsWith('/session')) return Response.json({ csrfToken: 'e'.repeat(64) });
    if (url.endsWith('/plans/options')) return Response.json(options);
    if (url.endsWith('/plans')) return Response.json({ plans: [{ id: planId, createdAt: plan.createdAt, changes: 11 }] });
    if (url.endsWith('/operations')) return Response.json({ operations: [operation] });
    if (url.endsWith(`/plans/${planId}`)) return Response.json(plan);
    const change = url.match(/changes\/([0-9a-f]{32})$/);
    if (change) return Response.json(details[change[1]]);
    return Response.json({ error: 'route_not_found' }, { status: 404 });
  }));
}
function view(planChoice?: string, replace?: { id: string; address: string }[], source?: { prefix: string; editor: 'vscode' | 'cursor'; notify: (t: string) => void }) {
  const choose = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlansView project="platform-infra" planId={planChoice} choosePlan={choose} replace={replace} copy={async () => {}} source={source} /></QueryClientProvider>);
  return choose;
}
beforeEach(() => serve());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Plans', () => {
  it('opens a planned change in the editor, or says why it cannot', async () => {
    const found = { state: 'found', confidence: 'block', fileId: 'f'.repeat(32), path: 'main.tf', line: 7, reason: 'Matched the resource block.' };
    let ref: object | Response = found;
    serve(url => url.includes('/source') ? (ref instanceof Response ? ref : Response.json(ref)) : undefined);
    view(undefined, undefined, { prefix: '/api/v1/git/', editor: 'vscode', notify: () => {} });
    await userEvent.click(await screen.findByRole('button', { name: 'Expand aws_s3_bucket.logs' }));
    expect(await screen.findByText('main.tf:7')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Open in VS Code/ })).toBeEnabled();
    cleanup();
    // A block that is not in the files: the button is disabled and the reason is shown.
    ref = { state: 'not_found', reason: 'No block for aws_s3_bucket.logs was found.' };
    view(undefined, undefined, { prefix: '/api/v1/git/', editor: 'vscode', notify: () => {} });
    await userEvent.click(await screen.findByRole('button', { name: 'Expand aws_s3_bucket.logs' }));
    expect(await screen.findByText('No block for aws_s3_bucket.logs was found.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Open in VS Code/ })).toBeDisabled();
    cleanup();
    // Git unavailable.
    ref = Response.json({ error: 'git_unavailable', reason: 'Git context is not available in this session.' }, { status: 409 });
    view(undefined, undefined, { prefix: '/api/v1/git/', editor: 'vscode', notify: () => {} });
    await userEvent.click(await screen.findByRole('button', { name: 'Expand aws_s3_bucket.logs' }));
    expect(await screen.findByText('Source links are unavailable in this session.')).toBeInTheDocument();
    // Three renders with many simulated clicks; allow for a loaded test machine.
  }, 15_000);
  it('shows no source link when the session offers none', async () => {
    view();
    await userEvent.click(await screen.findByRole('button', { name: 'Expand aws_s3_bucket.logs' }));
    await screen.findByLabelText(/Change details for aws_s3_bucket.logs/);
    expect(screen.queryByText('CONFIGURATION SOURCE')).not.toBeInTheDocument();
  });
  it('discards a saved plan only after one inline confirmation, then shows the server note', async () => {
    let discarded = false;
    const note = 'You discarded this plan. The saved plan file was deleted.';
    serve((url, init) => {
      if (init?.method === 'POST' && url.endsWith(`/plans/${planId}/discard`)) {
        discarded = true;
        return Response.json({ apply: { available: false, artifactRetained: false, code: 'not_applicable', reason: note } });
      }
      if (url.endsWith(`/plans/${planId}`)) return Response.json(discarded ? { ...applicable, apply: { available: false, artifactRetained: false, code: 'not_applicable', reason: note } } : applicable);
    });
    view();
    await screen.findByText('Ready to apply.');
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.getByRole('alert')).toHaveTextContent("Delete the saved plan? It can't be applied afterwards.");
    expect(posts.filter(p => p.url.endsWith('/discard'))).toHaveLength(0);
    // Keep backs out without calling the server.
    await userEvent.click(screen.getByRole('button', { name: 'Keep' }));
    expect(screen.queryByText(/Delete the saved plan/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
    await userEvent.click(screen.getByRole('button', { name: 'Delete plan' }));
    await waitFor(() => expect(screen.getByRole('note')).toHaveTextContent('Review only'));
    expect(screen.getByRole('note')).toHaveTextContent(note);
    expect(posts.filter(p => p.url.endsWith('/discard'))).toHaveLength(1);
    expect(screen.queryByRole('button', { name: /Review and apply/ })).not.toBeInTheDocument();
  });
  it('says the configuration could not be read instead of claiming it changed', async () => {
    const reason = 'Hclaro could not read the configuration, so it cannot tell whether it changed since this plan was created: "locked/main.tf" could not be read (permission denied).';
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json({ ...plan, contextChanged: true, apply: { available: false, artifactRetained: true, code: 'configuration_unreadable', reason } }) : undefined);
    view();
    expect(await screen.findByRole('note')).toHaveTextContent('locked/main.tf');
    expect(screen.queryByText(/changed since this plan was created\. Run a new plan/)).not.toBeInTheDocument();
  });
  it('names the unreadable configuration even when another refusal wins', async () => {
    const reason = 'Hclaro could not read the configuration: "locked/main.tf" could not be read (permission denied).';
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json({ ...plan, contextChanged: true, apply: { available: false, artifactRetained: false, code: 'not_applicable', reason: 'Remote execution backends are read-only in Hclaro.', configurationReason: reason } }) : undefined);
    view();
    expect(await screen.findByRole('alert')).toHaveTextContent('locked/main.tf');
    expect(screen.queryByText(/changed since this plan was created\. Run a new plan/)).not.toBeInTheDocument();
  });
  it('shows every planned change, including collateral, and offers no apply when unavailable', async () => {
    view();
    expect(await screen.findByText(/10 resource changes and 4 output changes/)).toBeInTheDocument();
    const counts = screen.getByRole('group', { name: 'Filter by action' });
    for (const [label, count] of [['Create', '1'], ['Update', '3'], ['Replace', '2'], ['Delete', '2'], ['Read', '1'], ['Forget', '1'], ['Unsupported', '0'], ['No change', '2']]) {
      expect(within(counts).getByRole('button', { name: new RegExp(`${label}\\s*${count}`) })).toBeInTheDocument();
    }
    const table = screen.getByRole('region', { name: 'Planned resource changes' });
    expect(within(table).getAllByRole('row')).toHaveLength(11);
    expect(within(table).getByText('aws_iam_role.legacy')).toBeInTheDocument();
    expect(within(table).getByText('The resource was removed from configuration')).toBeInTheDocument();
    expect(within(table).queryByText('module.network.aws_subnet.main[1]')).not.toBeInTheDocument();
    await userEvent.click(screen.getByLabelText('Show unchanged'));
    expect(within(table).getByText('module.network.aws_subnet.main[1]')).toBeInTheDocument();
    expect(screen.getByRole('note')).toHaveTextContent('Review only');
    expect(screen.getByRole('note')).toHaveTextContent('Remote execution backends are read-only');
    expect(screen.queryByRole('button', { name: /apply/i })).not.toBeInTheDocument();
    // Every action in this plan is supported, so there is no unsupported-action warning.
    expect(screen.queryByText(/does not support/)).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(canary);
  });

  it('marks unsupported actions, explains why apply is unavailable and offers no apply', async () => {
    const { details: unsupportedDetails, ...unsupportedReview } = unsupportedGolden as typeof unsupportedGolden & { details: Record<string, unknown> };
    const unsupported = { ...plan, ...unsupportedReview, apply: { available: false, artifactRetained: false, code: 'unknown_actions', reason: 'This plan contains 2 change(s) with an action combination Hclaro does not support (1 resource, 1 output).' } } as unknown as Plan;
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json(unsupported) : undefined, url => { const m = url.match(/changes\/([0-9a-f]{32})$/); return m ? Response.json(unsupportedDetails[m[1]]) : undefined; });
    view();
    const banner = await screen.findByText('Apply unavailable: unsupported actions.');
    expect(banner.closest('[role="alert"]')).toHaveTextContent('does not support (1 resource, 1 output)');
    expect(screen.queryByRole('button', { name: /Review and apply/ })).not.toBeInTheDocument();
    const counts = screen.getByRole('group', { name: 'Filter by action' });
    expect(within(counts).getByRole('button', { name: /Unsupported\s*1/ })).toBeInTheDocument();
    expect(screen.getAllByText(/does not support/).length).toBeGreaterThan(0);
    await userEvent.click(screen.getByRole('button', { name: 'Expand aws_lb.edge' }));
    expect(await screen.findByText(/Unsupported action combination\. Hclaro does not apply a plan containing it/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(canary);
    expect(posts).toHaveLength(0);
  });

  it('filters by action and explains a replacement attribute by attribute', async () => {
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /Replace\s*2/ }));
    const table = screen.getByRole('region', { name: 'Planned resource changes' });
    expect(within(table).getAllByRole('row')).toHaveLength(3);
    // Rows expand in place (aria-expanded) instead of opening a side panel.
    const expand = within(table).getByRole('button', { name: 'Expand module.compute.aws_instance.main[0]' });
    await user.click(expand);
    expect(expand).toHaveAttribute('aria-expanded', 'true');
    const inspector = await screen.findByLabelText('Change details for module.compute.aws_instance.main[0]');
    expect(await within(inspector).findByText('Destroy the existing object, then create a replacement.')).toBeInTheDocument();
    const ami = within(inspector).getByRole('rowheader', { name: /ami/ });
    expect(ami).toHaveTextContent('forces replacement');
    const id = within(inspector).getByRole('rowheader', { name: /^id/ }).closest('tr')!;
    expect(id).toHaveTextContent('i-0aa');
    expect(id).toHaveTextContent('known after apply');
    expect(inspector).not.toHaveTextContent(canary);
    await user.click(within(table).getByRole('button', { name: 'Collapse module.compute.aws_instance.main[0]' }));
    expect(screen.queryByLabelText('Change details for module.compute.aws_instance.main[0]')).not.toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: /Outputs/ }));
    expect(screen.getByText('db_password').closest('tr')).toHaveTextContent('sensitive');
    // Output values are never shown, sensitive or not.
    expect(screen.getByText('db_password').closest('tr')).toHaveTextContent('value hidden');
    expect(screen.getByText(/Output values are never shown in Hclaro/)).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: /Changed outside/ }));
    expect(screen.getByText('aws_s3_bucket.artifacts')).toBeInTheDocument();
  });

  it('requires the preflight before starting a plan and sends the CSRF token', async () => {
    serve((_url, init) => init?.method === 'POST' ? Response.json({ ...operation, status: 'running', planId: undefined }, { status: 202 }) : undefined);
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Run plan' }));
    const dialog = screen.getByRole('dialog', { name: 'Run a plan' });
    expect(dialog).toHaveTextContent('Contacts providers and the s3 backend');
    expect(dialog).toHaveTextContent('May lock the state');
    expect(dialog).toHaveTextContent('Does not change infrastructure');
    expect(posts).toHaveLength(0);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(posts).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Run plan' }));
    await user.click(screen.getByLabelText('prod.tfvars'));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Run plan' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(JSON.parse(posts[0].init.body as string)).toEqual({ acknowledgeBackendAccess: true, varFiles: ['prod.tfvars'], replace: [] });
    expect((posts[0].init.headers as Record<string, string>)['X-Hclaro-CSRF']).toBe('e'.repeat(64));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('enables planning once the workspace finishes loading', async () => {
    let calls = 0;
    serve(url => url.endsWith('/plans/options') ? Response.json({ ...options, canPlan: calls++ > 0 }) : undefined);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const tree = (status: string) => <QueryClientProvider client={client}><PlansView project="p" workspaceStatus={status} choosePlan={() => {}} copy={async () => {}} /></QueryClientProvider>;
    const { rerender } = render(tree('loading'));
    expect(await screen.findByText(/Reading the workspace/)).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Run plan' })).toBeDisabled();
    rerender(tree('ready'));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Run plan' })).toBeEnabled());
  });

  it('explains when planning is unavailable', async () => {
    serve(url => url.endsWith('/plans/options') ? Response.json({ ...options, canPlan: false, reason: 'Remote execution backends run plans remotely.' }) : url.endsWith('/plans') ? Response.json({ plans: [] }) : url.endsWith('/operations') ? Response.json({ operations: [] }) : undefined);
    view();
    expect(await screen.findByText('Remote execution backends run plans remotely.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    expect(screen.getByText('No plans yet')).toBeInTheDocument();
  });

  it('reports a failed plan without offering a plan to review', async () => {
    const failed: Operation = { ...operation, status: 'failed', planId: undefined, exitCode: 1, diagnostic: { code: 'state_locked', message: 'The state is locked by another operation. Hclaro never force-unlocks state.' }, engineDiagnostics: [{ severity: 'error', summary: 'Error acquiring the state lock', detail: '[line withheld: looks like a secret]' }] };
    serve(url => url.endsWith('/plans') ? Response.json({ plans: [] }) : url.endsWith('/operations') ? Response.json({ operations: [failed] }) : undefined);
    view();
    const alert = await screen.findByRole('alert');
    // §2.5: the cause as a title; the lock holder is never named.
    expect(alert).toHaveTextContent('State is locked by another process');
    expect(alert).toHaveTextContent('never force-unlocks');
    expect(within(alert).getByRole('button', { name: 'Try again' })).toBeInTheDocument();
    expect(alert).toHaveTextContent('No plan was kept');
    expect(alert).toHaveTextContent('Error acquiring the state lock');
    expect(screen.queryByText('No plans yet')).not.toBeInTheDocument();
  });

  it('labels an older plan shown below a failed run', async () => {
    const failed: Operation = { ...operation, id: 'f'.repeat(32), status: 'cancelled', planId: undefined, diagnostic: { code: 'cancelled', message: 'The plan was cancelled and no plan was kept.' } };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [failed, operation] }) : undefined);
    view();
    expect(await screen.findByText(/Below is the previous successful plan/)).toBeInTheDocument();
    expect(screen.getAllByRole('alert')[0]).toHaveTextContent('Cancelled');
  });

  it('streams a running plan and cancels it', async () => {
    const running: Operation = { ...operation, status: 'running', stage: 'Planning', planId: undefined, exitCode: undefined };
    const stream = [
      'id: 1\nevent: log\ndata: {"seq":1,"time":"2026-11-05T09:00:00Z","level":"info","text":"aws_s3_bucket.logs: plan to create"}\n\n',
      'event: status\ndata: ' + JSON.stringify(running) + '\n\n'
    ].join('');
    let cancelled = false;
    serve((url, init) => {
      if (url.endsWith('/operations')) return Response.json({ operations: [cancelled ? { ...running, status: 'cancelled' } : running] });
      if (url.includes('/events')) return new Response(stream, { headers: { 'Content-Type': 'text/event-stream' } });
      if (url.endsWith('/cancel') && init?.method === 'POST') {
        cancelled = true;
        return Response.json({ ...running, cancelRequested: true }, { status: 202 });
      }
      if (url.endsWith('/plans')) return Response.json({ plans: [] });
      return undefined;
    });
    view();
    const card = await screen.findByRole('region', { name: 'Planning…' });
    expect(await within(card).findByText('aws_s3_bucket.logs: plan to create')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    await userEvent.click(within(card).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(posts.some(p => p.url.endsWith(`/operations/${running.id}/cancel`))).toBe(true));
  });

  it('plans a replacement with opaque IDs and lists every requested instance', async () => {
    serve((_url, init) => init?.method === 'POST' ? Response.json({ ...operation, status: 'running', planId: undefined }, { status: 202 }) : undefined);
    const targets = [{ id: '1'.repeat(32), address: 'module.compute.aws_instance.main[0]' }, { id: '2'.repeat(32), address: 'module.compute.module.workers["blue.eu"].aws_instance.node["quote\\"key"]' }];
    view(undefined, targets);
    const dialog = await screen.findByRole('dialog', { name: 'Plan a replacement' });
    expect(dialog).toHaveTextContent('module.compute.aws_instance.main[0]');
    expect(dialog).toHaveTextContent('not a targeted one');
    expect(dialog).toHaveTextContent('Hclaro never uses -target');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Plan replacement' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    // Addresses never leave the browser as command input; the server resolves IDs from state.
    expect(JSON.parse(posts[0].init.body as string)).toEqual({ acknowledgeBackendAccess: true, varFiles: [], replace: targets.map(t => t.id) });
  });

  it('shows requested replacements next to collateral changes', async () => {
    const replacePlan = { ...plan, kind: 'replace', replace: ['module.compute.aws_instance.main[0]', 'aws_instance.gone'] } as Plan;
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json(replacePlan) : undefined);
    view();
    const summary = await screen.findByRole('region', { name: 'Requested replacements' });
    expect(summary).toHaveTextContent('Replacement requested for 2 instances');
    expect(within(summary).getByText('aws_instance.gone').closest('li')).toHaveTextContent('not in plan');
    expect(summary).toHaveTextContent(/other changes are part of this plan, including \d+ deletions or replacements you did not select/);
    expect(summary).toHaveTextContent('expect downtime');
    expect(summary).toHaveTextContent('never uses -target');
  });

  it('offers Inspect impact for a replace plan and starts nothing', async () => {
    const replacePlan = { ...plan, kind: 'replace', replace: ['module.compute.aws_instance.main[0]'] } as Plan;
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json(replacePlan) : undefined);
    const inspect = vi.fn();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlansView project="platform-infra" choosePlan={vi.fn()} copy={async () => {}} inspectImpact={inspect} /></QueryClientProvider>);
    const summary = await screen.findByRole('region', { name: 'Requested replacements' });
    expect(summary).toHaveTextContent('It does not change what is applied');
    await userEvent.click(within(summary).getByRole('button', { name: 'Inspect impact' }));
    expect(inspect).toHaveBeenCalledWith(planId, 'module.compute.aws_instance.main[0]');
    expect(posts).toHaveLength(0);
  });

  it('applies only after the server preflight, each warning acknowledged and the typed phrase; declining sends nothing', async () => {
    serve((url, init) => {
      if (url.endsWith(`/plans/${planId}`)) return Response.json(applicable);
      if (url.endsWith('/preflight') && init?.method === 'POST') return Response.json(report);
      if (url.endsWith('/approval') && init?.method === 'POST') return Response.json(approval);
      if (url.endsWith('/apply') && init?.method === 'POST') return Response.json({ ...operation, kind: 'apply', status: 'running' }, { status: 202 });
      return undefined;
    });
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    let dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    // Cancel has initial focus so Enter never applies by accident.
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    const identity = await within(dialog).findByRole('region', { name: 'Context identity' });
    expect(identity).toHaveTextContent('platform-infra');
    expect(identity).toHaveTextContent('Terraform 1.9.8');
    expect(identity).toHaveTextContent('s3 (local-cli)');
    expect(within(dialog).getByRole('list', { name: 'Changes to apply' })).toHaveTextContent('2 replace');
    expect(dialog).toHaveTextContent('terraform apply -input=false -json <reviewed saved plan cccccccccccc>');
    // The data-loss warning and the high-risk list are one box.
    expect(within(dialog).getByText(/Data stored in them can be lost/).closest('.data-loss')).toContainElement(within(dialog).getByRole('list', { name: 'High-risk resources' }));
    // The full plan, not only the selection: collateral changes are listed.
    expect(dialog).toHaveTextContent('The plan also changes 2 other resource instances');
    expect(dialog).toHaveTextContent('Hclaro never uses -target');
    expect(within(dialog).getByRole('list', { name: 'High-risk resources' })).toHaveTextContent('aws_lb_target_group_attachment.web[2]');
    expect(within(dialog).getByRole('list', { name: 'Planned changes' })).toHaveTextContent('aws_route53_record.www');
    // Opening the dialog runs the preflight only; the identity probe is not requested.
    expect(posts.filter(p => p.url.endsWith('/preflight')).map(p => JSON.parse(p.init.body as string))).toEqual([{ verifyIdentity: false }]);
    // Escape declines: nothing was approved or applied.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(posts.filter(p => p.url.endsWith('/approval') || p.url.endsWith('/apply'))).toHaveLength(0);
    await user.click(screen.getByRole('button', { name: 'Review and apply…' }));
    dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    const apply = await within(dialog).findByRole('button', { name: applyButton });
    await user.type(within(dialog).getByLabelText('Confirmation phrase'), 'apply demo');
    // There is no blanket acknowledgement: each warning has its own checkbox.
    const boxes = within(dialog).getAllByRole('checkbox');
    expect(boxes).toHaveLength(2);
    expect(apply).toBeDisabled();
    // Neutral until every condition holds, so the destructive style never invites a click early.
    expect(apply).not.toHaveClass('button-destructive');
    await user.click(boxes[0]);
    expect(apply).toBeDisabled();
    await user.keyboard('{Enter}');
    expect(posts.filter(p => p.url.endsWith('/approval'))).toHaveLength(0);
    await user.click(boxes[1]);
    expect(apply).toBeEnabled();
    expect(apply).toHaveClass('button-destructive');
    // A double click approves and applies once; nothing is retried.
    await user.dblClick(apply);
    await waitFor(() => expect(posts.filter(p => p.url.endsWith('/apply'))).toHaveLength(1));
    expect(posts.filter(p => p.url.endsWith('/approval'))).toHaveLength(1);
    const approvalSent = posts.find(p => p.url.endsWith('/approval'))!;
    expect(JSON.parse(approvalSent.init.body as string)).toEqual({ preflightBinding: report.binding, acknowledged: ['git.worktree', 'plan.scope'] });
    const sent = posts.find(p => p.url.endsWith('/apply'))!;
    expect(JSON.parse(sent.init.body as string)).toEqual({ confirmationToken: approval.confirmationToken, confirmation: 'apply demo' });
    expect((sent.init.headers as Record<string, string>)['X-Hclaro-CSRF']).toBe('e'.repeat(64));
  });

  it('disables apply while a blocker remains and shows its remediation', async () => {
    const blocked: PreflightReport = { ...report, blocked: true, blockers: 1, binding: undefined, acknowledge: [], checks: [check('context.fingerprint', 'block', 'fail', 'Configuration and planning environment are unchanged', 'Configuration changed since this plan was created.', { remediation: 'Run a new plan and review it again.' }), ...report.checks.slice(0, 2)] };
    serve((url, init) => {
      if (url.endsWith(`/plans/${planId}`)) return Response.json(applicable);
      if (url.endsWith('/preflight') && init?.method === 'POST') return Response.json(blocked);
      return undefined;
    });
    view();
    await userEvent.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    const dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    const section = await within(dialog).findByRole('region', { name: 'Blocking' });
    expect(section).toHaveTextContent('Blocks');
    expect(section).toHaveTextContent('Run a new plan and review it again.');
    expect(dialog).toHaveTextContent('1 blocking check failed');
    expect(within(dialog).queryByLabelText('Confirmation phrase')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: applyButton })).toBeDisabled();
  });

  it('verifies the AWS identity only when asked', async () => {
    const verified: PreflightReport = { ...report, identity: { state: 'verified', account: '123456789012', arn: 'arn:aws:sts::123456789012:assumed-role/deploy/s' }, checks: report.checks.map(c => c.id === 'cloud.aws.identity' ? { ...c, status: 'pass', evidence: 'STS reports account 123456789012.' } : c) };
    serve((url, init) => {
      if (url.endsWith(`/plans/${planId}`)) return Response.json(applicable);
      if (url.endsWith('/preflight') && init?.method === 'POST') return Response.json(JSON.parse(init.body as string).verifyIdentity ? verified : report);
      return undefined;
    });
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    const dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    await user.click(await within(dialog).findByRole('button', { name: 'Verify AWS identity' }));
    await waitFor(() => expect(within(dialog).queryByRole('button', { name: 'Verify AWS identity' })).not.toBeInTheDocument());
    expect(posts.filter(p => p.url.endsWith('/preflight')).map(p => JSON.parse(p.init.body as string).verifyIdentity)).toEqual([false, true]);
    expect(dialog).toHaveTextContent('STS reports account 123456789012');
  });

  it('keeps the dialog usable after a failed apply request by running the checks again', async () => {
    let preflights = 0, applies = 0;
    serve((url, init) => {
      if (url.endsWith(`/plans/${planId}`)) return Response.json(applicable);
      if (url.endsWith('/preflight') && init?.method === 'POST') return Response.json({ ...report, binding: String(++preflights).repeat(64) });
      if (url.endsWith('/approval') && init?.method === 'POST') return Response.json(approval);
      if (url.endsWith('/apply') && init?.method === 'POST') return ++applies === 1 ? Response.json({ error: 'preflight_changed', reason: 'The preflight warnings changed since you acknowledged them. Nothing was applied. Run the checks again.' }, { status: 409 }) : Response.json({ ...operation, kind: 'apply', status: 'running' }, { status: 202 });
      return undefined;
    });
    view();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    const dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    const confirm = async () => {
      await user.type(await within(dialog).findByLabelText('Confirmation phrase'), 'apply demo');
      for (const box of within(dialog).getAllByRole('checkbox')) await user.click(box);
      await user.click(within(dialog).getByRole('button', { name: applyButton }));
    };
    await confirm();
    expect(await within(dialog).findByText(/preflight warnings changed/)).toHaveAttribute('role', 'alert');
    // The checks ran again; acknowledgements and the phrase are given again.
    await waitFor(() => expect(preflights).toBe(2));
    expect(await within(dialog).findByLabelText('Confirmation phrase')).toHaveValue('');
    expect(within(dialog).getAllByRole('checkbox').every(b => !(b as HTMLInputElement).checked)).toBe(true);
    await confirm();
    await waitFor(() => expect(posts.filter(p => p.url.endsWith('/apply'))).toHaveLength(2));
    const approvals = posts.filter(p => p.url.endsWith('/approval'));
    expect(JSON.parse(approvals[1].init.body as string).preflightBinding).toBe('2'.repeat(64));
  });

  it('shows why the server refused the preflight', async () => {
    serve((url, init) => {
      if (url.endsWith(`/plans/${planId}`)) return Response.json(applicable);
      if (url.endsWith('/preflight') && init?.method === 'POST') return Response.json({ error: 'plan_not_found' }, { status: 404 });
      return undefined;
    });
    view();
    await userEvent.click(await screen.findByRole('button', { name: 'Review and apply…' }));
    const dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    expect(await within(dialog).findByRole('alert')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: applyButton })).toBeDisabled();
  });

  it('sends a Hub workspace apply only to its own context routes, never the launch routes', async () => {
    const contextId = '9'.repeat(32);
    serve((url, init) => {
      if (url.endsWith('/preflight') && init?.method === 'POST') return Response.json(report);
      if (url.endsWith('/approval') && init?.method === 'POST') return Response.json(approval);
      if (url.endsWith('/apply') && init?.method === 'POST') return Response.json({ ...operation, kind: 'apply', status: 'running' }, { status: 202 });
      return undefined;
    });
    const started = vi.fn();
    render(<ApplyDialog plan={applicable} scope={{ kind: 'context', contextId }} onStarted={started} onClose={() => {}} />);
    const user = userEvent.setup();
    const dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    const apply = await within(dialog).findByRole('button', { name: applyButton });
    await user.type(within(dialog).getByLabelText('Confirmation phrase'), 'apply demo');
    for (const box of within(dialog).getAllByRole('checkbox')) await user.click(box);
    await user.click(apply);
    await waitFor(() => expect(started).toHaveBeenCalledTimes(1));
    expect(posts.map(p => p.url)).toEqual([
      `/api/v1/contexts/${contextId}/plans/${planId}/preflight`,
      `/api/v1/contexts/${contextId}/plans/${planId}/approval`,
      `/api/v1/contexts/${contextId}/plans/${planId}/apply`
    ]);
  });

  it('refuses instead of falling back to the launch project when the workspace context is invalid', async () => {
    render(<ApplyDialog plan={applicable} scope={{ kind: 'context', contextId: '' }} onStarted={() => {}} onClose={() => {}} />);
    const dialog = await screen.findByRole('dialog', { name: /^Apply to / });
    expect(await within(dialog).findByText(/no longer open/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: applyButton })).toBeDisabled();
    expect(posts).toHaveLength(0);
    expect(() => planRoute({ kind: 'context', contextId: '../plans' }, planId, 'apply')).toThrow();
    expect(() => planRoute({ kind: 'launch' }, '../x', 'apply')).toThrow();
    expect(planRoute({ kind: 'launch' }, planId, 'apply')).toBe(`plans/${planId}/apply`);
  });

  it('warns when state could not be read again after an apply', async () => {
    const applied: Operation = { ...operation, kind: 'apply', status: 'succeeded', outcome: 'applied', stateReloaded: false, stateReloadFailed: true, completedAt: '2026-11-05T09:10:00Z' };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [applied, operation] }) : undefined);
    view();
    const alert = await screen.findByText(/State could not be read again after the apply/);
    expect(alert).toBeInTheDocument();
    // Engine success with a failed state read is a warning, never a clean success.
    expect(screen.getByRole('heading', { name: 'Apply finished, state not confirmed' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Apply finished' })).not.toBeInTheDocument();
    const status = screen.getByLabelText('Apply status');
    expect(status).toHaveTextContent('reported the apply as finished');
    expect(status).toHaveTextContent('could not be read again');
    expect(status).toHaveTextContent('Resource healthnot checked');
  });

  it('never reports a saved plan file it could not delete as deleted', async () => {
    const applied: Operation = { ...operation, kind: 'apply', status: 'succeeded', outcome: 'applied', stateReloaded: true, planFileLeft: true, completedAt: '2026-11-05T09:10:00Z' };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [applied, operation] }) : undefined);
    view();
    expect(await screen.findByText(/could not delete the saved plan file/)).toBeInTheDocument();
    expect(screen.queryByText(/was deleted/)).not.toBeInTheDocument();
  });

  it('separates engine success, state read and health on a clean apply', async () => {
    const applied: Operation = { ...operation, kind: 'apply', status: 'succeeded', outcome: 'applied', stateReloaded: true, completedAt: '2026-11-05T09:10:00Z' };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [applied, operation] }) : undefined);
    view();
    expect(await screen.findByRole('heading', { name: 'Applied: 10 changes' })).toBeInTheDocument();
    expect(screen.getByText(/The saved plan was used and deleted/)).toBeInTheDocument();
    const status = screen.getByLabelText('Apply status');
    expect(status).toHaveTextContent('State read again at');
    expect(within(status).getByText('Service health: not checked')).toHaveAttribute('title', expect.stringContaining('Hclaro has no health probe'));
    expect(screen.queryByText(/healthy|verified/i)).not.toBeInTheDocument();
  });

  it('reports an interrupted apply as an uncertain outcome', async () => {
    const interrupted: Operation = { ...operation, kind: 'apply', status: 'cancelled', outcome: 'uncertain', stateReloaded: true, completedAt: '2026-11-05T09:10:00Z', diagnostic: { code: 'apply_cancelled', message: 'The apply was interrupted. Infrastructure may be partly changed.' } };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [interrupted, operation] }) : undefined);
    view();
    const [alert] = await screen.findAllByRole('alert');
    expect(alert).toHaveTextContent('Apply interrupted · outcome uncertain');
    expect(alert).toHaveTextContent('may be partly changed');
    expect(alert).toHaveTextContent('read again at');
    expect(alert).toHaveTextContent('cannot be applied again');
    // Manual inspection steps, and no invitation to apply again.
    expect(alert).toHaveTextContent('Do not apply again from Hclaro');
    expect(alert).toHaveTextContent('never force-unlocks');
    expect(screen.queryByRole('button', { name: /apply/i })).not.toBeInTheDocument();
  });
  it('shows one Run plan button in the empty state and opens the run dialog from it', async () => {
    serve(url => url.endsWith('/plans') ? Response.json({ plans: [] }) : url.endsWith('/operations') ? Response.json({ operations: [] }) : undefined);
    view();
    expect(await screen.findByRole('heading', { name: 'No plans yet' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Run plan' })).toHaveLength(1);
    expect(screen.getByText('How this works')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Run plan' }));
    expect(screen.getByRole('dialog', { name: 'Run a plan' })).toBeInTheDocument();
    expect(posts).toHaveLength(0);
  });

  it('folds the destructive warning into the Ready card next to the apply button', async () => {
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json(applicable) : undefined);
    view();
    const card = (await screen.findByText('Ready to apply.')).closest('.apply-banner')!;
    expect(card).toHaveTextContent('Applies exactly this saved plan, once, until');
    expect(card).toHaveTextContent('Deletes 2, replaces 2 and forgets 1 objects.');
    expect(within(card as HTMLElement).getByRole('button', { name: 'Review and apply…' })).toHaveClass('button-destructive');
  });

  it('never offers apply on a plan whose deadline passed, by this clock or the server', async () => {
    const past = { ...applicable, apply: { ...applicable.apply, expiresAt: '2020-01-01T10:00:00Z' } };
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json(past) : undefined);
    view();
    expect(await screen.findByRole('alert')).toHaveTextContent(/This saved plan expired at .*It can no longer be applied/);
    expect(screen.queryByRole('button', { name: 'Review and apply…' })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Run new plan' }));
    expect(screen.getByRole('dialog', { name: 'Run a plan' })).toBeInTheDocument();
    expect(posts).toHaveLength(0);
    cleanup();
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json({ ...plan, apply: { available: false, artifactRetained: false, code: 'plan_expired', reason: 'This plan is older than 1 hour.' } }) : undefined);
    view();
    expect(await screen.findByText(/This saved plan expired\./)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Review and apply…' })).not.toBeInTheDocument();
  });

  it('replaces the Ready card when the deadline passes while the plan is open', async () => {
    const soon = { ...applicable, apply: { ...applicable.apply, expiresAt: new Date(Date.now() + 1200).toISOString() } };
    serve(url => url.endsWith(`/plans/${planId}`) ? Response.json(soon) : undefined);
    view();
    expect(await screen.findByRole('button', { name: 'Review and apply…' })).toBeInTheDocument();
    expect(await screen.findByText(/This saved plan expired at/, undefined, { timeout: 4000 })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Review and apply…' })).not.toBeInTheDocument();
  });

  it('notes uncommitted IaC files after a clean apply and links to Git and History', async () => {
    const dirty = { ...plan, source: { repository: true, branch: 'main', commit: 'abc1234def', detached: false, dirty: true, dirtyKnown: true, dirtyFiles: 2, contentSha256: '', files: 3, localModules: 0 } } as Plan;
    const applied: Operation = { ...operation, kind: 'apply', status: 'succeeded', outcome: 'applied', stateReloaded: true, completedAt: '2026-11-05T09:10:00Z' };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [applied, operation] }) : url.endsWith(`/plans/${planId}`) ? Response.json(dirty) : undefined);
    const git = vi.fn(), history = vi.fn();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlansView project="platform-infra" choosePlan={vi.fn()} copy={async () => {}} openGit={git} openHistory={history} /></QueryClientProvider>);
    expect(await screen.findByText(/Applied from main @ abc1234 with 2 uncommitted IaC files/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Open Git' }));
    await userEvent.click(screen.getByRole('button', { name: 'View in History' }));
    expect(git).toHaveBeenCalledOnce();
    expect(history).toHaveBeenCalledOnce();
  });
});
