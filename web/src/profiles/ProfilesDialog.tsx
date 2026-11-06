import { useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Copy, LoaderCircle, Pencil, Plus, Star, Trash2, X } from 'lucide-react';
import { api, send } from '../api';
import type { ExecutionProfile, ProfileInput, VarFile } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { IdentityBadge } from './ContextSummary';
import { profilesQuery } from './selection';

const blank: ProfileInput = { name: '', engine: '', workspace: '', varFiles: [], parallelism: 0, lockTimeoutSeconds: 0, awsProfile: '', awsRegion: '', envNames: [] };

function message(error: unknown) {
  return error instanceof Error ? error.message : 'The profile could not be saved.';
}

function size(bytes: number) {
  return bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KiB`;
}

// ProfilesDialog manages a project's execution profiles. Profiles store names and aliases
// only; the form has no field for a credential, token or variable value.
export function ProfilesDialog({ projectId, workspaces = [], selected, onSelect, onClose }: {
  projectId: string;
  workspaces?: string[];
  selected: string;
  onSelect: (id: string) => void;
  onClose: () => void;
}) {
  const client = useQueryClient();
  const profiles = useQuery(profilesQuery(projectId));
  const [editing, setEditing] = useState<ExecutionProfile | 'new'>();
  const [confirmDelete, setConfirmDelete] = useState<string>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, close, onClose);
  const base = `projects/${projectId}/execution-profiles`;
  async function act(action: () => Promise<unknown>) {
    setBusy(true);
    setError('');
    try {
      await action();
      await client.invalidateQueries({ queryKey: ['profiles', projectId] });
      // Edited or removed profiles close the views that used them; refresh their summaries.
      await client.invalidateQueries({ queryKey: ['execution-context'] });
      await client.invalidateQueries({ queryKey: ['plan-options'] });
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  const list = profiles.data?.profiles || [];
  const readOnly = profiles.data?.status === 'unavailable';
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog dialog-wide" role="dialog" aria-modal="true" aria-labelledby="profiles-title">
    <div className="dialog-header"><h2 id="profiles-title">Execution profiles</h2><Button ref={close} variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    {editing ? <ProfileEditor projectId={projectId} profile={editing === 'new' ? undefined : editing} workspaces={workspaces} onDone={async saved => {
        setEditing(undefined);
        await client.invalidateQueries({ queryKey: ['profiles', projectId] });
        await client.invalidateQueries({ queryKey: ['execution-context'] });
        await client.invalidateQueries({ queryKey: ['plan-options'] });
        if (saved && editing === 'new') onSelect(saved.id);
      }} onCancel={() => setEditing(undefined)} /> : <>
     <p>A profile names the workspace, variable files, AWS profile and region an operation uses. Choosing one affects only this browser tab. Profiles never store credentials, tokens or variable values. <IdentityBadge /></p>
     {profiles.data?.notice && <p className="plan-warning" role="note">{profiles.data.notice}</p>}
     {profiles.isPending ? <div className="empty-state" role="status"><LoaderCircle className="spin" /></div> : profiles.error ? <p className="dialog-error" role="alert">{message(profiles.error)}</p> : list.length === 0 ? <p className="muted">No profiles yet. Without a profile, operations use the environment Hclaro was started with.</p> : <ul className="profile-list">{list.map(p => <li key={p.id} className={p.id === selected ? 'chosen' : undefined}>
       <div className="profile-text"><strong>{p.name}</strong>{p.default && <span className="hub-tag">Default</span>}{p.id === selected && <span className="hub-tag hub-tag-quiet">This tab</span>}<span className="muted">{[p.workspace && `workspace ${p.workspace}`, p.awsProfile && `AWS ${p.awsProfile}`, p.awsRegion, p.varFiles.length ? `${p.varFiles.length} variable file${p.varFiles.length === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ') || 'Inherited environment with this name'} · rev {p.revision}</span></div>
       <div className="profile-actions">
        <Button size="small" variant="outline" onClick={() => onSelect(p.id)} disabled={p.id === selected}>Use in this tab</Button>
        <Button size="icon" variant="ghost" aria-label={`Edit ${p.name}`} disabled={readOnly || busy} onClick={() => setEditing(p)}><Pencil size={13} /></Button>
        <Button size="icon" variant="ghost" aria-label={`Clone ${p.name}`} disabled={readOnly || busy} onClick={() => void act(() => send('POST', `${base}/${p.id}/clone`, { name: `${p.name} copy`.slice(0, 60) }))}><Copy size={13} /></Button>
        <Button size="icon" variant="ghost" aria-label={p.default ? `Stop using ${p.name} as default` : `Make ${p.name} the default`} aria-pressed={p.default} disabled={readOnly || busy} onClick={() => void act(() => send('POST', `${base}/default`, { profileId: p.default ? '' : p.id }))}><Star size={13} /></Button>
        {confirmDelete === p.id ? <Button size="small" variant="outline" disabled={busy} onClick={() => void act(async () => {
            await send('DELETE', `${base}/${p.id}`, { expectedRevision: p.revision });
            setConfirmDelete(undefined);
            if (p.id === selected) onSelect('');
          })}>Remove {p.name}</Button> : <Button size="icon" variant="ghost" aria-label={`Remove ${p.name}`} disabled={readOnly || busy} onClick={() => setConfirmDelete(p.id)}><Trash2 size={13} /></Button>}
       </div>
      </li>)}</ul>}
     {error && <p className="dialog-error" role="alert">{error}</p>}
     <div className="dialog-actions"><Button variant="ghost" onClick={() => onSelect('')} disabled={!selected}>Use no profile in this tab</Button><span className="context-spacer" /><Button onClick={() => setEditing('new')} disabled={readOnly}><Plus size={14} />New profile</Button></div>
    </>}
   </div>
  </div>;
}

function ProfileEditor({ projectId, profile, workspaces, onDone, onCancel }: {
  projectId: string;
  profile?: ExecutionProfile;
  workspaces: string[];
  onDone: (saved?: ExecutionProfile) => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<ProfileInput>(() => profile ? { name: profile.name, engine: profile.engine, workspace: profile.workspace, varFiles: profile.varFiles, parallelism: profile.parallelism, lockTimeoutSeconds: profile.lockTimeoutSeconds, awsProfile: profile.awsProfile, awsRegion: profile.awsRegion, envNames: profile.envNames } : blank);
  const [envText, setEnvText] = useState(() => (profile?.envNames || []).join(', '));
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  // Candidate variable files: names, sizes and times only. Contents are never requested.
  const files = useQuery({ queryKey: ['var-files', projectId], queryFn: () => api<{ files: VarFile[]; truncated: boolean }>(`projects/${projectId}/var-files`) });
  const change = (patch: Partial<ProfileInput>) => setForm(f => ({ ...f, ...patch }));
  const known = new Set((files.data?.files || []).map(f => f.ref));
  async function save() {
    setSaving(true);
    setError('');
    const body = { ...form, envNames: envText.split(/[\s,]+/).filter(Boolean) };
    try {
      const saved = profile ? await send<ExecutionProfile>('PATCH', `projects/${projectId}/execution-profiles/${profile.id}`, { ...body, expectedRevision: profile.revision }) : await send<ExecutionProfile>('POST', `projects/${projectId}/execution-profiles`, body);
      onDone(saved);
    } catch (e) {
      setError(message(e));
    } finally {
      setSaving(false);
    }
  }
  return <form className="profile-editor" onSubmit={e => {
      e.preventDefault();
      void save();
    }}>
   <label className="field"><span>Profile name</span><input value={form.name} maxLength={60} onChange={e => change({ name: e.target.value })} required aria-describedby="profile-name-hint" /><small id="profile-name-hint" className="muted">A label, for example "Production EU". Profiles of other projects with the same name stay separate.</small></label>
   <fieldset><legend>Workspace</legend>
    <label className="field"><span>Engine</span><select value={form.engine} onChange={e => change({ engine: e.target.value as ProfileInput['engine'] })}><option value="">Project's choice</option><option value="terraform">Terraform</option><option value="tofu">OpenTofu</option></select></label>
    <label className="field"><span>Workspace selector</span><input value={form.workspace} list="profile-workspaces" maxLength={90} placeholder="Any workspace" onChange={e => change({ workspace: e.target.value.trim() })} /><datalist id="profile-workspaces">{workspaces.map(w => <option key={w} value={w} />)}</datalist><small className="muted">When set, this profile can only be used with that Terraform workspace. A workspace is a state namespace, not an AWS profile or account.</small></label>
   </fieldset>
   <fieldset><legend>Variables</legend>
    {files.isPending ? <p className="muted"><LoaderCircle className="spin" size={12} /> Listing variable files…</p> : files.error ? <p className="dialog-error">{message(files.error)}</p> : <div className="var-file-options">{(files.data?.files || []).length === 0 && <p className="muted">No optional .tfvars files in this project. terraform.tfvars and *.auto.tfvars load automatically.</p>}{(files.data?.files || []).map(f => <label key={f.ref}><input type="checkbox" checked={form.varFiles.includes(f.ref)} onChange={e => change({ varFiles: e.target.checked ? [...form.varFiles, f.ref] : form.varFiles.filter(v => v !== f.ref) })} /><code>{f.ref}</code>{f.linked && <span className="muted"> → <code>{f.path}</code></span>}<span className="muted"> · {size(f.size)} · modified {new Date(f.modifiedAt).toLocaleString()}</span></label>)}{form.varFiles.filter(v => !known.has(v)).map(v => <label key={v} className="issue-text"><input type="checkbox" checked onChange={() => change({ varFiles: form.varFiles.filter(x => x !== v) })} /><code>{v}</code> is missing or unsafe; uncheck to remove it</label>)}{files.data?.truncated && <p className="muted">The list was truncated.</p>}</div>}
    <small className="muted">Only approved files can be passed with -var-file. File contents are never shown.</small>
    <label className="field"><span>Expected environment variable names</span><input value={envText} onChange={e => setEnvText(e.target.value)} placeholder="VAULT_ADDR, TF_VAR_db_password" aria-describedby="env-hint" /><small id="env-hint" className="muted">Names only. Hclaro checks that they are set before running and never reads or stores values.</small></label>
   </fieldset>
   <fieldset><legend>Cloud identity</legend>
    <label className="field"><span>AWS profile</span><input value={form.awsProfile} maxLength={64} placeholder="Inherited" autoComplete="off" onChange={e => change({ awsProfile: e.target.value.trim() })} /><small className="muted">The name of a profile in your AWS configuration, used through your normal SSO or credential setup. Never enter an access key.</small></label>
    <label className="field"><span>AWS region</span><input value={form.awsRegion} maxLength={32} placeholder="Inherited" autoComplete="off" onChange={e => change({ awsRegion: e.target.value.trim() })} /></label>
    <p className="muted"><IdentityBadge /> Hclaro does not check which account these names resolve to.</p>
   </fieldset>
   <fieldset><legend>Plan options</legend>
    <label className="field"><span>Parallelism</span><input type="number" min={0} max={256} value={form.parallelism || ''} placeholder="Engine default" onChange={e => change({ parallelism: Number(e.target.value) || 0 })} /></label>
    <label className="field"><span>State lock timeout (seconds)</span><input type="number" min={0} max={3600} value={form.lockTimeoutSeconds || ''} placeholder="Engine default" onChange={e => change({ lockTimeoutSeconds: Number(e.target.value) || 0 })} /></label>
   </fieldset>
   {error && <p className="dialog-error" role="alert">{error}</p>}
   <div className="dialog-actions"><Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button><Button type="submit" disabled={saving}>{saving ? 'Saving…' : profile ? 'Save changes' : 'Create profile'}</Button></div>
  </form>;
}
