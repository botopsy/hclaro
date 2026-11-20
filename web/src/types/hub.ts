import type { ExecutionContext } from './profiles';
import type { Operation } from './plans';
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
