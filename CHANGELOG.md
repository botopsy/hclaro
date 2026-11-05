# Changelog

## Unreleased — M2 plan visualizer

- Explicit plan runs from the UI with a preflight dialog: `plan -input=false -json -out=<private temp>`, then `show -json`; optional variable files from the project root.
- Plan review: per-action counts and filters, complete change list (including collateral deletes, reads, moves, imports and deposed objects), attribute diffs with sensitive / known-after-apply / forces-replacement markers, outputs, drift, plan SHA-256 and configuration-change warning.
- Live sanitized progress over SSE with bounded replay, cancellation (interrupt, then kill after 15 s), one-hour timeout and shutdown cleanup. Saved plans are deleted as soon as review data is extracted.
- Operations view with session history.
- Two CSRF-protected POST routes (start plan, cancel). No apply endpoint.
- Windows cancellation sends `CTRL_BREAK` to the engine's own process group, so it can release a state lock before the 15 s kill.
- Diagnostics and log messages redact known input values (`TF_VAR_*` and loaded variable files) before the line heuristics.
- The context fingerprint now covers the data directory (backend metadata, workspace, modules) and planning environment variables, and is taken before the engine runs.
- Verified against real Terraform 1.5.5 on a disposable local project. Fixes found there: the default local backend (no `backend` block) is now recognized for planning; partly unknown outputs show "known after apply" per element; a blank first line no longer hides the interrupt message.


## Unreleased — M1 verification closed locally

- Embedded the production Vite build in place of the offline preview.
- Verified locally: Go race tests, Vitest, typecheck, ESLint, production and cross builds, live loopback demo with Host/Origin/session checks, full-API canary sweep, Ctrl+C shutdown, and Playwright browser checks.
- Fixed third-party notice generation for packages that declare a license without shipping a license file.
- Fixed an ambiguous browser-test locator.
- Scoped Go test/vet to project packages so `web/node_modules` is never compiled; CI and `make install` now use `npm ci` only.
- Updated frontend dependencies within their current majors, fixing critical/high Vitest and Vite dev-server advisories. Playwright held at 1.58 for macOS 13.
- Raised the Go minimum to 1.26. Pinned CI actions to v7 release commits, disabled credential persistence and dependency caches, and added an embedded-asset drift check.
- Upgraded majors: Vite 8, @vitejs/plugin-react 6, Vitest 5, ESLint 10, lucide-react 1.x, jsdom 29, jest-dom 7. TypeScript stays on 5.9 until typescript-eslint supports 7.
- CI passes on Ubuntu 24.04, macOS and Windows; Ubuntu is pinned for Playwright 1.58.
- Open: Linux/Windows packaged-binary runtime checks, real engine certification.

## 0.1.0-alpha.1 — M1 implementation, verification incomplete

- Added Go CLI, official read-only engine adapter, discovery, bounded state parsing/redaction and secure local HTTP API.
- Added React resource explorer, module tree, inspector, overview, provider/module views and appearance settings.
- Added synthetic fixtures, fake executables, API/security/parser tests, and build/development tooling.
- Included an explicitly labeled offline UI preview; npm/production frontend checks and browser/live-loopback verification remain blocked by the development sandbox. No verified release is declared.
