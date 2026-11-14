# Real-engine qualification tools (M27)

Tools for `tasks/M27` (local, ignored task files). Nothing here runs an engine, applies, imports or touches cloud accounts. Engine binaries, provider archives, mirrors and evidence stay **outside** the repository (owner decision OD-1d: `~/hclaro-qualify/`).

## `internal/qualify/netprobe` — network-isolation probe (F-0)

```sh
go build -o "$TMP/netprobe" ./internal/qualify/netprobe
"$TMP/netprobe" control                                    # outside the isolation
sandbox-exec -D REAL_HOME="$HOME" -f profile.sb "$TMP/netprobe" isolated -read <canary file>
```

- Contacts only the owner-approved endpoints (OD-5, hard-coded): TCP handshake to `9.9.9.9:53` and `[2620:fe::fe]:53`, one `. NS` DNS query to `9.9.9.9:53` over UDP, and one system-resolver lookup of `registry.terraform.io`.
- `isolated` also checks loopback (IPv4 and IPv6, retried for up to 5 s; the attempt count is reported) and repeats the outbound checks from a grandchild process (it re-executes itself, so build it to a file; do not use `go run`).
- `-read` attempts to read a canary file (F-0 check 8, OD-14); only the outcome is reported.
- It prints one JSON document. Each check has `outcome`, `error`, `ms` and, where it applies, `expected` and `ok`. Judging the isolation level (`enforced`, `weak-proxy`, `none`) needs the control as well; see `tasks/M27/FEASIBILITY-CHECKLIST.md`.
- Unit tests use loopback only.

## `scripts/qualify/fetch-metadata` — release metadata only (F-6)

```sh
go run ./scripts/qualify/fetch-metadata -out ~/hclaro-qualify/evidence/<run> <url>...
```

- Only `https` URLs on the OD-1a allowlist (host **and** path prefix). Archive and binary extensions are refused before any request; redirects off the allowlist are refused; a non-text `Content-Type` or a body over 5 MiB is refused and discarded.
- Owner-approved exceptions (2026-11-14), found when the strict rule refused real sources:
  - E1: `application/octet-stream` accepted for SHA256SUMS signature files (up to 64 KiB) and for `*_SHA256SUMS` files whose every line is `<sha256>  <name>`.
  - E2: a GitHub release download in an allowlisted repo may redirect to `release-assets.githubusercontent.com`, only for checksum and signature files.
  - E3: `application/octet-stream` from `registry.opentofu.org` provider metadata accepted when it is valid JSON.
  - E4: `github.com/opentofu/terraform-provider-{random,local}` release downloads, checksum and signature files only (registry.opentofu.org serves these OpenTofu rebuilds).
- Every attempt is logged to `<out>/fetch-log.jsonl` (URL, source, time, result, final URL, headers, SHA-256); bodies go to `<out>/bodies/<sha256>`. No credentials, cookies or identifying headers are sent.
