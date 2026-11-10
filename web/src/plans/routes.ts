// Where a saved plan's preflight, approval and apply requests go. The launch project uses its
// original routes; a Hub workspace uses only its own read context's routes (M24.06). There is
// no fallback from a Hub workspace to the launch project: an invalid context refuses instead.
export type PlanScope = { kind: 'launch' } | { kind: 'context'; contextId: string };

export const launchScope: PlanScope = { kind: 'launch' };

const id = /^[0-9a-f]{32}$/;

export function planRoute(scope: PlanScope, planId: string, step: 'preflight' | 'approval' | 'apply') {
  if (!id.test(planId)) throw new Error('This plan is no longer available. Run a new plan.');
  if (scope.kind === 'launch') return `plans/${planId}/${step}`;
  if (!id.test(scope.contextId)) throw new Error('This workspace view is no longer open. Open the workspace again.');
  return `contexts/${scope.contextId}/plans/${planId}/${step}`;
}
