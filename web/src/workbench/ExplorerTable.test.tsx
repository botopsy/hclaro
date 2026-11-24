import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { Resource } from '../types';
import { ResourceTable } from './Explorer';

const row = (id: string, keyAttributes?: string) => ({ id, address: `aws_s3_bucket.${id}`, mode: 'managed', type: 'aws_s3_bucket', name: id, provider: 'aws', module: '', attributes: null, keyAttributes }) as Resource;

function table(resources: Resource[]) {
  render(<ResourceTable resources={resources} groupBy="none" showProvider={false} drift={() => ({ label: 'Not checked', tone: 'neutral' })} selected={new Set()} onSelectedChange={() => {}} onOpen={() => {}} density="comfortable" />);
}

describe('Key attributes column', () => {
  it('shows the summary with a full-text tooltip', () => {
    table([row('a', 'logs-bucket')]);
    expect(screen.getByText('Key attributes')).toBeTruthy();
    expect(screen.getByTitle('logs-bucket')).toBeTruthy();
  });
  it('is hidden when no row has a summary', () => {
    table([row('a')]);
    expect(screen.queryByText('Key attributes')).toBeNull();
  });
});
