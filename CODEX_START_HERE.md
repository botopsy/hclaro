# Codex task: Bootstrap Hclaro and implement Milestone 1

You are implementing **Hclaro** (`hclaro`), a new local-first Terraform/OpenTofu visual workspace.

Read **`docs/PRD.md`** completely. Treat it as the product specification. Read `AGENTS.md` and `tasks/M01-readonly-explorer.md` for mandatory implementation constraints and acceptance tests. The product should eventually support M1–M4, but **this task is M1 only**.

## Desired result for this Codex run

Create a functional, polished, end-to-end alpha with:

1. Go CLI binary: `hclaro [--path PATH] [--port PORT] [--engine terraform|tofu] [--no-browser] [--help] [--version]`. Default working directory is current directory. A normal run starts a loopback-only server, opens the default browser, and prints the URL. `Ctrl+C` exits cleanly.
2. Engine abstraction for both Terraform and OpenTofu, robust executable detection and version reporting. Do not assume one vendor's newest flags work everywhere. Detect backend/workspace/init status when available; show truthful errors/capabilities.
3. Read-only, secure local API that obtains normalized resource-instance state from supported CLI JSON output; handle nested modules, `count`, `for_each`, data sources, missing state and backend errors. Sanitize all resource attributes server-side. Never leak planted canary secrets.
4. React + TypeScript + Vite UI embedded in the Go binary: modern, professional infrastructure explorer with resource tree/table, search and filters, resource inspector, overview, theme toggle, keyboard navigation, loading/error/empty states. Use accessible, responsive components; no placeholder-only UI.
5. Local security: loopback bind, strict Host/Origin validation, random per-session authorization, no wildcard CORS, no mutating command endpoints, no analytics/external assets; sanitize terminal/error payloads and local storage.
6. Fixture-driven tests and mocks for no-Terraform environments; Go unit/integration tests and frontend typecheck/tests; build instructions and a reproducible packaging step.
7. README with CLI usage, supported versions/capabilities, instructions to run frontend in development, limitations and security caveats. Include minimal architectural documentation.

## Non-negotiable boundaries

- **Do not implement** real `plan`, `apply`, `replace`, `import`, `state rm/mv`, `destroy`, `force-unlock`, `init`, or any mutating infrastructure operation in this first task. Future nav items may be disabled with explanatory label, not misleading fake functionality.
- **Do not** access real cloud accounts, credentials, remote state or networks for testing. Use synthetic sanitized fixtures and fake CLIs unless a user explicitly authorizes a disposable environment later.
- **Do not** send any state, secret, plan or command output to external servers or telemetry. The working UI must run entirely offline after installation, except when the official selected CLI contacts its configured backend as a deliberate read.
- **Do not** run `terraform init` / `tofu init` automatically. Opening the UI is read-only.
- No shell string construction. Use argv arrays. API must not allow arbitrary executable paths, commands or project switching.
- Prefer security and honest unsupported states to clever but unsafe fallback behavior.

## Suggested execution sequence

1. Inspect the repository and this spec. If it is empty, scaffold from scratch. Present a concise plan in your own working context; proceed without waiting for a confirmation.
2. Define Go domain models and engine interface. Implement a fake engine and golden fixtures first.
3. Build state parser, redaction tests, workspace discovery and launch CLI.
4. Build loopback session-protected API and embedded SPA serving.
5. Build polished Explorer UI with real fixture/API interaction; then connect real read-only engine.
6. Run formatting, static analysis, typecheck, unit tests, integration tests and a real build; fix failures.
7. Report changed files, commands to run, tests actually executed, limitations and the next M2 step. Take a screenshot if a supported browser test runner is available.

## Acceptance checks

- `hclaro --help` shows all documented flags.
- With an initialized test fixture/fake engine, `hclaro --path <project> --no-browser` starts a local web server and serves functional UI.
- Nested modules and indexed resources appear exactly once with correct canonical addresses.
- Sensitive planted `DO_NOT_LEAK_HCLARO_TEST_SECRET` does not appear in any API response, UI rendered text, saved history or CLI logs.
- Uninitialized, inaccessible and empty state cases are distinct.
- No request can execute a modifying Terraform/OpenTofu command.
- API denies invalid Host, Origin and unauthorized session.
- End-user packaged binary serves UI without a Node runtime.
- All tests/builds are green (if environment lacks a toolchain, clearly state what could not be tested; never claim success).

Deliver M1 as an independently useful alpha. Read `tasks/M02-visual-plan.md`, `tasks/M03-replace.md`, `tasks/M04-import.md` only to preserve future compatibility, **do not implement them yet**.
