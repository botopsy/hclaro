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
type ToolFinding = {
  severity: 'error' | 'warning';
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
