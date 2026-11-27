import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { AlertTriangle, CheckCircle2, ExternalLink, LoaderCircle, Play, ScanSearch, ShieldCheck } from 'lucide-react';
import { api, send } from '../api';
import { operationActive, type ContextInfo, type Operation, type ScanSeverity, type ScannerInfo, type ScannerName, type ToolResult } from '../types';
import { Button } from '../components/ui/button';
import { OperationLog } from '../plans/OperationLog';
import { clock } from '../lib/format';
import { errorMessage } from '../lib/errorMessage';

const docs: Record<ScannerName, string> = {
  tflint: 'https://github.com/terraform-linters/tflint',
  trivy: 'https://trivy.dev/',
  checkov: 'https://www.checkov.io/'
};
const severities: ScanSeverity[] = ['critical', 'high', 'medium', 'low', 'unrated'];
// Fixed argv shown on the card; the server builds the real one.
const argv: Record<ScannerName, (recursive: boolean) => string[]> = {
  tflint: recursive => ['tflint', '--format=json', '--no-color', ...(recursive ? ['--recursive'] : [])],
  trivy: () => ['trivy', 'config', '--format', 'json', '--quiet', '--skip-check-update', '.'],
  checkov: () => ['checkov', '-d', '.', '-o', 'json', '--framework', 'terraform', '--quiet', '--compact', '--skip-download']
};

// "3 high · 5 medium · 2 low", or "No findings" for a clean scan.
export function scanSummary(counts: Partial<Record<ScanSeverity, number>>) {
  const parts = severities.filter(s => counts[s]).map(s => `${counts[s]} ${s}`);
  return parts.length ? parts.join(' · ') : 'No findings';
}

// Scanner cards for one open Hub workspace. Detection reads `--version` when this page opens;
// nothing runs until Run is clicked.
export function ScannerCards({ context, operations, blocked, refresh, onStart, onOpen }: { context: ContextInfo; operations: Operation[]; blocked?: string; refresh: () => void; onStart: (name: ScannerName) => void; onOpen: (name: ScannerName) => void }) {
  const [recursive, setRecursive] = useState(false);
  const [starting, setStarting] = useState<ScannerName>();
  const [error, setError] = useState<{ name: ScannerName; text: string }>();
  const detected = useQuery({ queryKey: ['scanners', context.projectId], queryFn: () => api<{ scanners: ScannerInfo[]; support: string }>(`projects/${context.projectId}/scanners`), retry: false });
  async function run(name: ScannerName) {
    setStarting(name);
    setError(undefined);
    onStart(name);
    try {
      await send<Operation>('POST', `projects/${context.projectId}/operations/${name}`, name === 'tflint' ? { contextId: context.id, recursive } : { contextId: context.id });
      refresh();
    } catch (e) {
      setError({ name, text: errorMessage(e) });
    } finally {
      setStarting(undefined);
    }
  }
  return <section aria-labelledby="scanners-title" className="scanners">
   <h2 id="scanners-title">Scanners</h2>
   <p className="muted">Optional tools you install yourself. Each runs only when you click Run, offline, with no cloud credentials, and only reads the project folder. Findings are advice: they never block a plan or apply.</p>
   {detected.isPending ? <p className="muted" role="status"><LoaderCircle className="spin" size={13} /> Looking for scanners…</p>
    : detected.error ? <p className="dialog-error" role="alert">{errorMessage(detected.error)}</p>
    : <div className="tool-grid">{detected.data.scanners.map(info => {
       const last = operations.find(o => o.tool?.contextId === context.id && o.tool.tool === info.name);
       const active = !!last && operationActive(last);
       const busy = active || starting === info.name;
       const reason = !info.installed || !info.supported ? undefined : busy ? undefined : blocked;
       return <section key={info.name} className="tool-card" aria-labelledby={`tool-${info.name}`}>
        <div className="tool-card-head"><ScanSearch size={17} aria-hidden="true" /><h2 id={`tool-${info.name}`}>{info.title}</h2><span className="hub-tag hub-tag-quiet">{info.version ? `v${info.version}` : 'Configuration only'}</span></div>
        {!info.installed || !info.supported ? <p className="muted">{info.reason} <a href={docs[info.name]} target="_blank" rel="noreferrer noopener">Documentation<ExternalLink size={11} aria-hidden="true" /></a></p>
         : <>
           <code className="tool-command">{argv[info.name](recursive && info.name === 'tflint').join(' ')}</code>
           {info.config.length > 0 && <p className="muted">Uses the project's own config: <span className="mono">{info.config.join(', ')}</span></p>}
           {info.name === 'tflint' && <label className="tool-option"><input type="checkbox" checked={recursive} onChange={e => setRecursive(e.target.checked)} disabled={busy} /><span>Include subfolders (<code>--recursive</code>).</span></label>}
          </>}
        {error?.name === info.name && <p className="dialog-error" role="alert">{error.text}</p>}
        {last && (active ? last.logRetained && <OperationLog key={last.id} operation={last} onFinished={refresh} /> : <ScanNote op={last} onOpen={() => onOpen(info.name)} />)}
        <div className="tool-actions">
         {reason && <span className="tool-blocked" id={`tool-${info.name}-blocked`}>{reason}</span>}
         <Button size="small" variant="outline" onClick={() => void run(info.name)} disabled={busy || !!reason || !info.installed || !info.supported} aria-describedby={reason ? `tool-${info.name}-blocked` : undefined}>{busy ? <><LoaderCircle className="spin" size={13} />Running…</> : <><Play size={13} />{last ? 'Run again' : 'Run'}</>}</Button>
        </div>
       </section>;
      })}</div>}
  </section>;
}

// One finished scan: its summary line and findings grouped by file.
export function ScanResultPanel({ op }: { op: Operation }) {
  const t = op.tool as ToolResult;
  const scan = t.scan!;
  const ok = op.status === 'succeeded';
  const complete = t.outcome !== 'not_checked';
  const byFile = new Map<string, ToolResult['findings']>();
  for (const f of t.findings) byFile.set(f.file || 'Whole project', [...(byFile.get(f.file || 'Whole project') || []), f]);
  const shown = t.findings.length;
  const total = Object.values(scan.counts).reduce((a, b) => a + (b || 0), 0);
  return <section className={`init-result tool-result ${ok ? 'init-result-ok' : complete ? 'init-result-problem' : 'init-result-problem'}`} role={ok ? 'status' : 'alert'} aria-label={`${scan.scanner} result`}>
   <h3>{complete ? ok ? <span className="tool-outcome tool-outcome-passed"><CheckCircle2 size={13} />✓ No findings</span> : <span className="tool-outcome tool-outcome-findings"><AlertTriangle size={13} />{scanSummary(scan.counts)}</span> : <span className="tool-outcome tool-outcome-not_checked">Not scanned</span>}<span className="muted"> · {clock(op.startedAt)}{scan.version && ` · ${scan.scanner} ${scan.version}`}</span></h3>
   <p>{ok ? 'No findings. This is a static scan by the scanner\'s own rules; it does not prove the configuration is secure.' : op.diagnostic?.message}</p>
   {[...byFile].map(([file, findings]) => <div key={file}>
     <h4 className="mono">{file}</h4>
     <ul className="tool-findings">{findings.map((f, i) => <li key={i} className={`finding-${f.severity}`}>
       <span className="finding-severity"><span className={`severity-chip severity-${f.severity}`}>{f.severity}</span></span>
       <span><strong>{f.summary}</strong><span className="mono finding-location">{f.rule}{f.line ? ` · line ${f.line}` : ''}</span></span>
      </li>)}</ul>
    </div>)}
   {total > shown && <p className="muted">{total - shown} more findings are counted but not listed.{scan.hidden > 0 && ` ${scan.hidden} more were not read.`}</p>}
   <p className="safe-note"><ShieldCheck size={13} />Read-only: nothing was written, applied or downloaded.</p>
  </section>;
}

// The card keeps a one-line outcome; the findings open in a dialog.
function ScanNote({ op, onOpen }: { op: Operation; onOpen: () => void }) {
  const t = op.tool as ToolResult;
  const done = t.outcome !== 'not_checked';
  return <p className="muted">{done ? <span className={`tool-outcome tool-outcome-${t.outcome}`}>{t.outcome === 'passed' ? <CheckCircle2 size={13} /> : <AlertTriangle size={13} />}{scanSummary(t.scan!.counts)}</span> : <span className="tool-outcome tool-outcome-not_checked">Not scanned</span>} · {clock(op.startedAt)} <button className="link-button" onClick={onOpen}>Open results</button></p>;
}
