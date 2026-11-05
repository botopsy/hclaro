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
      planReason?: string;
      canApplySavedPlan: boolean;
      applyReason?: string;
      supportsImportBlocks: boolean;
      supportsGeneratedConfig: boolean;
      canImport: boolean;
      importReason?: string;
    };
  };
  context?: ContextInfo;
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
  canApply: boolean;
  applyReason?: string;
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
  kind: 'plan' | 'apply' | 'init' | 'workspace';
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
  import?: ImportTarget;
  // Redacted display copy of configuration the engine generated for an import.
  generatedConfig?: string;
  generatedConfigTruncated?: boolean;
  destructive: boolean;
  artifactSha256: string;
  fingerprint: string;
  contextChanged: boolean;
  apply: ApplyStatus;
};
export type ApplyStatus = {
  available: boolean;
  reason?: string;
  artifactRetained: boolean;
  expiresAt?: string;
};
// A one-time confirmation issued by the server for one reviewed plan.
export type Approval = {
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
};
// A managed instance chosen in the explorer; the server receives only the opaque ID.
export type ReplaceTarget = {
  id: string;
  address: string;
};
export type PlanKind = 'plan' | 'replace' | 'import';
// What an import plan was asked to import. The import ID stays in the user's file.
export type ImportTarget = {
  address: string;
  file: string;
  generatedFile?: string;
  provider?: string;
};
export type ImportOptions = {
  canImport: boolean;
  reason?: string;
  canApply: boolean;
  applyReason?: string;
  supportsImportBlocks: boolean;
  supportsGeneratedConfig: boolean;
  engine: string;
  version: string;
  workspace: string;
  backend: string;
  varFiles: string[];
};
export type ImportRequest = {
  address: string;
  id: string;
  provider: string;
  file: string;
  generateConfig: boolean;
};
export type ImportIssue = {
  code: string;
  severity: 'error' | 'warning';
  message: string;
};
export type ImportPreview = {
  address: string;
  file: string;
  generatedFile?: string;
  content: string;
  sha256: string;
  config: 'found' | 'missing' | 'unknown';
  configFile?: string;
  canGenerate: boolean;
  issues: ImportIssue[];
  blocked: boolean;
  recipe?: {
    block: string;
    commands: string[];
    legacy: string;
  };
};

// Workspace Hub
export type Diagnostic = {
  code: string;
  message: string;
};
export type Inspection = {
  engine?: string;
  version?: string;
  workspaces: string[];
  cliDefault?: string;
  hiddenWorkspaces: number;
  inspectedAt: string;
  diagnostic?: Diagnostic;
};
export type ProjectStatus = 'ready' | 'missing' | 'unreadable' | 'not_project' | 'path_changed';
export type Project = {
  id: string;
  alias: string;
  rootPath: string;
  enginePreference: '' | 'terraform' | 'tofu';
  pinned: boolean;
  addedAt: string;
  lastOpenedAt?: string;
  lastWorkspace?: string;
  status: ProjectStatus;
  problem?: string;
  initialized: boolean;
  backend: string;
  executionMode: string;
  providerLock: boolean;
  launch: boolean;
  inspection?: Inspection;
  // The operation holding the project root: "init", "workspace" or "plan".
  busy?: string;
  workspaceActions: WorkspaceSupport;
};
export type WorkspaceAction = 'new' | 'select' | 'delete';
// Which workspace commands the project's backend allows in Hclaro.
export type WorkspaceSupport = {
  change: boolean;
  reason?: string;
  delete: boolean;
  deleteReason?: string;
};
export type WorkspaceResult = {
  action: WorkspaceAction;
  name: string;
  cliDefaultBefore?: string;
  cliDefaultAfter?: string;
  workspaces?: string[];
  ran: boolean;
  verified: boolean;
};
// The reviewed preflight of one workspace command. Producing it lists workspaces (and, for
// delete, reads that workspace's state); it never changes anything.
export type WorkspaceReview = {
  projectId: string;
  alias: string;
  rootPath: string;
  action: WorkspaceAction;
  name: string;
  engine?: string;
  engineVersion?: string;
  backend: string;
  executionMode: string;
  dataDir: string;
  workspaces: string[];
  cliDefault?: string;
  command: string[];
  notices: string[];
  empty?: boolean;
  support: WorkspaceSupport;
  launch: boolean;
  canRun: boolean;
  reason?: string;
  reasonCode?: string;
  busy?: string;
  expiresAt?: string;
  contextId?: string;
};
// Read-only init preflight; no engine command runs to produce it.
export type InitStatus = {
  projectId: string;
  alias: string;
  rootPath: string;
  status: ProjectStatus;
  problem?: string;
  initialized: boolean;
  backend: string;
  executionMode: string;
  backendDeclared: boolean;
  engine?: string;
  engineVersion?: string;
  dataDir: string;
  lockfile: boolean;
  cliDefault?: string;
  command: string[];
  notices: string[];
  canInit: boolean;
  reason?: string;
  reasonCode?: string;
  contextId?: string;
  busy?: string;
  operation?: Operation;
};
export type ProjectList = {
  projects: Project[];
  registry: {
    status: 'ready' | 'unavailable';
    notice: string;
    path: string;
  };
  launchRoot?: string;
  environment: {
    dataDir: string;
    dataDirShared: boolean;
    inheritedWorkspace?: string;
    workspaceInherited: boolean;
    cliArgsInherited: boolean;
  };
};
export type ContextInfo = {
  id: string;
  projectId: string;
  alias: string;
  rootPath: string;
  engine: string;
  workspace: string;
  dataDir: string;
  createdAt: string;
};
export type FolderListing = {
  path: string;
  parent?: string;
  home?: string;
  project: boolean;
  entries: string[];
  truncated: boolean;
};
