# Contributing

Read `AGENTS.md`, `docs/PRD.md` and the current milestone task before changing code. M1 is strictly read-only; later operations require a separate milestone review.

Use synthetic data, the mock engine or `make demo`. Never use real cloud accounts/backends or credentials in tests. Do not execute mutation commands while developing.

Run `make test`, `make lint` and `make build`; record missing dependencies or sandbox restrictions rather than claiming skipped checks pass. `make cross` checks compilation only. Install with `npm ci --prefix web`; commit lockfile changes with any dependency update. Run `make browser-test` for UI changes. Frontend changes must rebuild embedded assets before building the Go binary.

Keep APIs typed, bounded and authenticated. Add regressions for parser, redaction, subprocess and request-boundary changes. Never include raw state or CLI errors in HTTP responses. Preserve canonical addresses emitted by the official CLI.

The license decision remains with the product owner; no open-source license has been granted for this repository.
