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
  kind: 'plan' | 'apply' | 'init' | 'workspace' | 'tool' | 'drift' | 'graph';
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
  // Dependency graphs only.
  graphId?: string;
  // Set for applies: the preflight policy and the warning check IDs acknowledged.
  preflight?: { checkedAt: string; policy: string; acknowledged: string[]; identityVerified: boolean };
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
  // The Git commit and IaC file fingerprint the plan was made from (M15).
  source?: PlanSourceFingerprint;
  sourceStatus?: SourceStatus;
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
  message?: string;
  verifiedAt?: string;
};
export type PreflightReport = {
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
  policy: { source: string; version: number; valid: boolean; error?: string };
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

// Dependency graphs (M11). An edge points from a node to what it depends on. "declared"
// edges are in the engine output; "derived" edges join resources through internal nodes.
export type GraphNodeKind = 'resource' | 'data' | 'module' | 'provider' | 'variable' | 'local' | 'output' | 'check' | 'ephemeral' | 'unknown';
// exact: mapped to recorded instances by address; configuration: a resource with no recorded
// instance; internal: not a resource; unknown: never mapped.
export type GraphConfidence = 'exact' | 'configuration' | 'internal' | 'unknown';
export type PlannedChange = {
  changeId: string;
  address: string;
  mode: string;
  action: PlanAction;
  node?: string;
};
export type GraphNode = {
  id: string;
  displayName: string;
  resourceAddress?: string;
  modulePath: string;
  nodeKind: GraphNodeKind;
  type?: string;
  confidence: GraphConfidence;
  provider?: string;
  instances?: { resourceId: string; address: string }[];
  instanceCount: number;
  inCycle?: boolean;
  planned?: PlannedChange[];
  redacted?: boolean;
};
export type GraphEdge = {
  from: string;
  to: string;
  edgeKind: 'depends_on';
  confidence: 'declared' | 'derived';
};
export type GraphSummary = {
  nodes: number;
  edges: number;
  resources: number;
  dataSources: number;
  modules: number;
  internal: number;
  unknown: number;
  dropped: number;
  redacted: number;
  cycles: number;
  cycleNodes: number;
  derivedEdges: number;
  derivedTruncated: boolean;
};
export type DependencyGraph = {
  id: string;
  operationId: string;
  status: 'running' | 'completed' | 'failed' | 'cancelled';
  startedAt: string;
  completedAt?: string;
  engine: string;
  engineVersion: string;
  workspace: string;
  executionContext?: ExecutionContext;
  format?: 'resources' | 'full';
  summary: GraphSummary;
  warnings: string[];
  diagnostic?: { code: string; message: string };
  current: boolean;
  staleReason?: string;
  withheld: boolean;
  nodes: GraphNode[];
  edges: GraphEdge[];
  resourceEdges: GraphEdge[];
  plan?: {
    id: string;
    kind: PlanKind;
    createdAt: string;
    replace: string[];
    changes: number;
    unmapped: PlannedChange[];
  };
};
export type GraphReach = { node: string; distance: number };
export type GraphImpact = {
  node: string;
  directDependencies: string[];
  directDependents: string[];
  upstream: GraphReach[];
  downstream: GraphReach[];
  upstreamTotal: number;
  downstreamTotal: number;
  downstreamResources: number;
  inCycle: boolean;
  truncated: boolean;
  plan?: {
    id: string;
    inImpact: PlannedChange[];
    elsewhere: PlannedChange[];
    unmapped: PlannedChange[];
  };
};
export type GraphOptions = {
  canGenerate: boolean;
  reason?: string;
  engine: string;
  version: string;
  workspace: string;
  backend: string;
  executionMode: string;
  command: string[];
  projectId?: string;
  context?: ExecutionSummary;
  profileError?: string;
};

// Workspace comparison (M12). Both sides are recorded state; "different" is never drift.
export type MatchStatus = 'attribute-different' | 'only-left' | 'only-right' | 'unknown' | 'uncomparable' | 'same';
export type ComparisonRule = {
  kind: 'map' | 'ignore' | 'substitute';
  left?: string;
  right?: string;
  path?: string;
  resourceType?: string;
};
export type ComparisonTarget = {
  projectId: string;
  workspace: string;
  profileId?: string;
};
export type ComparisonSide = {
  target: ComparisonTarget;
  status: 'reading' | 'ready' | 'empty' | 'error' | 'stale';
  contextId?: string;
  alias?: string;
  rootPath?: string;
  engine?: string;
  version?: string;
  backend?: string;
  executionMode?: string;
  capturedAt?: string;
  executionContext?: ExecutionContext;
  error?: { code: string; message: string };
};
export type StatusCounts = {
  total: number;
  same: number;
  different: number;
  onlyLeft: number;
  onlyRight: number;
  unknown: number;
  uncomparable: number;
};
export type ComparisonSummary = {
  managed: StatusCounts;
  data: StatusCounts;
  mapped: number;
  ignoredAttributes: number;
  normalized: number;
  notCompared: number;
  ruleHits: number[];
  left: { managed: number; data: number; modules: number; providers: number };
  right: { managed: number; data: number; modules: number; providers: number };
};
export type ComparisonModule = {
  address: string;
  leftAddress?: string;
  left: number;
  right: number;
  status: 'both' | 'only-left' | 'only-right';
  counts: StatusCounts;
};
export type ComparisonProvider = {
  source: string;
  leftSource?: string;
  rightSource?: string;
  left: number;
  right: number;
  status: 'both' | 'only-left' | 'only-right';
  notes: string[];
};
export type Comparison = {
  id: string;
  createdAt: string;
  status: 'reading' | 'ready' | 'partial' | 'stale';
  rules: ComparisonRule[];
  left: ComparisonSide;
  right: ComparisonSide;
  current: boolean;
  staleReason?: string;
  withheld: boolean;
  notice: string;
  summary?: ComparisonSummary;
  modules: ComparisonModule[];
  providers: ComparisonProvider[];
  types: string[];
  warnings: string[];
  truncated: boolean;
};
export type ComparisonMatch = {
  id: string;
  status: MatchStatus;
  mode: 'managed' | 'data';
  type: string;
  leftAddress?: string;
  rightAddress?: string;
  leftId?: string;
  rightId?: string;
  module: string;
  provider: string;
  rule?: number;
  reason?: string;
  providerNote?: string;
  differences: number;
  notCompared: number;
  ignored: number;
  normalized: number;
  paths: string[];
  truncated?: boolean;
};
export type ComparisonAttribute = {
  path: string;
  status: 'same' | 'different' | 'only-left' | 'only-right' | 'sensitive' | 'ignored';
  left?: unknown;
  right?: unknown;
  normalized?: boolean;
  rule?: number;
};
export type ComparisonDetail = ComparisonMatch & {
  attributes: ComparisonAttribute[];
  left: unknown;
  right: unknown;
};
export type TargetStatus = {
  project: Project;
  open: boolean;
  status?: string;
  capturedAt?: string;
  engine?: string;
  version?: string;
  executionContext?: ExecutionContext;
};

// Operation history (M13): redacted metadata kept on this machine. A historical plan can
// never be applied; it has no plan file.
export type HistoryKind = 'plan' | 'replace' | 'import' | 'apply' | 'drift' | 'graph' | 'init' | 'workspace' | 'tool';
export type HistoryStatus = 'running' | 'succeeded' | 'failed' | 'cancelled' | 'unknown';
export type HistoryPlan = {
  planId: string;
  kind: string;
  counts: Record<string, number>;
  changes: { address: string; action: string }[];
  omitted?: number;
  outputs: number;
  drift: number;
  errored: boolean;
  destructive: boolean;
  varFiles: string[];
  replace: string[];
  importAddress?: string;
  fingerprint: string;
  execution?: { operationId: string; status: HistoryStatus; result: string; finishedAt?: string };
};
export type HistoryEntry = {
  id: string;
  kind: HistoryKind;
  action?: string;
  status: HistoryStatus;
  startedAt: string;
  finishedAt?: string;
  durationMs: number;
  exitCode?: number;
  engine: string;
  engineVersion?: string;
  projectId: string;
  projectName: string;
  registered: boolean;
  workspace?: string;
  context?: { fingerprint?: string; profileName?: string; profileRevision?: number };
  command: string[];
  diagnostic?: { code: string; message: string };
  plan?: HistoryPlan;
  outcome: {
    result: string;
    summary: string;
    stateReloaded?: boolean;
    verified: boolean;
    planId?: string;
    drift?: { changed: number; missing: number; cannotVerify: number };
  };
  git?: { commit: string; branch?: string };
  excerpt: { time: string; level: string; text: string }[];
  excerptOmitted?: number;
  // Live is true while this session still holds the operation's full sanitized log.
  live: boolean;
};
export type HistoryRetention = { maxEntries: number; maxAgeDays: number; maxBytes: number };
export type HistoryPage = {
  status: 'ready' | 'read_only' | 'unavailable';
  notice?: string;
  retention: HistoryRetention;
  total: number;
  stored: number;
  entries: HistoryEntry[];
  projects: { id: string; name: string; registered: boolean; workspaces: string[] }[];
};
export type PlanComparison = {
  left: string;
  right: string;
  notices: string[];
  added: { address: string; action: string }[];
  removed: { address: string; action: string }[];
  changed: { address: string; left: string; right: string }[];
  same: number;
  complete: boolean;
};

// Git context (M15). The server never returns file contents except redacted diffs of .tf and
// .tf.json files, and never absolute repository paths.
export type GitHead = {
  commit?: string;
  branch?: string;
  detached: boolean;
  unborn: boolean;
  worktree: boolean;
};
export type GitState = 'repository' | 'not_repository' | 'unavailable' | 'error';
export type GitFileKind = 'hcl' | 'json' | 'vars' | 'lock';
export type GitFileStatus = {
  id: string;
  path: string;
  place: 'root' | 'module';
  module?: string;
  kind: GitFileKind;
  status: 'modified' | 'added' | 'deleted' | 'untracked' | 'typechange' | 'conflicted';
  staged: boolean;
  unstaged: boolean;
  diffable: boolean;
  reason?: string;
};
export type GitContext = {
  state: GitState;
  message?: string;
  repository?: { name: string; projectPath: string; worktree: boolean; localModules: number };
  head?: GitHead;
  clean: boolean;
  files: GitFileStatus[];
  counts: { changed: number; untracked: number; outside: number; other: number };
  warnings: { code: string; message: string }[];
  incomplete: boolean;
  checkedAt: string;
};
export type DiffLine = { kind: 'context' | 'add' | 'delete' | 'note'; old?: number; new?: number; text: string; redacted?: boolean };
export type GitDiffHunk = { oldStart: number; oldLines: number; newStart: number; newLines: number; section?: string; lines: DiffLine[] };
export type GitDiff = {
  file: GitFileStatus;
  base: 'HEAD' | 'none';
  hunks: GitDiffHunk[];
  added: number;
  removed: number;
  redacted: number;
  truncated: boolean;
  binary: boolean;
  notice?: string;
};
export type PlanSourceFingerprint = {
  repository: boolean;
  commit?: string;
  branch?: string;
  detached: boolean;
  dirty: boolean;
  dirtyKnown: boolean;
  dirtyFiles: number;
  contentSha256: string;
  files: number;
  localModules: number;
  recordedAt: string;
};
export type SourceStatus = {
  state: 'unchanged' | 'changed' | 'unknown' | 'not_recorded';
  headChanged: boolean;
  filesChanged: boolean;
  message: string;
  recorded?: PlanSourceFingerprint;
  current?: PlanSourceFingerprint;
};
export type SourceReference = {
  state: 'found' | 'not_found' | 'unsupported';
  confidence?: 'block' | 'file';
  fileId?: string;
  path?: string;
  line?: number;
  module?: string;
  blockType?: 'resource' | 'data';
  reason: string;
};
export type EditorName = 'vscode' | 'cursor';
// Cloud discovery (M16): existing AWS objects listed on request and labelled against the
// launch workspace's recorded state. Labels are conservative; see docs/architecture.md.
export type DiscoveryMatchStatus = 'already_tracked' | 'possibly_untracked' | 'unresolved' | 'unknown';
export type DiscoveryResourceType = {
  type: string;
  service: string;
  label: string;
  global: boolean;
  operation: string;
  importIdFormat: string;
  permission: string;
};
export type DiscoveryPin = {
  projectId?: string;
  profileId?: string;
  profileName?: string;
  contextFingerprint: string;
  awsProfile?: string;
  awsRegion?: string;
  workspace: string;
  engine: string;
};
export type CloudIdentity = {
  id: string;
  provider: 'aws';
  account: string;
  arn: string;
  principalType: string;
  partition: string;
  region: string;
  regionSource: 'profile' | 'environment' | 'default';
  cliVersion: string;
  verifiedAt: string;
  expiresAt: string;
  context: DiscoveryPin;
};
export type DiscoveryOptions = {
  canScan: boolean;
  reason?: string;
  provider: 'aws';
  types: DiscoveryResourceType[];
  regions: string[];
  defaultRegion: string;
  regionSource: 'profile' | 'environment' | 'default';
  limits: { maxRegions: number; maxPerType: number; defaultPerType: number; maxConcurrency: number; defaultConcurrency: number; pageSize: number };
  project: string;
  engine: string;
  workspace: string;
  backend: string;
  stateStatus: string;
  identity?: CloudIdentity;
  projectId?: string;
  context?: ExecutionSummary;
  profileError?: string;
};
export type DiscoveryScope = { regions: string[]; types: string[]; maxPerType: number; concurrency: number };
export type DiscoveryTask = {
  type: string;
  region: string;
  status: 'pending' | 'running' | 'done' | 'failed' | 'cancelled';
  count: number;
  pages: number;
  retries: number;
  truncated: boolean;
  error?: string;
  message?: string;
};
export type CloudResourceCandidate = {
  id: string;
  type: string;
  service: string;
  resourceId: string;
  arn?: string;
  region: string;
  account: string;
  name?: string;
  tags: { key: string; value: string }[];
  tagsOmitted?: number;
  details: Record<string, string>;
  importId?: string;
  importIdStatus: 'verified' | 'unresolved';
  importIdNote?: string;
  observedAt: string;
  seenInRegions: string[];
  match: { status: DiscoveryMatchStatus; reason: string; addresses: string[]; resourceIds: string[] };
};
export type DiscoveryScan = {
  id: string;
  status: 'running' | 'completed' | 'partial' | 'failed' | 'cancelled';
  startedAt: string;
  completedAt?: string;
  scope: DiscoveryScope;
  estimate: { tasks: number; minCalls: number; maxCalls: number };
  calls: number;
  identity: CloudIdentity;
  context: DiscoveryPin;
  tasks: DiscoveryTask[];
  duplicatesMerged: number;
  error?: string;
  message?: string;
  // Present on a single-scan read only.
  results?: CloudResourceCandidate[];
  summary?: Record<DiscoveryMatchStatus, number>;
  current?: boolean;
  staleReason?: string;
  withheld?: boolean;
};
export type DiscoveryPrefill = {
  token: string;
  scanId: string;
  candidateId: string;
  type: string;
  importId: string;
  suggestedAddress: string;
  region: string;
  account: string;
  match: DiscoveryMatchStatus;
  warnings: string[];
  expiresAt: string;
};

// Cost estimation (M17). Amounts are decimal strings rounded to cents; null means not estimated.
export type ExternalDataDisclosure = {
  provider: string;
  providerName: string;
  version: string;
  externalCalls: boolean;
  offline: boolean;
  endpoint: string;
  reads: string[];
  sentOffDevice: string[];
  neverSent: string[];
  authentication: string;
  storage: string;
  documentation: string;
};
export type CostProviderState = 'strict_local' | 'cli_missing' | 'off' | 'ready' | 'unavailable';
export type CostProviderCapabilities = {
  provider: string;
  state: CostProviderState;
  installed: boolean;
  enabled: boolean;
  canEstimate: boolean;
  reason?: string;
  cliVersion?: string;
  optedInAt?: string;
  strictLocal: boolean;
  offlineAvailable: boolean;
  disclosure?: ExternalDataDisclosure;
};
export type CostItemStatus = 'estimated' | 'partial' | 'usage_unknown' | 'unsupported' | 'free' | 'not_estimated';
export type CostComponent = { name: string; unit: string; monthlyQuantity: string | null; price: string | null; monthlyCost: string | null; usageBased?: boolean };
export type CostLineItem = {
  address: string;
  resourceType: string;
  action: string;
  status: CostItemStatus;
  pastMonthly: string | null;
  monthly: string | null;
  delta: string | null;
  unpricedComponents: number;
  components: CostComponent[];
  note?: string;
};
export type CostTypeSummary = { resourceType: string; count: number; estimated: number; notEstimated: number; delta: string | null };
export type EstimateCoverage = {
  changed: number;
  costBearing: number;
  estimated: number;
  partial: number;
  usageUnknown: number;
  unsupported: number;
  free: number;
  notEstimated: number;
  percent: number | null;
  unsupportedTypes: Record<string, number>;
};
export type CostCaveat = { code: string; message: string; count?: number };
export type CostBreakdown = {
  currency: string;
  monthlyDelta: string;
  addedMonthly: string;
  removedMonthly: string;
  changedDelta: string;
  pastMonthly: string;
  projectedMonthly: string;
  coverage: EstimateCoverage;
  caveats: CostCaveat[];
  types: CostTypeSummary[];
  items: CostLineItem[];
  truncated: boolean;
};
export type CostEstimate = {
  id: string;
  planId: string;
  status: 'running' | 'completed' | 'failed' | 'cancelled';
  error?: { code: string; message: string };
  startedAt: string;
  finishedAt?: string;
  provider: string;
  providerVersion?: string;
  pricingSource: string;
  basis: string;
  workspace: string;
  engine: string;
  fingerprint: string;
  artifactDigest: string;
  cancelRequested: boolean;
  stale: boolean;
  staleReason?: string;
  breakdown?: CostBreakdown;
};
export type CostAggregate = {
  id: string;
  projectName: string;
  workspace: string;
  planId: string;
  createdAt: string;
  currency: string;
  monthlyDelta: string;
  projectedMonthly: string;
  coveragePercent: number | null;
  changed: number;
  estimated: number;
  notEstimated: number;
  providerVersion: string;
  pricingSource: string;
};
