// Hclaro brand components (M19). Geometry follows the reference mockup in
// docs/design/reference/Cursor.dc.html exactly; docs/design/BRAND.md explains each value.
// Sizes are em-based, so a logo scales with its font-size and keeps its proportions.
import type { CSSProperties } from 'react';

// "auto" follows the active light/dark theme; the others pin one reference variant.
export type LogoVariant = 'auto' | 'light' | 'dark' | 'inverse';

type Accessible = {
  // The accessible name. Pass decorative when a surrounding control already names it.
  label?: string;
  decorative?: boolean;
  className?: string;
};

function a11y({ label = 'Hclaro', decorative }: Accessible) {
  return decorative ? { 'aria-hidden': true as const } : { role: 'img' as const, 'aria-label': label };
}

// The terminal-style chevron: viewBox 0 0 10 14, points 2,2 8,7 2,12, stroke 2.4, round caps.
function Chevron() {
  return <svg className="hclaro-chevron" viewBox="0 0 10 14" aria-hidden="true" focusable="false"><polyline points="2,2 8,7 2,12" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" /></svg>;
}

// An unfilled, static cursor rectangle: 0.52em × 0.73em, 0.09em border, 0.06em radius.
function Cursor() {
  return <span className="hclaro-cursor" aria-hidden="true" />;
}

// The primary wordmark: chevron, lowercase "hclaro" and the hollow cursor.
export function HclaroLogo({ variant = 'auto', size, cursor = true, className, ...rest }: Accessible & {
  variant?: LogoVariant;
  // Font size in px; omit to inherit the surrounding font size.
  size?: number;
  // The reference shows a secondary lockup without the cursor under the app icon.
  cursor?: boolean;
}) {
  const style: CSSProperties | undefined = size ? { fontSize: size } : undefined;
  return <span className={['hclaro-logo', `hclaro-${variant}`, className].filter(Boolean).join(' ')} style={style} data-variant={variant} {...a11y(rest)}><Chevron /><span className="hclaro-word" aria-hidden="true">hclaro</span>{cursor && <Cursor />}</span>;
}

// Reference app-icon containers and the letter size inside each (88/54, 64/40, 40/25, 24/15).
export const markSizes = { 88: 54, 64: 40, 40: 25, 24: 15 } as const;
export type MarkSize = keyof typeof markSizes;

// The compact mark for narrow layouts: lowercase "h" and the lime hollow cursor on navy.
export function HclaroMark({ size = 40, className, ...rest }: Accessible & { size?: MarkSize }) {
  const style: CSSProperties = { width: size, height: size, borderRadius: size / 4, fontSize: markSizes[size] };
  return <span className={['hclaro-mark', className].filter(Boolean).join(' ')} style={style} data-size={size} {...a11y(rest)}><span className="hclaro-mark-glyph" aria-hidden="true">h<Cursor /></span></span>;
}
