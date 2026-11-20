import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Code2, LoaderCircle } from 'lucide-react';
import { ApiError, api, send } from '../api';
import type { EditorName, SourceReference } from '../types';
import { Button } from '../components/ui/button';
import { errorMessage } from '../lib/errorMessage';

// Opening configuration files in the user's editor, from the Git view and the resource inspector.
export const editorLabels: Record<EditorName, string> = { vscode: 'VS Code', cursor: 'Cursor' };

export function EditorButtons({ prefix, fileId, line, editor, onDone }: { prefix: string; fileId: string; line: number; editor: EditorName; onDone: (text: string) => void }) {
  const [busy, setBusy] = useState(false);
  async function open() {
    setBusy(true);
    try {
      const r = await send<{ path: string; line: number }>('POST', `${prefix}editor/open`, { fileId, line, editor });
      onDone(`Asked ${editorLabels[editor]} to open ${r.path}:${r.line}`);
    } catch (e) {
      onDone(errorMessage(e));
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
  return <div className="source-ref" aria-live="polite"><div className="label-row"><span>CONFIGURATION SOURCE</span>{r?.state === 'found' && <span className="best-effort" title={r.reason}>Best effort</span>}</div>{ref.isPending ? <span className="muted"><LoaderCircle className="spin" size={12} /> Locating…</span> : ref.error ? <span className="muted">{ref.error instanceof ApiError && ref.error.code === 'git_unavailable' ? 'Source links are unavailable in this session.' : errorMessage(ref.error)}</span> : r && r.state === 'found' && r.fileId && r.line ? <><code className="source-path">{r.path}:{r.line}</code><p className="muted small">{r.reason} The plan and state, not this link, decide what exists and what changes.</p><EditorButtons prefix={prefix} fileId={r.fileId} line={r.line} editor={editor} onDone={notify} /></> : r && <p className="muted small">{r.reason}</p>}</div>;
}
