import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DataTable, type Column } from './DataTable';

afterEach(cleanup);

type Row = { address: string; type: string };
const columns: Column<Row>[] = [{ key: 'address', header: 'Address', cell: r => r.address }, { key: 'type', header: 'Type', cell: r => r.type }];
const vpc = { address: 'aws_vpc.main', type: 'aws_vpc' };
const a = { address: 'aws_subnet.a', type: 'aws_subnet' };
const b = { address: 'aws_subnet.b', type: 'aws_subnet' };

describe('DataTable', () => {
  it('collapses a group header row', () => {
    render(<DataTable label="Resources" columns={columns} rowKey={r => r.address} groups={[{ key: 'aws_subnet', label: 'aws_subnet', rows: [a, b] }, { key: 'aws_vpc', label: 'aws_vpc', rows: [vpc] }]} />);
    const header = screen.getByRole('button', { name: /aws_subnet\s*2/ });
    expect(header).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(header);
    expect(header).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('aws_subnet.a')).toBeNull();
    expect(screen.getByText('aws_vpc.main')).toBeInTheDocument();
  });

  it('selects rows and all rows through checkboxes', () => {
    const change = vi.fn();
    const { rerender } = render(<DataTable label="Resources" columns={columns} rowKey={r => r.address} rows={[vpc, a]} selected={new Set()} onSelectedChange={change} />);
    fireEvent.click(screen.getByRole('checkbox', { name: 'Select aws_subnet.a' }));
    expect(change).toHaveBeenLastCalledWith(new Set(['aws_subnet.a']));
    rerender(<DataTable label="Resources" columns={columns} rowKey={r => r.address} rows={[vpc, a]} selected={new Set(['aws_subnet.a'])} onSelectedChange={change} />);
    const all = screen.getByRole('checkbox', { name: 'Select all rows' }) as HTMLInputElement;
    expect(all.indeterminate).toBe(true);
    fireEvent.click(all);
    expect(change).toHaveBeenLastCalledWith(new Set(['aws_subnet.a', 'aws_vpc.main']));
  });

  it('expands a row, highlights the active row and applies the density', () => {
    render(<DataTable label="Operations" columns={columns} rowKey={r => r.address} rows={[vpc]} expand={r => `log for ${r.address}`} active="aws_vpc.main" density="compact" />);
    const table = screen.getByRole('table', { name: 'Operations' });
    expect(table).toHaveClass('density-compact');
    const toggle = screen.getByRole('button', { name: 'Expand aws_vpc.main' });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(toggle);
    expect(screen.getByRole('button', { name: 'Collapse aws_vpc.main' })).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('log for aws_vpc.main')).toBeInTheDocument();
    expect(within(table).getByText('aws_vpc.main').closest('tr')).toHaveClass('active');
  });
});
