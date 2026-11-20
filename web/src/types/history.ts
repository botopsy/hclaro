// Operation history (M13): redacted metadata kept on this machine. A historical plan can
// never be applied; it has no plan file.
export type HistoryKind = 'plan' | 'replace' | 'import' | 'apply' | 'drift' | 'graph' | 'init' | 'workspace' | 'tool' | 'branch' | 'assist' | 'assist-change' | 'unknown';
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
  assist?: { provider: string; contextKind: string; packageSha256: string; prompt?: string; conversation?: string; path?: string; baseSha256?: string; newSha256?: string };
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
