# M01 — CLI + Read-only Explorer (implement first)

**Goal:** User runs `hclaro` inside an existing initialized Terraform/OpenTofu directory, and opens a useful read-only local UI for state resources.

## Backend scope
- CLI flags `--path`, `--engine`, `--port`, `--no-browser`, `--version`, `--help`; one portable Go binary.
- Detect root project, executable version, selected workspace, backend/init status, and basic capabilities. Truthful failure messages.
- Parse supported machine-readable state JSON through a CLI adapter (no cloud SDK). Flatten recursive child modules and indexed instances; preserve managed/data distinction.
- Redaction layer before any API response; do not expose raw state or secrets.
- Bind only to loopback and apply Host/Origin/session defense and restrictive asset loading.
- Read-only API endpoints for workspace, resource list, resource details, and health. No generic executor.
- Fake CLI integration tests with stdout/stderr and fixture coverage.

## Frontend scope
- Professional dark/light UI, nav + header + resource tree/table + details inspector.
- Filter/search full address, module, provider, type; copy address.
- Clear workspace status and errors; no fake status indicators.
- Keyboard navigation, empty/loading/error states, responsive down to tablet.
- Future Plans/Operations navigation clearly disabled or labeled coming later.
- Prefer small accessible components, not a wall of generic cards.

## Acceptance tests
1. A fake fixture with root, child modules, indexed and data instances gives exact instance counts and addresses.
2. Search/filter works on a ~1000-resource synthetic fixture without major typing lag.
3. Missing backend initialization and missing CLI are clearly distinguished from empty state.
4. User navigates without writing infrastructure or auto-running plan/refresh.
5. Canary secret never crosses API boundary or appears in logs.
6. Invalid Host/Origin and missing/invalid session are rejected.
7. Cross-platform build outputs can be produced with embedded UI, without requiring Node at runtime.
8. README includes usage and troubleshooting; tests and build run to completion.

## Not included
No real plan, apply, replace, import, state mutation, remote service, AI or account settings.
