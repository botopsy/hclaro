// Where plan and operation requests go. The launch project uses its original routes; a Hub
// workspace uses only its own read context's routes (M24.05–M24.07). There is no fallback from
// a Hub workspace to the launch project: an invalid context refuses instead.
export type PlanScope = { kind: 'launch' } | { kind: 'context'; contextId: string };

export const launchScope: PlanScope = { kind: 'launch' };

const id = /^[0-9a-f]{32}$/;

// scopedPath prefixes a launch API path (for example "plans" or "operations/<id>/events")
// with the scope's context.
export function scopedPath(scope: PlanScope, path: string) {
  return scope.kind === 'launch' ? path : `${contextRoot(scope.contextId)}/${path}`;
}

// snapshotPath is the workspace snapshot: "workspace" at launch, the context itself in the Hub.
export function snapshotPath(scope: PlanScope) {
  return scope.kind === 'launch' ? 'workspace' : contextRoot(scope.contextId);
}

function contextRoot(contextId: string) {
  if (!id.test(contextId)) throw new Error('This workspace view is no longer open. Open the workspace again.');
  return `contexts/${contextId}`;
}

// scopeKey separates cached queries of different scopes.
export function scopeKey(scope: PlanScope) {
  return scope.kind === 'launch' ? 'launch' : scope.contextId;
}

export function planRoute(scope: PlanScope, planId: string, step: 'preflight' | 'approval' | 'apply') {
  if (!id.test(planId)) throw new Error('This plan is no longer available. Run a new plan.');
  return scopedPath(scope, `plans/${planId}/${step}`);
}
