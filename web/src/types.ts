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
  // The launch project's Hub ID; execution profiles exist only when it is set.
  projectId?: string;
  context?: ExecutionSummary;
  profileError?: string;
};
export type EngineDiagnostic = {
  severity: string;
  summary: string;
  detail?: string;
  address?: string;
};
export type Operation = {
  id: string;
  kind: 'plan' | 'apply' | 'init' | 'workspace' | 'tool' | 'drift';
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
  // Hub project tools only.
  tool?: ToolResult;
  // The execution profile context the operation was bound to, if any.
  executionContext?: ExecutionContext;
  // Drift scans only.
  driftScanId?: string;
};
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
export type ToolName = 'validate' | 'fmt-check' | 'providers' | 'outputs-metadata';
// A tool's sanitized result. outcome is passed, findings (the check ran and found problems)
// or not_checked (it did not complete; nothing can be concluded).
export type ToolResult = {
  tool: ToolName;
  contextId: string;
  workspace: string;
  usesWorkspace: boolean;
  engineVersion?: string;
  outcome: 'passed' | 'findings' | 'not_checked';
  output?: 'json' | 'human';
  partial?: boolean;
  valid?: boolean;
  errors: number;
  warnings: number;
  findings: ToolFinding[];
  format?: {
    recursive: boolean;
    checked: number;
    folders: number;
    unformatted: string[];
    hidden: number;
    skipped: string[];
  };
  providers?: {
    configuration: ProviderRequirement[];
    state: ProviderRequirement[];
    hidden: number;
  };
  outputs?: {
    items: OutputMeta[];
    hidden: number;
  };
};
export type ToolFinding = {
  severity: 'error' | 'warning';
  summary: string;
  detail?: string;
  file?: string;
  line?: number;
  column?: number;
};
export type ProviderRequirement = {
  source: string;
  constraint?: string;
  module?: string;
};
export type OutputMeta = {
  name: string;
  type: string;
  sensitive: boolean;
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
  executionContext?: ExecutionContext;
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
  executionContext?: ExecutionContext;
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
  executionContext?: ExecutionContext;
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
  executionContext?: ExecutionContext;
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
  executionContext?: ExecutionContext;
};
export type FolderListing = {
  path: string;
  parent?: string;
  home?: string;
  project: boolean;
  entries: string[];
  truncated: boolean;
};
// A stored execution profile. It holds names and aliases only, never values.
export type ExecutionProfile = {
  id: string;
  projectId: string;
  name: string;
  engine: '' | 'terraform' | 'tofu';
  workspace: string;
  varFiles: string[];
  parallelism: number;
  lockTimeoutSeconds: number;
  awsProfile: string;
  awsRegion: string;
  envNames: string[];
  revision: number;
  createdAt: string;
  updatedAt: string;
  default: boolean;
};
export type ProfileInput = Pick<ExecutionProfile, 'name' | 'engine' | 'workspace' | 'varFiles' | 'parallelism' | 'lockTimeoutSeconds' | 'awsProfile' | 'awsRegion' | 'envNames'>;
export type ProfileList = {
  profiles: ExecutionProfile[];
  defaultId: string;
  status: 'ready' | 'unavailable';
  notice: string;
};
export type VarFile = {
  ref: string;
  path: string;
  size: number;
  modifiedAt: string;
  linked: boolean;
};
export type ContextIssue = {
  code: string;
  message: string;
};
// A profile resolved at one revision, as the server will use it.
export type ResolvedContext = {
  projectId: string;
  profileId?: string;
  profileName?: string;
  profileRevision?: number;
  enginePreference?: string;
  workspace?: string;
  varFiles: VarFile[];
  options: { parallelism?: number; lock_timeout_seconds?: number };
  awsProfile?: string;
  awsRegion?: string;
  env: { name: string; present: boolean }[];
  identity: 'not_verified';
  notices: string[];
  issues: ContextIssue[];
  fingerprint: string;
};
export type ExecutionSummary = {
  projectId: string;
  alias: string;
  rootPath: string;
  status: ProjectStatus;
  backend: string;
  executionMode: string;
  engine: string;
  engineFound: boolean;
  engineVersion?: string;
  workspace?: string;
  workspaceSource: 'profile' | 'tab' | 'launch' | 'cli_default';
  dataDir: string;
  context: ResolvedContext;
  issues: ContextIssue[];
};
// The immutable context recorded with an operation, plan or approval.
export type ExecutionContext = {
  projectId?: string;
  profileId?: string;
  profileName?: string;
  profileRevision?: number;
  enginePreference?: string;
  workspace: string;
  varFiles: string[];
  parallelism?: number;
  lockTimeoutSeconds?: number;
  awsProfile?: string;
  awsRegion?: string;
  envNames: string[];
  identity: string;
  notices: string[];
  fingerprint: string;
};
