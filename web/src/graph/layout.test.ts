import { describe, expect, it } from 'vitest';
import { configAddress, layout, moduleAncestors, nodeForAddress, project, splitSegments } from './layout';
import type { GraphEdge, GraphNode } from '../types';

const n = (id: string, name: string, kind: GraphNode['nodeKind'] = 'resource', modulePath = ''): GraphNode => ({ id, displayName: name, resourceAddress: kind === 'resource' ? name : undefined, modulePath, nodeKind: kind, confidence: 'configuration', instanceCount: 0 });
const e = (from: string, to: string, confidence: GraphEdge['confidence'] = 'declared'): GraphEdge => ({ from, to, edgeKind: 'depends_on', confidence });

describe('graph layout', () => {
  it('reads addresses with quoted keys', () => {
    expect(splitSegments('module.a["x.y"].aws_s3_bucket.b["k\\"]"]')).toEqual(['module', 'a["x.y"]', 'aws_s3_bucket', 'b["k\\"]"]']);
    expect(configAddress('module.a["x.y"].module.b[2].aws_s3_bucket.c[0]')).toBe('module.a.module.b.aws_s3_bucket.c');
    expect(moduleAncestors('module.a.module.b')).toEqual(['module.a', 'module.a.module.b']);
    const nodes = [n('n0', 'aws_instance.web'), n('n1', 'aws_instance.web[1]')];
    expect(nodeForAddress(nodes, 'aws_instance.web[1]')?.id).toBe('n1');
    expect(nodeForAddress(nodes, 'aws_instance.web[0]')?.id).toBe('n0');
    expect(nodeForAddress(nodes, 'aws_vpc.x')).toBeUndefined();
  });

  it('places dependencies left of dependents and terminates on cycles', () => {
    const nodes = [n('a', 'a.a'), n('b', 'b.b'), n('c', 'c.c'), n('d', 'd.d')];
    const edges = [e('a', 'b'), e('b', 'c'), e('c', 'a'), e('d', 'a')];
    const { positions } = layout(nodes.map(x => ({ id: x.id, label: x.displayName, kind: x.nodeKind, node: x })), edges.map(x => ({ ...x })));
    expect(positions.size).toBe(4);
    expect(positions.get('d')!.x).toBeGreaterThan(positions.get('a')!.x);
  });

  it('collapses modules and hides internal nodes with derived links', () => {
    const nodes = [n('r', 'aws_vpc.main'), n('v', 'module.app.var.vpc', 'variable', 'module.app'), n('i', 'module.app.aws_instance.x', 'resource', 'module.app'), n('j', 'module.app.module.db.aws_db.x', 'resource', 'module.app.module.db')];
    const edges = [e('i', 'v'), e('v', 'r'), e('j', 'i')];
    const resourceEdges = [e('i', 'r', 'derived'), e('j', 'i')];
    const plain = project(nodes, edges, resourceEdges, { showData: true, showInternal: false, provider: '', type: '', collapsed: new Set() });
    expect(plain.nodes.map(x => x.id).sort()).toEqual(['i', 'j', 'r']);
    expect(plain.edges).toContainEqual({ from: 'i', to: 'r', confidence: 'derived' });
    const collapsed = project(nodes, edges, resourceEdges, { showData: true, showInternal: false, provider: '', type: '', collapsed: new Set(['module.app']) });
    expect(collapsed.nodes.map(x => x.id).sort()).toEqual(['group:module.app', 'r']);
    expect(collapsed.edges).toEqual([{ from: 'group:module.app', to: 'r', confidence: 'derived' }]);
    const internal = project(nodes, edges, resourceEdges, { showData: true, showInternal: true, provider: '', type: '', collapsed: new Set() });
    expect(internal.nodes).toHaveLength(4);
    expect(internal.edges.every(x => x.confidence === 'declared')).toBe(true);
  });

  it('lays out 1,000 nodes quickly', () => {
    const nodes = Array.from({ length: 1000 }, (_, i) => ({ id: `n${i}`, label: `r${i}`, kind: 'resource' as const }));
    const edges = nodes.slice(1).map((x, i) => ({ from: x.id, to: `n${Math.floor(i / 3)}`, confidence: 'declared' as const }));
    const start = performance.now();
    const { positions } = layout(nodes, edges);
    expect(positions.size).toBe(1000);
    expect(performance.now() - start).toBeLessThan(1000);
  });
});
