import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { PageHeader } from './PageHeader';

afterEach(cleanup);

describe('PageHeader', () => {
  it('shows the title, count, one chip and the actions', () => {
    render(<PageHeader title="Plans" count={4} chip="Nothing is applied until you confirm"><button>Run plan</button></PageHeader>);
    expect(screen.getByRole('heading', { level: 1, name: 'Plans' })).toBeInTheDocument();
    expect(screen.getByLabelText('4 total')).toHaveTextContent('4');
    expect(screen.getByText('Nothing is applied until you confirm')).toHaveClass('safety-chip');
    expect(screen.getByRole('button', { name: 'Run plan' })).toBeInTheDocument();
  });

  it('renders nothing optional when not given', () => {
    const { container } = render(<PageHeader title="Resources" />);
    expect(container.querySelector('.safety-chip, .page-count, .page-header-actions')).toBeNull();
  });
});
