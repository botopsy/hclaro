# Building and testing

Contributions policy: [CONTRIBUTING.md](../../CONTRIBUTING.md). Releasing: [RELEASING.md](../RELEASING.md). Brand assets: [BRAND.md](../design/BRAND.md).

## Build from source

Build prerequisites: Go 1.26+, Node.js 22.12+ (Node 24 recommended), npm, and Make. Node is a **build tool only**. Go race tests also require a C compiler.

```sh
make install     # install frontend dependencies; requires npm registry access
make build       # TypeScript + Vite, then embed assets into bin/hclaro
make test        # Go race tests + Vitest
make lint        # go vet + TypeScript + ESLint
```

`make install` runs `npm ci` against the committed `web/package-lock.json`. Update dependencies deliberately with `npm install --prefix web <pkg>@<version>` and commit the lockfile change.

`make binary` builds from the already bundled assets without Node. It uses `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and a fixed version; rebuilding with the same Go toolchain, target, version, and assets produces the same binary bytes. `make build` regenerates the embedded assets; Vite output is deterministic for the same lockfile, so rebuilding unchanged sources produces identical files.

## Development server and browser tests

```sh
make install
make dev
```

This starts the **synthetic** backend at `127.0.0.1:9273` and Vite at `127.0.0.1:5173`; open the printed private URL. Vite proxies only same-machine, validated read-only requests; the backend still requires authorization. Neither server enables wildcard CORS. Production uses only the Go server and embedded assets.

`make browser-test` builds an isolated HTTP fixture helper and runs Playwright against the real Go handler using intercepted requests. Install a browser first (`cd web && npx playwright install chromium`; Playwright is held at 1.58 because newer releases do not support macOS 13), or set `HCLARO_CHROME` to an installed Chrome executable. It checks rendering, copying, filters, themes, tablet overflow, secret boundaries, plan, apply-confirmation and import flows, and a 1,000-instance search, and writes screenshots into `.cache/screenshots/` (the README screenshot, `docs/screenshots/navigation-expanded-dark.png`, is copied from there by hand when the UI changes). It never reaches a real engine or cloud account. It also runs `tests/e2e/quick-actions.mjs`: the real binary on 127.0.0.1 with a fake `claude` (by `--claude-path`), a fake codex and the fake Infracost, checking the six AI quick actions (preview before Start, sent bytes equal the preview, follow-up), the cost checkbox and estimate staleness, and the disabled and blocked states, with no request outside 127.0.0.1 (skipped on Windows).

Brand colours, logo geometry and contrast results are documented in [`docs/design/BRAND.md`](../design/BRAND.md). Colours live once as tokens in `web/src/styles/tokens.css`; components use the semantic tokens only. `make brand-assets` regenerates the committed favicon, touch icon and `docs/design/assets` from the reference geometry (offline, with the test Chromium).

## Cross-platform binaries

After `make build`, run `make cross` for macOS amd64/arm64, Linux amd64/arm64, and Windows amd64. The frontend is embedded in each binary. End users need only that binary plus the chosen Terraform/OpenTofu CLI and its normal initialized workspace.

Without Make, after building the frontend:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o hclaro ./cmd/hclaro
```

PowerShell:

```powershell
$env:CGO_ENABLED='0'; $env:GOOS='windows'; $env:GOARCH='amd64'
go build -trimpath -buildvcs=false -ldflags='-s -w' -o hclaro.exe ./cmd/hclaro
```

Cross-compilation is not OS runtime validation. Release binaries are not code-signed or notarized.

## Continuous integration

Pull requests and `master` run on GitHub Actions (`.github/workflows/ci.yml`, checks `changes`, `linux`, `windows`, `macos`). Pull requests run the full suite, the embedded-asset check and the synthetic browser checks on Linux. Go and unit tests run on Windows (without the race detector) and macOS only when a pull request changes Go code, `go.mod`, `scripts/`, `fixtures/` or `.github/`. Merges to `master` always run the suite and browser checks on Linux, Windows and macOS. Changes that touch only `docs/` or Markdown files skip the checks; a skipped job reports success. The workflow has a read-only token, no secrets and no caches, and never publishes; actions are pinned to commit SHAs and Go and Node to exact versions. Releases also build on GitHub Actions, below.

## Releases

Pushing an annotated tag such as `v0.2.0` or `v0.2.0-rc.1` runs the `Release` workflow on GitHub Actions (`.github/workflows/release.yml`). It checks the tag points to a `master` commit whose CI passed and whose version matches, rebuilds the frontend and checks it matches the committed embed, cross-compiles with the tag's version, and packages each target as `hclaro_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) with the license, README, changelog, security policy and third-party notices, plus a SHA-256 checksums file. It adds a CycloneDX SBOM, checks that each archive holds only the expected files with no credential-like content (`scripts/release-check`), checksum-verifies every package, runs it with `--version` and starts the Hub natively on all five targets ([what that proves](../support-matrix.md#platforms)), and attests build provenance for every file. Finally it creates a **draft** GitHub release with the archives, checks the uploaded files against the checksums, and stops: the maintainer publishes the draft after review. Tags with a suffix (`-rc.1`, `-beta.1`) are marked as pre-releases. Only the publish job can write, with the workflow's own `GITHUB_TOKEN`; no personal access token is used. See [docs/RELEASING.md](../RELEASING.md).

```sh
git tag -a v<version> -m "Hclaro <version>" && git push origin v<version>
```

Binaries are unsigned: macOS Gatekeeper and Windows SmartScreen will warn on first launch. See [Getting started](../GETTING_STARTED.md#2-install) for verification.
