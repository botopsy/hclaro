import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CopyButton } from './CopyButton';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function clipboard(writeText: (value: string) => Promise<void>) {
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
}

describe('CopyButton', () => {
  it('copies the value and says so', async () => {
    const write = vi.fn().mockResolvedValue(undefined);
    clipboard(write);
    render(<CopyButton value="aws_vpc.main" label="Copy address" />);
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Copy address' })));
    expect(write).toHaveBeenCalledWith('aws_vpc.main');
    expect(screen.getByRole('status')).toHaveTextContent('Copied');
  });

  it('reports a denied clipboard', async () => {
    clipboard(vi.fn().mockRejectedValue(new Error('denied')));
    render(<CopyButton value="x" label="Copy" iconOnly />);
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Copy' })));
    expect(screen.getByRole('status')).toHaveTextContent('Clipboard is unavailable');
  });
});
