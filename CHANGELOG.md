# Changelog

## Unreleased — M1 verification closed locally

- Embedded the production Vite build in place of the offline preview.
- Verified locally: Go race tests, Vitest, typecheck, ESLint, production and cross builds, live loopback demo with Host/Origin/session checks, full-API canary sweep, Ctrl+C shutdown, and Playwright browser checks.
- Fixed third-party notice generation for packages that declare a license without shipping a license file.
- Fixed an ambiguous browser-test locator.
- Scoped Go test/vet to project packages so `web/node_modules` is never compiled; CI and `make install` now use `npm ci` only.
- Updated frontend dependencies within their current majors, fixing critical/high Vitest and Vite dev-server advisories. Playwright held at 1.58 for macOS 13.
- Raised the Go minimum to 1.26. Pinned CI actions to v7 release commits, disabled credential persistence and dependency caches, and added an embedded-asset drift check.
- Upgraded majors: Vite 8, @vitejs/plugin-react 6, Vitest 5, ESLint 10, lucide-react 1.x, jsdom 29, jest-dom 7. TypeScript stays on 5.9 until typescript-eslint supports 7.
- Open: remote CI run, Linux/Windows runtime checks, real engine certification.

## 0.1.0-alpha.1 — M1 implementation, verification incomplete

- Added Go CLI, official read-only engine adapter, discovery, bounded state parsing/redaction and secure local HTTP API.
- Added React resource explorer, module tree, inspector, overview, provider/module views and appearance settings.
- Added synthetic fixtures, fake executables, API/security/parser tests, and build/development tooling.
- Included an explicitly labeled offline UI preview; npm/production frontend checks and browser/live-loopback verification remain blocked by the development sandbox. No verified release is declared.
