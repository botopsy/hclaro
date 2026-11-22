import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { EmptyState } from './EmptyState';
import { Disclosure } from './Disclosure';

afterEach(cleanup);

describe('EmptyState and Disclosure', () => {
  it('shows a title, one sentence, the action and a collapsed How this works', () => {
    const { container } = render(<EmptyState title="No plans yet" action={<button>Run plan</button>} how="Plans run only when you click.">Run a plan to see every change.</EmptyState>);
    expect(screen.getByRole('heading', { name: 'No plans yet' })).toBeInTheDocument();
    expect(screen.getByText('Run a plan to see every change.')).toBeInTheDocument();
    expect(screen.getAllByRole('button')).toHaveLength(1);
    const details = container.querySelector('details')!;
    expect(details).not.toHaveAttribute('open');
    expect(screen.getByText('How this works').tagName).toBe('SUMMARY');
  });

  it('a disclosure can start open', () => {
    const { container } = render(<Disclosure summary="Rules" open>Body</Disclosure>);
    expect(container.querySelector('details')).toHaveAttribute('open');
  });
});
