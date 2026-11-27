import { AlertTriangle, CircleHelp, Fingerprint, KeyRound, ShieldAlert } from 'lucide-react';
import type { ExecutionContext, ExecutionSummary, ScopeView } from '../types';
import { AuthIndicator } from './AuthPanel';
import { engineLabel } from '../lib/format';


export const workspaceSources: Record<ExecutionSummary['workspaceSource'], string> = {
  profile: 'selected by the profile',
  tab: 'this tab',
  launch: 'read when Hclaro started',
  cli_default: 'CLI default'
};

// The account a credential belongs to is never looked up: say so wherever a context is shown.
export function IdentityBadge() {
  return <span className="identity-badge" title="Hclaro does not call cloud APIs to check which account these credentials belong to."><ShieldAlert size={11} />Identity not verified</span>;
}

// ContextSummary is the review of everything an operation will use, shown before it runs.
// It contains names, aliases and paths only; values are never sent to the browser.
export function ContextSummary({ summary, title = 'Execution context' }: { summary: ExecutionSummary; title?: string }) {
  const c = summary.context;
  const vars = c.varFiles;
  return <section className="context-summary" aria-label={title}>
   <div className="context-summary-heading"><h3>{title}</h3><span className="hub-tag">{c.profileName ? <>Profile <strong>{c.profileName}</strong> · rev {c.profileRevision}</> : 'No profile · inherited environment'}</span>{c.profileId && c.awsProfile ? <AuthIndicator projectId={c.projectId} profileId={c.profileId} /> : <IdentityBadge />}</div>
   <dl className="metadata compact-metadata">
    <div><dt>Engine</dt><dd>{engineLabel(summary.engine, 'Automatic')}{summary.engineVersion ? ` ${summary.engineVersion}` : ''}{summary.engine && !summary.engineFound && ' (not found on PATH)'}</dd></div>
    <div><dt>Workspace</dt><dd>{summary.workspace ? <><strong>{summary.workspace}</strong> <span className="muted">({workspaceSources[summary.workspaceSource]})</span></> : <span className="muted">CLI default (not listed yet)</span>}</dd></div>
    <div><dt>Root folder</dt><dd className="mono">{summary.rootPath}</dd></div>
    <div><dt>Backend · mode</dt><dd>{summary.backend} · {summary.executionMode}</dd></div>
    <div><dt>Data directory</dt><dd className="mono">{summary.dataDir}</dd></div>
    <div><dt>Variable files</dt><dd>{vars.length ? <ul className="plain-list">{vars.map(f => <li key={f.ref}><code>{f.ref}</code>{f.linked && <span className="muted"> → <code>{f.path}</code></span>}</li>)}</ul> : <span className="muted">Only files the engine loads automatically</span>}</dd></div>
    <div><dt>AWS profile</dt><dd>{c.awsProfile ? <><code>{c.awsProfile}</code> <span className="muted">via {c.awsAuth === 'aws-vault' ? 'aws-vault' : 'AWS CLI'}</span></> : <span className="muted">Inherited (not set by Hclaro)</span>}</dd></div>
    {(c.awsExpectedAccount || c.awsExpectedRole) && <div><dt>Expected AWS identity</dt><dd>{c.awsExpectedAccount && <code>{c.awsExpectedAccount}</code>}{c.awsExpectedRole && <> <code>{c.awsExpectedRole}</code></>} <span className="muted">(checked before apply)</span></dd></div>}
    <div><dt>AWS region</dt><dd>{c.awsRegion ? <code>{c.awsRegion}</code> : <span className="muted">Inherited (not set by Hclaro)</span>}</dd></div>
    {(c.options.parallelism || c.options.lock_timeout_seconds) ? <div><dt>Plan and apply options</dt><dd className="mono">{c.options.parallelism ? `-parallelism=${c.options.parallelism} ` : ''}{c.options.lock_timeout_seconds ? `-lock-timeout=${c.options.lock_timeout_seconds}s` : ''}</dd></div> : null}
    {c.env.length > 0 && <div><dt>Expected variables</dt><dd><ul className="plain-list">{c.env.map(e => <li key={e.name}><KeyRound size={11} aria-hidden="true" /> <code>{e.name}</code> <span className={e.present ? 'muted' : 'issue-text'}>{e.present ? 'set (value not shown)' : 'missing'}</span></li>)}</ul></dd></div>}
    <div><dt>Context</dt><dd className="mono muted" title="Fingerprint of this reviewed context. The operation is refused if it changes."><Fingerprint size={11} aria-hidden="true" /> {c.fingerprint.slice(0, 12)}</dd></div>
   </dl>
   {summary.issues.length > 0 && <ul className="context-issues" role="alert">{summary.issues.map(i => <li key={i.code + i.message}><AlertTriangle size={13} aria-hidden="true" />{i.message}</li>)}</ul>}
   {c.notices.length > 0 && <ul className="context-notices">{c.notices.map(n => <li key={n}><CircleHelp size={13} aria-hidden="true" />{n}</li>)}</ul>}
   <p className="muted context-explainer">A Terraform workspace is a state namespace inside this project. It is not an AWS profile or a cloud account: the credentials come from the AWS profile and your local credential setup.</p>
  </section>;
}

// RecordedContext shows the context an operation or plan was bound to.
export function RecordedContext({ context }: { context?: ExecutionContext }) {
  if (!context?.profileId) return <span className="muted">No profile (inherited environment)</span>;
  return <span>Profile <strong>{context.profileName}</strong> (rev {context.profileRevision}) · workspace <strong>{context.workspace}</strong>{context.awsProfile && <> · AWS profile <code>{context.awsProfile}</code>{context.awsAuth === 'aws-vault' && ' via aws-vault'}</>}{context.awsExpectedAccount && <> · expects <code>{context.awsExpectedAccount}</code></>}{context.awsRegion && <> · <code>{context.awsRegion}</code></>} {context.projectId && context.profileId && context.awsProfile ? <AuthIndicator projectId={context.projectId} profileId={context.profileId} /> : <IdentityBadge />}</span>;
}

// ScopeSummary shows the identity of a Hub workspace's execution scope, as the server pinned it.
export function ScopeSummary({ scope, executionContext, title }: { scope: ScopeView; executionContext?: ExecutionContext | null; title: string }) {
  return <section className="context-summary" aria-label={title}><div className="context-summary-heading"><h3>{title}</h3></div><dl className="metadata compact-metadata">
   <div><dt>Project</dt><dd><strong>{scope.alias}</strong></dd></div>
   <div><dt>Root folder</dt><dd className="mono">{scope.rootPath}</dd></div>
   <div><dt>Workspace</dt><dd><strong>{scope.workspace}</strong> <span className="muted">(this tab; the CLI default is not changed)</span></dd></div>
   <div><dt>Engine</dt><dd>{engineLabel(scope.engine)} {scope.version}</dd></div>
   <div><dt>Backend · mode</dt><dd>{scope.backend} · {scope.executionMode}</dd></div>
   <div><dt>Profile</dt><dd><RecordedContext context={executionContext ?? undefined} /></dd></div>
  </dl></section>;
}
