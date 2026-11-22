import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Segmented } from './Segmented';

afterEach(cleanup);

describe('Segmented', () => {
  it('presses exactly the current option and reports a change', () => {
    const change = vi.fn();
    render(<Segmented label="Group by" value="none" onChange={change} options={[{ value: 'none', label: 'None' }, { value: 'type', label: 'Type' }]} />);
    expect(screen.getByRole('group', { name: 'Group by' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'None' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('button', { name: 'Type' })).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(screen.getByRole('button', { name: 'Type' }));
    expect(change).toHaveBeenCalledWith('type');
  });
});
