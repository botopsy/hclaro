import type { ExecutionContext } from './profiles';
import type { ScopeView } from './plans';
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
  // Set for a Hub workspace (M24.08): why importing is unavailable, its scope, the recorded
  // profile context and the fingerprint an import start must send back.
  code?: string;
  scope?: ScopeView | null;
  executionContext?: ExecutionContext | null;
  contextFingerprint?: string;
};
export type ImportRequest = {
  address: string;
  id: string;
  provider: string;
  file: string;
  generateConfig: boolean;
};
type ImportIssue = {
  code: string;
  severity: 'error' | 'warning';
  message: string;
};
export type ImportPreview = {
  // Set for a Hub workspace: the scope this response belongs to.
  scope?: ScopeView;
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
