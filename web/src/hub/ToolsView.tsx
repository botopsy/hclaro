import { useCallback, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, ArrowRight, Boxes, CheckCircle2, CircleX, FileCheck2, FileCode2, Info, KeyRound, LoaderCircle, Play, ShieldCheck, Terminal } from 'lucide-react';
import { api, ApiError, post } from '../api';
import { operationActive, type ContextInfo, type Operation, type ToolName, type ToolResult } from '../types';
import { RecordedContext } from '../profiles/ContextSummary';
import { Button } from '../components/ui/button';
import { OperationLog } from '../plans/OperationLog';
import { clock, duration } from '../plans/labels';
import { navigate, projectPath } from '../router';

type ToolInfo = {
  name: ToolName;
  title: string;
  icon: typeof FileCheck2;
  argv: (recursive: boolean) => string[];
  description: string;
};

// The argv here is for display; the server builds the real one from the tool name.
const tools: ToolInfo[] = [{
  name: 'validate',
  title: 'Validate',
  icon: FileCheck2,
  argv: () => ['validate', '-json'],
  description: 'Checks the configuration\'s syntax and internal consistency. It is not a plan: it does not contact the backend, detect drift or prove that an apply would succeed. It may need installed providers.'
}, {
  name: 'fmt-check',
  title: 'Format check',
  icon: FileCode2,
  argv: recursive => recursive ? ['fmt', '-check', '-no-color', '-recursive'] : ['fmt', '-check', '-no-color'],
  description: 'Lists configuration files that are not in canonical format. Check only: Hclaro never rewrites files.'
}, {
  name: 'providers',
  title: 'Providers',
  icon: Boxes,
  argv: () => ['providers'],
  description: 'Lists the providers the configuration and this workspace\'s state require. Requirements do not show which cloud account or identity is in use. Nothing is downloaded or upgraded.'
}, {
  name: 'outputs-metadata',
  title: 'Outputs',
  icon: KeyRound,
  argv: () => ['output', '-json'],
  description: 'Shows output names, types and whether they are marked sensitive. Values are discarded inside Hclaro and never shown, including values that are not marked sensitive.'
}];
const toolTitle = Object.fromEntries(tools.map(t => [t.name, t.title])) as Record<ToolName, string>;
const readsState = (tool: ToolName) => tool === 'providers' || tool === 'outputs-metadata';

function message(error: unknown) {
  return error instanceof Error ? error.message : 'Unable to reach the local Hclaro server. Check that it is still running, then retry.';
}
export function operationLabel(op: Operation) {
  if (op.kind === 'tool' && op.tool) return toolTitle[op.tool.tool] || 'Project tool';
  if (op.kind === 'init') return 'Init';
  if (op.kind === 'workspace' && op.workspace) return `Workspace ${op.workspace.action} ${op.workspace.name}`;
  return op.kind;
}

// Project tools for one open Hub workspace. Each run is bound to this context: project, root,
// engine executable and workspace are pinned by the server when the request arrives, so
// switching the tab's workspace later does not retarget a running command.
export function ToolsView({ context }: { context: ContextInfo }) {
  const client = useQueryClient();
  const [recursive, setRecursive] = useState(false);
  const [selected, setSelected] = useState<string>();
  const [starting, setStarting] = useState<ToolName>();
  const [error, setError] = useState<{ tool: ToolName; text: string; code: string }>();
  const [scope, setScope] = useState<'workspace' | 'project'>('workspace');
  const key = ['project-operations', context.projectId];
  const list = useQuery({
    queryKey: key,
    queryFn: () => api<{ operations: Operation[] }>(`projects/${context.projectId}/operations`),
    refetchInterval: q => q.state.data?.operations.some(operationActive) ? 1500 : false
  });
  const operations = list.data?.operations || [];
  const mine = operations.filter(o => o.tool?.contextId === context.id);
  const running = (tool: ToolName) => mine.find(o => o.tool?.tool === tool && operationActive(o));
  const latest = (tool: ToolName) => mine.find(o => o.tool?.tool === tool);
  const shown = scope === 'workspace' ? operations.filter(o => o.tool?.workspace === context.workspace || o.kind !== 'tool') : operations;
  const current = operations.find(o => o.id === selected);
  const refresh = useCallback(() => {
    void client.invalidateQueries({ queryKey: key });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, context.projectId]);
  async function run(tool: ToolName) {
    setStarting(tool);
    setError(undefined);
    try {
      const body = tool === 'fmt-check' ? { contextId: context.id, recursive } : { contextId: context.id };
      const op = await post<Operation>(`projects/${context.projectId}/operations/${tool}`, body);
      setSelected(op.id);
      refresh();
    } catch (e) {
      setError({ tool, text: message(e), code: e instanceof ApiError ? e.code : '' });
    } finally {
      setStarting(undefined);
    }
  }
  const engine = context.engine || 'terraform';
  return <div className="tools-view">
   <p className="context-line"><strong>Execution context:</strong> <RecordedContext context={context.executionContext} /></p>
   <p className="info-note"><ShieldCheck size={15} /><span>These commands are read-only and run only when you click. They never write configuration, change state or contact your infrastructure through a plan. <strong>Validate</strong> and <strong>Format check</strong> read configuration files only. <strong>Providers</strong> and <strong>Outputs</strong> read this tab's workspace, <strong className="mono">{context.workspace}</strong>, through a per-command <code>TF_WORKSPACE</code>; your terminal's CLI default is not changed.</span></p>
   <div className="tool-grid">{tools.map(t => {
       const active = running(t.name);
       const last = latest(t.name);
       const Icon = t.icon;
       return <section key={t.name} className="surface tool-card" aria-labelledby={`tool-${t.name}`}>
        <div className="tool-card-head"><Icon size={17} /><h2 id={`tool-${t.name}`}>{t.title}</h2><span className="hub-tag hub-tag-quiet">{readsState(t.name) ? `Reads ${context.workspace} state` : 'Configuration only'}</span></div>
        <p className="muted">{t.description}</p>
        <code className="tool-command">{[engine, ...t.argv(recursive && t.name === 'fmt-check')].join(' ')}</code>
        {t.name === 'fmt-check' && <label className="tool-option"><input type="checkbox" checked={recursive} onChange={e => setRecursive(e.target.checked)} disabled={!!active} /><span>Include subfolders (<code>-recursive</code>). Hidden folders are skipped and linked folders are never followed.</span></label>}
        {error?.tool === t.name && <p className="dialog-error" role="alert">{error.text}</p>}
        <div className="tool-actions">
         {last && <button className="tool-last" onClick={() => setSelected(last.id)} aria-label={`Show the last ${t.title} result`}><OutcomeLabel op={last} /><span className="muted">{clock(last.startedAt)}</span></button>}
         <span className="context-spacer" />
         <Button size="small" variant={last ? 'outline' : 'default'} onClick={() => void run(t.name)} disabled={!!active || starting === t.name}>{active || starting === t.name ? <><LoaderCircle className="spin" size={13} />Running…</> : <><Play size={13} />{last ? 'Run again' : 'Run'}</>}</Button>
        </div>
       </section>;
     })}</div>
   {current && <section className="tool-detail" aria-label="Selected operation">
     {current.tool && <ToolResultPanel op={current} projectId={context.projectId} />}
     {current.kind !== 'tool' && current.diagnostic && !operationActive(current) && <p className="muted">{current.diagnostic.message}</p>}
     {current.logRetained ? <OperationLog key={current.id} operation={current} onFinished={refresh} /> : <p className="muted">The log for this operation is no longer kept. Only the most recent logs are retained.</p>}
    </section>}
   <section className="operation-center" aria-labelledby="operation-center-title">
    <div className="workspace-heading"><h2 className="hub-section" id="operation-center-title">Operations in this project</h2>
     <div className="segmented" role="group" aria-label="Show operations"><button aria-pressed={scope === 'workspace'} onClick={() => setScope('workspace')}>This workspace</button><button aria-pressed={scope === 'project'} onClick={() => setScope('project')}>All workspaces</button></div>
     <Button variant="ghost" size="small" onClick={() => void list.refetch()} disabled={list.isFetching}>Refresh</Button>
     <Button variant="ghost" size="small" onClick={() => navigate(projectPath(context.projectId))}>Project page<ArrowRight size={13} /></Button></div>
    {list.isPending ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Loading operations…</p>
     : list.error ? <p className="dialog-error" role="alert">{message(list.error)}</p>
     : !shown.length ? <div className="empty-state"><Terminal size={24} /><h2>No operations yet</h2><p>Init, workspace commands and project tools you run for this project appear here with their status and sanitized output.</p></div>
     : <table className="resources-table tool-ops"><thead><tr><th scope="col">Operation</th><th scope="col">Workspace</th><th scope="col">Status</th><th scope="col">Started</th><th scope="col">Duration</th><th scope="col">Result</th></tr></thead><tbody>{shown.map(o => <tr key={o.id} className={selected === o.id ? 'selected' : ''}>
       <td><button className="resource-button" aria-pressed={selected === o.id} onClick={() => setSelected(o.id)}><span className="resource-text"><strong>{operationLabel(o)}</strong><span className="resource-address mono">{o.command.join(' ')}</span></span></button></td>
       <td>{o.tool ? o.tool.usesWorkspace ? <span className="mono">{o.tool.workspace}</span> : <span className="muted" title={`Opened from ${o.tool.workspace}; this command does not read a workspace.`}>Configuration</span> : <span className="muted">CLI default</span>}</td>
       <td><span className={`status-pill status-${o.status}`}>{operationActive(o) ? o.cancelRequested ? 'Cancelling' : o.stage : o.status}</span></td>
       <td>{clock(o.startedAt)}</td>
       <td>{duration(o.durationMs)}</td>
       <td>{o.tool ? <OutcomeLabel op={o} /> : <span className="muted">{o.diagnostic?.code.replaceAll('_', ' ') || '—'}{o.exitCode !== undefined && ` · exit ${o.exitCode}`}</span>}</td>
      </tr>)}</tbody></table>}
    <p className="info-line">This list and its logs are kept in memory, for this Hclaro session; redacted metadata also goes to Operation history. Logs contain sanitized summaries; output values and raw engine output are never kept.</p>
   </section>
  </div>;
}

const outcomeText: Record<ToolName, Record<ToolResult['outcome'], string>> = {
  validate: { passed: 'Valid', findings: 'Not valid', not_checked: 'Not validated' },
  'fmt-check': { passed: 'Formatted', findings: 'Needs formatting', not_checked: 'Not checked' },
  providers: { passed: 'Listed', findings: 'Listed', not_checked: 'Not listed' },
  'outputs-metadata': { passed: 'Read', findings: 'Read', not_checked: 'Not read' }
};

function OutcomeLabel({ op }: { op: Operation }) {
  const t = op.tool!;
  if (operationActive(op)) return <span className="tool-outcome"><LoaderCircle className="spin" size={13} />Running</span>;
  if (op.status === 'cancelled') return <span className="tool-outcome tone-warn"><CircleX size={13} />Cancelled</span>;
  const Icon = t.outcome === 'passed' ? CheckCircle2 : t.outcome === 'findings' ? AlertTriangle : CircleX;
  return <span className={`tool-outcome tool-outcome-${t.outcome}`}><Icon size={13} />{outcomeText[t.tool][t.outcome]}{t.tool === 'validate' && t.outcome !== 'not_checked' && (t.errors || t.warnings) ? ` · ${t.errors} error${t.errors === 1 ? '' : 's'}, ${t.warnings} warning${t.warnings === 1 ? '' : 's'}` : ''}</span>;
}

const successText: Record<ToolName, string> = {
  validate: 'The configuration is valid. This checks syntax and internal consistency only: it is not a plan, does not detect drift and does not prove an apply would succeed.',
  'fmt-check': 'Every checked file is in canonical format. Nothing was written.',
  providers: 'Provider requirements of the configuration and of this workspace\'s state. They do not prove which cloud account or identity is in use.',
  'outputs-metadata': 'Names, types and sensitivity of the root module outputs in this workspace\'s state. Values were discarded inside Hclaro and are never displayed.'
};

function ToolResultPanel({ op, projectId }: { op: Operation; projectId: string }) {
  const t = op.tool!;
  if (operationActive(op)) return null;
  const ok = op.status === 'succeeded';
  const needsInit = op.diagnostic?.code === 'init_required' || op.diagnostic?.code === 'plugins_missing';
  return <section className={`init-result tool-result ${ok ? 'init-result-ok' : 'init-result-problem'}`} role={ok ? 'status' : 'alert'} aria-label={`${toolTitle[t.tool]} result`}>
   <h3><OutcomeLabel op={op} /><span className="muted">{op.exitCode !== undefined && ` · exit code ${op.exitCode}`} · {duration(op.durationMs)}{t.usesWorkspace && <> · workspace <span className="mono">{t.workspace}</span></>}{t.engineVersion && ` · ${op.command[0] === 'tofu' ? 'OpenTofu' : 'Terraform'} ${t.engineVersion}`}</span></h3>
   <p>{ok ? successText[t.tool] : op.diagnostic?.message}</p>
   {t.tool === 'validate' && t.output === 'human' && <p className="muted"><Info size={13} /> The engine printed no machine-readable result, so these diagnostics come from its human-readable output and may be incomplete.</p>}
   {t.partial && <p className="muted"><AlertTriangle size={13} /> The command failed; what it printed before failing is shown below and may be incomplete.</p>}
   {needsInit && <div className="tool-actions"><Button size="small" onClick={() => navigate(projectPath(projectId))}><Play size={13} />Review Init on the project page</Button><span className="muted">Nothing was initialized automatically.</span></div>}
   {t.findings.length > 0 && <ul className="tool-findings">{t.findings.map((f, i) => <li key={i} className={`finding-${f.severity}`}>
     <span className="finding-severity">{f.severity === 'error' ? <CircleX size={13} /> : <AlertTriangle size={13} />}{f.severity === 'error' ? 'Error' : 'Warning'}</span>
     <span><strong>{f.summary}</strong>{f.file && <span className="mono finding-location" title="Location in the project folder. Hclaro does not open files.">{f.file}{f.line ? `:${f.line}` : ''}{f.column ? `:${f.column}` : ''}</span>}{f.detail && <span className="finding-detail">{f.detail}</span>}</span>
    </li>)}</ul>}
   {(t.errors + t.warnings > t.findings.length) && <p className="muted">{t.errors + t.warnings - t.findings.length} more diagnostics are not shown.</p>}
   {t.format && <FormatSummary format={t.format} />}
   {t.providers && <ProvidersTable providers={t.providers} />}
   {t.outputs && <OutputsTable outputs={t.outputs} />}
   <p className="safe-note"><ShieldCheck size={13} />Read-only: nothing was written, applied or retried.</p>
  </section>;
}

function FormatSummary({ format: f }: { format: NonNullable<ToolResult['format']> }) {
  return <>
   <p>Checked {f.checked} configuration file{f.checked === 1 ? '' : 's'} in {f.folders} folder{f.folders === 1 ? '' : 's'}{f.recursive ? ', including subfolders' : ' (this folder only)'}.</p>
   {f.unformatted.length > 0 && <ul className="tool-files">{f.unformatted.map(name => <li key={name} className="mono">{name}</li>)}</ul>}
   {f.hidden > 0 && <p className="muted">{f.hidden} reported path{f.hidden === 1 ? ' is' : 's are'} not shown because Hclaro did not list {f.hidden === 1 ? 'it' : 'them'} in this project folder.</p>}
   {f.skipped.length > 0 && <p className="muted">Not followed (symbolic links): <span className="mono">{f.skipped.join(', ')}</span></p>}
  </>;
}

function ProvidersTable({ providers: p }: { providers: NonNullable<ToolResult['providers']> }) {
  const rows = [...p.configuration.map(r => ({ ...r, from: 'Configuration' })), ...p.state.map(r => ({ ...r, from: 'State' }))];
  if (!rows.length) return <p className="muted">No provider requirements were listed.</p>;
  return <>
   <table className="resources-table tool-table"><thead><tr><th scope="col">Provider</th><th scope="col">Version constraint</th><th scope="col">Required by</th></tr></thead><tbody>{rows.map((r, i) => <tr key={i}><td className="mono">{r.source}</td><td className="mono">{r.constraint || <span className="muted">any</span>}</td><td>{r.from === 'State' ? 'Recorded state' : r.module ? <span className="mono">{r.module}</span> : 'Root module'}</td></tr>)}</tbody></table>
   {p.hidden > 0 && <p className="muted">{p.hidden} line{p.hidden === 1 ? '' : 's'} with unexpected characters {p.hidden === 1 ? 'is' : 'are'} not shown.</p>}
  </>;
}

function OutputsTable({ outputs: o }: { outputs: NonNullable<ToolResult['outputs']> }) {
  return <>
   {o.items.length ? <table className="resources-table tool-table"><thead><tr><th scope="col">Output</th><th scope="col">Type</th><th scope="col">Sensitive</th><th scope="col">Value</th></tr></thead><tbody>{o.items.map(item => <tr key={item.name}><td className="mono">{item.name}</td><td className="mono">{item.type}</td><td>{item.sensitive ? <span className="hub-tag hub-tag-warn">Marked sensitive</span> : <span className="muted">Not marked</span>}</td><td className="muted">Not shown</td></tr>)}</tbody></table> : <p className="muted">This workspace's state records no root module outputs.</p>}
   {o.hidden > 0 && <p className="muted">{o.hidden} output name{o.hidden === 1 ? '' : 's'} that look{o.hidden === 1 ? 's' : ''} like a secret or use{o.hidden === 1 ? 's' : ''} unusual characters {o.hidden === 1 ? 'is' : 'are'} not shown.</p>}
   <p className="muted">Values are never displayed in this release, even for outputs not marked sensitive: an unmarked output can still contain a secret.</p>
  </>;
}
