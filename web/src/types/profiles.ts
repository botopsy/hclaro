import type { ProjectStatus } from './hub';
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
  // '' without an AWS profile; aws-cli or aws-vault otherwise (M20).
  awsAuth: '' | AWSAuthMethod;
  awsExpectedAccount: string;
  awsExpectedRole: string;
  // Google Cloud selection (M33): names of a gcloud configuration, project and region.
  // Azure selection (M33): subscription and tenant IDs and a location.
  gcpConfiguration?: string;
  gcpProject?: string;
  gcpRegion?: string;
  azureSubscription?: string;
  azureTenant?: string;
  azureLocation?: string;
  envNames: string[];
  revision: number;
  createdAt: string;
  updatedAt: string;
  default: boolean;
};
export type ProfileInput = Pick<ExecutionProfile, 'name' | 'engine' | 'workspace' | 'varFiles' | 'parallelism' | 'lockTimeoutSeconds' | 'awsProfile' | 'awsRegion' | 'awsAuth' | 'awsExpectedAccount' | 'awsExpectedRole' | 'gcpConfiguration' | 'gcpProject' | 'gcpRegion' | 'azureSubscription' | 'azureTenant' | 'azureLocation' | 'envNames'>;
type AWSAuthMethod = 'aws-cli' | 'aws-vault';
// AWS authentication state of one execution profile (M20). Names, the STS account and ARN,
// timestamps and codes only: never credentials, sign-in URLs, codes or CLI output.
export type AuthState = 'not_configured' | 'tool_missing' | 'not_checked' | 'checking' | 'verified' | 'auth_required' | 'access_denied_or_error' | 'identity_mismatch' | 'unknown';
type AuthTool = { available: boolean; version?: string; supported: boolean; message?: string };
type AuthTools = { aws: AuthTool; awsVault: AuthTool; ssoLogin: boolean; login: boolean; loginMessage?: string; detectedAt: string };
type AuthLogin = { id: string; mode: 'sso' | 'login'; state: 'running' | 'completed' | 'failed' | 'cancelled' | 'timed_out'; code?: string; message: string; startedAt: string; finishedAt?: string };
export type AuthStatus = {
  projectId: string;
  profileId: string;
  profileName: string;
  profileRevision: number;
  method?: AWSAuthMethod;
  awsProfile?: string;
  region?: string;
  expectedAccount?: string;
  expectedRole?: string;
  profileKind?: 'sso' | 'other' | 'unknown';
  state: AuthState;
  code?: string;
  message: string;
  account?: string;
  arn?: string;
  checkedAt?: string;
  login?: AuthLogin;
  canSsoLogin: boolean;
  canLogin: boolean;
  commands: { label: string; command: string }[];
  disclosure: string;
  tools: AuthTools;
};
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
type ContextIssue = {
  code: string;
  message: string;
};
// A profile resolved at one revision, as the server will use it.
type ResolvedContext = {
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
  awsAuth?: AWSAuthMethod;
  awsExpectedAccount?: string;
  awsExpectedRole?: string;
  gcpConfiguration?: string;
  gcpProject?: string;
  gcpRegion?: string;
  azureSubscription?: string;
  azureTenant?: string;
  azureLocation?: string;
  env: { name: string; present: boolean }[];
  identity: 'not_verified';
  notices: string[];
  issues: ContextIssue[];
  varFileIssues: { code: string; ref: string; message: string }[];
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
  awsAuth?: AWSAuthMethod;
  awsExpectedAccount?: string;
  awsExpectedRole?: string;
  gcpConfiguration?: string;
  gcpProject?: string;
  gcpRegion?: string;
  azureSubscription?: string;
  azureTenant?: string;
  azureLocation?: string;
  envNames: string[];
  identity: string;
  notices: string[];
  fingerprint: string;
};
