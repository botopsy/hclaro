import type { ExecutionContext } from './profiles';
import type { Project } from './hub';
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
type ComparisonSummary = {
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
type ComparisonModule = {
  address: string;
  leftAddress?: string;
  left: number;
  right: number;
  status: 'both' | 'only-left' | 'only-right';
  counts: StatusCounts;
};
type ComparisonProvider = {
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
  addresses?: string[];
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
