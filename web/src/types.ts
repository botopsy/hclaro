export type Resource = {
  id: string;
  address: string;
  mode: 'managed' | 'data';
  type: string;
  name: string;
  provider: string;
  module: string;
  index?: unknown;
  attributes: unknown;
};
export type Module = {
  address: string;
  parent: string;
  count: number;
};
export type Workspace = {
  status: 'loading' | 'ready' | 'empty' | 'error';
  capturedAt?: string;
  diagnostic?: {
    code: string;
    message: string;
  };
  info: {
    project: string;
    engine: string;
    version: string;
    workspace: string;
    backend: string;
    executionMode: string;
    initialized: boolean;
    providerLock: boolean;
    warnings: string[];
    capabilities: {
      canReadState: boolean;
      canPlan: boolean;
      canApplySavedPlan: boolean;
    };
  };
  managed: number;
  data: number;
  modules: Module[];
  providers: Record<string, number>;
  types: Record<string, number>;
};
export type ResourcePage = {
  resources: Resource[];
  total: number;
};
export type PlanAction = 'create' | 'update' | 'delete' | 'replace' | 'read' | 'no-op' | 'forget' | 'unknown';
export type PlanOptions = {
  canPlan: boolean;
  reason?: string;
  engine: string;
  version: string;
  workspace: string;
  backend: string;
  varFiles: string[];
};
export type EngineDiagnostic = {
  severity: string;
  summary: string;
  detail?: string;
  address?: string;
};
export type Operation = {
  id: string;
  kind: 'plan';
  status: 'running' | 'succeeded' | 'failed' | 'cancelled';
  stage: string;
  startedAt: string;
  completedAt?: string;
  durationMs: number;
  exitCode?: number;
  command: string[];
  varFiles: string[];
  planId?: string;
  diagnostic?: {
    code: string;
    message: string;
  };
  engineDiagnostics: EngineDiagnostic[];
  cancelRequested: boolean;
  logDropped: number;
  logRetained: boolean;
};
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
  summary: Partial<Record<PlanAction, number>>;
  errored: boolean;
  changes: number;
  outputs: number;
  drift: number;
};
export type Plan = {
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
  artifactSha256: string;
  artifactRetained: boolean;
  fingerprint: string;
  contextChanged: boolean;
  apply: {
    available: boolean;
    reason: string;
  };
};
