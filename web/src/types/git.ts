// Git context (M15). The server never returns file contents except redacted diffs of .tf and
// .tf.json files, and never absolute repository paths.
export type GitHead = {
  commit?: string;
  branch?: string;
  detached: boolean;
  unborn: boolean;
  worktree: boolean;
};
export type GitState = 'repository' | 'not_repository' | 'unavailable' | 'error';
export type GitFileKind = 'hcl' | 'json' | 'vars' | 'lock';
export type GitFileStatus = {
  id: string;
  path: string;
  place: 'root' | 'module';
  module?: string;
  kind: GitFileKind;
  status: 'modified' | 'added' | 'deleted' | 'untracked' | 'typechange' | 'conflicted';
  staged: boolean;
  unstaged: boolean;
  diffable: boolean;
  reason?: string;
};
export type GitContext = {
  state: GitState;
  message?: string;
  repository?: { name: string; projectPath: string; worktree: boolean; localModules: number };
  head?: GitHead;
  clean: boolean;
  files: GitFileStatus[];
  counts: { changed: number; untracked: number; outside: number; other: number };
  warnings: { code: string; message: string }[];
  incomplete: boolean;
  checkedAt: string;
};
// Git branch switching (M29): existing local branches only.
export type GitBranch = { name: string; commit: string; current: boolean };
export type GitBranchList = {
  state: GitState;
  message?: string;
  repository?: GitContext['repository'];
  head?: GitHead;
  branches: GitBranch[];
  hidden: number;
  incomplete: boolean;
  checkedAt: string;
};
export type GitNotice = { code: string; message: string };
export type GitSwitchPreview = {
  from: GitHead;
  to: GitBranch;
  workingTree: { clean: boolean; staged: number; modified: number; untracked: number };
  sourceChanges: { files: number; iac: number; inProject: number };
  canSwitch: boolean;
  blocked: GitNotice[];
  warnings: GitNotice[];
  checkedAt: string;
};
export type DiffLine = { kind: 'context' | 'add' | 'delete' | 'note'; old?: number; new?: number; text: string; redacted?: boolean };
export type GitDiffHunk = { oldStart: number; oldLines: number; newStart: number; newLines: number; section?: string; lines: DiffLine[] };
export type GitDiff = {
  file: GitFileStatus;
  base: 'HEAD' | 'none';
  hunks: GitDiffHunk[];
  added: number;
  removed: number;
  redacted: number;
  truncated: boolean;
  binary: boolean;
  notice?: string;
};
export type PlanSourceFingerprint = {
  repository: boolean;
  commit?: string;
  branch?: string;
  detached: boolean;
  dirty: boolean;
  dirtyKnown: boolean;
  dirtyFiles: number;
  contentSha256: string;
  files: number;
  localModules: number;
  recordedAt: string;
};
export type SourceStatus = {
  state: 'unchanged' | 'changed' | 'unknown' | 'not_recorded';
  headChanged: boolean;
  filesChanged: boolean;
  message: string;
  recorded?: PlanSourceFingerprint;
  current?: PlanSourceFingerprint;
};
export type SourceReference = {
  state: 'found' | 'not_found' | 'unsupported';
  confidence?: 'block' | 'file';
  fileId?: string;
  path?: string;
  line?: number;
  module?: string;
  blockType?: 'resource' | 'data';
  reason: string;
};
export type EditorName = 'vscode' | 'cursor';
