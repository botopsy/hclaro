import type { GraphEdge, GraphNode, GraphNodeKind } from '../types';

// A node as drawn: one graph node, or a collapsed module standing for its members.
export type VisNode = {
  id: string;
  label: string;
  kind: GraphNodeKind | 'group';
  node?: GraphNode;
  group?: { module: string; members: string[]; planned: number };
};
export type VisEdge = { from: string; to: string; confidence: 'declared' | 'derived' };
export type Point = { x: number; y: number };

export const NODE_W = 230;
export const NODE_H = 44;
const GAP_X = 80;
const GAP_Y = 16;
const PER_COLUMN = 24;

export const internalKinds: GraphNodeKind[] = ['module', 'provider', 'variable', 'local', 'output', 'check', 'ephemeral', 'unknown'];

export type Projection = {
  showData: boolean;
  showInternal: boolean;
  provider: string;
  type: string;
  collapsed: Set<string>;
};

// The provider a resource type implies by its prefix ("aws_instance" -> "aws"). Engines use
// the same rule unless configuration names another provider, so filters label it as such.
export function typePrefix(type?: string) {
  return type ? type.split('_')[0] : '';
}

// Module paths from the outermost: "module.a.module.b" -> ["module.a", "module.a.module.b"].
export function moduleAncestors(path: string) {
  if (!path) return [];
  const out: string[] = [];
  const parts = splitSegments(path);
  for (let i = 1; i < parts.length; i += 2) out.push(parts.slice(0, i + 1).join('.'));
  return out;
}

// Splits an address on dots outside index brackets and quotes.
export function splitSegments(address: string) {
  const out: string[] = [];
  let current = '';
  let depth = 0;
  let quoted = false;
  for (let i = 0; i < address.length; i++) {
    const c = address[i];
    if (quoted) {
      current += c;
      if (c === '\\') current += address[++i] ?? '';
      else if (c === '"') quoted = false;
      continue;
    }
    if (c === '"') quoted = true;
    if (c === '[') depth++;
    if (c === ']') depth--;
    if (c === '.' && depth === 0) {
      out.push(current);
      current = '';
      continue;
    }
    current += c;
  }
  out.push(current);
  return out;
}

// Removes instance keys: module.a["x"].aws_instance.b[0] -> module.a.aws_instance.b.
export function configAddress(address: string) {
  return splitSegments(address).map(s => s.replace(/\[.*\]$/, '')).join('.');
}

// The node an instance address belongs to: the exact instanced node first, then the
// configuration resource. Mode is not known here, so data addresses keep their prefix.
export function nodeForAddress(nodes: GraphNode[], address: string) {
  return nodes.find(n => n.resourceAddress === address) ?? nodes.find(n => n.resourceAddress === configAddress(address));
}

export function project(nodes: GraphNode[], edges: GraphEdge[], resourceEdges: GraphEdge[], p: Projection): { nodes: VisNode[]; edges: VisEdge[] } {
  const kept = new Map<string, string>();
  const vis = new Map<string, VisNode>();
  for (const n of nodes) {
    const internal = internalKinds.includes(n.nodeKind);
    if (internal && !p.showInternal) continue;
    if (n.nodeKind === 'data' && !p.showData) continue;
    if (p.provider && typePrefix(n.type) !== p.provider) continue;
    if (p.type && n.type !== p.type) continue;
    const outer = moduleAncestors(n.modulePath).find(m => p.collapsed.has(m));
    if (outer) {
      const id = `group:${outer}`;
      const group = vis.get(id) ?? { id, label: outer, kind: 'group' as const, group: { module: outer, members: [], planned: 0 } };
      group.group!.members.push(n.id);
      group.group!.planned += n.planned?.length ?? 0;
      vis.set(id, group);
      kept.set(n.id, id);
      continue;
    }
    vis.set(n.id, { id: n.id, label: n.displayName, kind: n.nodeKind, node: n });
    kept.set(n.id, n.id);
  }
  const out = new Map<string, VisEdge>();
  for (const e of p.showInternal ? edges : resourceEdges) {
    const from = kept.get(e.from);
    const to = kept.get(e.to);
    if (!from || !to || from === to) continue;
    const key = `${from}>${to}`;
    const existing = out.get(key);
    if (!existing || e.confidence === 'declared') out.set(key, { from, to, confidence: e.confidence });
  }
  return { nodes: [...vis.values()], edges: [...out.values()] };
}

// Layered layout: dependencies on the left, dependents to the right. Rank is the longest
// dependency chain below a node; edges that close a cycle are ignored for ranking. Long
// ranks wrap into several columns.
export function layout(nodes: VisNode[], edges: VisEdge[]): { positions: Map<string, Point>; width: number; height: number } {
  const deps = new Map<string, string[]>(nodes.map(n => [n.id, []]));
  for (const e of edges) deps.get(e.from)?.push(e.to);
  const rank = new Map<string, number>();
  const state = new Map<string, 1 | 2>();
  // Iterative depth-first ranking keeps deep chains off the call stack.
  for (const start of nodes) {
    if (state.has(start.id)) continue;
    const stack: { id: string; i: number }[] = [{ id: start.id, i: 0 }];
    state.set(start.id, 1);
    while (stack.length) {
      const top = stack[stack.length - 1];
      const next = deps.get(top.id)!;
      if (top.i < next.length) {
        const w = next[top.i++];
        if (!state.has(w)) {
          state.set(w, 1);
          stack.push({ id: w, i: 0 });
        }
        continue;
      }
      let r = 0;
      for (const w of next) if (state.get(w) === 2) r = Math.max(r, (rank.get(w) ?? 0) + 1);
      rank.set(top.id, r);
      state.set(top.id, 2);
      stack.pop();
    }
  }
  const ranks: VisNode[][] = [];
  for (const n of nodes) (ranks[rank.get(n.id) ?? 0] ??= []).push(n);
  const order = (n: VisNode) => `${n.node?.modulePath ?? n.group?.module ?? ''}\u0000${n.label}`;
  const positions = new Map<string, Point>();
  let x = 0;
  let height = 0;
  for (const column of ranks) {
    if (!column) continue;
    // Barycenter of already placed dependencies, then module and name for stability.
    const weight = (n: VisNode) => {
      const ys = (deps.get(n.id) ?? []).map(d => positions.get(d)?.y).filter((y): y is number => y !== undefined);
      return ys.length ? ys.reduce((a, b) => a + b, 0) / ys.length : Number.POSITIVE_INFINITY;
    };
    const sorted = [...column].sort((a, b) => weight(a) - weight(b) || order(a).localeCompare(order(b)));
    sorted.forEach((n, i) => {
      const sub = Math.floor(i / PER_COLUMN);
      const row = i % PER_COLUMN;
      positions.set(n.id, { x: x + sub * (NODE_W + GAP_X / 2), y: row * (NODE_H + GAP_Y) });
      height = Math.max(height, row * (NODE_H + GAP_Y) + NODE_H);
    });
    x += Math.ceil(sorted.length / PER_COLUMN) * (NODE_W + GAP_X / 2) + GAP_X / 2;
  }
  return { positions, width: Math.max(x - GAP_X, NODE_W), height: Math.max(height, NODE_H) };
}
