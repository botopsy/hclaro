// Renders docs/design/screenshots/brand-sheet.png from the BUILT stylesheet (internal/ui/dist), so the
// sheet shows exactly what ships. Run `npm --prefix web run build` first, then `node scripts/brand-sheet.mjs`.
// The markup mirrors HclaroLogo/HclaroMark (web/src/brand/HclaroLogo.tsx); Brand.test.tsx covers the components.
import fs from 'node:fs/promises'
import path from 'node:path'
import {pathToFileURL, fileURLToPath} from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const dist = path.join(root, 'internal/ui/dist')
const css = (await fs.readdir(path.join(dist, 'assets'))).find(file => /^index-.*\.css$/.test(file))
if (!css) throw new Error('internal/ui/dist has no built stylesheet; run npm --prefix web run build')

const logo = (variant, size, cursor = true) => `<span class="hclaro-logo hclaro-${variant}" style="font-size:${size}px"><svg class="hclaro-chevron" viewBox="0 0 10 14"><polyline points="2,2 8,7 2,12" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/></svg><span class="hclaro-word">hclaro</span>${cursor ? '<span class="hclaro-cursor"></span>' : ''}</span>`
const markSizes = {88: 54, 64: 40, 40: 25, 24: 15}
const mark = (size, scale = 2) => `<span class="hclaro-mark" style="width:${size * scale}px;height:${size * scale}px;border-radius:${size * scale / 4}px;font-size:${markSizes[size] * scale}px"><span class="hclaro-mark-glyph">h<span class="hclaro-cursor"></span></span></span>`

const html = `<!doctype html><html data-theme="light"><head><link rel="stylesheet" href="/assets/${css}"><style>
body{margin:0;width:1440px;height:1080px;display:grid;grid-template-columns:repeat(6,240px);grid-template-rows:600px 480px}
.p{display:flex;align-items:center;justify-content:center;gap:32px}
.wide{grid-column:span 3}.third{grid-column:span 2;flex-direction:column}
.paper{background:var(--brand-paper)}.graphite{background:var(--brand-graphite)}.lime{background:var(--brand-lime)}.white{background:var(--surface)}
</style></head><body>
<div class="p wide paper">${logo('light', 120)}</div>
<div class="p wide graphite">${logo('dark', 120)}</div>
<div class="p third white">${mark(88)}${logo('light', 60, false)}</div>
<div class="p third paper" style="flex-direction:row;align-items:flex-end;padding-bottom:176px;box-sizing:border-box">${mark(64)}${mark(40)}${mark(24)}</div>
<div class="p third lime">${logo('inverse', 80)}</div>
</body></html>`

const {chromium} = await import(pathToFileURL(path.join(root, 'web/node_modules/playwright/index.mjs')))
const browser = await chromium.launch({headless: true, args: ['--disable-background-networking', '--disable-component-update']})
try {
 const page = await browser.newPage({viewport: {width: 1440, height: 1080}, deviceScaleFactor: 1})
 // Offline: the page and the built assets are served from disk; anything else is refused.
 await page.route('**/*', async route => {
  const url = new URL(route.request().url())
  if (url.host !== 'sheet.local') return route.abort()
  if (url.pathname === '/') return route.fulfill({contentType: 'text/html', body: html})
  const file = path.join(dist, path.normalize(url.pathname))
  if (!file.startsWith(dist + path.sep)) return route.abort()
  return route.fulfill({path: file})
 })
 await page.goto('http://sheet.local/')
 await page.evaluate(() => document.fonts.ready)
 await page.screenshot({path: path.join(root, 'docs/design/screenshots/brand-sheet.png')})
} finally {
 await browser.close()
}
