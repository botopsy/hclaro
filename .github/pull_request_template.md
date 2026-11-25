## What and why

<!-- What does this change, and what problem does it solve? Link the issue or milestone (for example M30). -->

## Type

- [ ] Bug fix
- [ ] Feature
- [ ] UI change (screenshots below)
- [ ] Documentation only
- [ ] Build, CI or dependencies

## Checks

- [ ] `make test` and `make lint` pass
- [ ] `make build` was run and the rebuilt `internal/ui/dist` is committed (needed for any `web/src` change, or Linux CI fails)
- [ ] `make browser-test` passes (UI changes)
- [ ] Docs updated (`README.md`, `docs/`, `CHANGELOG.md` under "Unreleased") where behavior or limits changed
- [ ] `git diff --check` is clean

## Safety

- [ ] Tests use synthetic fixtures and fake engines only: no real cloud account, backend, state or credentials
- [ ] Nothing new runs `init`, `plan`, `refresh`, `apply`, `import` or `destroy` without an explicit user action, and CLI calls stay on the allowlisted argument-array adapters
- [ ] Plan, state, log and output data is redacted on the server before it reaches HTTP, the UI or history
- [ ] Loopback authorization, Host/Origin/CSRF checks and per-workspace serialization are unchanged, or the change is explained above
- [ ] Not applicable: this change touches none of the above

## Screenshots

<!-- UI changes: before and after, light and dark. Use synthetic demo data only (`make demo`); never paste real resource names, account IDs or the session URL. -->

## Notes for reviewers

<!-- Risks, limitations, follow-ups, anything you did not check. -->
