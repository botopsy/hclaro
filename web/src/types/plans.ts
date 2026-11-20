import type { ExecutionContext, ExecutionSummary } from './profiles';
import type { ImportTarget } from './imports';
import type { PlanSourceFingerprint, SourceStatus } from './git';
import type { ToolResult } from './tools';
import type { WorkspaceResult } from './hub';
export type PlanAction = 'create' | 'update' | 'delete' | 'replace' | 'read' | 'no-op' | 'forget' | 'unknown';
export type PlanOptions = {
  canPlan: boolean;
  reason?: string;
  canApply: boolean;
  applyReason?: string;
  engine: string;
  version: string;
  workspace: string;
  backend: string;
  varFiles: string[];
  // The launch project's Hub ID; execution profiles exist only when it is set.
  projectId?: string;
  context?: ExecutionSummary;
  profileError?: string;
  // Set for a Hub workspace (M24.07): why planning is unavailable, its scope, the recorded
  // profile context and the fingerprint a plan start must send back.
  code?: string;
  scope?: ScopeView;
  executionContext?: ExecutionContext;
  contextFingerprint?: string;
};
// The sanitized identity of a Hub workspace's execution scope.
export type ScopeView = {
  contextId: string;
  projectId: string;
  alias: string;
  rootPath: string;
  engine: string;
  version: string;
  workspace: string;
  profile: { id: string; name: string; revision: number } | null;
  backend: string;
  executionMode: string;
};
// GET contexts/<id>/plans/options.
export type ScopedPlanOptions = {
  canPlan: boolean;
  code?: string;
  reason?: string;
  scope: ScopeView | null;
  backendAccessRequired: boolean;
  varFiles: string[];
  executionContext: ExecutionContext | null;
  contextFingerprint: string;
};
export type EngineDiagnostic = {
  severity: string;
  summary: string;
  detail?: string;
  address?: string;
};
export type Operation = {
  id: string;
  kind: 'plan' | 'apply' | 'init' | 'workspace' | 'tool' | 'drift' | 'graph' | 'branch';
  // queued, cancelling and unknown are used by Hub initialization and workspace commands only.
  status: 'queued' | 'running' | 'cancelling' | 'succeeded' | 'failed' | 'cancelled' | 'unknown';
  stage: string;
  startedAt: string;
  completedAt?: string;
  durationMs: number;
  exitCode?: number;
  command: string[];
  varFiles: string[];
  replace: string[];
  import?: ImportTarget;
  planId?: string;
  // Applies only: whether the engine changed infrastructure is known (applied, not_applied)
  // or not (uncertain).
  outcome?: 'applied' | 'not_applied' | 'uncertain';
  stateReloaded: boolean;
  // Applies only: the apply ran, but reading state again afterwards failed.
  stateReloadFailed?: boolean;
  // Applies only: the saved plan file could not be deleted afterwards; the terminal names it.
  planFileLeft?: boolean;
  diagnostic?: {
    code: string;
    message: string;
  };
  engineDiagnostics: EngineDiagnostic[];
  cancelRequested: boolean;
  logDropped: number;
  logRetained: boolean;
  // Hub initialization only.
  projectId?: string;
  notices?: string[];
  lockfileChanged?: boolean;
  // Hub workspace commands only.
  workspace?: WorkspaceResult;
  // Hub project tools only.
  tool?: ToolResult;
  // The execution profile context the operation was bound to, if any.
  executionContext?: ExecutionContext;
  // Drift scans only.
  driftScanId?: string;
  // Dependency graphs only.
  graphId?: string;
  // Set for applies: the preflight policy and the warning check IDs acknowledged.
  preflight?: { checkedAt: string; policy: string; acknowledged: string[]; identityVerified: boolean };
  // Git branch switches only.
  branch?: { from: string; to: string; ran: boolean };
};
export function operationActive(op: Pick<Operation, 'status'>) {
  return op.status === 'queued' || op.status === 'running' || op.status === 'cancelling';
}
export type OperationEvent = {
  seq: number;
  time: string;
  level: 'info' | 'warn' | 'error';
  text: string;
};
export type PlanChange = {
  id: string;
  address: string;
  previousAddress?: string;
  module: string;
  mode: 'managed' | 'data';
  type: string;
  name: string;
  index?: unknown;
  provider: string;
  deposed?: string;
  actions: string[];
  action: PlanAction;
  reason?: string;
  importing?: boolean;
  attributeChanges: number;
};
export type AttributeChange = {
  path: string;
  before: unknown;
  after: unknown;
  kind: 'added' | 'removed' | 'changed' | 'unknown';
  unknown?: boolean;
  sensitive?: boolean;
  forcesReplacement?: boolean;
};
export type ChangeDetail = PlanChange & {
  attributes: AttributeChange[];
  truncated?: boolean;
  before: unknown;
  after: unknown;
};
export type OutputChange = {
  name: string;
  actions: string[];
  action: PlanAction;
  before: unknown;
  after: unknown;
  unknown?: boolean;
  sensitive?: boolean;
};
export type PlanSummary = {
  id: string;
  operationId: string;
  createdAt: string;
  engine: string;
  workspace: string;
  varFiles: string[];
  kind: PlanKind;
  replace: string[];
  summary: Partial<Record<PlanAction, number>>;
  errored: boolean;
  changes: number;
  outputs: number;
  drift: number;
};
export type Plan = {
  // Set for a Hub workspace: the scope this response belongs to.
  scope?: ScopeView;
  id: string;
  operationId: string;
  createdAt: string;
  engine: string;
  engineVersion: string;
  currentEngineVersion: string;
  workspace: string;
  varFiles: string[];
  formatVersion: string;
  timestamp?: string;
  errored: boolean;
  summary: Partial<Record<PlanAction, number>>;
  changes: PlanChange[];
  drift: PlanChange[];
  outputs: OutputChange[];
  warnings: string[];
  kind: PlanKind;
  replace: string[];
  imports: number;
  // Resource and output changes with an action combination Hclaro does not support (M24.01).
  // Any such change makes the plan ineligible for apply; the server enforces this.
  unsupportedActions?: number;
  import?: ImportTarget;
  // Redacted display copy of configuration the engine generated for an import.
  generatedConfig?: string;
  generatedConfigTruncated?: boolean;
  destructive: boolean;
  artifactSha256: string;
  fingerprint: string;
  contextChanged: boolean;
  apply: ApplyStatus;
  executionContext?: ExecutionContext;
  // The Git commit and IaC file fingerprint the plan was made from (M15).
  source?: PlanSourceFingerprint;
  sourceStatus?: SourceStatus;
};
export type ApplyStatus = {
  available: boolean;
  reason?: string;
  // Stable refusal code when unavailable, such as unknown_actions.
  code?: string;
  // Set when the configuration could not be read, whichever refusal code wins (M26.12).
  configurationReason?: string;
  artifactRetained: boolean;
  expiresAt?: string;
};
// A one-time confirmation issued by the server for one reviewed plan.
export type Approval = {
  // Set for a Hub workspace: the scope this response belongs to.
  scope?: ScopeView;
  confirmationToken: string;
  expiresAt: string;
  planId: string;
  artifactSha256: string;
  engine: string;
  workspace: string;
  summary: Partial<Record<PlanAction, number>>;
  replace: string[];
  imports: number;
  import?: ImportTarget;
  destructive: boolean;
  phrase?: string;
  command: string[];
  executionContext?: ExecutionContext;
  acknowledged: string[];
};
// M14: server-side apply preflight. Checks are evaluated on the server; the UI only shows
// them and returns the acknowledged warning IDs with the opaque binding.
export type PreflightSeverity = 'block' | 'warn' | 'info';
export type PreflightStatus = 'pass' | 'fail' | 'unknown';
export type PreflightCheck = {
  id: string;
  title: string;
  severity: PreflightSeverity;
  status: PreflightStatus;
  evidence: string;
  remediation?: string;
  source: 'built-in' | 'policy';
};
export type PreflightResource = {
  address: string;
  action: PlanAction;
  reason?: string;
  deposed?: boolean;
  requested?: boolean;
  importing?: boolean;
};
export type PreflightIdentity = {
  state: 'not_requested' | 'verified' | 'unavailable' | 'failed';
  account?: string;
  arn?: string;
  kind?: string;
  message?: string;
  verifiedAt?: string;
};
export type PreflightReport = {
  // Set for a Hub workspace: the scope this response belongs to.
  scope?: ScopeView;
  planId: string;
  generatedAt: string;
  expiresAt?: string;
  context: {
    project: string;
    engine: string;
    engineVersion: string;
    workspace: string;
    backend: string;
    executionMode: string;
    planKind: PlanKind;
    planCreatedAt: string;
    artifactSha256: string;
    fingerprint: string;
    varFiles: string[];
    executionContext?: ExecutionContext;
  };
  checks: PreflightCheck[];
  blocked: boolean;
  blockers: number;
  acknowledge: string[];
  impact: {
    summary: Partial<Record<PlanAction, number>>;
    total: number;
    destructive: number;
    imports: number;
    requested: string[];
    extra: number;
    highRisk: PreflightResource[];
    changes: PreflightResource[];
    truncated: boolean;
  };
  policy: {
    source: string;
    version: number;
    valid: boolean;
    error?: string;
    rules: {
      gitDirty: PreflightSeverity;
      productionLabels: { severity: PreflightSeverity; patterns: string[] };
      destructiveLimit?: { severity: PreflightSeverity; max: number };
      awsAccounts?: { severity: PreflightSeverity; allowed: string[] };
    };
  };
  identity: PreflightIdentity;
  binding?: string;
  phrase?: string;
  command: string[];
};
// A managed instance chosen in the explorer; the server receives only the opaque ID.
export type ReplaceTarget = {
  id: string;
  address: string;
};
export type PlanKind = 'plan' | 'replace' | 'import';
