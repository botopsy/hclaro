import type { EngineDiagnostic, ScopeView } from './plans';
import type { ExecutionContext, ExecutionSummary } from './profiles';
export type DriftOptions = {
  canScan: boolean;
  reason?: string;
  engine: string;
  version: string;
  workspace: string;
  backend: string;
  executionMode: string;
  varFiles: string[];
  projectId?: string;
  context?: ExecutionSummary;
  profileError?: string;
  // Hub workspaces (M24.09): the scope the run is bound to, and the fingerprint to send back.
  code?: string;
  scope?: ScopeView;
  executionContext?: ExecutionContext | null;
  contextFingerprint?: string;
};
// changed: values differ from state; missing: the remote object no longer exists;
// cannot_verify: values were unknown during the scan; unrecognized: never treated as safe.
export type DriftClassification = 'changed' | 'missing' | 'cannot_verify' | 'unrecognized';
export type DriftResource = {
  id: string;
  address: string;
  module: string;
  type: string;
  name: string;
  index?: unknown;
  provider: string;
  deposed?: string;
  actions: string[];
  classification: DriftClassification;
  attributeChanges: number;
  cannotVerify: number;
  // The explorer ID of the same address in the current snapshot, when recorded there.
  resourceId?: string;
};
export type AttributeObservation = {
  path: string;
  recorded: unknown;
  observed: unknown;
  kind: 'changed' | 'added' | 'removed' | 'cannot_verify';
  sensitive?: boolean;
};
export type DriftDetail = DriftResource & {
  attributes: AttributeObservation[];
  truncated?: boolean;
  recorded: unknown;
  observed: unknown;
};
export type DriftScan = {
  id: string;
  operationId: string;
  status: 'running' | 'completed' | 'partial' | 'failed' | 'cancelled';
  outcome?: 'drift_detected' | 'cannot_verify' | 'no_drift';
  // Only an exhaustive scan can support a no-drift conclusion.
  exhaustive: boolean;
  startedAt: string;
  completedAt?: string;
  engine: string;
  engineVersion: string;
  workspace: string;
  varFiles: string[];
  executionContext?: ExecutionContext;
  summary: { changed: number; missing: number; cannotVerify: number; unrecognized: number; total: number; outputChanges: number };
  resources: DriftResource[];
  warnings: string[];
  diagnostic?: { code: string; message: string };
  engineDiagnostics: EngineDiagnostic[];
  current: boolean;
  staleReason?: string;
  withheld: boolean;
};
