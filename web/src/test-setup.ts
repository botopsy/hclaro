import { configure } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { vi } from 'vitest';
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation(query => ({
    matches: true,
    media: query,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn()
  }))
});
// The first render in a file also loads the module graph; under a parallel `make test`
// that can exceed the 1 s default wait for findBy* queries.
configure({ asyncUtilTimeout: 4000 });
