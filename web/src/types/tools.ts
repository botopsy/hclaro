export type ToolName = 'validate' | 'fmt-check' | 'providers' | 'outputs-metadata' | ScannerName;
export type ScannerName = 'tflint' | 'trivy' | 'checkov';
export type ScanSeverity = 'critical' | 'high' | 'medium' | 'low' | 'unrated';
// A scanner's card state before anything runs: only its version was read.
export type ScannerInfo = { name: ScannerName; title: string; installed: boolean; version?: string; supported: boolean; reason?: string; config: string[] };
// A finished scan that still describes the workspace's configuration (Plan Review hint).
export type ScanSummary = { scanner: ScannerName; counts: Partial<Record<ScanSeverity, number>>; finishedAt: string };
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
  scan?: {
    scanner: ScannerName;
    version?: string;
    recursive: boolean;
    config: string[];
    counts: Partial<Record<ScanSeverity, number>>;
    hidden: number;
    fingerprint?: string;
  };
};
type ToolFinding = {
  // error and warning come from the engine; the rest from scanners.
  severity: 'error' | 'warning' | ScanSeverity;
  rule?: string;
  summary: string;
  detail?: string;
  file?: string;
  line?: number;
  column?: number;
};
type ProviderRequirement = {
  source: string;
  constraint?: string;
  module?: string;
};
type OutputMeta = {
  name: string;
  type: string;
  sensitive: boolean;
};
// One output with its value, from "Show values". A hidden output (sensitive, or its name or
// value looks like a secret) carries no value until it is revealed.
export type OutputValue = {
  name: string;
  type: string;
  sensitive: boolean;
  hidden: boolean;
  value?: string;
  truncated?: boolean;
};
