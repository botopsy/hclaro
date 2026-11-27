// G2 accessibility guard: eslint-plugin-jsx-a11y does not support ESLint 10, so clickable
// non-interactive elements are caught by scanning the sources.
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap(e => {
    const p = path.join(dir, e.name);
    return e.isDirectory() ? sources(p) : /\.tsx$/.test(e.name) && !/\.test\./.test(e.name) ? [p] : [];
  });
}

describe('no clickable divs', () => {
  // data-table-clickable: a row click is a mouse shortcut for the row's own Expand button, which stays the keyboard path.
  it('puts onClick only on real controls, or on decorative aria-hidden backdrops that Escape also closes, or combobox options driven from the input', () => {
    const found: string[] = [];
    const tag = /<(div|span|li|tr|td|p|section|article|img|svg)\b((?:[^<>]|=>)*?)\bonClick\b/g;
    for (const file of sources(import.meta.dirname)) {
      const text = readFileSync(file, 'utf8');
      for (const m of text.matchAll(tag)) if (!/aria-hidden="true"|role="option"|data-table-clickable/.test(m[2])) found.push(`${path.relative(import.meta.dirname, file)}: <${m[1]}>`);
    }
    expect(found).toEqual([]);
  });
});
