import { describe, expect, it } from 'vitest';
import { launchScope, scopedPath, snapshotPath } from './routes';

describe('scoped API paths', () => {
  const cid = 'a'.repeat(32);
  it('keeps launch routes and prefixes only a valid Hub context', () => {
    expect(scopedPath(launchScope, 'resources?limit=50')).toBe('resources?limit=50');
    expect(scopedPath({ kind: 'context', contextId: cid }, 'resources/x')).toBe(`contexts/${cid}/resources/x`);
    expect(snapshotPath(launchScope)).toBe('workspace');
    expect(snapshotPath({ kind: 'context', contextId: cid })).toBe(`contexts/${cid}`);
  });
  it('refuses a malformed context instead of falling back to launch routes', () => {
    for (const contextId of ['', 'ctx-dev', '../workspace', 'A'.repeat(32)]) {
      expect(() => scopedPath({ kind: 'context', contextId }, 'resources')).toThrow(/no longer open/);
      expect(() => snapshotPath({ kind: 'context', contextId })).toThrow(/no longer open/);
    }
  });
});
