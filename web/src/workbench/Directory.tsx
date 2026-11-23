import { FolderTree, Network } from 'lucide-react';
import type { Module } from '../types';
import { moduleName, providerName } from '../lib/format';

// moduleCards is the root module plus one card per top-level module, each with every module nested under it.
export function moduleCards(modules: Module[]) {
  const top = modules.filter(m => !m.parent);
  return top.map(m => {
    const children = m.address ? modules.filter(c => c.address.startsWith(`${m.address}.module.`)) : [];
    return { module: m, children, total: children.reduce((n, c) => n + c.count, m.count) };
  });
}

export function ModulesGrid({ modules, open }: { modules: Module[]; open: (address: string) => void }) {
  return <div className="card-grid module-grid">{moduleCards(modules).map(({ module: m, children, total }) => <article className="directory-card" key={m.address} aria-label={moduleName(m.address)}>
    <header><span className="tile-icon" aria-hidden="true"><FolderTree size={16} /></span><div><h2>{moduleName(m.address)}</h2><span className="mono">{m.address || 'root'}</span></div></header>
    <p className="directory-count"><strong>{total.toLocaleString()}</strong> {total === 1 ? 'resource' : 'resources'}</p>
    {children.length > 0 && <section className="directory-children" aria-label="Child modules"><h3>Child modules</h3><ul>{children.map(c => <li key={c.address} className="mono"><span>{moduleName(c.address.slice(m.address.length + 1))}</span><span>{c.count}</span></li>)}</ul></section>}
    <button type="button" className="link-button" onClick={() => open(m.address)}>View resources →</button>
  </article>)}</div>;
}

export function ProvidersGrid({ providers, lockfile, open }: { providers: Record<string, number>; lockfile: boolean; open: (provider: string) => void }) {
  return <div className="card-grid provider-grid">{Object.entries(providers).sort().map(([p, count]) => <article className="directory-card" key={p} aria-label={providerName(p)}>
    <header><span className="tile-icon" aria-hidden="true"><Network size={16} /></span><div><h2>{providerName(p)}</h2><span className="mono">{p || 'Provider not recorded'}</span></div></header>
    <dl>
     {/* shortcut: the workspace API reports only whether the lockfile exists, not its versions; read them server-side when a backend task adds it. */}
     <dt>Version</dt><dd className={lockfile ? 'muted' : 'tone-warn'}>{lockfile ? 'In .terraform.lock.hcl' : 'Unknown: no lockfile'}</dd>
     <dt>Constraint</dt><dd className="muted">Run Tools › Providers</dd>
     <dt>Resources</dt><dd>{count.toLocaleString()}</dd>
    </dl>
    <button type="button" className="link-button" onClick={() => open(p)}>View resources →</button>
  </article>)}</div>;
}
