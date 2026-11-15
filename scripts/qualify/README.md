# Real-engine qualification tools (M27)

Tools for `tasks/M27` (local, ignored task files). Nothing here applies, plans, refreshes, imports or touches cloud accounts; engines run only `init`, `providers mirror` and `providers schema`, with no state. Engine binaries, provider archives, mirrors and evidence stay **outside** the repository (owner decision OD-1d: `~/hclaro-qualify/`).

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

## Engines and provider mirrors (M27.01 phase 2)

Requires `gpg`, `jq`, `curl` and `unzip`. Downloads are owner-approved per version and platform (OD-1b, OD-2): every OD-1b version on `darwin_amd64`, and the `PLATFORM` set (Terraform 1.17.2, OpenTofu 1.13.9, `random` 3.9.1, `local` 2.9.1) on `darwin_arm64`, `linux_amd64`, `linux_arm64` and `windows_amd64` (OD-3-M27.07, run only by the real-engine workflow).

```sh
scripts/qualify/fetch-engines.sh ~/hclaro-qualify/engines              # Terraform 1.5.5, 1.5.7, 1.17.2; OpenTofu 1.6.3, 1.13.9
scripts/qualify/mirror-providers.sh ~/hclaro-qualify/mirror ~/hclaro-qualify/engines
scripts/qualify/verify-mirror.sh ~/hclaro-qualify/mirror ~/hclaro-qualify/engines ~/hclaro-qualify/evidence/<run>
```

- `fetch-engines.sh [--platform <os_arch>]` refuses any version or platform not pinned in the script, before any request. Without engine arguments it fetches every approved engine for that platform. Each archive must match its pinned SHA-256 **and** the `SHA256SUMS` signed by the approved primary key (`keys/`, OD-6a/b; expiry and revocation checked in a throwaway keyring). It prints the archive and binary SHA-256.
- `mirror-providers.sh` (`PLATFORM=<os_arch>`, default `darwin_amd64`; the engines must be that platform's) runs `providers mirror` (Terraform 1.17.2 and OpenTofu 1.13.9, with `env -i`, a throwaway HOME and `CHECKPOINT_DISABLE=1`) for `hashicorp/random` 3.9.1 and `hashicorp/local` 2.9.1 (pinned in `fixtures/qualify/providers/versions.tf`). It builds one packed mirror per engine family: Terraform reads `registry.terraform.io` (HashiCorp builds) and OpenTofu reads `registry.opentofu.org` (OpenTofu rebuilds, different hashes). Every archive must match the provider's signed `SHA256SUMS` (OD-6e), and each `SHA256SUMS` file must match the SHA-256 seen in F-6, pinned in the script. It writes `mirror-manifest.json` (no timestamps or absolute paths, so two runs are byte-identical) and `<family>.tfrc` with `direct { exclude = ["*/*"] }`. The mirror path in `<family>.tfrc` is an `@MIRROR@` placeholder, which `verify-mirror.sh` replaces with the run dir's copy.
- `COMBINED=1 verify-mirror.sh …` checks the combined Hub config `hub.tfrc` (both mirrors, each limited by `include` to its registry host) with every engine in place of the family config (M27.05).
- `verify-mirror.sh` (macOS only for now) copies everything into a run dir under `/tmp`, re-hashes the mirror against the manifest, and runs netprobe control and isolated before the first engine and after the last. It fails unless both are `enforced`. Under `sandbox-exec` with `sandbox.sb` it runs `init` for every engine and checks the following:
  - the lock file holds exactly the pinned versions, and every lock-file hash is in the manifest, and every manifest `h1:` hash for the host platform is in the lock file;
  - the providers are installed under the mirror's host directory;
  - the provider plugins start (`providers schema -json`).
- `sandbox.sb` is the F-0 profile plus one scoped allow, for Unix-socket connects under the run's `TMPDIR` only. go-plugin providers talk to the engine over such a socket, and without it they fail to start. System sockets such as `/var/run/mDNSResponder` stay denied.
- `internal/qualify` `TestNoQualifyBinaries` fails if an archive, an engine or provider binary, or executable content is committed under `scripts/qualify/`, `fixtures/qualify/` or `internal/qualify/`.

## `qualify.sh` / `qualify.ps1` — real-engine harness runner (M27.02)

`make qualify TF=… TOFU=… MIRROR=… OUT=… [ISOLATION=sandbox-exec|unshare|proxy|none]` (Windows: `qualify.ps1 … -Isolation firewall|proxy|none`) compiles the `realengine` test binary outside the isolation, copies it with `hclaro`, `netprobe`, the engines and the mirror into a run directory outside the home directory, and runs it with `env -i` under `sandbox.sb` (`RUN_TMP` = the run directory). Details: `docs/qualification/README.md`.

## Real-engine workflow (M27.07)

`.github/workflows/real-engine.yml` runs the steps above on GitHub-hosted runners, one job per platform. It runs only when started by hand (`gh workflow run real-engine.yml`; the file must be on `master`). Details and the platform scenarios: `docs/qualification/README.md` §Platforms.
