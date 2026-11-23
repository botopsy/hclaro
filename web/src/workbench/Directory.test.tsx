import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ModulesGrid, ProvidersGrid, moduleCards } from './Directory';

const modules = [
  { address: '', parent: '', count: 3 },
  { address: 'module.compute', parent: '', count: 40 },
  { address: 'module.compute.module.workers["blue.eu"]', parent: 'module.compute', count: 10 },
  { address: 'module.compute.module.workers["blue.eu"].module.disk', parent: 'module.compute.module.workers["blue.eu"]', count: 2 },
  { address: 'module.compute2', parent: '', count: 1 }
];

describe('Modules', () => {
  it('makes one card per top-level module plus root, counting nested modules', () => {
    expect(moduleCards(modules).map(c => [c.module.address, c.total, c.children.length])).toEqual([['', 3, 0], ['module.compute', 52, 2], ['module.compute2', 1, 0]]);
  });
  it('shows the short name, full address, total and child modules, and opens resources', async () => {
    const open = vi.fn();
    render(<ModulesGrid modules={modules} open={open} />);
    const card = screen.getByRole('article', { name: 'compute' });
    expect(within(card).getByRole('heading', { name: 'compute' })).toBeInTheDocument();
    expect(card).toHaveTextContent('module.compute');
    expect(card).toHaveTextContent('52 resources');
    const children = within(card).getByRole('region', { name: 'Child modules' });
    expect(within(children).getAllByRole('listitem').map(li => li.textContent)).toEqual(['workers["blue.eu"]10', 'workers["blue.eu"].module.disk2']);
    expect(within(screen.getByRole('article', { name: 'compute2' })).queryByRole('region')).not.toBeInTheDocument();
    expect(screen.getByRole('article', { name: 'Root module' })).toHaveTextContent('3 resources');
    await userEvent.setup().click(within(card).getByRole('button', { name: 'View resources →' }));
    expect(open).toHaveBeenCalledWith('module.compute');
  });
});

describe('Providers', () => {
  it('shows the source, resource count and an amber unknown version without a lockfile', async () => {
    const open = vi.fn();
    render(<ProvidersGrid providers={{ 'registry.terraform.io/hashicorp/aws': 316 }} lockfile={false} open={open} />);
    const card = screen.getByRole('article', { name: 'aws' });
    expect(card).toHaveTextContent('registry.terraform.io/hashicorp/aws');
    expect(within(card).getByText('Unknown: no lockfile')).toHaveClass('tone-warn');
    expect(within(card).getByText('316')).toBeInTheDocument();
    await userEvent.setup().click(within(card).getByRole('button', { name: 'View resources →' }));
    expect(open).toHaveBeenCalledWith('registry.terraform.io/hashicorp/aws');
  });
  it('never claims a version is unknown when the lockfile exists', () => {
    render(<ProvidersGrid providers={{ 'registry.terraform.io/hashicorp/aws': 1 }} lockfile open={vi.fn()} />);
    expect(screen.queryByText('Unknown: no lockfile')).not.toBeInTheDocument();
  });
});
