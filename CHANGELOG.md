# Changelog

## 0.1.0-alpha.4 — 2026-11-05

Replace, saved-plan apply and import are new in this release. They have been tested only against synthetic engine output, not a real Terraform/OpenTofu engine; treat them as drafts and use them only on disposable workspaces.

### Import

- New **Import** view: enter an address, import ID, optional provider and a new file name; **Check and preview** validates without writing or running anything and shows the exact import block.
- Detects an address already in state, an object with the same `id` already managed, a missing resource block, count/for_each key mismatches, an existing import/moved block for the address, existing destination files and credential-shaped IDs.
- After explicit consent, writes the block to a new root-module file (never overwrites; symlinks refused) and runs a full plan. Optional `-generate-config-out` for root, unindexed addresses writes `<name>_generated.tf`; the review shows a redacted copy.
- Import plans count imports separately, are applicable even when the import is the only (no-op) change, and always require the typed `apply <workspace>` phrase.
- Unavailable workspaces (for example remote execution) get a manual recipe; `terraform import` is never run.
- Capabilities gain `supportsImportBlocks`, `supportsGeneratedConfig`, `canImport` and `importReason`. New routes: `GET /api/v1/imports/options`, `POST /api/v1/imports/preview`, `POST /api/v1/imports`.

### Replace

- Select managed instances in the resource explorer (up to 50; data sources cannot be selected) and plan their replacement: a full plan with one `-replace=<address>` per instance, never `-target`.
- Replacement review lists the requested instances, flags any the plan does not replace, and counts every other change, including deletions and replacements you did not select.

### Saved-plan apply

- Apply a reviewed plan for locally executed workspaces. Remote/cloud execution stays plan-only.
- Only the most recent eligible plan keeps its saved file (private `0700` temp directory, one hour). It is deleted after one apply attempt, on expiry, when superseded and on shutdown.
- Apply needs a one-time, five-minute approval bound to the plan, and typing `apply <workspace>` for plans that delete, replace or forget objects. Context, engine version, workspace and the file's SHA-256 are re-checked; the engine's stale-plan refusal is reported, never retried.
- Apply logs stream like plan logs. State is re-read afterwards, and the outcome (applied, not applied, uncertain) is reported separately from the exit status.
- New routes: `POST /api/v1/plans/{id}/approval`, `POST /api/v1/plans/{id}/apply`. `POST /api/v1/plans` accepts `replace` (resource IDs).
- After a failed apply request, the dialog requests a new approval and asks for the phrase again instead of having to be closed and reopened.
- On startup, saved-plan directories older than three hours (left only by a Hclaro process that was killed) are deleted from the temp directory.

### Details drawer

- Resource and change details open in a non-modal drawer docked to the right edge: 720px by default, resizable by dragging or with arrow keys, width remembered per browser. Esc or × closes it.

### Fixes

- A leading icon in information lines (plan, replacement and import summaries) no longer sits on its own line.

### Development

- Pull requests and `master` are verified on CircleCI instead of GitHub Actions; tagged releases still build on GitHub Actions.

## 0.1.0-alpha.3 — 2026-11-05

### Readability

- Larger type: text that was 7–11px is now 10–14px; main UI text is 13–14px.
- Attribute names in the JSON tree no longer shrink when values are long (`arn` no longer wraps to "ar / n").

### Redaction policy

- Redaction now hides likely secrets instead of everything outside a 25-name allowlist. Hidden: values marked sensitive (at any depth), secret-like attribute names, and credential-shaped or random-looking values (PEM, JWT, bearer, AWS access key IDs, URLs with passwords, vendor token prefixes, long mixed-case or hex strings). Tags, enums, booleans, numbers, IDs, ARNs and regions are now shown in the inspector and plan review.
- Output values follow the same policy instead of always being hidden.
- Fixed: a plan diff row whose value is an object ignored sensitivity markers nested inside it.
- `asia`/`akia` substrings and plain URLs no longer hide values or reject state metadata; only full AWS access key IDs and URLs with credentials do.
- Plan diffs say "not set" / "removed" instead of "—".

## 0.1.0-alpha.2 — 2026-11-05

First release published by the release workflow. Covers everything since 0.1.0-alpha.1.

### Release packaging

- Tag-triggered release workflow: full CI, embed drift check, cross-compiled archives for macOS/Linux (amd64, arm64) and Windows amd64 with checksums, native `--version` smoke tests, and a GitHub release (pre-release for suffixed tags).
- CI no longer runs twice on tag pushes; the release workflow calls it instead.

### M2 plan visualizer

- Explicit plan runs from the UI with a preflight dialog: `plan -input=false -json -out=<private temp>`, then `show -json`; optional variable files from the project root.
- Plan review: per-action counts and filters, complete change list (including collateral deletes, reads, moves, imports and deposed objects), attribute diffs with sensitive / known-after-apply / forces-replacement markers, outputs, drift, plan SHA-256 and configuration-change warning.
- Live sanitized progress over SSE with bounded replay, cancellation (interrupt, then kill after 15 s), one-hour timeout and shutdown cleanup. Saved plans are deleted as soon as review data is extracted.
- Operations view with session history.
- Two CSRF-protected POST routes (start plan, cancel). No apply endpoint.
- Windows cancellation sends `CTRL_BREAK` to the engine's own process group, so it can release a state lock before the 15 s kill.
- Diagnostics and log messages redact known input values (`TF_VAR_*` and loaded variable files) before the line heuristics.
- The context fingerprint now covers the data directory (backend metadata, workspace, modules) and planning environment variables, and is taken before the engine runs.
- Verified against real Terraform 1.5.5 on a disposable local project. Fixes found there: the default local backend (no `backend` block) is now recognized for planning; partly unknown outputs show "known after apply" per element; a blank first line no longer hides the interrupt message.


### M1 verification closed locally

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
