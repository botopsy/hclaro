import { useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, ChevronDown, GitBranch, LoaderCircle, ShieldCheck, TriangleAlert, X } from 'lucide-react';
import { post } from '../api';
import type { GitBranchList, GitContext, GitSwitchPreview } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { shortCommit } from './GitView';

function message(error: unknown) {
  return error instanceof Error ? error.message : 'Unable to reach the local Hclaro server.';
}
// Queries whose answers depend on the checked-out source. None of them runs Terraform/OpenTofu.
const sourceQueries = ['git-head', 'git-status', 'git-diff', 'git-branches', 'source', 'plans', 'plan', 'plan-options', 'var-files', 'execution-context', 'operations', 'history', 'projects'];

// The Git branch selector (M29). It switches only to existing local branches, after a
// preview and an explicit confirmation; the server re-checks everything before switching.
// It is independent of the Terraform workspace selector.
export function BranchSwitcher({ prefix, workspace, notify }: { prefix: string; workspace?: string; notify: (text: string) => void }) {
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState('');
  const [target, setTarget] = useState<string>();
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  // Listing runs one read-only git for-each-ref, only when the selector is opened.
  const list = useQuery({ queryKey: ['git-branches', prefix], queryFn: () => post<GitBranchList>(`${prefix}git/branches`, {}), enabled: open, retry: false });
  useEffect(() => {
    if (!open) return;
    function key(event: KeyboardEvent) {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      setOpen(false);
      trigger.current?.focus();
    }
    function pointer(event: PointerEvent) {
      if (!wrapper.current?.contains(event.target as Node)) setOpen(false);
    }
    window.addEventListener('keydown', key, true);
    document.addEventListener('pointerdown', pointer);
    return () => {
      window.removeEventListener('keydown', key, true);
      document.removeEventListener('pointerdown', pointer);
    };
  }, [open]);
  const l = list.data;
  const h = l?.head;
  const shown = (l?.branches ?? []).filter(b => b.name.toLowerCase().includes(filter.trim().toLowerCase()));
  const others = (l?.branches ?? []).filter(b => !b.current);
  return <div className="branch-switcher" ref={wrapper}>
   <button ref={trigger} type="button" className="git-badge branch-trigger" aria-expanded={open} aria-controls="branch-switcher-panel" aria-label="Switch Git branch" title="Switch to another existing local Git branch. The Terraform workspace does not change." onClick={() => setOpen(o => !o)}><ChevronDown size={13} aria-hidden="true" /></button>
   {open && <div id="branch-switcher-panel" className="switcher-popover branch-popover" role="group" aria-label="Switch Git branch">
    <div className="switcher-label">Git branch</div>
    {list.isPending ? <p className="branch-note" role="status"><LoaderCircle className="spin" size={12} /> Listing local branches…</p>
     : list.error ? <p className="branch-note" role="alert">{message(list.error)}</p>
     : l && l.state !== 'repository' ? <p className="branch-note" role="status">{l.message}</p>
     : l && <>
      <p className="branch-note">{h?.detached ? <><TriangleAlert size={12} aria-hidden="true" /> Detached HEAD at <code>{shortCommit(h.commit)}</code></> : <>Current: <strong className="mono">{h?.branch}</strong></>}</p>
      {others.length > 0 && <input className="branch-filter" type="search" placeholder="Filter branches" aria-label="Filter branches" value={filter} onChange={e => setFilter(e.target.value)} autoFocus />}
      {others.length === 0 ? <p className="branch-note">No other local branches. Hclaro never creates or fetches branches.</p>
       : <ul aria-label="Local branches">{shown.map(b => <li key={b.name}><button type="button" className="switcher-option" aria-current={b.current ? 'true' : undefined} disabled={b.current} onClick={() => {
          setOpen(false);
          setTarget(b.name);
        }}><GitBranch size={14} aria-hidden="true" /><span><strong className="mono">{b.name}</strong><span>{b.current ? 'Checked out' : shortCommit(b.commit)}</span></span></button></li>)}
        {shown.length === 0 && <li className="branch-note">No branch matches.</li>}</ul>}
      {(l.hidden > 0 || l.incomplete) && <p className="branch-note">{l.hidden > 0 && `${l.hidden} branch${l.hidden === 1 ? '' : 'es'} with unusual names not shown. `}{l.incomplete && 'More branches exist than are listed.'}</p>}
     </>}
    <p className="branch-note branch-safety"><ShieldCheck size={12} aria-hidden="true" /> Existing local branches only. Never creates, fetches, stashes or discards.</p>
   </div>}
   {target && <SwitchDialog prefix={prefix} target={target} workspace={workspace} notify={notify} onClose={() => {
     setTarget(undefined);
     trigger.current?.focus();
   }} />}
  </div>;
}

function SwitchDialog({ prefix, target, workspace, notify, onClose }: { prefix: string; target: string; workspace?: string; notify: (text: string) => void; onClose: () => void }) {
  const dialog = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useModal(dialog, cancel, onClose);
  const client = useQueryClient();
  // The preview reads the working tree; it authorizes nothing, the server checks again.
  const preview = useQuery({ queryKey: ['git-switch-preview', prefix, target], queryFn: () => post<GitSwitchPreview>(`${prefix}git/switch/preview`, { branch: target }), retry: false, gcTime: 0 });
  const p = preview.data;
  const run = useMutation({
    mutationFn: () => post<{ git: GitContext }>(`${prefix}git/switch`, { branch: target, expectedBranch: p!.from.branch ?? '', expectedCommit: p!.from.commit ?? '' }),
    onSuccess: () => {
      for (const key of sourceQueries) void client.invalidateQueries({ queryKey: [key] });
      notify(`Switched to ${target}. No Terraform/OpenTofu command ran; plans made before the switch must pass their checks again. Use Read again to re-inspect backend settings.`);
      onClose();
    },
    onSettled: () => void client.invalidateQueries({ queryKey: ['git-switch-preview', prefix, target] })
  });
  const from = p?.from.detached ? `detached HEAD @ ${shortCommit(p.from.commit)}` : p?.from.branch;
  const t = p?.workingTree;
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog branch-dialog" role="dialog" aria-modal="true" aria-labelledby="branch-title" aria-describedby="branch-summary">
    <div className="dialog-header"><h2 id="branch-title">Switch Git branch?</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    {preview.isPending ? <p id="branch-summary" role="status"><LoaderCircle className="spin" size={13} /> Checking the working tree with <code>git status</code>. Nothing is changed.</p>
     : preview.error ? <p id="branch-summary" className="info-note warn-note" role="alert"><TriangleAlert size={15} /><span>{message(preview.error)}</span></p>
     : p && <>
      <p id="branch-summary" className="branch-route"><strong className="mono">{from}</strong><ArrowRight size={14} aria-label="to" /><strong className="mono">{p.to.name}</strong></p>
      <dl className="branch-facts">
       <dt>Working tree</dt><dd className={t!.clean ? 'tone-text-create' : 'tone-text-update'}>{t!.clean ? 'Clean' : [t!.staged && `${t!.staged} staged`, t!.modified && `${t!.modified} modified`, t!.untracked && `${t!.untracked} untracked`].filter(Boolean).join(', ') || 'Not fully read'}</dd>
       <dt>Source changes</dt><dd>{p.sourceChanges.files === 0 ? 'No files differ' : `${p.sourceChanges.files} file${p.sourceChanges.files === 1 ? '' : 's'} differ · ${p.sourceChanges.iac} IaC · ${p.sourceChanges.inProject} in this project`}</dd>
       <dt>Terraform workspace</dt><dd>{workspace ? <><span className="mono">{workspace}</span> (unchanged)</> : 'Unchanged'}</dd>
      </dl>
      {p.blocked.map(b => <div className="info-note warn-note" role="alert" key={b.code}><TriangleAlert size={15} /><span>{b.message}</span></div>)}
      {p.warnings.map(w => <div className="info-note" role="note" key={w.code}><TriangleAlert size={15} /><span>{w.message}</span></div>)}
      <p className="muted small">Switching changes files in the working tree. It runs no init, plan, refresh or apply; plans made before the switch must pass their checks again.</p>
     </>}
    {run.error && <div className="info-note warn-note" role="alert"><TriangleAlert size={15} /><span>{message(run.error)}</span></div>}
    <div className="dialog-actions"><Button ref={cancel} variant="ghost" onClick={onClose}>Cancel</Button><Button onClick={() => run.mutate()} disabled={!p?.canSwitch || run.isPending}>{run.isPending ? <LoaderCircle className="spin" size={13} /> : <GitBranch size={13} />}Switch to {target}</Button></div>
   </div>
  </div>;
}
