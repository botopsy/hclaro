import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { ChangeChip, Chip, StatusDot } from './Chip';

afterEach(cleanup);

describe('chips', () => {
  it('a change chip always carries a symbol and a word', () => {
    const { container } = render(<><ChangeChip action="create" count={3} /><ChangeChip action="update" /><ChangeChip action="replace" /><ChangeChip action="delete" /><ChangeChip action="read" /></>);
    expect(Array.from(container.querySelectorAll('.chip')).map(c => c.textContent)).toEqual(['+3create', '~update', '±replace', '−delete', '<=read']);
    expect(container.querySelector('.chip')).toHaveClass('chip-tone-create');
  });

  it('a chip and a status dot keep their label', () => {
    render(<><Chip tone="accent">Beta</Chip><StatusDot tone="ok" label="Ready" /><StatusDot tone="delete" label="Backend unreachable" hideLabel /></>);
    expect(screen.getByText('Beta')).toHaveClass('chip-tone-accent');
    expect(screen.getByText('Ready')).toBeInTheDocument();
    expect(screen.getByText('Backend unreachable')).toHaveClass('sr-only');
  });
});
