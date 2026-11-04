# Milestone 1 architecture

## Runtime and trust boundaries

`cmd/hclaro` parses flags, validates one explicit project root, resolves one engine executable, binds `127.0.0.1`, generates 256 random session bits, and serves embedded assets. State loading runs asynchronously so a slow backend does not delay the HTTP UI. Each child has a 45-second deadline and cancellation; Ctrl+C/SIGTERM cancels reads and shuts HTTP down with a bounded grace period.

The only engine interface methods are `Version`, `Workspace`, and `State`. `internal/engine.CLI` is the shared official-CLI adapter for the common Terraform/OpenTofu read dialect. Its explicit engine name selects compatibility policy and version fields. It uses `exec.CommandContext` and argv, never a shell. Selection prefers Terraform, then OpenTofu, unless `--engine` forces one. A mock adapter and a compiled fake CLI keep tests independent of installed engines and credentials.

The HTTP layer has no reference to an engine or filesystem root. It can only read a snapshot store and embedded assets. This keeps browsing incapable of invoking commands. A later milestone can add a separate explicit operation coordinator without weakening the M1 engine interface or resource parser. No operation/history/plan abstractions have been implemented ahead of their milestones.

## Packages

| Path | Responsibility |
| --- | --- |
| `cmd/hclaro`, `internal/cli` | Flags, lifecycle, loopback listener, browser argv |
| `internal/engine` | Resolution, compatibility, bounded subprocess output, injectable adapter |
| `internal/workspace` | Root/metadata discovery, loading status, actionable safe diagnostics, immutable snapshot publication |
| `internal/state` | Official JSON decoding, module traversal, normalization, redaction, indexes |
| `internal/security` | Random session token and exact Host/Origin/method/body policy |
| `internal/api` | Authenticated typed snapshot, list/filter/pagination and detail responses |
| `internal/ui` | Go embedding of `dist/`; no frontend runtime server required |
| `web/src` | React UI, TanStack Query reads, accessible native disclosures, shadcn-style Radix Button, local appearance preferences |
| `fixtures`, `scripts/fake-engine` | Explicit synthetic state and executable |
| `scripts/fixture-http`, `scripts/browser-smoke.mjs` | Browser requests bridged to the real Go handler without a real engine or TCP backend |

## Discovery and compatibility

Discovery never walks parents or writes files. The selected root is resolved through symlinks and must contain a recognized project/state filename. Initialization is inferred from the selected data directory or existing local state. Only the initialized backend **type** is read, through a bounded regular-file JSON read; config, credentials, absolute paths and environment values are never returned. `TF_DATA_DIR` can be relative to the selected root or absolute. The lockfile is reported by presence only. Backend configuration is not inferred by an unreliable HCL regex; absent metadata is labeled unknown.

Workspace selection comes from the CLI, including inherited `TF_WORKSPACE`; an error never substitutes `default`. Remote/cloud backend metadata is labeled `remote-exec`. Unknown backend execution mode stays `unsupported`. Read capability is enabled only after a successful state read/parse; plan and apply capabilities remain false everywhere.

Stable Terraform 1.5–1.17 and OpenTofu 1.6–1.13 are the intended compatibility range. Only synthetic outputs have been exercised. Version prereleases and unsupported majors/minors are refused; format major 1 is accepted with unknown fields ignored. The common `show -json` subset is documented by [Terraform](https://developer.hashicorp.com/terraform/cli/commands/show) and [OpenTofu](https://opentofu.org/docs/cli/commands/show/). Real providers/versions need a disposable-workspace certification matrix before release.

Inherited credential, workspace and CLI configuration variables are retained. Injected `TF_CLI_ARGS` for the active command are rejected unless all arguments are `-no-color`, preventing file-output/path overrides. `TF_LOG*` is removed, `TF_LOG=OFF` and `CHECKPOINT_DISABLE=1` are forced, and raw stderr is discarded. Diagnostics explain safe troubleshooting categories rather than returning secret-bearing CLI text. This deliberately trades diagnostic specificity for protection in M1.

## Snapshot parsing and redaction

The parser consumes at most 64 MiB of official `show -json` output. It refuses plan JSON, trailing JSON, duplicate/invalid instances, more than 100,000 resources, and module nesting beyond 128. Module paths and resource addresses are retained **exactly as emitted by the CLI**. Dots inside quoted keys are never split. Deterministic opaque IDs derive from canonical addresses; maps index IDs, resource types and provider sources. Root/child module indexes include their direct instance counts. Managed and data modes remain separate.

Attributes are copied through the redactor before a snapshot is published. The redactor honors sensitivity trees at map, list, and whole-value levels, masks credential-like keys regardless of markers, masks credential-shaped strings even in permitted fields, and masks **all unknown scalar values**. A short allowlist exposes ordinary identity/location/status attributes. Nested objects remain navigable; excessively deep values and suspicious keys are masked. Outputs and raw JSON are never exposed. Potentially unsafe address/provider metadata rejects the snapshot rather than returning misleading altered addresses.

This policy intentionally hides arbitrary tags, policies and provider-specific fields. Sensitivity cannot be inferred perfectly from arbitrary strings: resource identity metadata and selected allowlisted values must not themselves be used as secret stores. There is no reveal switch. Raw subprocess bytes exist only transiently in memory; no state, logs, operation history or credentials are written by Hclaro. Installed engine/provider plugins remain trusted local executables with their own behavior.

## HTTP contract

Every API endpoint requires `Authorization: Bearer <launch-token>`:

| Route | Response |
| --- | --- |
| `GET /api/v1/health` | Process/version status |
| `GET /api/v1/workspace` | Loading/ready/empty/error, safe context, capabilities, summary indexes, diagnostic |
| `GET /api/v1/resources` | Summary page (default 50, max 200) and total matches |
| `GET /api/v1/resources/{opaque-id}` | One redacted instance |

List parameters: `query`, exact `module`, `provider`, `type`, `mode`, `sort=asc|desc`, `offset`, `limit`. An absent module filter means all modules; `module=` selects root resources. Queries search full address, name, provider, type and module. Details use an ID map; no raw address becomes a command argument.

The guard accepts only the exact bound authority and, when present, the exact matching Origin. Cross-site fetch metadata is refused, no CORS headers are emitted, all request bodies are rejected, queries are bounded, and only GET/HEAD methods exist. Unknown API paths cannot fall back to SPA HTML. HTTP header/body/idle/write limits bound requests. Assets cannot be directory-listed. CSP permits only local scripts/styles/fonts/connections and denies framing/forms; referrers and caching are disabled.

A private launch fragment bootstraps bearer authorization. The UI removes it from history immediately, keeps the token in sessionStorage for reload, and sends it only in headers. The token expires when the process stops; new launches use independent tokens. No cookies or CSRF-bearing write routes exist in M1.

## Frontend and performance

The UI requests workspace status while loading, then uses the immutable snapshot. TanStack Query caches reads without focus/reconnect refresh. Search is debounced 180 ms, filters compose on the server, and resource pages render at most 50 rows. This bounds DOM work independently of total instances. Only selected details are downloaded. Resource selection stays mounted during detail changes, preserving list scroll. Module/attribute structures use native disclosures, resource buttons support arrow navigation, and reduced-motion preferences are respected.

No runtime fonts, CDNs, analytics or hosted APIs are used. Display preferences are validated before use and are the only localStorage data. The sidebar, dashboard, modules and providers all derive from real snapshot counts; future operations are disabled.

The build is TypeScript + Vite + Tailwind, with React, TanStack Query, Lucide and a shadcn-style Button. Vite writes content-hashed files to `internal/ui/dist/assets/` plus `THIRD-PARTY-NOTICES.txt` for runtime dependencies; the Go binary embeds that directory. `scripts/build-offline-preview.mjs` remains only as a no-network fallback and must not be committed as release assets.
