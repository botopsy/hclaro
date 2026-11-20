import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { engineLabel } from './format';
import { errorMessage } from './errorMessage';
import { readJSON, readStorage, storageKeys, writeStorage } from './storage';
import { useDebounced } from './useDebounced';

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  sessionStorage.clear();
});

describe('shared lib', () => {
  it('treats blocked storage as empty and drops writes', () => {
    writeStorage('session', storageKeys.profile('a'.repeat(32)), 'none');
    expect(readStorage('session', storageKeys.profile('a'.repeat(32)))).toBe('none');
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    expect(readStorage('session', storageKeys.compareId)).toBeUndefined();
    expect(() => writeStorage('local', storageKeys.preferences, '{}')).not.toThrow();
    expect(readJSON('local', storageKeys.preferences)).toBeNull();
  });

  it('reads malformed JSON as undefined', () => {
    sessionStorage.setItem(storageKeys.compareDraft, '{not json');
    expect(readJSON('session', storageKeys.compareDraft)).toBeUndefined();
  });

  it('settles a debounced value only after it stops changing', () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(({ v }) => useDebounced(v, 100), { initialProps: { v: 'a' } });
    rerender({ v: 'ab' });
    act(() => vi.advanceTimersByTime(60));
    rerender({ v: 'abc' });
    act(() => vi.advanceTimersByTime(60));
    expect(result.current).toBe('a');
    act(() => vi.advanceTimersByTime(40));
    expect(result.current).toBe('abc');
  });

  it('labels engines and errors with one fallback each', () => {
    expect([engineLabel('tofu'), engineLabel('terraform'), engineLabel(''), engineLabel()]).toEqual(['OpenTofu', 'Terraform', 'The engine', 'The engine']);
    expect(errorMessage(new Error('refused'))).toBe('refused');
    expect(errorMessage('x', 'fallback')).toBe('fallback');
  });
});
