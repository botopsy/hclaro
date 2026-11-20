import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, FilePen, LoaderCircle, Undo2, X } from 'lucide-react';
import { api, post } from '../api';
import { Button } from '../components/ui/button';
import { scopeKey, scopedPath, type PlanScope } from '../lib/scope';
import type { ChangeView } from '../types';
import { errorMessage } from '../lib/errorMessage';

// Proposed changes (AI-01.12): a reply may carry whole-file proposals. The server validates each
// one against the project files and computes the diff; the reply's own text is never trusted.
// Each proposal is applied or rejected on its own (no "apply all"); applying writes the file and
// nothing else: no validate, plan or apply runs.


// The fenced header the prompts ask for; a reply without it has no proposals to fetch.
export const hasProposal = (text: string) => /^`{3,}hclaro-change /m.test(text);

export function useChanges(scope: PlanScope, id: string, turns: number, enabled: boolean) {
  return useQuery({
    queryKey: ['ai-changes', scopeKey(scope), id, turns],
    queryFn: () => api<{ changes: ChangeView[] }>(scopedPath(scope, `ai/conversations/${id}/changes`)),
    enabled,
    retry: false,
    gcTime: 0
  });
}

export function ProposedChanges({ scope, id, changes }: { scope: PlanScope; id: string; changes: ChangeView[] }) {
  return <ul className="assist-changes" aria-label="Proposed changes">{changes.map(c => <ChangeCard key={`${c.turn}/${c.index}`} scope={scope} id={id} change={c} />)}</ul>;
}

function DiffText({ diff }: { diff: string }) {
  return <pre className="assist-diff" aria-label="Diff computed by Hclaro" tabIndex={0}>{diff.split('\n').map((l, i) =>
    <span key={i} className={l.startsWith('+++') || l.startsWith('---') ? 'diff-head' : l.startsWith('+') ? 'diff-add' : l.startsWith('-') ? 'diff-del' : l.startsWith('@@') ? 'diff-hunk' : undefined}>{l}{'\n'}</span>)}</pre>;
}

function ChangeCard({ scope, id, change }: { scope: PlanScope; id: string; change: ChangeView }) {
  const client = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [refused, setRefused] = useState<string>();
  const [result, setResult] = useState<ChangeView>();
  const c = result ?? change;
  async function decide(action: 'apply' | 'reject' | 'undo') {
    setBusy(true);
    setRefused(undefined);
    try {
      const body = { turn: c.turn, index: c.index, baseSha256: c.baseSha256 ?? '', newSha256: c.newSha256 ?? '' };
      setResult(await post<ChangeView>(scopedPath(scope, `ai/conversations/${id}/changes/${action}`), body));
      // A written file changes plans, the Git view and the project context.
      if (action !== 'reject') void client.invalidateQueries();
    } catch (err) {
      setRefused(errorMessage(err, 'The request was refused.'));
    } finally {
      setBusy(false);
    }
  }
  const size = c.created ? 'new file' : `+${c.added} −${c.removed}`;
  return <li className={`assist-change ${c.status}`}>
   <p className="assist-change-head"><FilePen size={14} /><strong>Proposed change: <code>{c.path}</code></strong>{c.status !== 'invalid' && <span className="muted">{size}</span>}</p>
   {c.status === 'invalid' && <p className="plan-warning" role="note"><span>Cannot be applied: {c.reason}</span></p>}
   {c.diff && c.status !== 'invalid' && <DiffText diff={c.diff} />}
   {c.status === 'applied' && <p className="info-line" role="status">Written to <code>{c.path}</code>{c.created ? ' (new file)' : <>; a backup was kept for Undo, and the replaced file stays next to it as <code>{c.aside}</code> in case another program still had it open. Hclaro deletes that file after 30 days only if nothing was written into it</>}. Saved plans of this project no longer match its configuration. Nothing was run: run a plan to review the effect, and see Git for the file's status.</p>}
   {(c.status === 'applied' || c.status === 'undone') && c.reason && <p className="plan-warning" role="alert"><span>{c.reason}</span></p>}
   {c.status === 'rejected' && <p className="muted" role="status">Rejected. Nothing was written.</p>}
   {c.status === 'undone' && <p className="muted" role="status">Undone: {c.created ? 'the new file was removed' : 'the file was restored from its backup'}.</p>}
   {refused && <p className="dialog-error" role="alert">{refused}</p>}
   {(c.status === 'ready' || c.canUndo) && <div className="form-actions">
    {c.status === 'ready' && <>
     <Button variant="outline" size="small" disabled={busy} onClick={() => void decide('reject')}><X size={13} />Reject</Button>
     <Button size="small" disabled={busy} onClick={() => void decide('apply')}>{busy ? <LoaderCircle className="spin" size={13} /> : <Check size={13} />}Apply to {c.path}</Button>
    </>}
    {c.canUndo && <Button variant="outline" size="small" disabled={busy} onClick={() => void decide('undo')}><Undo2 size={13} />Undo</Button>}
   </div>}
  </li>;
}
