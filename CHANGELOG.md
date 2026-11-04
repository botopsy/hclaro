# Changelog

## Unreleased — M1 verification closed locally

- Embedded the production Vite build in place of the offline preview.
- Verified locally: Go race tests, Vitest, typecheck, ESLint, production and cross builds, live loopback demo with Host/Origin/session checks, full-API canary sweep, Ctrl+C shutdown, and Playwright browser checks.
- Fixed third-party notice generation for packages that declare a license without shipping a license file.
- Fixed an ambiguous browser-test locator.
- Scoped Go test/vet to project packages so `web/node_modules` is never compiled; CI and `make install` now use `npm ci` only.
- Open: remote CI run, Linux/Windows runtime checks, real engine certification, dev-tool security patches (Vite/Vitest).

## 0.1.0-alpha.1 — M1 implementation, verification incomplete

- Added Go CLI, official read-only engine adapter, discovery, bounded state parsing/redaction and secure local HTTP API.
- Added React resource explorer, module tree, inspector, overview, provider/module views and appearance settings.
- Added synthetic fixtures, fake executables, API/security/parser tests, and build/development tooling.
- Included an explicitly labeled offline UI preview; npm/production frontend checks and browser/live-loopback verification remain blocked by the development sandbox. No verified release is declared.
