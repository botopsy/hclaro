// Generates Hclaro's static brand assets (M19) from the reference geometry in
// docs/design/reference/Cursor.dc.html. Run with `make brand-assets`; the output is committed.
//
// The browser UI renders the logo as live text (HclaroLogo/HclaroMark). Favicons, app icons and
// the SVGs in docs/design/assets cannot rely on a web font, so here every glyph is a vector path.
// The outlines are JetBrains Mono Bold (SIL OFL 1.1, @fontsource/jetbrains-mono, latin 700),
// extracted once with fontTools: 1000 units per em, y up, baseline at 0, advance 600.
// PNGs are rendered offline with the Playwright Chromium the repository already uses for tests.
import fs from 'node:fs/promises'
import path from 'node:path'
import {pathToFileURL, fileURLToPath} from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

// The five literal reference colours. Nothing in this file derives or approximates them.
export const brand = {navy: '#0B1220', ivory: '#F4F4F0', stone: '#E8E8E1', lime: '#C6F432', olive: '#6A8F00'}

const glyphs = {
 h: 'M77 0V730H202V550L199 445H201Q208 493 239 522Q278 560 347 560Q428 560 476.5 506.0Q525 452 525 361V0H400V348Q400 398 374.0 425.0Q348 452 301 452Q255 452 228.5 424.0Q202 396 202 344V0Z',
 c: 'M303 -10Q233 -10 180.5 16.5Q128 43 99.0 91.5Q70 140 70 206V344Q70 411 99.0 459.0Q128 507 180.5 533.5Q233 560 303 560Q407 560 469.0 506.5Q531 453 534 361H409Q406 404 378.5 427.5Q351 451 303 451Q253 451 224.0 423.5Q195 396 195 345V206Q195 155 224.0 127.0Q253 99 303 99Q351 99 378.5 122.5Q406 146 409 189H534Q531 97 469.0 43.5Q407 -10 303 -10Z',
 l: 'M384 0Q330 0 289.5 22.0Q249 44 226.0 84.5Q203 125 203 178V617H27V730H328V178Q328 148 345.5 130.5Q363 113 393 113H559V0Z',
 a: 'M239 -10Q154 -10 105.0 35.0Q56 80 56 156Q56 237 110.0 281.0Q164 325 263 325H400V372Q400 412 374.0 434.5Q348 457 303 457Q262 457 235.0 439.0Q208 421 203 390H81Q90 468 151.0 514.0Q212 560 307 560Q408 560 466.5 509.5Q525 459 525 373V0H404V96H402Q394 54 359 26Q314 -10 239 -10ZM280 84Q333 84 366.5 111.0Q400 138 400 181V248H265Q227 248 204.0 226.0Q181 204 181 168Q181 129 207.5 106.5Q234 84 280 84Z',
 r: 'M94 0V550H212V445Q222 488 246 515Q286 560 364 560Q453 560 504.0 505.0Q555 450 555 353V313H424V344Q424 397 396.5 426.0Q369 455 321 455Q272 455 245.5 426.0Q219 397 219 344V0Z',
 o: 'M300 -9Q230 -9 178.5 17.5Q127 44 98.5 92.5Q70 141 70 207V343Q70 409 98.5 457.5Q127 506 178.5 532.5Q230 559 300 559Q371 559 422.0 532.5Q473 506 501.5 457.5Q530 409 530 344V207Q530 141 501.5 92.5Q473 44 422.0 17.5Q371 -9 300 -9ZM300 100Q350 100 377.5 127.5Q405 155 405 207V343Q405 396 377.5 423.0Q350 450 300 450Q251 450 223.0 423.0Q195 396 195 343V207Q195 155 223.0 127.5Q251 100 300 100Z'
}

// Reference geometry in em (font-size 1em, letter-spacing -0.04em, line-height 1).
const em = 1000, advance = 600, tracking = -40
const chevron = {width: 500, height: 700, gap: 220, stroke: 2.4 * 50} // viewBox 0 0 10 14 drawn at 0.5em × 0.7em
const cursor = {gap: 100, width: 520, height: 730, border: 90, radius: 60}
// The ascent/descent of the font put the baseline 0.86em below the top of a line-height: 1 box.
const baselineInLineBox = 860
const round = n => Math.round(n * 100) / 100

// A border-box rectangle with a 0.09em border and 0.06em outer radius: an even-odd ring.
function cursorRing(x, baseline) {
 const {width: w, height: h, border: b, radius: r} = cursor
 const top = baseline - h
 const outer = `M${x + r} ${top}H${x + w - r}Q${x + w} ${top} ${x + w} ${top + r}V${baseline - r}Q${x + w} ${baseline} ${x + w - r} ${baseline}H${x + r}Q${x} ${baseline} ${x} ${baseline - r}V${top + r}Q${x} ${top} ${x + r} ${top}Z`
 const inner = `M${x + b} ${top + b}V${baseline - b}H${x + w - b}V${top + b}Z`
 return outer + inner
}

// Text glyphs are placed left to right with the reference tracking; y is flipped to SVG space.
function glyphRun(text, x, baseline, fill) {
 return [...text].map((ch, i) => `<path fill="${fill}" transform="translate(${x + i * (advance + tracking)} ${baseline}) scale(1 -1)" d="${glyphs[ch]}"/>`).join('')
}

function chevronPath(x, baseline, stroke) {
 const s = chevron.width / 10, top = baseline - chevron.height
 const points = [[2, 2], [8, 7], [2, 12]].map(([px, py]) => `${x + px * s},${top + py * s}`).join(' ')
 return `<polyline points="${points}" fill="none" stroke="${stroke}" stroke-width="${chevron.stroke}" stroke-linecap="round" stroke-linejoin="round"/>`
}

const wordmarkColours = {
 light: {background: brand.ivory, text: brand.navy, chevron: brand.olive, cursor: brand.navy},
 dark: {background: brand.navy, text: brand.ivory, chevron: brand.lime, cursor: brand.lime},
 inverse: {background: brand.lime, text: brand.navy, chevron: brand.navy, cursor: brand.navy}
}

// The full wordmark: chevron, "hclaro", hollow cursor. Transparent unless a background is asked for.
export function wordmarkSVG(variant, {background = false} = {}) {
 const c = wordmarkColours[variant]
 const baseline = 0, textX = chevron.width + chevron.gap
 const cursorX = textX + 6 * (advance + tracking) + cursor.gap
 const width = cursorX + cursor.width
 const pad = 60, top = -cursor.height - pad, height = cursor.height + 10 + pad * 2
 const body = chevronPath(0, baseline, c.chevron) + glyphRun('hclaro', textX, baseline, c.text) + `<path fill="${c.cursor}" fill-rule="evenodd" d="${cursorRing(cursorX, baseline)}"/>`
 const bg = background ? `<rect x="${-pad}" y="${top}" width="${width + pad * 2}" height="${height}" fill="${c.background}"/>` : ''
 return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${-pad} ${top} ${width + pad * 2} ${height}" role="img" aria-label="Hclaro"><title>Hclaro</title>${bg}${body}</svg>\n`
}

// The compact app mark: an ivory "h" and a lime hollow cursor on a navy rounded square.
// Reference containers 88/64/40/24 px use font sizes 54/40/25/15 and radius = size / 4; the
// icons below use the 64/40/24 ratio (0.625) so every raster size has identical proportions.
export function markSVG({fontRatio = 0.625, label = true} = {}) {
 const size = em / fontRatio, radius = size / 4
 const content = advance + tracking + cursor.gap + cursor.width
 const x = (size - content) / 2
 const baseline = (size - em) / 2 + baselineInLineBox
 const title = label ? ' role="img" aria-label="Hclaro"><title>Hclaro</title' : ' aria-hidden="true"'
 return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${round(size)} ${round(size)}"${title}><rect width="${round(size)}" height="${round(size)}" rx="${round(radius)}" fill="${brand.navy}"/>${glyphRun('h', round(x), round(baseline), brand.ivory)}<path fill="${brand.lime}" fill-rule="evenodd" d="${cursorRing(round(x + advance + tracking + cursor.gap), round(baseline))}"/></svg>\n`
}

// Favicons and the touch icon ship in the embedded UI. The 192/512 app icons are kept with the
// docs for packagers: the local server's CSP (default-src 'none') deliberately loads no manifest.
// At 16 px the 0.09em border is under one pixel and the cursor smears into a filled block, so
// the 16 px favicon snaps the same shapes to the pixel grid: 1 px ring, 3 × 6 px hollow, h on 4–12.
export function markSVG16() {
 const scale = 0.011, x = 3 - 77 * scale
 return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" aria-hidden="true"><rect width="16" height="16" rx="4" fill="${brand.navy}"/><path fill="${brand.ivory}" transform="translate(${round(x)} 12) scale(${scale} -${scale})" d="${glyphs.h}"/><path fill="${brand.lime}" fill-rule="evenodd" d="M9.5 4H13.5Q14 4 14 4.5V11.5Q14 12 13.5 12H9.5Q9 12 9 11.5V4.5Q9 4 9.5 4ZM10 5V11H13V5Z"/></svg>\n`
}

const rasters = [['web/public/favicon-16.png', 16], ['web/public/favicon-32.png', 32], ['web/public/apple-touch-icon.png', 180], ['docs/design/assets/hclaro-icon-192.png', 192], ['docs/design/assets/hclaro-icon-512.png', 512]]

async function main() {
 const pub = path.join(root, 'web/public'), docs = path.join(root, 'docs/design/assets')
 await fs.mkdir(docs, {recursive: true})
 const mark = markSVG()
 await fs.writeFile(path.join(pub, 'favicon.svg'), mark)
 await fs.writeFile(path.join(docs, 'hclaro-mark.svg'), mark)
 for (const variant of Object.keys(wordmarkColours)) {
  await fs.writeFile(path.join(docs, `hclaro-wordmark-${variant}.svg`), wordmarkSVG(variant, {background: true}))
 }
 if (process.argv.includes('--no-png')) return
 const modules = path.join(root, 'web/node_modules')
 const {chromium} = await import(pathToFileURL(path.join(modules, 'playwright/index.mjs')))
 const browser = await chromium.launch({headless: true, args: ['--disable-background-networking', '--disable-component-update']})
 try {
  const page = await browser.newPage({deviceScaleFactor: 1})
  // Offline by construction: the SVG is inlined and every other request is refused.
  await page.route('**/*', route => route.abort())
  for (const [name, size] of rasters) {
   await page.setViewportSize({width: size, height: size})
   const svg = size === 16 ? markSVG16() : mark
   await page.setContent(`<!doctype html><html><body style="margin:0;background:transparent">${svg.replace('<svg ', `<svg width="${size}" height="${size}" `)}</body></html>`)
   await page.locator('svg').screenshot({path: path.join(root, name), omitBackground: true})
  }
 } finally {
  await browser.close()
 }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
