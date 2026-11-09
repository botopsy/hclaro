# Contributing to hclaro

hclaro is published by [BotOpsy](https://github.com/botopsy). Thank you for your interest. During the public beta the best way to contribute is feedback.

## How to help

- **Report bugs** with the [bug report form](https://github.com/botopsy/hclaro/issues/new/choose): version (`hclaro --version`), OS, engine and version, what you did and what happened. A reproduction with a minimal, synthetic configuration is the most useful thing you can send.
- **Request features** with the feature request form: describe the problem you want solved before the solution.
- **Ask questions and share ideas** in [GitHub Discussions](https://github.com/botopsy/hclaro/discussions).

Never include state, plans, plan output, `.tfvars` contents, credentials, the private session URL or real account identifiers. Report security problems privately as described in [SECURITY.md](SECURITY.md).

## Pull requests during the beta

BotOpsy is **not accepting external code pull requests for hclaro during the beta**. This keeps review focused while the security-sensitive core settles; the repository limits pull requests to collaborators, and any external pull request that still arrives may be closed without review. Please open an issue instead, and include a patch or a link to your fork if it helps explain the idea.

This is a project contribution policy, not a license restriction. hclaro is licensed under the [Apache License 2.0](LICENSE): you are free to fork it, modify it and distribute your changes under that license. The policy may change after the beta; this file will say so.

## Working on your own fork

The product specification is [docs/PRD.md](docs/PRD.md) and the architecture is described in [docs/architecture.md](docs/architecture.md). The development rules:

- Use synthetic data, the fake engine (`make demo`, `make demo-hub`) and fixtures. Never use real cloud accounts, backends or credentials in tests, and never run mutating commands while developing.
- Install with `npm ci --prefix web` (`make install`); commit lockfile changes together with any dependency change.
- Run `make test`, `make lint` and `make build`, and `make browser-test` for UI changes. Frontend changes must rebuild the embedded assets (`internal/ui/dist`) before building the Go binary; CI checks that they match a fresh build.
- Keep APIs typed, bounded and authenticated. Add regression tests for parser, redaction, subprocess and request-boundary changes. Never return raw state or raw CLI errors over HTTP.
- Subprocesses use argument arrays through the allowlisted engine adapters; never a shell or a generic command runner.

Release steps are in [docs/RELEASING.md](docs/RELEASING.md).
