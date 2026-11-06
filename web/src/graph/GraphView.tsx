import { memo, useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowRight, Boxes, CircleAlert, CircleX, Copy, FileDiff, History, List, LoaderCircle, Maximize, Play, Repeat, Search, ShieldCheck, Target, TriangleAlert, Waypoints, X, ZoomIn, ZoomOut } from 'lucide-react';
import { api, post } from '../api';
import type { DependencyGraph, GraphImpact, GraphNode, GraphOptions, Operation, PlannedChange, PlanSummary } from '../types';
import { Button } from '../components/ui/button';
import { useModal } from '../components/useModal';
import { OperationLog } from '../plans/OperationLog';
import { ActionBadge } from '../plans/PlanReview';
import { clock, engineLabel } from '../plans/labels';
import { ContextSummary, RecordedContext } from '../profiles/ContextSummary';
import { internalKinds, layout, moduleAncestors, NODE_H, NODE_W, nodeForAddress, project, typePrefix, type Point, type Projection, type VisEdge, type VisNode } from './layout';

const kindLabels: Record<string, string> = {
  resource: 'Resource',
  data: 'Data source',
  module: 'Module call',
  provider: 'Provider',
  variable: 'Variable',
  local: 'Local value',
  output: 'Output',
  check: 'Check',
  ephemeral: 'Ephemeral resource',
  unknown: 'Unrecognized node',
  group: 'Collapsed module'
};

const confidenceText: Record<string, string> = {
  exact: 'Mapped to recorded instances by address.',
  configuration: 'A resource in configuration with no instance recorded in this state (not created yet, count = 0, or removed).',
  internal: 'An engine-internal node. It is never mapped to state.',
  unknown: 'Hclaro could not read this node as an address, so it is never mapped to state.'
};

type Focus = { planId: string; address: string };
type Mode = 'canvas' | 'list';

export function GraphView({
  project: projectName,
  profileId = '',
  workspaceStatus,
  focus,
  openResource,
  openPlan,
  copy
}: {
  project: string;
  profileId?: string;
  workspaceStatus?: string;
  // Set by "Inspect impact" in a replace plan: overlay that plan and select the address.
  focus?: Focus;
  openResource: (id: string) => void;
  openPlan: (planId: string) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const client = useQueryClient();
  const [preflight, setPreflight] = useState(false);
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState('');
  const [graphId, setGraphId] = useState<string>();
  const [planId, setPlanId] = useState(() => focus?.planId ?? '');
  const options = useQuery({ queryKey: ['graph-options', workspaceStatus, profileId], queryFn: () => api<GraphOptions>(`graphs/options${profileId ? `?profileId=${profileId}` : ''}`) });
  const graphs = useQuery({ queryKey: ['graphs', workspaceStatus], queryFn: () => api<{ graphs: DependencyGraph[] }>('graphs') });
  const plans = useQuery({ queryKey: ['plans'], queryFn: () => api<{ plans: PlanSummary[] }>('plans') });
  const operations = useQuery({
    queryKey: ['operations'],
    queryFn: () => api<{ operations: Operation[] }>('operations'),
    refetchInterval: q => q.state.data?.operations.some(o => o.status === 'running') ? 2000 : false
  });
  const running = operations.data?.operations.find(o => o.status === 'running');
  const shownId = graphId ?? graphs.data?.graphs[0]?.id;
  const graph = useQuery({
    queryKey: ['graph', shownId, planId, workspaceStatus],
    queryFn: () => api<DependencyGraph>(`graphs/${shownId}${planId ? `?planId=${planId}` : ''}`),
    enabled: !!shownId,
    retry: false
  });
  const finished = useCallback(() => {
    void client.invalidateQueries({ queryKey: ['operations'] });
    void client.invalidateQueries({ queryKey: ['graphs'] });
    void client.invalidateQueries({ queryKey: ['graph'] });
  }, [client]);
  async function start(contextFingerprint?: string) {
    setStarting(true);
    setStartError('');
    try {
      const started = await post<{ operation: Operation; graph: DependencyGraph }>('graphs', { acknowledgeEngineRun: true, ...(contextFingerprint ? { profileId, contextFingerprint } : {}) });
      setPreflight(false);
      setGraphId(started.graph.id);
      await client.invalidateQueries({ queryKey: ['operations'] });
      await client.invalidateQueries({ queryKey: ['graphs'] });
    } catch (e) {
      setStartError(e instanceof Error ? e.message : 'The graph could not be generated.');
      void options.refetch();
    } finally {
      setStarting(false);
    }
  }
  const canGenerate = !!options.data?.canGenerate && !running;
  const latest = graphs.data?.graphs[0];
  const shown = graphs.data?.graphs.find(g => g.id === shownId);
  const planChoices = (plans.data?.plans ?? []).filter(p => !shown || p.workspace === shown.workspace);
  return <div className="plans graph-page">
   <div className="plans-toolbar">
    <Button onClick={() => { setStartError(''); setPreflight(true); }} disabled={!canGenerate} title={running && running.kind !== 'graph' ? 'Another operation is running.' : undefined}><Waypoints size={14} />Generate graph</Button>
    {graphs.data && graphs.data.graphs.length > 0 && <label className="history-select"><History size={14} /><span className="sr-only">Graph history</span><select aria-label="Graph history" value={shownId} onChange={e => setGraphId(e.target.value)}>{graphs.data.graphs.map((g, i) => <option key={g.id} value={g.id}>{i === 0 ? 'Latest · ' : ''}{clock(g.startedAt)} · {g.workspace} · {g.status === 'completed' ? `${g.summary.resources + g.summary.dataSources} resources` : g.status}{!g.current && g.status !== 'running' ? ' · stale' : ''}</option>)}</select></label>}
    <label className="history-select"><FileDiff size={14} /><span className="muted">Compare with plan</span><select aria-label="Compare with plan" value={planId} onChange={e => setPlanId(e.target.value)}><option value="">None (dependencies only)</option>{planId && !planChoices.some(p => p.id === planId) && <option value={planId}>Selected plan</option>}{planChoices.map(p => <option key={p.id} value={p.id} disabled={p.errored}>{clock(p.createdAt)} · {p.kind === 'replace' ? `replace ${p.replace.length}` : p.kind} · {p.changes} change{p.changes === 1 ? '' : 's'}{p.errored ? ' · errored' : ''}</option>)}</select></label>
    <span className="toolbar-note" role="status">{latest ? <>Last graph {clock(latest.completedAt ?? latest.startedAt)}{latest.current ? '' : ' · no longer current'}</> : <>No graph yet. Nothing runs until you ask.</>}</span>
   </div>
   <p className="info-line drift-explainer"><ShieldCheck size={13} />Generated with <strong>{options.data?.command.join(' ') || 'graph'}</strong>. Links show what configuration depends on. <strong>Potential impact</strong> is what can be reached through those links; only a plan shows <strong>confirmed planned changes</strong>.</p>
   {workspaceStatus === 'loading' && !running ? <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Reading the workspace…</div> : options.data && !options.data.canGenerate && <div className="plan-warning" role="note"><TriangleAlert size={15} /><span><strong>Graphs are unavailable here.</strong> {options.data.reason}</span></div>}
   {running && running.kind === 'graph' && <OperationLog key={running.id} operation={running} onFinished={finished} />}
   {running && running.kind !== 'graph' && <div className="info-line" role="status"><LoaderCircle className="spin" size={13} /> Another operation is running in this project. Generating a graph is available when it finishes.</div>}
   {graph.isPending && shownId ? <div className="empty-state" role="status"><LoaderCircle className="spin" /><p>Loading graph…</p></div> : graph.error ? <div className="empty-state error-state" role="alert"><p>{graph.error.message}</p>{planId && <Button variant="outline" size="small" onClick={() => setPlanId('')}>Show dependencies without the plan</Button>}</div> : graph.data && graph.data.status !== 'running' ? <GraphResult key={`${graph.data.id}:${planId}`} graph={graph.data} planId={planId} focus={focus} regenerate={() => setPreflight(true)} canGenerate={canGenerate} openResource={openResource} openPlan={openPlan} copy={copy} /> : !running && !shownId && <div className="empty-state"><Waypoints size={30} /><h2>No dependency graph yet</h2><p>Generate a graph to explore how resources, modules and data sources depend on each other. It runs <code>graph</code> only when you ask.</p>{options.data?.canGenerate && <Button variant="outline" onClick={() => setPreflight(true)}><Waypoints size={14} />Generate graph</Button>}</div>}
   {preflight && options.data && <GraphPreflight options={options.data} project={projectName} busy={starting} error={startError} onConfirm={fp => void start(fp)} onClose={() => { setPreflight(false); setStartError(''); }} />}
  </div>;
}

function GraphPreflight({ options, project, busy, error, onConfirm, onClose }: { options: GraphOptions; project: string; busy: boolean; error?: string; onConfirm: (contextFingerprint?: string) => void; onClose: () => void }) {
  const summary = options.context;
  const blocked = !!summary && summary.issues.length > 0;
  const confirm = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  useModal(dialog, confirm, onClose);
  return <div className="dialog-backdrop">
   <div ref={dialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="graph-title" aria-describedby="graph-summary">
    <div className="dialog-header"><h2 id="graph-title">Generate dependency graph</h2><Button variant="ghost" size="icon" aria-label="Close" onClick={onClose}><X size={15} /></Button></div>
    <p id="graph-summary">{engineLabel(options.engine)} will run <code>{options.command.join(' ')}</code> in <strong>{project}</strong> for workspace <strong>{options.workspace}</strong>.</p>
    <ul className="preflight-list">
     <li><Waypoints size={16} /><span><strong>Reads configuration and provider schemas</strong> from the initialized working directory. It may read state from the {options.backend} backend, using your existing credentials.</span></li>
     <li><ShieldCheck size={16} /><span><strong>Does not plan, refresh, apply or write state.</strong> No flags are passed, and no saved plan is read. Only a redacted summary of the graph is kept, in memory.</span></li>
    </ul>
    {summary ? <ContextSummary summary={summary} title="The graph will use" /> : options.profileError ? <p className="dialog-error" role="alert">{options.profileError}</p> : <p className="muted">No execution profile: the engine runs with the environment Hclaro was started with.</p>}
    {error && <p className="dialog-error" role="alert">{error}</p>}
    <div className="dialog-actions"><Button variant="ghost" onClick={onClose}>Cancel</Button><Button ref={confirm} onClick={() => onConfirm(summary?.context.profileId ? summary.context.fingerprint : undefined)} disabled={busy || blocked || !!options.profileError}><Play size={14} />{busy ? 'Starting…' : 'Generate graph'}</Button></div>
   </div>
  </div>;
}

function Failure({ graph }: { graph: DependencyGraph }) {
  return <section className="operation-failed" role="alert">
   <h2><CircleX size={17} />{graph.status === 'cancelled' ? 'Graph cancelled' : 'Graph not available'}</h2>
   {graph.diagnostic && <p>{graph.diagnostic.message}</p>}
   <p className="muted">No partial graph is shown. An unreadable or failed graph is never treated as “no dependencies”.</p>
  </section>;
}

function GraphResult({ graph, planId, focus, regenerate, canGenerate, openResource, openPlan, copy }: {
  graph: DependencyGraph;
  planId: string;
  focus?: Focus;
  regenerate: () => void;
  canGenerate: boolean;
  openResource: (id: string) => void;
  openPlan: (planId: string) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const hasInternal = graph.nodes.some(n => internalKinds.includes(n.nodeKind) && n.nodeKind !== 'unknown');
  const large = graph.nodes.length > 300;
  const modules = useMemo(() => [...new Set(graph.nodes.flatMap(n => moduleAncestors(n.modulePath)))].sort(), [graph.nodes]);
  const [projection, setProjection] = useState<Projection>(() => ({ showData: true, showInternal: false, provider: '', type: '', collapsed: new Set(large ? modules.filter(m => moduleAncestors(m).length === 1) : []) }));
  const [mode, setMode] = useState<Mode>(large ? 'list' : 'canvas');
  const [search, setSearch] = useState('');
  const [picked, setPicked] = useState<string>();
  const byId = useMemo(() => new Map(graph.nodes.map(n => [n.id, n])), [graph.nodes]);
  // "Inspect impact" selects the requested address once; a click replaces it.
  const focused = useMemo(() => focus && graph.current ? nodeForAddress(graph.nodes, focus.address)?.id : undefined, [focus, graph]);
  const selected = picked ?? focused;
  const view = useMemo(() => project(graph.nodes, graph.edges, graph.resourceEdges, projection), [graph, projection]);
  const providers = useMemo(() => [...new Set(graph.nodes.map(n => typePrefix(n.type)).filter(Boolean))].sort(), [graph.nodes]);
  const types = useMemo(() => [...new Set(graph.nodes.map(n => n.type).filter((t): t is string => !!t))].sort(), [graph.nodes]);
  const query = search.trim().toLowerCase();
  const matches = useMemo(() => query ? view.nodes.filter(n => `${n.label} ${n.node?.type ?? ''} ${n.node?.modulePath ?? ''}`.toLowerCase().includes(query)) : [], [view.nodes, query]);
  const impact = useQuery({
    queryKey: ['graph-impact', graph.id, selected, planId],
    queryFn: () => api<GraphImpact>(`graphs/${graph.id}/nodes/${selected}${planId ? `?planId=${planId}` : ''}`),
    enabled: !!selected && byId.has(selected) && graph.current,
    retry: false
  });
  const highlight = useMemo(() => {
    const down = new Set(impact.data?.downstream.map(r => r.node));
    const up = new Set(impact.data?.upstream.map(r => r.node));
    return { down, up };
  }, [impact.data]);
  function toggleCollapsed(module: string) {
    setProjection(p => {
      const collapsed = new Set(p.collapsed);
      if (collapsed.has(module)) collapsed.delete(module);
      else collapsed.add(module);
      return { ...p, collapsed };
    });
  }
  function choose(id: string) {
    if (id.startsWith('group:')) {
      toggleCollapsed(id.slice(6));
      return;
    }
    setPicked(id);
  }
  const s = graph.summary;
  const planned = graph.nodes.reduce((n, node) => n + (node.planned?.length ?? 0), 0);
  return <div className="graph-result">
   <div className="plan-meta">
    <div><span className="muted">Generated</span><strong>{clock(graph.completedAt ?? graph.startedAt)}</strong></div>
    <div><span className="muted">Engine</span><strong>{engineLabel(graph.engine)} {graph.engineVersion}</strong></div>
    <div><span className="muted">Workspace</span><strong>{graph.workspace}</strong></div>
    <div><span className="muted">Graph format</span><strong>{graph.format === 'full' ? 'Full walk graph' : graph.format === 'resources' ? 'Resources only' : '—'}</strong></div>
    <div><span className="muted">Execution context</span><strong><RecordedContext context={graph.executionContext} /></strong></div>
   </div>
   {!graph.current && graph.status !== 'running' && <div className="plan-warning" role="alert"><TriangleAlert size={15} /><span><strong>This graph no longer matches.</strong> {graph.staleReason} {graph.withheld && 'Its nodes are withheld so it cannot be mistaken for the current configuration.'}</span>{canGenerate && <Button variant="outline" size="small" onClick={regenerate}><Waypoints size={13} />Generate again</Button>}</div>}
   {(graph.status === 'failed' || graph.status === 'cancelled') && <Failure graph={graph} />}
   {graph.warnings.map(w => <div className="plan-warning" key={w} role="note"><TriangleAlert size={15} />{w}</div>)}
   {graph.current && graph.status === 'completed' && <>
    <div className="graph-counts" role="group" aria-label="Graph summary">
     <span><strong>{s.resources}</strong> resources</span><span><strong>{s.dataSources}</strong> data sources</span>{s.internal > 0 && <span><strong>{s.internal}</strong> internal nodes</span>}<span><strong>{s.edges}</strong> declared links</span>{s.derivedEdges > 0 && <span><strong>{s.derivedEdges}</strong> derived links</span>}{s.cycles > 0 && <span className="tone-warn"><Repeat size={12} aria-hidden="true" /><strong>{s.cycles}</strong> cycle{s.cycles === 1 ? '' : 's'}</span>}{s.unknown > 0 && <span><strong>{s.unknown}</strong> unrecognized</span>}
    </div>
    {graph.plan ? <div className="info-line graph-plan-note" role="note"><FileDiff size={13} /><span>Showing <strong>{planned} confirmed planned change{planned === 1 ? '' : 's'}</strong> from the {graph.plan.kind === 'replace' ? 'replace plan' : 'plan'} of {clock(graph.plan.createdAt)}.{graph.plan.unmapped.length > 0 && ` ${graph.plan.unmapped.length} change${graph.plan.unmapped.length === 1 ? ' does' : 's do'} not match a graph node.`}</span><Button variant="ghost" size="small" onClick={() => openPlan(graph.plan!.id)}>View in Plans<ArrowRight size={12} /></Button></div> : <p className="info-line muted">No plan selected: the graph shows dependencies only. Choose a plan above to see confirmed planned changes.</p>}
    {graph.nodes.length === 0 ? <div className="empty-state"><Boxes size={28} /><h2>No resources in this graph</h2></div> : <div className={`graph-layout ${selected ? 'with-inspector' : ''}`}>
     <aside className="graph-filters" aria-label="Graph filters">
      <div className="search-field compact"><Search size={14} /><input value={search} onChange={e => setSearch(e.target.value)} onKeyDown={e => { if (e.key === 'Enter' && matches[0]) choose(matches[0].id); }} placeholder="Find a node…" aria-label="Find a node" />{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch('')}><X size={12} /></button>}</div>
      {query && <p className="muted graph-match-count" role="status">{matches.length} match{matches.length === 1 ? '' : 'es'}{matches[0] && ' · Enter selects the first'}</p>}
      <fieldset><legend>Show</legend>
       <label className="check"><input type="checkbox" checked={projection.showData} onChange={e => setProjection(p => ({ ...p, showData: e.target.checked }))} />Data sources</label>
       <label className="check" title={hasInternal ? undefined : 'This graph format has no internal nodes.'}><input type="checkbox" checked={projection.showInternal} disabled={!hasInternal} onChange={e => setProjection(p => ({ ...p, showInternal: e.target.checked }))} />Internal nodes</label>
      </fieldset>
      <label className="graph-select"><span>Provider <span className="muted">(from type name)</span></span><select value={projection.provider} onChange={e => setProjection(p => ({ ...p, provider: e.target.value }))}><option value="">All</option>{providers.map(p => <option key={p} value={p}>{p}</option>)}</select></label>
      <label className="graph-select"><span>Type</span><select value={projection.type} onChange={e => setProjection(p => ({ ...p, type: e.target.value }))}><option value="">All</option>{types.map(t => <option key={t} value={t}>{t}</option>)}</select></label>
      {modules.length > 0 && <fieldset className="graph-modules"><legend>Collapse modules</legend>
       <div className="graph-module-actions"><Button variant="ghost" size="small" onClick={() => setProjection(p => ({ ...p, collapsed: new Set(modules.filter(m => moduleAncestors(m).length === 1)) }))}>Collapse all</Button><Button variant="ghost" size="small" onClick={() => setProjection(p => ({ ...p, collapsed: new Set() }))}>Expand all</Button></div>
       {modules.map(m => <label key={m} className="check mono" style={{ paddingLeft: `${(moduleAncestors(m).length - 1) * 12}px` }}><input type="checkbox" checked={projection.collapsed.has(m)} onChange={() => toggleCollapsed(m)} />{m.split('.').slice(-1)[0]}</label>)}
      </fieldset>}
     </aside>
     <section className="graph-main" aria-label="Dependency graph">
      <div className="graph-main-toolbar"><div className="segmented" role="tablist" aria-label="Graph display"><button role="tab" aria-selected={mode === 'canvas'} onClick={() => setMode('canvas')}><Waypoints size={13} />Canvas</button><button role="tab" aria-selected={mode === 'list'} onClick={() => setMode('list')}><List size={13} />List</button></div><span className="muted">{view.nodes.length} node{view.nodes.length === 1 ? '' : 's'} · {view.edges.length} link{view.edges.length === 1 ? '' : 's'} shown</span></div>
      {view.nodes.length === 0 ? <div className="empty-state"><Boxes size={26} /><h2>No nodes match these filters</h2></div> : mode === 'canvas' ? <GraphCanvas nodes={view.nodes} edges={view.edges} selected={selected} highlight={highlight} matches={matches} onSelect={choose} /> : <NodeList nodes={view.nodes} edges={view.edges} selected={selected} highlight={highlight} query={query} onSelect={choose} />}
      <Legend plan={!!graph.plan} />
     </section>
     {selected && byId.has(selected) && <NodeInspector node={byId.get(selected)!} byId={byId} impact={impact.data} loading={impact.isPending} error={impact.error?.message} plan={graph.plan?.id} close={() => setPicked(undefined)} select={choose} openResource={openResource} openPlan={openPlan} copy={copy} />}
    </div>}
   </>}
  </div>;
}

function Legend({ plan }: { plan: boolean }) {
  return <div className="graph-legend" role="group" aria-label="Legend">
   <ul>
    <li><svg width="34" height="10" aria-hidden="true"><line x1="0" y1="5" x2="30" y2="5" className="graph-edge" markerEnd="url(#graph-arrow)" /></svg>Depends on (declared by the engine)</li>
    <li><svg width="34" height="10" aria-hidden="true"><line x1="0" y1="5" x2="30" y2="5" className="graph-edge derived" /></svg>Derived through internal nodes</li>
    <li><span className="legend-swatch impact" aria-hidden="true">◆</span>Potential impact: depends on the selection</li>
    <li><span className="legend-swatch upstream" aria-hidden="true">▲</span>Selection depends on it</li>
    {plan && <li><span className="legend-swatch planned" aria-hidden="true">●</span>Confirmed planned change (from the plan)</li>}
   </ul>
  </div>;
}

type Highlight = { down: Set<string>; up: Set<string> };

function nodeClass(n: VisNode, selected: string | undefined, h: Highlight, matched: boolean) {
  const member = n.group?.members ?? [n.id];
  const any = (set: Set<string>) => member.some(id => set.has(id));
  const related = selected ? n.id === selected || any(h.down) || any(h.up) : true;
  return ['graph-node', `kind-${n.kind}`, n.id === selected ? 'selected' : '', selected && any(h.down) ? 'impact' : '', selected && any(h.up) ? 'upstream' : '', selected && !related ? 'dim' : '', matched ? 'match' : '', n.node?.planned?.length || n.group?.planned ? 'planned' : ''].filter(Boolean).join(' ');
}

function plannedLabel(n: VisNode) {
  if (n.group) return n.group.planned ? `${n.group.planned} planned` : '';
  const planned = n.node?.planned ?? [];
  if (!planned.length) return '';
  const actions = [...new Set(planned.map(c => c.action))];
  return actions.length === 1 ? `${actions[0]}${planned.length > 1 ? ` ×${planned.length}` : ''}` : `${planned.length} changes`;
}

function truncate(text: string, max = 30) {
  return text.length > max ? `…${text.slice(text.length - max + 1)}` : text;
}

function edgePath(a: Point, b: Point) {
  // From the dependent's left side to the dependency's right side.
  const x1 = a.x;
  const y1 = a.y + NODE_H / 2;
  const x2 = b.x + NODE_W;
  const y2 = b.y + NODE_H / 2;
  const bend = Math.max(40, Math.abs(x1 - x2) / 2);
  return `M${x1},${y1} C${x1 - bend},${y1} ${x2 + bend},${y2} ${x2},${y2}`;
}

const Edges = memo(function Edges({ edges, positions, selected, highlight }: { edges: VisEdge[]; positions: Map<string, Point>; selected?: string; highlight: Highlight }) {
  return <g className="graph-edges">{edges.map(e => {
    const a = positions.get(e.from);
    const b = positions.get(e.to);
    if (!a || !b) return null;
    const lit = selected && ((e.to === selected || highlight.down.has(e.to)) && (e.from === selected || highlight.down.has(e.from)) || (e.from === selected || highlight.up.has(e.from)) && (e.to === selected || highlight.up.has(e.to)));
    return <path key={`${e.from}>${e.to}`} d={edgePath(a, b)} className={`graph-edge ${e.confidence === 'derived' ? 'derived' : ''} ${lit ? 'lit' : selected ? 'faded' : ''}`} markerEnd="url(#graph-arrow)" />;
  })}</g>;
});

const Nodes = memo(function Nodes({ nodes, positions, selected, highlight, matched, onSelect }: { nodes: VisNode[]; positions: Map<string, Point>; selected?: string; highlight: Highlight; matched: Set<string>; onSelect: (id: string) => void }) {
  return <g>{nodes.map(n => {
    const p = positions.get(n.id)!;
    const planned = plannedLabel(n);
    const kind = kindLabels[n.kind] ?? n.kind;
    const label = n.group ? `${n.label} (${n.group.members.length})` : n.label;
    const members = n.group?.members ?? [n.id];
    const mark = !selected || n.id === selected ? '' : members.some(id => highlight.down.has(id)) ? '◆ impact' : members.some(id => highlight.up.has(id)) ? '▲ dependency' : '';
    return <g key={n.id} transform={`translate(${p.x},${p.y})`} className={nodeClass(n, selected, highlight, matched.has(n.id))} role="button" tabIndex={0} aria-label={`${kind} ${label}${planned ? `, planned ${planned}` : ''}${n.node?.inCycle ? ', in a cycle' : ''}`} aria-pressed={n.id === selected} onClick={() => onSelect(n.id)} onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect(n.id); } }}>
     <title>{label}</title>
     <rect width={NODE_W} height={NODE_H} rx={n.kind === 'group' ? 10 : 6} />
     <text x={10} y={18} className="graph-node-label">{truncate(label)}</text>
     <text x={10} y={34} className="graph-node-sub">{mark ? <tspan className="graph-node-mark">{mark} · </tspan> : null}{n.group ? 'module · click to expand' : truncate(`${kind}${n.node?.inCycle ? ' · cycle' : ''}`, mark ? 12 : 22)}</text>
     {planned && <text x={NODE_W - 8} y={34} textAnchor="end" className="graph-node-planned">● {planned}</text>}
    </g>;
  })}</g>;
});

type ViewBox = { x: number; y: number; k: number };

function fit(size: { w: number; h: number }, bounds: { width: number; height: number }): ViewBox {
  const pad = 40;
  const k = Math.min(1.2, Math.max(0.05, Math.min((size.w - pad) / bounds.width, (size.h - pad) / bounds.height)));
  return { k, x: (size.w - bounds.width * k) / 2, y: Math.max(pad / 2, (size.h - bounds.height * k) / 2) };
}

function GraphCanvas({ nodes, edges, selected, highlight, matches, onSelect }: { nodes: VisNode[]; edges: VisEdge[]; selected?: string; highlight: Highlight; matches: VisNode[]; onSelect: (id: string) => void }) {
  const { positions, width, height } = useMemo(() => layout(nodes, edges), [nodes, edges]);
  const container = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 900, h: 560 });
  // null means "fit to screen"; any zoom or pan makes the view explicit.
  const [box, setBox] = useState<ViewBox | null>(null);
  const drag = useRef<{ x: number; y: number; start: ViewBox } | null>(null);
  const bounds = useMemo(() => ({ width, height }), [width, height]);
  const current = box ?? fit(size, bounds);
  const matched = useMemo(() => new Set(matches.map(m => m.id)), [matches]);
  useEffect(() => {
    const el = container.current;
    if (!el) return;
    const observer = new ResizeObserver(([entry]) => {
      const { width: w, height: h } = entry.contentRect;
      if (w > 0 && h > 0) setSize({ w, h });
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    const el = container.current;
    if (!el) return;
    // A native listener: React's wheel handler is passive and cannot stop page scrolling.
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      const rect = el.getBoundingClientRect();
      const px = e.clientX - rect.left;
      const py = e.clientY - rect.top;
      setBox(b => zoomAt(b ?? fit(size, bounds), e.deltaY < 0 ? 1.15 : 1 / 1.15, px, py));
    };
    el.addEventListener('wheel', wheel, { passive: false });
    return () => el.removeEventListener('wheel', wheel);
  }, [size, bounds]);
  function zoomAt(b: ViewBox, factor: number, px: number, py: number): ViewBox {
    const k = Math.min(3, Math.max(0.05, b.k * factor));
    return { k, x: px - (px - b.x) * (k / b.k), y: py - (py - b.y) * (k / b.k) };
  }
  function down(e: PointerEvent<HTMLDivElement>) {
    if ((e.target as Element).closest('.graph-node')) return;
    drag.current = { x: e.clientX, y: e.clientY, start: current };
    e.currentTarget.setPointerCapture(e.pointerId);
  }
  function move(e: PointerEvent<HTMLDivElement>) {
    const d = drag.current;
    if (d) setBox({ ...d.start, x: d.start.x + e.clientX - d.x, y: d.start.y + e.clientY - d.y });
  }
  function key(e: KeyboardEvent<HTMLDivElement>) {
    if (e.target !== e.currentTarget) return;
    const step = 60;
    const moves: Record<string, () => ViewBox> = {
      ArrowLeft: () => ({ ...current, x: current.x + step }),
      ArrowRight: () => ({ ...current, x: current.x - step }),
      ArrowUp: () => ({ ...current, y: current.y + step }),
      ArrowDown: () => ({ ...current, y: current.y - step }),
      '+': () => zoomAt(current, 1.2, size.w / 2, size.h / 2),
      '=': () => zoomAt(current, 1.2, size.w / 2, size.h / 2),
      '-': () => zoomAt(current, 1 / 1.2, size.w / 2, size.h / 2)
    };
    if (e.key === '0') {
      e.preventDefault();
      setBox(null);
    } else if (moves[e.key]) {
      e.preventDefault();
      setBox(moves[e.key]());
    }
  }
  function center(id: string) {
    const p = positions.get(id);
    if (!p) return;
    const k = Math.max(current.k, 0.8);
    setBox({ k, x: size.w / 2 - (p.x + NODE_W / 2) * k, y: size.h / 2 - (p.y + NODE_H / 2) * k });
  }
  return <div className="graph-canvas-wrap">
   <div className="graph-zoom" role="toolbar" aria-label="Zoom">
    <Button variant="ghost" size="icon" aria-label="Zoom in" onClick={() => setBox(zoomAt(current, 1.25, size.w / 2, size.h / 2))}><ZoomIn size={15} /></Button>
    <Button variant="ghost" size="icon" aria-label="Zoom out" onClick={() => setBox(zoomAt(current, 1 / 1.25, size.w / 2, size.h / 2))}><ZoomOut size={15} /></Button>
    <Button variant="ghost" size="icon" aria-label="Fit to screen" onClick={() => setBox(null)}><Maximize size={15} /></Button>
    {selected && positions.has(selected) && <Button variant="ghost" size="icon" aria-label="Center the selection" onClick={() => center(selected)}><Target size={15} /></Button>}
   </div>
   <div ref={container} className="graph-canvas" tabIndex={0} aria-label="Graph canvas. Drag or use arrow keys to pan, plus and minus to zoom, 0 to fit. Tab to reach nodes." onPointerDown={down} onPointerMove={move} onPointerUp={() => { drag.current = null; }} onPointerCancel={() => { drag.current = null; }} onKeyDown={key}>
    <svg width="100%" height="100%" role="group" aria-label={`${nodes.length} nodes`}>
     <defs><marker id="graph-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" className="graph-arrow" /></marker></defs>
     <g transform={`translate(${current.x},${current.y}) scale(${current.k})`}>
      <Edges edges={edges} positions={positions} selected={selected} highlight={highlight} />
      <Nodes nodes={nodes} positions={positions} selected={selected} highlight={highlight} matched={matched} onSelect={onSelect} />
     </g>
    </svg>
   </div>
  </div>;
}

const rowHeight = 40;

// Accessible list alternative to the canvas, virtualized for large graphs.
function NodeList({ nodes, edges, selected, highlight, query, onSelect }: { nodes: VisNode[]; edges: VisEdge[]; selected?: string; highlight: Highlight; query: string; onSelect: (id: string) => void }) {
  const [top, setTop] = useState(0);
  const counts = useMemo(() => {
    const deps = new Map<string, number>();
    const dependents = new Map<string, number>();
    for (const e of edges) {
      deps.set(e.from, (deps.get(e.from) ?? 0) + 1);
      dependents.set(e.to, (dependents.get(e.to) ?? 0) + 1);
    }
    return { deps, dependents };
  }, [edges]);
  const rows = useMemo(() => {
    const sorted = [...nodes].sort((a, b) => a.label.localeCompare(b.label));
    return query ? sorted.filter(n => `${n.label} ${n.node?.type ?? ''}`.toLowerCase().includes(query)) : sorted;
  }, [nodes, query]);
  const height = 560;
  const first = Math.max(0, Math.floor(top / rowHeight) - 5);
  const last = Math.min(rows.length, Math.ceil((top + height) / rowHeight) + 5);
  return <div className="graph-list" style={{ height }} onScroll={e => setTop(e.currentTarget.scrollTop)} role="list" aria-label={`${rows.length} graph nodes`}>
   <div style={{ height: rows.length * rowHeight, position: 'relative' }}>
    {rows.slice(first, last).map((n, i) => {
     const planned = plannedLabel(n);
     return <div key={n.id} role="listitem" aria-setsize={rows.length} aria-posinset={first + i + 1} className="graph-list-row" style={{ top: (first + i) * rowHeight, height: rowHeight }}>
      <button className={nodeClass(n, selected, highlight, false)} aria-pressed={n.id === selected} onClick={() => onSelect(n.id)}>
       <span className="graph-list-name mono">{n.group ? `${n.label} (${n.group.members.length})` : n.label}</span>
       <span className="muted">{kindLabels[n.kind] ?? n.kind}</span>
       <span className="muted">{counts.deps.get(n.id) ?? 0} deps · {counts.dependents.get(n.id) ?? 0} dependents</span>
       {selected && highlight.down.has(n.id) && <span className="flag impact">◆ Potential impact</span>}
       {selected && highlight.up.has(n.id) && <span className="flag upstream">▲ Dependency</span>}
       {planned && <span className="flag planned">● {planned}</span>}
      </button>
     </div>;
    })}
   </div>
  </div>;
}

function NodeRef({ id, byId, select, distance }: { id: string; byId: Map<string, GraphNode>; select: (id: string) => void; distance?: number }) {
  const n = byId.get(id);
  if (!n) return null;
  return <li><button className="link-button mono" onClick={() => select(id)}>{n.displayName}</button><span className="muted">{kindLabels[n.nodeKind]}{distance !== undefined && distance > 1 ? ` · ${distance} hops` : ''}</span>{n.planned?.map(c => <ActionBadge key={c.changeId} action={c.action} />)}</li>;
}

function ChangeList({ changes, byId, select }: { changes: PlannedChange[]; byId: Map<string, GraphNode>; select: (id: string) => void }) {
  return <ul className="graph-refs">{changes.map(c => <li key={c.changeId}><ActionBadge action={c.action} />{c.node && byId.has(c.node) ? <button className="link-button mono" onClick={() => select(c.node!)}>{c.address}</button> : <code>{c.address}</code>}</li>)}</ul>;
}

function NodeInspector({ node, byId, impact, loading, error, plan, close, select, openResource, openPlan, copy }: {
  node: GraphNode;
  byId: Map<string, GraphNode>;
  impact?: GraphImpact;
  loading: boolean;
  error?: string;
  plan?: string;
  close: () => void;
  select: (id: string) => void;
  openResource: (id: string) => void;
  openPlan: (planId: string) => void;
  copy: (value: string, label: string) => Promise<void>;
}) {
  const resources = (list: { node: string; distance: number }[]) => list.filter(r => {
    const k = byId.get(r.node)?.nodeKind;
    return k === 'resource' || k === 'data';
  });
  const downstream = impact ? resources(impact.downstream) : [];
  const upstream = impact ? resources(impact.upstream) : [];
  return <aside className="inspector graph-inspector" aria-label="Node details">
   <div className="inspector-header"><span><Waypoints size={15} />Node details</span><Button variant="ghost" size="icon" aria-label="Close node details" onClick={close}><X size={16} /></Button></div>
   <div className="inspector-body">
    <div className="address-box"><div className="label-row"><span>{kindLabels[node.nodeKind]?.toUpperCase()}</span>{!node.redacted && <Button variant="ghost" size="icon" aria-label="Copy node name" onClick={() => void copy(node.resourceAddress ?? node.displayName, 'Name')}><Copy size={13} /></Button>}</div><code>{node.displayName}</code></div>
    <p className="muted graph-confidence">{confidenceText[node.confidence]}</p>
    {node.inCycle && <p className="plan-warning"><Repeat size={14} />This node is part of a dependency cycle. The engine reports cycles as errors when it plans.</p>}
    <dl className="metadata">
     <div><dt>Module</dt><dd className="mono">{node.modulePath || 'Root module'}</dd></div>
     {node.type && <div><dt>Type</dt><dd className="mono">{node.type}</dd></div>}
     {node.provider && <div><dt>Provider</dt><dd className="mono">{node.provider}</dd></div>}
     {(node.nodeKind === 'resource' || node.nodeKind === 'data') && <div><dt>Recorded instances</dt><dd>{node.instanceCount}</dd></div>}
    </dl>
    {node.instances && node.instances.length > 0 && <section><h3>Open in Resources</h3><ul className="graph-refs">{node.instances.map(i => <li key={i.resourceId}><button className="link-button mono" onClick={() => openResource(i.resourceId)}>{i.address}</button><ArrowRight size={12} /></li>)}</ul>{node.instanceCount > node.instances.length && <p className="muted">and {node.instanceCount - node.instances.length} more</p>}</section>}
    {node.planned && node.planned.length > 0 && plan && <section><h3>Planned for this node</h3><ChangeList changes={node.planned} byId={byId} select={select} /><Button variant="outline" size="small" onClick={() => openPlan(plan)}>View in Plans<ArrowRight size={12} /></Button></section>}
    {loading ? <div className="empty-state" role="status"><LoaderCircle className="spin" />Analyzing…</div> : error ? <p className="dialog-error" role="alert">{error}</p> : impact && <>
     <section className="graph-impact" aria-label="Potential impact">
      <h3>Potential impact <span className="muted">{impact.downstreamResources} resource{impact.downstreamResources === 1 ? '' : 's'}</span></h3>
      <p className="muted">Resources that depend on this node, directly or through others. A change here <em>could</em> affect them; it does not mean they will change.</p>
      {downstream.length ? <ul className="graph-refs">{downstream.slice(0, 200).map(r => <NodeRef key={r.node} id={r.node} byId={byId} select={select} distance={r.distance} />)}</ul> : <p className="muted">Nothing depends on this node.</p>}
      {impact.truncated && <p className="muted">The list is capped; {impact.downstreamTotal} nodes in total depend on it.</p>}
     </section>
     {plan && impact.plan ? <section className="graph-confirmed" aria-label="Confirmed planned changes">
      <h3><CircleAlert size={14} />Confirmed planned changes</h3>
      <p className="muted">From the selected plan only. These are listed because the plan contains them; a dependency link does not show that one change causes another.</p>
      <h4>Here and in its potential impact <span className="muted">{impact.plan.inImpact.length}</span></h4>
      {impact.plan.inImpact.length ? <ChangeList changes={impact.plan.inImpact} byId={byId} select={select} /> : <p className="muted">None.</p>}
      <h4>Elsewhere in the plan <span className="muted">{impact.plan.elsewhere.length}</span></h4>
      {impact.plan.elsewhere.length ? <ChangeList changes={impact.plan.elsewhere} byId={byId} select={select} /> : <p className="muted">None.</p>}
      {impact.plan.unmapped.length > 0 && <><h4>Not in this graph <span className="muted">{impact.plan.unmapped.length}</span></h4><ChangeList changes={impact.plan.unmapped} byId={byId} select={select} /></>}
     </section> : <p className="info-line muted">Choose a plan to compare potential impact with confirmed planned changes.</p>}
     <section><h3>Depends on <span className="muted">{impact.directDependencies.length} direct · {upstream.length} resource{upstream.length === 1 ? '' : 's'} in total</span></h3>{impact.directDependencies.length ? <ul className="graph-refs">{impact.directDependencies.map(id => <NodeRef key={id} id={id} byId={byId} select={select} />)}</ul> : <p className="muted">No dependencies.</p>}</section>
     <section><h3>Direct dependents <span className="muted">{impact.directDependents.length}</span></h3>{impact.directDependents.length ? <ul className="graph-refs">{impact.directDependents.map(id => <NodeRef key={id} id={id} byId={byId} select={select} />)}</ul> : <p className="muted">No dependents.</p>}</section>
    </>}
   </div>
  </aside>;
}
