import type { ContextInfo } from './hub';
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
  keyAttributes?: string;
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
  groups?: { key: string; count: number }[];
};
