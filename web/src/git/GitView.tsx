import { skipToken, useQuery } from '@tanstack/react-query';
import { CircleHelp, FileCode2, FolderGit2, LoaderCircle, LockKeyhole, RefreshCcw, TriangleAlert } from 'lucide-react';
import { send } from '../api';
import { EditorButtons } from './editor';
import type { EditorName, GitContext, GitDiff, GitFileStatus } from '../types';
import { Button } from '../components/ui/button';
import { Chip } from '../components/Chip';
import { DataTable, type Column } from '../components/DataTable';
import { Disclosure } from '../components/Disclosure';
import { EmptyState } from '../components/EmptyState';
import { errorMessage } from '../lib/errorMessage';
import { ErrorState } from '../components/ErrorState';
import { BranchSwitcher } from './BranchSwitcher';

// gitPrefix is the API prefix of a project's Git routes: the launch project's are at the
// API root, a Hub project's under projects/<id>/.
export function gitPrefix(projectId?: string) {
  return projectId ? `projects/${projectId}/` : '';
}
const statusLabels: Record<GitFileStatus['status'], string> = {
  modified: 'Modified',
  added: 'Added',
  deleted: 'Deleted',
  untracked: 'Untracked',
  typechange: 'Type changed',
  conflicted: 'Conflict'
};
const statusLetters: Record<GitFileStatus['status'], string> = { modified: 'M', added: 'A', deleted: 'D', untracked: '?', typechange: 'T', conflicted: 'U' };
const kindLabels: Record<GitFileStatus['kind'], string> = { hcl: 'HCL', json: 'JSON', vars: 'Variables', lock: 'Lock file' };
export function shortCommit(commit?: string) {
  return commit ? commit.slice(0, 7) : '';
}
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

export function GitView({ projectId, workspace, editor, notify }: { projectId?: string; workspace?: string; editor: EditorName; notify: (text: string) => void }) {
  const prefix = gitPrefix(projectId);
  // Opening this view is the explicit request: it runs one read-only `git status`.
  const status = useQuery({
    queryKey: ['git-status', prefix],
    queryFn: () => send<GitContext>('POST', `${prefix}git/status`, {}),
    retry: false,
    staleTime: Infinity
  });
  const c = status.data;
  const refresh = () => void status.refetch();
  if (status.isPending) return <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Reading the Git working tree with <code>git status</code>. Nothing is changed.</p></div>;
  if (status.error) return <ErrorState inline icon={CircleHelp} title="Git context unavailable" message={errorMessage(status.error)} retry={refresh} retryLabel="Try again" />;
  if (!c) return null;
  if (c.state !== 'repository') return <EmptyState icon={<FolderGit2 size={26} />} title={c.state === 'not_repository' ? 'Not a Git repository' : 'Git context unavailable'} action={c.state !== 'not_repository' && <Button variant="outline" onClick={refresh}><RefreshCcw size={13} />Read again</Button>}>{c.message}</EmptyState>;
  const h = c.head!;
  const columns: Column<GitFileStatus>[] = [
    { key: 'status', header: <span className="sr-only">Status</span>, className: 'git-status-cell', cell: f => <span className={`git-status git-${f.status}`} title={statusLabels[f.status]}><span aria-hidden="true">{statusLetters[f.status]}</span><span className="sr-only">{statusLabels[f.status]}</span></span> },
    { key: 'file', header: 'File', cell: f => <><span className="mono">{f.path}</span>{(f.place === 'module' || f.staged) && <span className="muted">{f.place === 'module' ? ` · local module ${f.module}` : ''}{f.staged ? ' · staged' : ''}{f.unstaged && f.staged ? ', also unstaged' : ''}</span>}</> },
    { key: 'kind', header: 'Kind', cell: f => kindLabels[f.kind] },
    { key: 'lines', header: <span className="sr-only">Lines</span>, className: 'git-lines-cell', cell: f => <LineCounts prefix={prefix} fileId={f.id} checkedAt={c.checkedAt} /> }
  ];
  return <div className="git-view">
   <section className="git-summary" aria-label="Repository">
    <div><span className="muted">Repository</span><span>{c.repository?.name}{c.repository?.worktree && <Chip>Linked worktree</Chip>}</span></div>
    <div><span className="muted">Project path</span><span className="mono">{c.repository?.projectPath}</span></div>
    <div><span className="muted">Branch</span><BranchSwitcher prefix={prefix} current={h.detached ? 'Detached HEAD' : h.branch || '—'} workspace={workspace} notify={notify} /></div>
    <div><span className="muted">HEAD</span><span className="mono" title={h.commit}>{h.unborn ? 'No commits yet' : shortCommit(h.commit)}</span></div>
    <div><span className="muted">IaC tree</span><span className={c.clean ? 'tone-text-create' : 'tone-text-update'}>{c.clean ? 'Clean' : `${plural(c.counts.changed, 'uncommitted file')}`}</span></div>
    <Button variant="outline" size="small" onClick={refresh} disabled={status.isFetching}><RefreshCcw size={12} />Refresh</Button>
   </section>
   {c.warnings.map(w => <div className="info-note warn-note" key={w.code} role="note"><TriangleAlert size={15} /><span>{w.message}</span></div>)}
   {c.counts.outside > 0 && <p className="info-line">{plural(c.counts.outside, 'IaC file')} changed elsewhere in the repository.</p>}
   {c.files.length === 0 ? <EmptyState icon={<FileCode2 size={26} />} title="No IaC changes">The project's .tf, .tf.json, variable and lock files{c.repository?.localModules ? ' and its local modules' : ''} match HEAD.</EmptyState> : <>
    <p className="git-files-heading"><strong>{plural(c.files.length, 'changed IaC file')}</strong><span className="muted">Commit in your terminal or editor; Hclaro only shows the diff against HEAD.</span></p>
    <DataTable label="Changed IaC files" columns={columns} rows={c.files} rowKey={f => f.id} rowLabel={f => f.path} expand={f => <FileDiff file={f} prefix={prefix} checkedAt={c.checkedAt} editor={editor} notify={notify} />} />
    {c.incomplete && <p className="info-line">More files changed than are listed.</p>}
    <p className="muted small">A Git diff shows edited text. Only a plan shows which resources actually change.</p>
   </>}
   {c.counts.other > 0 && <Disclosure summary={`${plural(c.counts.other, 'other changed file')} (not IaC)`}><p className="muted small">Hclaro lists and diffs only IaC files. Run <code>git status</code> in your terminal to see the others.</p></Disclosure>}
  </div>;
}

// +N −N of a file whose diff was read by expanding its row; this never fetches anything.
function LineCounts({ prefix, fileId, checkedAt }: { prefix: string; fileId: string; checkedAt: string }) {
  const d = useQuery<GitDiff>({ queryKey: ['git-diff', prefix, fileId, checkedAt], queryFn: skipToken }).data;
  return d ? <span className="mono"><span className="tone-text-create">+{d.added}</span> <span className="tone-text-destroy">−{d.removed}</span></span> : null;
}

// An expanded row: the server-redacted diff of one file, read only when the row is opened.
function FileDiff({ file, prefix, checkedAt, editor, notify }: { file: GitFileStatus; prefix: string; checkedAt: string; editor: EditorName; notify: (text: string) => void }) {
  const diff = useQuery({
    queryKey: ['git-diff', prefix, file.id, checkedAt],
    queryFn: () => send<GitDiff>('POST', `${prefix}git/diff`, { fileId: file.id }),
    enabled: file.diffable,
    retry: false
  });
  return <div className="git-diff" role="region" aria-label={`Diff of ${file.path}`}>
   <p className="muted small">{statusLabels[file.status]} · {file.place === 'module' ? `local module ${file.module}` : 'project root'}{diff.data && ` · compared with ${diff.data.base === 'none' ? 'nothing (new to Git)' : 'HEAD'}`}</p>
   {!file.diffable ? <div className="info-note" role="note"><LockKeyhole size={15} /><span>{file.reason}</span></div> : diff.isPending ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Reading the diff…</p> : diff.error ? <div className="info-note warn-note" role="alert"><TriangleAlert size={15} /><span>{errorMessage(diff.error)}</span></div> : diff.data && <DiffView diff={diff.data} prefix={prefix} editor={editor} notify={notify} />}
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
