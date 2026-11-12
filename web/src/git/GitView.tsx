import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { CircleHelp, Code2, FileCode2, FileLock2, FolderGit2, GitBranch, GitCommitHorizontal, LoaderCircle, LockKeyhole, RefreshCcw, ShieldCheck, TriangleAlert, X } from 'lucide-react';
import { ApiError, api, post } from '../api';
import type { EditorName, GitContext, GitDiff, GitFileStatus, SourceReference } from '../types';
import { Button } from '../components/ui/button';
import { Drawer } from '../components/Drawer';

// gitPrefix is the API prefix of a project's Git routes: the launch project's are at the
// API root, a Hub project's under projects/<id>/.
export function gitPrefix(projectId?: string) {
  return projectId ? `projects/${projectId}/` : '';
}
export const editorLabels: Record<EditorName, string> = { vscode: 'VS Code', cursor: 'Cursor' };
const statusLabels: Record<GitFileStatus['status'], string> = {
  modified: 'Modified',
  added: 'Added',
  deleted: 'Deleted',
  untracked: 'Untracked',
  typechange: 'Type changed',
  conflicted: 'Conflict'
};
const kindLabels: Record<GitFileStatus['kind'], string> = { hcl: 'HCL', json: 'JSON', vars: 'Variables', lock: 'Lock file' };
function message(error: unknown) {
  return error instanceof Error ? error.message : 'Unable to reach the local Hclaro server.';
}
export function shortCommit(commit?: string) {
  return commit ? commit.slice(0, 7) : '';
}

// The HEAD badge reads .git files on the server; it runs no Git command.
export function useGitHead(prefix: string | undefined) {
  return useQuery({
    queryKey: ['git-head', prefix],
    queryFn: () => api<GitContext>(`${prefix}git/head`),
    enabled: prefix !== undefined,
    retry: false
  });
}
export function GitBadge({ prefix, onOpen }: { prefix: string | undefined; onOpen: () => void }) {
  const head = useGitHead(prefix);
  // Shares the Git view's status query: the badge shows clean or dirty once it has run, and
  // never runs Git itself.
  const status = useQuery({ queryKey: ['git-status', prefix], queryFn: () => post<GitContext>(`${prefix}git/status`, {}), enabled: false, staleTime: Infinity });
  const h = head.data?.head;
  if (!head.data) return null;
  if (head.data.state === 'not_repository') return <div className="context-item muted" title="This project is not inside a Git repository.">No Git</div>;
  if (!h) return <div className="context-item muted" title={head.data.message}>Git unavailable</div>;
  const dirty = status.data?.state === 'repository' ? !status.data.clean : undefined;
  const label = h.detached ? `detached @ ${shortCommit(h.commit)}` : h.unborn ? `${h.branch || 'branch'} · no commits` : `${h.branch} @ ${shortCommit(h.commit)}`;
  return <button className="context-item git-badge" onClick={onOpen} title={`Git: ${label}${dirty === undefined ? '' : dirty ? ', uncommitted IaC changes' : ', IaC files clean'}${h.worktree ? ', linked worktree' : ''}. Open Git changes.`} aria-label={`Git ${label}${dirty ? ', uncommitted changes' : ''}. Open Git changes`}><GitBranch size={13} /><strong className="mono">{label}</strong>{dirty !== undefined && <span className={`git-dirty-dot ${dirty ? 'dirty' : 'clean'}`} aria-hidden="true" />}{h.detached && <TriangleAlert size={12} aria-hidden="true" />}</button>;
}

export function EditorButtons({ prefix, fileId, line, editor, onDone }: { prefix: string; fileId: string; line: number; editor: EditorName; onDone: (text: string) => void }) {
  const [busy, setBusy] = useState(false);
  async function open() {
    setBusy(true);
    try {
      const r = await post<{ path: string; line: number }>(`${prefix}editor/open`, { fileId, line, editor });
      onDone(`Asked ${editorLabels[editor]} to open ${r.path}:${r.line}`);
    } catch (e) {
      onDone(message(e));
    } finally {
      setBusy(false);
    }
  }
  return <Button variant="outline" size="small" onClick={() => void open()} disabled={busy} title={`Open line ${line} in ${editorLabels[editor]} through its link handler. Change the editor in Settings.`}><Code2 size={12} />Open in {editorLabels[editor]}</Button>;
}

export function SourceLink({ prefix, sourcePath, editor, notify }: { prefix: string; sourcePath: string | undefined; editor: EditorName; notify: (text: string) => void }) {
  const ref = useQuery({
    queryKey: ['source', sourcePath],
    queryFn: () => api<SourceReference>(sourcePath!),
    enabled: !!sourcePath,
    retry: false
  });
  if (!sourcePath) return null;
  const r = ref.data;
  return <div className="source-ref" aria-live="polite"><div className="label-row"><span>CONFIGURATION SOURCE</span>{r?.state === 'found' && <span className="best-effort" title={r.reason}>Best effort</span>}</div>{ref.isPending ? <span className="muted"><LoaderCircle className="spin" size={12} /> Locating…</span> : ref.error ? <span className="muted">{ref.error instanceof ApiError && ref.error.code === 'git_unavailable' ? 'Source links are unavailable in this session.' : message(ref.error)}</span> : r && r.state === 'found' && r.fileId && r.line ? <><code className="source-path">{r.path}:{r.line}</code><p className="muted small">{r.reason} The plan and state, not this link, decide what exists and what changes.</p><EditorButtons prefix={prefix} fileId={r.fileId} line={r.line} editor={editor} onDone={notify} /></> : r && <p className="muted small">{r.reason}</p>}</div>;
}

export function GitView({ projectId, editor, notify }: { projectId?: string; editor: EditorName; notify: (text: string) => void }) {
  const prefix = gitPrefix(projectId);
  const [selected, setSelected] = useState<string>();
  // Opening this view is the explicit request: it runs one read-only `git status`.
  const status = useQuery({
    queryKey: ['git-status', prefix],
    queryFn: () => post<GitContext>(`${prefix}git/status`, {}),
    retry: false,
    staleTime: Infinity
  });
  const c = status.data;
  const file = c?.files.find(f => f.id === selected);
  const diff = useQuery({
    queryKey: ['git-diff', prefix, selected, c?.checkedAt],
    queryFn: () => post<GitDiff>(`${prefix}git/diff`, { fileId: selected }),
    enabled: !!file?.diffable,
    retry: false
  });
  const refresh = () => {
    setSelected(undefined);
    void status.refetch();
  };
  if (status.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Reading the Git working tree with <code>git status</code>. Nothing is changed.</p></div>;
  if (status.error) return <div className="empty-state error-state" role="alert"><CircleHelp size={28} /><h2>Git context unavailable</h2><p>{message(status.error)}</p><Button variant="outline" onClick={refresh}>Try again</Button></div>;
  if (!c) return null;
  if (c.state !== 'repository') return <div className="empty-state" role="status"><FolderGit2 size={28} /><h2>{c.state === 'not_repository' ? 'Not a Git repository' : 'Git context unavailable'}</h2><p>{c.message}</p>{c.state !== 'not_repository' && <Button variant="outline" onClick={refresh}><RefreshCcw size={13} />Read again</Button>}</div>;
  const h = c.head!;
  return <div className="git-view">
   <section className="surface git-summary" aria-label="Repository">
    <div className="git-facts">
     <div><span className="muted">Repository</span><strong>{c.repository?.name}</strong>{c.repository?.worktree && <span className="pill">Linked worktree</span>}</div>
     <div><span className="muted">Project path</span><strong className="mono">{c.repository?.projectPath}</strong></div>
     <div><span className="muted">Branch</span><strong className="mono">{h.detached ? 'Detached HEAD' : h.branch || '—'}</strong></div>
     <div><span className="muted">HEAD</span><strong className="mono" title={h.commit}><GitCommitHorizontal size={13} />{h.unborn ? 'No commits yet' : shortCommit(h.commit)}</strong></div>
     <div><span className="muted">IaC working tree</span><strong className={c.clean ? 'tone-text-create' : 'tone-text-update'}>{c.clean ? 'Clean' : `${c.counts.changed} changed file${c.counts.changed === 1 ? '' : 's'}`}</strong></div>
    </div>
    <div className="git-actions"><span className="read-only-badge"><ShieldCheck size={13} />Never commits, checks out or pushes</span><Button variant="ghost" size="small" onClick={refresh} disabled={status.isFetching}><RefreshCcw size={12} />Refresh</Button></div>
   </section>
   {c.warnings.map(w => <div className="info-note warn-note" key={w.code} role="note"><TriangleAlert size={15} /><span>{w.message}</span></div>)}
   {(c.counts.other > 0 || c.counts.outside > 0) && <p className="info-line">{c.counts.outside > 0 && `${c.counts.outside} IaC file${c.counts.outside === 1 ? '' : 's'} changed elsewhere in the repository. `}{c.counts.other > 0 && `${c.counts.other} other changed file${c.counts.other === 1 ? '' : 's'} (not IaC) are not listed.`}</p>}
   {c.files.length === 0 ? <div className="empty-state"><FileCode2 size={26} /><h2>No IaC changes</h2><p>The project's .tf, .tf.json, variable and lock files{c.repository?.localModules ? ' and its local modules' : ''} match HEAD.</p></div> : <div className={`explorer git-explorer ${selected ? 'with-inspector' : ''}`}>
    <section className="resource-list" aria-label="Changed IaC files"><div className="list-caption"><span>Changed files</span><span>AGAINST HEAD</span></div>
     <table className="resources-table git-files"><thead><tr><th scope="col">Status</th><th scope="col">File</th><th scope="col">Kind</th></tr></thead><tbody>{c.files.map(f => <tr key={f.id} className={selected === f.id ? 'selected' : ''}><td><span className={`git-status git-${f.status}`}>{statusLabels[f.status]}</span></td><td><button className="resource-button git-file" aria-pressed={selected === f.id} onClick={() => setSelected(f.id)}>{f.kind === 'vars' || f.kind === 'lock' ? <FileLock2 size={15} /> : <FileCode2 size={15} />}<span className="resource-text"><strong className="mono">{f.path}</strong><span className="resource-type">{f.place === 'module' ? `Local module ${f.module}` : 'Project'}{f.staged ? ' · staged' : ''}{f.unstaged && f.staged ? ' · also unstaged' : ''}</span></span></button></td><td><span className="mode-badge">{kindLabels[f.kind]}</span></td></tr>)}</tbody></table>
     {c.incomplete && <p className="info-line">More files changed than are listed.</p>}
    </section>
    {file && <Drawer label="Diff inspector" onClose={() => setSelected(undefined)}><div className="inspector-header"><span><FileCode2 size={15} />Changes</span><Button variant="ghost" size="icon" aria-label="Close diff" onClick={() => setSelected(undefined)}><X size={16} /></Button></div><div className="inspector-body git-diff-body">
     <h2 className="mono git-diff-title">{file.path}</h2>
     <p className="muted small">{statusLabels[file.status]} · {file.place === 'module' ? `local module ${file.module}` : 'project root'} · compared with {diff.data?.base === 'none' ? 'nothing (new to Git)' : 'HEAD'}</p>
     {!file.diffable ? <div className="info-note" role="note"><LockKeyhole size={15} /><span>{file.reason}</span></div> : diff.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Reading the diff…</div> : diff.error ? <div className="info-note warn-note" role="alert"><TriangleAlert size={15} /><span>{message(diff.error)}</span></div> : diff.data && <DiffView diff={diff.data} prefix={prefix} editor={editor} notify={notify} />}
     <p className="muted small git-disclaimer">A Git diff shows edited text. Only a plan shows which resources actually change.</p>
    </div></Drawer>}
   </div>}
  </div>;
}

function DiffView({ diff, prefix, editor, notify }: { diff: GitDiff; prefix: string; editor: EditorName; notify: (text: string) => void }) {
  const firstLine = diff.hunks[0]?.lines.find(l => l.new)?.new || 1;
  return <>
   <div className="git-diff-stats"><span className="tone-text-create">+{diff.added}</span><span className="tone-text-destroy">−{diff.removed}</span>{diff.redacted > 0 && <span className="redacted"><LockKeyhole size={11} />{diff.redacted} line{diff.redacted === 1 ? '' : 's'} redacted</span>}{diff.file.status !== 'deleted' && <EditorButtons prefix={prefix} fileId={diff.file.id} line={firstLine} editor={editor} onDone={notify} />}</div>
   {diff.binary ? <div className="info-note" role="note"><CircleHelp size={15} /><span>{diff.notice}</span></div> : diff.hunks.length === 0 ? (diff.truncated ? null : <p className="muted">No line changes (for example, only the file mode changed).</p>) : diff.hunks.map((h, i) => <div className="git-hunk" key={i}><div className="git-hunk-header mono">@@ −{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@ {h.section}</div><table className="git-diff-lines"><tbody>{h.lines.map((l, j) => <tr key={j} className={`git-line git-line-${l.kind}`}><td className="git-ln" aria-label={l.old ? `old line ${l.old}` : undefined}>{l.old || ''}</td><td className="git-ln" aria-label={l.new ? `new line ${l.new}` : undefined}>{l.new || ''}</td><td className="git-mark" aria-hidden="true">{l.kind === 'add' ? '+' : l.kind === 'delete' ? '−' : l.kind === 'note' ? '\\' : ' '}</td><td className="git-text mono"><span className="sr-only">{l.kind === 'add' ? 'Added: ' : l.kind === 'delete' ? 'Removed: ' : ''}</span>{l.text}{l.redacted && <LockKeyhole size={10} className="git-redacted-icon" aria-label="redacted" />}</td></tr>)}</tbody></table></div>)}
   {diff.truncated && <p className="info-line">The diff is too long to show in full.</p>}
  </>;
}
