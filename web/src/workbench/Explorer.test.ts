import { describe, expect, it } from 'vitest';
import type { DriftScan } from '../types';
import { driftOf, targetFlags } from './Explorer';

const scan = (over: Partial<DriftScan>) => ({ id: 's', status: 'completed', exhaustive: true, current: true, withheld: false, resources: [], ...over }) as unknown as DriftScan;

describe('driftOf', () => {
  it('matches the latest finished scan by snapshot ID', () => {
    const scans = [scan({ status: 'running' }), scan({ resources: [{ resourceId: 'a', classification: 'missing' }] as DriftScan['resources'] })];
    expect(driftOf(scans, 'a')).toEqual({ label: 'Missing', tone: 'delete' });
    expect(driftOf(scans, 'b')).toEqual({ label: 'No drift', tone: 'ok' });
  });
  it('never claims no drift without a current, complete, exhaustive scan', () => {
    expect(driftOf(undefined, 'a').label).toBe('Not checked');
    expect(driftOf([scan({ current: false })], 'a').label).toBe('Not checked');
    expect(driftOf([scan({ withheld: true })], 'a').label).toBe('Not checked');
    expect(driftOf([scan({ exhaustive: false })], 'a').label).toBe('Not checked');
    expect(driftOf([scan({ status: 'partial' })], 'a').label).toBe('Not checked');
    expect(driftOf([scan({ status: 'failed' })], 'a').label).toBe('Not checked');
  });
});

describe('targetFlags', () => {
  it('single-quotes each address for a shell', () => {
    expect(targetFlags(['aws_vpc.main', `module.m["it's"].aws_instance.x`])).toBe(`'-target=aws_vpc.main' '-target=module.m["it'\\''s"].aws_instance.x'`);
  });
});
