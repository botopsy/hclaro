import type { ExecutionContext, ExecutionSummary } from './profiles';
import type { ScopeView } from './plans';
// Cloud discovery (M16): existing AWS objects listed on request and labelled against the
// launch workspace's recorded state. Labels are conservative; see docs/architecture.md.
export type DiscoveryMatchStatus = 'already_tracked' | 'possibly_untracked' | 'unresolved' | 'unknown';
type DiscoveryResourceType = {
  type: string;
  service: string;
  label: string;
  global: boolean;
  operation: string;
  importIdFormat: string;
  permission: string;
  provider?: string;
};
export type DiscoveryProvider = 'aws' | 'gcp' | 'azure';
export type DiscoveryProviderOption = { name: DiscoveryProvider; label: string; cli: string; detected: boolean };
type DiscoveryPin = {
  contextId?: string;
  projectId?: string;
  profileId?: string;
  profileName?: string;
  contextFingerprint: string;
  provider?: string;
  awsProfile?: string;
  awsRegion?: string;
  gcpConfiguration?: string;
  gcpProject?: string;
  gcpRegion?: string;
  azureSubscription?: string;
  azureTenant?: string;
  azureLocation?: string;
  workspace: string;
  engine: string;
};
export type CloudIdentity = {
  id: string;
  provider: DiscoveryProvider;
  account: string;
  arn: string;
  // Azure: the tenant of the subscription in account.
  tenant?: string;
  principalType: string;
  partition: string;
  region: string;
  regionSource: 'profile' | 'environment' | 'gcloud' | 'azure' | 'default';
  cliVersion: string;
  verifiedAt: string;
  expiresAt: string;
  context: DiscoveryPin;
};
export type DiscoveryOptions = {
  canScan: boolean;
  reason?: string;
  provider: DiscoveryProvider;
  providers?: DiscoveryProviderOption[];
  types: DiscoveryResourceType[];
  regions: string[];
  defaultRegion: string;
  regionSource: 'profile' | 'environment' | 'gcloud' | 'azure' | 'default';
  limits: { maxRegions: number; maxPerType: number; maxObjects: number; defaultPerType: number; maxConcurrency: number; defaultConcurrency: number; pageSize: number };
  project: string;
  engine: string;
  // Hub workspaces only.
  version?: string;
  workspace: string;
  backend: string;
  executionMode?: string;
  stateStatus: string;
  identity?: CloudIdentity;
  projectId?: string;
  context?: ExecutionSummary;
  profileError?: string;
  // Hub workspaces (M24.09): the scope discovery is bound to, and the fingerprint to send back.
  code?: string;
  scope?: ScopeView;
  executionContext?: ExecutionContext | null;
  contextFingerprint?: string;
};
export type DiscoveryScope = { regions: string[]; types: string[]; maxPerType: number; concurrency: number };
type DiscoveryTask = {
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
  cloudId?: string;
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
  truncated?: boolean;
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
