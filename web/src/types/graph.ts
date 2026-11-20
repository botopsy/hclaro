import type { ExecutionContext, ExecutionSummary } from './profiles';
import type { PlanAction, PlanKind, ScopeView } from './plans';
// Dependency graphs (M11). An edge points from a node to what it depends on. "declared"
// edges are in the engine output; "derived" edges join resources through internal nodes.
export type GraphNodeKind = 'resource' | 'data' | 'module' | 'provider' | 'variable' | 'local' | 'output' | 'check' | 'ephemeral' | 'unknown';
// exact: mapped to recorded instances by address; configuration: a resource with no recorded
// instance; internal: not a resource; unknown: never mapped.
type GraphConfidence = 'exact' | 'configuration' | 'internal' | 'unknown';
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
type GraphSummary = {
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
type GraphReach = { node: string; distance: number };
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
  // Hub workspaces (M24.09): the scope the run is bound to, and the fingerprint to send back.
  code?: string;
  scope?: ScopeView;
  executionContext?: ExecutionContext | null;
  contextFingerprint?: string;
};
