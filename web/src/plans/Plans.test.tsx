import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
// The Go parser's golden output keeps the frontend honest about the API contract.
import golden from '../../../fixtures/plan/all-actions.golden.json';
import unsupportedGolden from '../../../fixtures/plan/unknown-actions.golden.json';
import { PlansView } from './PlansView';
import { ApplyDialog } from './ApplyDialog';
import { planRoute } from './routes';
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
const applicable: Plan = { ...plan, apply: { available: true, artifactRetained: true, expiresAt: '2026-11-05T10:00:00Z' } };
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
  policy: { source: 'default', version: 1, valid: true }, identity: { state: 'not_requested' },
  binding: 'b'.repeat(64), phrase: 'apply demo', command: ['terraform', 'apply', '-input=false', '-json', '<reviewed saved plan cccccccccccc>']
};

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
function view(planChoice?: string, replace?: { id: string; address: string }[]) {
  const choose = vi.fn();
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><PlansView project="platform-infra" planId={planChoice} choosePlan={choose} replace={replace} copy={async () => {}} /></QueryClientProvider>);
  return choose;
}
beforeEach(() => serve());
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('Plans', () => {
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
    await userEvent.click(screen.getByText('aws_lb.edge'));
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
    await user.click(within(table).getByRole('button', { name: /module\.compute\.aws_instance\.main\[0\]/ }));
    const inspector = await screen.findByRole('complementary', { name: 'Change details' });
    expect(await within(inspector).findByText('Destroy the existing object, then create a replacement.')).toBeInTheDocument();
    const ami = within(inspector).getByRole('rowheader', { name: /ami/ });
    expect(ami).toHaveTextContent('forces replacement');
    const id = within(inspector).getByRole('rowheader', { name: /^id/ }).closest('tr')!;
    expect(id).toHaveTextContent('i-0aa');
    expect(id).toHaveTextContent('known after apply');
    expect(inspector).not.toHaveTextContent(canary);
    await user.click(within(inspector).getByRole('button', { name: 'Close change details' }));
    await user.click(screen.getByRole('tab', { name: /Outputs/ }));
    expect(screen.getByText('db_password').closest('tr')).toHaveTextContent('sensitive');
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
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
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
    expect(alert).toHaveTextContent('Plan failed');
    expect(alert).toHaveTextContent('never force-unlocks');
    expect(alert).toHaveTextContent('No plan was kept');
    expect(alert).toHaveTextContent('Error acquiring the state lock');
    expect(screen.queryByText('No plans yet')).not.toBeInTheDocument();
  });

  it('labels an older plan shown below a failed run', async () => {
    const failed: Operation = { ...operation, id: 'f'.repeat(32), status: 'cancelled', planId: undefined, diagnostic: { code: 'cancelled', message: 'The plan was cancelled and no plan was kept.' } };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [failed, operation] }) : undefined);
    view();
    expect(await screen.findByText(/Below is the previous successful plan/)).toBeInTheDocument();
    expect(screen.getAllByRole('alert')[0]).toHaveTextContent('Plan cancelled');
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
    const log = await screen.findByRole('log', { name: 'Plan output' });
    expect(await within(log).findByText('aws_s3_bucket.logs: plan to create')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeDisabled();
    await userEvent.click(screen.getByRole('button', { name: 'Cancel plan' }));
    await waitFor(() => expect(posts.some(p => p.url.endsWith(`/operations/${running.id}/cancel`))).toBe(true));
    expect(await screen.findByText('Cancelling')).toBeInTheDocument();
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
    const identity = await within(dialog).findByRole('region', { name: 'Context identity' });
    expect(identity).toHaveTextContent('platform-infra');
    expect(identity).toHaveTextContent('Terraform 1.9.8');
    expect(identity).toHaveTextContent('s3 (local-cli)');
    expect(within(dialog).getByRole('list', { name: 'Changes to apply' })).toHaveTextContent('2 replace');
    expect(dialog).toHaveTextContent('terraform apply -input=false -json <reviewed saved plan cccccccccccc>');
    expect(dialog).toHaveTextContent('cannot guarantee zero downtime');
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
    const apply = await within(dialog).findByRole('button', { name: 'Apply plan' });
    await user.type(within(dialog).getByLabelText('Confirmation phrase'), 'apply demo');
    // There is no blanket acknowledgement: each warning has its own checkbox.
    const boxes = within(dialog).getAllByRole('checkbox');
    expect(boxes).toHaveLength(2);
    expect(apply).toBeDisabled();
    await user.click(boxes[0]);
    expect(apply).toBeDisabled();
    await user.keyboard('{Enter}');
    expect(posts.filter(p => p.url.endsWith('/approval'))).toHaveLength(0);
    await user.click(boxes[1]);
    expect(apply).toBeEnabled();
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
    expect(within(dialog).getByRole('button', { name: 'Apply plan' })).toBeDisabled();
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
      await user.click(within(dialog).getByRole('button', { name: 'Apply plan' }));
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
    expect(within(dialog).getByRole('button', { name: 'Apply plan' })).toBeDisabled();
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
    const apply = await within(dialog).findByRole('button', { name: 'Apply plan' });
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
    expect(within(dialog).getByRole('button', { name: 'Apply plan' })).toBeDisabled();
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

  it('separates engine success, state read and health on a clean apply', async () => {
    const applied: Operation = { ...operation, kind: 'apply', status: 'succeeded', outcome: 'applied', stateReloaded: true, completedAt: '2026-11-05T09:10:00Z' };
    serve(url => url.endsWith('/operations') ? Response.json({ operations: [applied, operation] }) : undefined);
    view();
    expect(await screen.findByRole('heading', { name: 'Apply finished' })).toBeInTheDocument();
    const status = screen.getByLabelText('Apply status');
    expect(status).toHaveTextContent('read again at');
    expect(status).toHaveTextContent('Hclaro has no health probe');
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
});
