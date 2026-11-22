// M19 brand: logo components, theme tokens and theme resolution.
/// <reference types="vite/client" />
import { render, screen, within } from '@testing-library/react';
import { HclaroLogo, HclaroMark, markSizes, type MarkSize } from './HclaroLogo';
import { applyTheme, resolveTheme, savedTheme } from './theme';

import { readFileSync } from 'node:fs';
import path from 'node:path';
import html from '../../index.html?raw';
import favicon from '../../public/favicon.svg?raw';

// The declarations of one exact selector, e.g. rule('.hclaro-cursor').
function rule(selector: string) {
  const start = css.indexOf(`\n${selector} {`);
  expect(start, selector).toBeGreaterThan(-1);
  const body = css.slice(css.indexOf('{', start) + 1, css.indexOf('}', start)).replace(/\/\*[\s\S]*?\*\//g, '');
  return Object.fromEntries(body.split(';').map(d => d.trim()).filter(Boolean).map(d => [d.slice(0, d.indexOf(':')).trim(), d.slice(d.indexOf(':') + 1).trim()]));
}

// Vitest replaces CSS imports, even ?raw ones, so the stylesheet is read from disk.
// Git may check it out with CRLF (Windows CI); the parser below expects LF.
// The entry only @imports the split files, so they are inlined in cascade order.
const read = (file: string) => readFileSync(path.join(import.meta.dirname, '..', file), 'utf8').replace(/\r\n/g, '\n');
const css = read('styles.css').replace(/^@import "\.\/(styles\/[\w-]+\.css)";.*$/gm, (_, file) => read(file));

const reference = { graphite: '#0E1116', paper: '#F6F7F9', ink: '#151A21', letter: '#E6E8EB', lime: '#C5F04A', olive: '#3F6212', onLime: '#10140A', edge: '#2A323D' };

// Every custom property of the leading :root block, resolved for one theme.
function tokens(theme: 'light' | 'dark') {
  const raw: Record<string, string> = rule(':root');
  const resolve = (value: string): string => {
    const pair = value.match(/^light-dark\((.+), (.+)\)$/);
    if (pair) return resolve(theme === 'light' ? pair[1] : pair[2]);
    const variable = value.match(/^var\((--[\w-]+)\)$/);
    if (variable) return resolve(raw[variable[1]]);
    return value.toUpperCase();
  };
  return (name: string) => resolve(raw[`--${name}`]);
}

function luminance(hex: string) {
  const [r, g, b] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
function contrast(a: string, b: string) {
  const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m);
  return (x + 0.05) / (y + 0.05);
}

describe('HclaroLogo', () => {
  it('renders the lowercase wordmark, the vector chevron and the hollow cursor with one accessible name', () => {
    render(<HclaroLogo />);
    const logo = screen.getByRole('img', { name: 'Hclaro' });
    expect(logo).toHaveTextContent(/^hclaro$/);
    // Decorative parts are hidden; the visible text is not announced a second time.
    expect(within(logo).queryAllByText('hclaro')[0]).toHaveAttribute('aria-hidden', 'true');
    const svg = logo.querySelector('svg.hclaro-chevron')!;
    expect(svg).toHaveAttribute('viewBox', '0 0 10 14');
    expect(svg).toHaveAttribute('aria-hidden', 'true');
    const line = svg.querySelector('polyline')!;
    expect(line).toHaveAttribute('points', '2,2 8,7 2,12');
    expect(line).toHaveAttribute('fill', 'none');
    expect(line).toHaveAttribute('stroke-width', '2.4');
    expect(line).toHaveAttribute('stroke-linecap', 'round');
    expect(line).toHaveAttribute('stroke-linejoin', 'round');
    expect(logo.querySelector('.hclaro-cursor')).toHaveAttribute('aria-hidden', 'true');
    // The chevron is geometry, not a ">" glyph.
    expect(logo.textContent).not.toContain('>');
  });

  it.each(['light', 'dark', 'inverse'] as const)('renders the %s reference variant', variant => {
    render(<HclaroLogo variant={variant} size={40} />);
    const logo = screen.getByRole('img', { name: 'Hclaro' });
    expect(logo).toHaveClass(`hclaro-${variant}`);
    expect(logo).toHaveAttribute('data-variant', variant);
    expect(logo.style.fontSize).toBe('40px');
  });

  it('pins each variant to the literal reference colours', () => {
    expect(rule('.hclaro-logo.hclaro-light')).toMatchObject({ color: 'var(--brand-ink)', '--logo-chevron-color': 'var(--brand-olive)', '--logo-cursor-color': 'var(--brand-ink)' });
    expect(rule('.hclaro-logo.hclaro-dark')).toMatchObject({ color: 'var(--brand-letter)', '--logo-chevron-color': 'var(--brand-lime)', '--logo-cursor-color': 'var(--brand-lime)' });
    expect(rule('.hclaro-logo.hclaro-inverse')).toMatchObject({ color: 'var(--brand-on-lime)', '--logo-chevron-color': 'var(--brand-on-lime)', '--logo-cursor-color': 'var(--brand-on-lime)' });
    // The theme-following logo resolves to the light and dark reference variants.
    for (const [theme, text, chevron, cursor] of [['light', reference.ink, reference.olive, reference.ink], ['dark', reference.letter, reference.lime, reference.lime]] as const) {
      const t = tokens(theme);
      expect([t('logo-text'), t('logo-chevron'), t('logo-cursor')]).toEqual([text, chevron, cursor]);
    }
  });

  it('keeps the reference geometry in em units', () => {
    expect(rule('.hclaro-logo, .hclaro-mark-glyph')).toMatchObject({ 'font-weight': '700', 'letter-spacing': '-.04em', 'line-height': '1' });
    expect(rule('.hclaro-logo, .hclaro-mark-glyph')['font-family']).toMatch(/^"JetBrains Mono", .*monospace$/);
    // Baseline-aligned like the reference; Tailwind's preflight would otherwise centre the svg.
    expect(rule('.hclaro-chevron')).toMatchObject({ 'vertical-align': 'baseline', width: '.5em', height: '.7em', 'margin-right': '.22em' });
    // Hollow: a border and no fill, and never a blinking animation.
    const cursor = rule('.hclaro-cursor');
    expect(cursor).toMatchObject({ border: '.09em solid var(--logo-cursor-color)', 'border-radius': '.06em', width: '.52em', height: '.73em', 'margin-left': '.1em' });
    expect(cursor).not.toHaveProperty('background');
    expect(cursor).not.toHaveProperty('animation');
    expect(css).toMatch(/\* \{\n\s+box-sizing: border-box;/);
  });

  it('can be decorative inside a control that names it', () => {
    const { container } = render(<a href="#" aria-label="Hclaro overview"><HclaroLogo decorative /></a>);
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
    expect(container.querySelector('.hclaro-logo')).toHaveAttribute('aria-hidden', 'true');
    expect(screen.getByRole('link')).toHaveAccessibleName('Hclaro overview');
  });

  it('omits the cursor for the secondary lockup only when asked', () => {
    render(<HclaroLogo cursor={false} />);
    expect(screen.getByRole('img', { name: 'Hclaro' }).querySelector('.hclaro-cursor')).toBeNull();
  });
});

describe('HclaroMark', () => {
  it.each(Object.entries(markSizes).map(([size, font]) => [Number(size) as MarkSize, font]))('renders the %ipx app mark at the reference proportions', (size, font) => {
    render(<HclaroMark size={size} />);
    const mark = screen.getByRole('img', { name: 'Hclaro' });
    expect(mark.style.width).toBe(`${size}px`);
    expect(mark.style.height).toBe(`${size}px`);
    expect(mark.style.borderRadius).toBe(`${size / 4}px`);
    expect(mark.style.fontSize).toBe(`${font}px`);
    // Lowercase h and the hollow cursor, both hidden behind the single accessible name.
    const glyph = mark.querySelector('.hclaro-mark-glyph')!;
    expect(glyph).toHaveTextContent(/^h$/);
    expect(glyph).toHaveAttribute('aria-hidden', 'true');
    expect(glyph.querySelector('.hclaro-cursor')).not.toBeNull();
  });

  it('uses the graphite tile with its edge, light letter and lime cursor', () => {
    expect(rule('.hclaro-mark')).toMatchObject({ background: 'var(--brand-graphite)', color: 'var(--brand-letter)', '--logo-cursor-color': 'var(--brand-lime)', 'box-shadow': 'inset 0 0 0 1px var(--brand-edge)' });
  });
});

describe('theme tokens', () => {
  it('declares the brand colours literally, once', () => {
    const root = rule(':root');
    expect(['graphite', 'paper', 'ink', 'letter', 'lime', 'olive', 'on-lime', 'edge'].map(name => root[`--brand-${name}`])).toEqual(Object.values(reference));
    for (const hex of Object.values(reference)) expect(css.match(new RegExp(hex, 'gi'))).toHaveLength(1);
  });

  it('builds both themes on graphite + lime', () => {
    const light = tokens('light'), dark = tokens('dark');
    expect([light('bg'), light('text'), light('accent'), light('primary'), light('primary-foreground')]).toEqual([reference.paper, reference.ink, reference.olive, reference.lime, reference.onLime]);
    expect([dark('bg'), dark('text'), dark('accent'), dark('primary'), dark('primary-foreground')]).toEqual([reference.graphite, reference.letter, reference.lime, reference.lime, reference.onLime]);
  });

  // Every pair here is documented with its ratio in docs/design/BRAND.md.
  const surfaces = ['bg', 'sidebar', 'surface', 'surface-hover', 'surface-selected', 'inset'];
  it.each(['light', 'dark'] as const)('meets WCAG AA for text and controls in the %s theme', theme => {
    const t = tokens(theme);
    for (const fg of ['text', 'muted', 'subtle', 'accent-text', 'read', 'create', 'update', 'replace', 'destroy']) {
      for (const bg of surfaces) expect(contrast(t(fg), t(bg)), `${fg} on ${bg}`).toBeGreaterThanOrEqual(4.5);
    }
    expect(contrast(t('primary-foreground'), t('primary'))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t('primary-foreground'), t('primary-hover'))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t('destructive-foreground'), t('destructive')), 'destructive button label').toBeGreaterThanOrEqual(4.5);
    // Each semantic colour as text on its own tint (chips, banners).
    for (const tone of ['create', 'update', 'replace', 'destroy']) expect(contrast(t(tone), t(`${tone}-bg`)), `${tone} on its tint`).toBeGreaterThanOrEqual(4.5);
    expect(contrast(t('accent-text'), t('accent-dim'))).toBeGreaterThanOrEqual(4.5);
    // Focus rings, accents and form-control boundaries are non-text UI: 3:1.
    for (const bg of surfaces) {
      expect(contrast(t('ring'), t(bg)), `ring on ${bg}`).toBeGreaterThanOrEqual(3);
      expect(contrast(t('accent'), t(bg)), `accent on ${bg}`).toBeGreaterThanOrEqual(3);
      expect(contrast(t('ok'), t(bg)), `ok dot on ${bg}`).toBeGreaterThanOrEqual(3);
    }
    for (const bg of ['bg', 'surface', 'inset']) expect(contrast(t('input'), t(bg)), `input on ${bg}`).toBeGreaterThanOrEqual(3);
  });

  it('keeps plan statuses distinct from the brand accent', () => {
    for (const theme of ['light', 'dark'] as const) {
      const t = tokens(theme);
      const statuses = ['create', 'update', 'replace', 'destroy'].map(t);
      expect(statuses).not.toContain(t('accent'));
      expect(statuses).not.toContain(t('accent-text'));
      expect(new Set(statuses).size).toBe(4);
    }
  });

  it('leaves no old brand colours or hardcoded component colours behind', () => {
    for (const old of ['#0b1220', '#f4f4f0', '#e8e8e1', '#c6f432', '#6a8f00', '#a3e8bc', '#14211b']) expect(css.toLowerCase()).not.toContain(old);
    // Every colour lives in tokens.css; no other stylesheet has a raw hex value.
    expect(css.replace(read('styles/tokens.css'), '').match(/#[0-9a-fA-F]{3,8}\b/g)).toBeNull();
    expect(read('brand/theme.ts').match(/#[0-9a-fA-F]{3,8}\b/g)).toBeNull();
  });

  it('follows the system palette before any script runs', () => {
    expect(rule(':root')['color-scheme']).toBe('light dark');
    expect(rule(':root[data-theme="light"]')).toEqual({ 'color-scheme': 'light' });
    expect(rule(':root[data-theme="dark"]')).toEqual({ 'color-scheme': 'dark' });
  });
});

describe('theme preference', () => {
  afterEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.theme;
  });

  it('reads only a valid saved theme', () => {
    expect(savedTheme()).toBe('system');
    localStorage.setItem('hclaro.preferences', JSON.stringify({ theme: 'light' }));
    expect(savedTheme()).toBe('light');
    localStorage.setItem('hclaro.preferences', JSON.stringify({ theme: 'neon' }));
    expect(savedTheme()).toBe('system');
    localStorage.setItem('hclaro.preferences', '{not json');
    expect(savedTheme()).toBe('system');
  });

  it('resolves system from the media query and pins explicit choices', () => {
    // The test setup reports prefers-color-scheme: dark.
    expect(resolveTheme('system')).toBe('dark');
    expect(resolveTheme('light')).toBe('light');
  });

  it('sets the document theme and the browser chrome colour', () => {
    document.head.innerHTML = '<meta name="theme-color" content="#F6F7F9" media="(prefers-color-scheme: light)"><meta name="theme-color" content="#0E1116" media="(prefers-color-scheme: dark)">';
    applyTheme('light');
    expect(document.documentElement.dataset.theme).toBe('light');
    expect([...document.querySelectorAll('meta[name="theme-color"]')].map(m => m.getAttribute('content'))).toEqual(['#F6F7F9', '#F6F7F9']);
    applyTheme('dark');
    expect([...document.querySelectorAll('meta[name="theme-color"]')].map(m => m.getAttribute('content'))).toEqual(['#0E1116', '#0E1116']);
  });
});

describe('document entry point', () => {
  it('carries the new icon set, title and theme colours, and no external or mockup assets', () => {
    expect(html).toContain('<title>Hclaro · Local infrastructure workspace</title>');
    expect(html).toContain('href="/favicon.svg"');
    expect(html).toContain('href="/favicon-32.png"');
    expect(html).toContain('href="/favicon-16.png"');
    expect(html).toContain('rel="apple-touch-icon"');
    // Filled from tokens.css by the brand-colours Vite plugin (internal/ui/embed_test.go checks the built page).
    expect(html).toContain('<meta name="theme-color" content="__brand-graphite__" media="(prefers-color-scheme: dark)"/>');
    expect(html).toContain('<meta name="theme-color" content="__brand-paper__" media="(prefers-color-scheme: light)"/>');
    expect(html).not.toMatch(/https?:\/\//);
    expect(html).not.toMatch(/support\.js|vendor\/react|x-dc|fonts\.googleapis/);
    // The static splash has no inline style attribute: the CSP forbids them.
    expect(html).not.toMatch(/style="/);
  });

  it('ships the hollow-cursor favicon, not the old H mark', () => {
    expect(favicon).toContain(`fill="${reference.graphite}"`);
    expect(favicon).toContain(`fill="${reference.letter}"`);
    expect(favicon).toContain(`fill="${reference.lime}"`);
    expect(favicon).toContain('fill-rule="evenodd"');
    expect(favicon).not.toMatch(/#a3e8bc|#14211b|<text/i);
  });
});
