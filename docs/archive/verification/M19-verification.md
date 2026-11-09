# M19 implementation and verification

**Status: draft.** The hollow-cursor identity from the supplied reference mockup is implemented in the real UI: a centralized light/dark token layer built on the five literal reference colours, reusable `HclaroLogo`/`HclaroMark` components, the sidebar (expanded and collapsed), a static loading mark, the Hub first-run state, a Settings "About" row, favicons and theme colour. Design documentation: [design/BRAND.md](../../design/BRAND.md).

M19 is independent of M18 (state refactoring studio), which is not on `master`; nothing here depends on it.

## Scope decisions

- **Reference kept as documentation only.** `docs/design/reference/` holds `Cursor.dc.html` and its README. The preview runtime (`support.js`, `vendor/react*.js`) was not copied, and nothing from the mockup is imported by the app; `internal/ui/embed_test.go` fails if it ever reaches the embedded bundle.
- **Existing token names kept.** The stylesheet already used `--bg`, `--surface`, `--text`, `--accent`… everywhere, so those names stay; `--primary`, `--primary-foreground`, `--primary-hover`, `--ring`, `--input`, `--accent-foreground` and `--logo-*` were added. BRAND.md maps them to shadcn names.
- **`light-dark()` instead of duplicated theme blocks.** Each token is declared once for both themes. `color-scheme: light dark` follows the OS before JavaScript runs; the saved preference is applied in `main.tsx` before React renders. This removes the previous dark-first flash for light-system users. Tailwind's Lightning CSS compiles `light-dark()` to a variable fallback.
- **Light primary is navy, not lime.** Lime on ivory has a 1.16:1 edge, so primary buttons are navy/ivory in light mode and lime/navy in dark mode.
- **Olive is graphics-only in light mode.** `#6A8F00` stays the chevron and accent marker colour (3.44:1); accent-coloured text and focus rings use the derived `#4A6600`.
- **Form controls gained a 3:1 boundary** (`--input`) for selects, search and text inputs. Other borders remain decorative separators as before.
- **No manifest.** The server's CSP (`default-src 'none'`, no `manifest-src`) would block one; adding it would require a security-header change outside this milestone. The 192/512 px icons are in `docs/design/assets/` for packagers.
- **Font.** `@fontsource/jetbrains-mono` 5.2.8 (OFL-1.1), Latin 700 subset only, bundled by Vite and embedded in the binary; `assetsInlineLimit: 0` keeps fonts out of `data:` URIs that `font-src 'self'` would block. Added to `THIRD-PARTY-NOTICES.txt`. Only the logo uses it; the interface typography is unchanged.
- **Vector icons.** Favicons/app icons do not depend on a font: `scripts/brand-assets.mjs` holds the JetBrains Mono Bold outlines of `hclaro` (extracted once with fontTools) and the reference geometry, and renders PNGs offline with the test Chromium. The 16 px favicon is pixel-snapped because the exact 0.9 px border blurs into a filled block.

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.3, Node 24.2, Playwright Chromium 1208. No network, credential or real engine was used; screenshots came from `make demo-hub` (synthetic projects and fake engine) with every non-loopback request aborted (none were attempted).

| Check | Result |
| --- | --- |
| `make test` (`go test -race`, Vitest) | Passed: all Go packages with `-race`; Vitest 146/146 in 17 files. |
| New UI tests | `web/src/brand/Brand.test.tsx` (25): wordmark text, single accessible name, `aria-hidden` decorative parts, chevron SVG attributes (`viewBox`, points, stroke 2.4, round caps/joins, no `>` glyph), cursor present/omitted; light/dark/inverse classes and literal colours; em geometry of chevron and cursor (incl. baseline alignment, unfilled, no animation); decorative mode; `HclaroMark` at 88/64/40/24 with reference radius and letter size; raw palette declared exactly once; both themes resolve to navy/ivory/stone/lime/olive; **WCAG AA for 9 text tokens × 6 surfaces per theme, filled-control pairs, and 3:1 for ring/accent/input**; statuses distinct from the accent; no old brand colours and no stray hex outside tokens; system palette before script; saved-theme parsing, system resolution, `data-theme` and `theme-color`; `index.html` icons/title/no external or mockup references/no inline styles; favicon colours. `web/src/App.test.tsx` (+1): sidebar brand link has one accessible name, decorative wordmark and compact mark, old three-bar mark gone; Settings shows the logo. |
| New Go test | `internal/ui/embed_test.go` `TestEmbeddedBrandAssetsAreLocal`: favicons and touch icon embedded, favicon is the new mark, a font is embedded, every stylesheet `url()` resolves to an embedded `/assets/` file, no `support.js`/`react-dom.js`/`Cursor.dc`/`DCLogic`/Google Fonts in the bundle. |
| Existing tests | All M01–M17 Go, Vitest and browser tests pass unchanged. |
| `make lint`, `GOOS=windows go vet`, `gofmt -l` | Passed / clean. |
| `make build`, `make browser-test` | Passed (including themes, tablet layout and *no external requests*). Its screenshots in `docs/screenshots/` were regenerated by the same run with the new brand. |
| Reference fidelity | The components rendered by the **built** stylesheet, laid out like the 720 × 540 artboard (`docs/design/screenshots/brand-sheet.png`), differ from the reference rendered with the same font in 0.47 % of pixels (anti-aliasing). The first pass found the chevron 5 px low (Tailwind preflight sets `svg { vertical-align: middle }`); fixed with `vertical-align: baseline` and covered by a test. |
| Small sizes | 24 px mark: cursor box 7.8 × 10.9 px with a hollow centre at 1× and 2×. Favicon 16/32 px and touch icon inspected magnified; 16 px keeps a 1 px ring with a 3 × 6 px hollow. |
| Live loopback run | `make demo-hub`: title *Hclaro · Local infrastructure workspace*, icon `/favicon.svg`, `document.fonts.check('700 20px "JetBrains Mono"')` true, no non-loopback request. Hub, project, overview, resources, settings (with keyboard focus ring) and collapsed sidebar captured in light and dark in `docs/design/screenshots/`. |
| Behaviour unchanged | No Go code outside `internal/ui/embed_test.go` changed; no route, API, CLI, engine, security header, redaction or confirmation logic was touched. |

## Known limitations

- Plan review, apply, import, replace, drift, discovery and cost screens were verified in **dark** mode by the browser suite and only through token contrast tests in light mode; Hub workspaces disable those flows, so the light screenshot run did not reach them.
- Screenshots in `docs/screenshots/` that `make browser-test` does not produce (manual captures from M07–M16, such as `hub-light.png`, `graph-full-light.png`, `compare-*.png`, `history-*.png`) still show the previous brand. They are historical evidence and not referenced from the README.
- `light-dark()` needs Chrome 123, Safari 17.5 or Firefox 120 natively; older engines get the Lightning CSS fallback, which assumes `prefers-color-scheme` support.
- Decorative borders (`--border`, about 1.4–1.5:1) are unchanged in role; they never carry meaning alone.
- The touch icon has rounded corners baked in; iOS applies its own mask on top.

## Manual verification

1. `make demo-hub` and open the printed URL. The tab shows the navy `h` + lime hollow-cursor icon; the sidebar shows `> hclaro▯` with *ALPHA*.
2. Toggle the theme (top-right) and **Settings → Appearance**: light uses ivory/stone surfaces, navy text and primary buttons, olive chevron; dark uses navy, ivory text, lime accents and lime primary buttons. Choose **System** and switch the OS appearance: the app follows.
3. Reload with a saved **light** theme on a dark OS (and vice versa): no flash of the other palette.
4. Narrow the window below 900 px: the sidebar collapses to the 40 px `h` mark.
5. Tab through the sidebar, settings and a dialog: focus rings are visible (dark olive in light mode, lime in dark mode).
6. In `make demo`, open **Plans → Run plan** and the **Review and apply…** dialog in both themes: create/update/delete use green/amber/red with icons and words, and destructive buttons remain red, never lime.
7. With the network disabled, reload: logo and icons render identically (the font and icons are embedded).
8. A screen reader announces the sidebar link as "Hclaro overview" once, and the Settings logo as "Hclaro".
