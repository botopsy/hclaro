# Hclaro — Agent operating rules

This repository implements a local-first, security-sensitive Terraform/OpenTofu UI. The canonical product spec is `docs/PRD.md`. Implementation milestones live in `tasks/`.

## Highest-priority rules

- Do not run commands against the user's real infrastructure, cloud accounts or remote backends while developing.
- Do not run any `apply`, `destroy`, `import`, `state rm`, `state mv`, `force-unlock` or similar action without explicit later approval from the product owner in a disposable test environment.
- No external telemetry, analytics, hosted auth, remote APIs or CDNs by default.
- Redact secrets before returning data via local HTTP APIs. Assume Terraform/OpenTofu state/plan JSON can contain plaintext secrets even if tagged `sensitive`.
- The Go backend must use `exec.CommandContext` (or an equally safe argv-based subprocess API); never invoke `sh -c`/`bash -c`/PowerShell with composed user strings.
- The local server binds to loopback only and rejects unexpected Host/Origin; enforce a random per-launch session credential. Mutating routes in later phases need CSRF defense and exact-plan confirmation.
- Never implicitly initialize a workspace, refresh provider data, generate a plan, import or apply on app launch.
- Keep unsupported engine versions and backend execution modes explicit; do not claim a risky workflow is supported without testing it.

## Engineering conventions

- Go backend + React/TypeScript/Vite frontend; the final frontend is embedded in one Go CLI binary.
- Use small, testable packages with explicit adapters for `terraform` and `tofu`.
- Place sanitized JSON fixtures in `fixtures/` and write parser/redaction unit tests before complex UI logic.
- APIs use typed responses, validate inputs, have bounded body sizes/timeouts and return sanitized errors.
- UI should be polished, accessible, keyboard friendly and fast with large resource lists.
- Use clear and concise copy. Distinguish actual Terraform state from live cloud provider state.
- Write deterministic tests; never rely on real credentials in CI.
- Prefer readable code to speculative abstractions. Document important tradeoffs in `docs/`.
- Do not silently expand scope from one milestone to the next.

## Definition of Done for any milestone

- All acceptance criteria in its task file are demonstrated or listed as unresolved.
- Go tests, web typecheck/tests, linters and a production build have been run, or explicit missing-toolchain blockers are documented.
- Security-focused regression tests are present for relevant data flows.
- README and relevant architecture notes reflect actual behavior, not future plans.
- Final response summarizes implementation, test evidence, limitations and safe next steps.
