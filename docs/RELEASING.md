# Releasing hclaro (draft)

hclaro releases are published by BotOpsy on the `botopsy/hclaro` GitHub repository. They are built and published by CircleCI (`.circleci/config.yml`, workflow `release`) when a version tag is pushed. Distribution is **GitHub Releases only**; a Homebrew tap is deferred until the first non-pre-release. Nothing in this document has been run for a beta tag yet.

## Version scheme

- Semantic versions with a pre-release suffix during the beta: `v0.1.0-beta.1`, `v0.1.0-beta.2`, … (`0.1.0-alpha.6` < `0.1.0-beta.1` in semver order). Any version with a suffix is published as a GitHub **pre-release**.
- The tag must match `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`; the release job refuses anything else.
- The version shown by `hclaro --version` comes from the tag (`make cross VERSION=…`). The default for local builds lives in four places, bumped together in one commit: `Makefile` (`VERSION ?=`), `cmd/hclaro/main.go`, `web/package.json` and `web/package-lock.json` (the root package entry).

## Before tagging

1. Work from an up-to-date `master` with green GitHub Actions `CI` checks on `master` (`linux`, `windows`, `macos`).
2. Bump the default version (see above) and move the `## Unreleased` entries in `CHANGELOG.md` under `## <version> — <date>`. Merge that through a pull request.
3. Run the full local suite from a clean checkout:

   ```sh
   git clone https://github.com/botopsy/hclaro.git hclaro-release && cd hclaro-release
   make install            # npm ci
   make lint               # go vet, tsc, eslint
   make test               # go test -race, vitest
   make build              # rebuilds internal/ui/dist and bin/hclaro
   git diff --exit-code -- internal/ui/dist   # the committed bundle must match
   make browser-test       # needs: npm --prefix web exec -- playwright install chromium
   make cross VERSION=0.1.0-beta.1
   ./dist/hclaro-$(go env GOOS)-$(go env GOARCH) --version
   ```

4. Check the public-beta checklist (`docs/audits/M22-public-beta-checklist.md`) for open items.

## Tag and publish

Only the BotOpsy maintainer does this, after approving the release:

```sh
git tag -a v0.1.0-beta.1 -m "Hclaro 0.1.0-beta.1"
git push origin v0.1.0-beta.1
```

CircleCI then:

1. Reruns the full suite and browser checks on Linux, Windows and macOS.
2. Rebuilds the web UI from empty caches and checks it matches the committed `internal/ui/dist`.
3. Cross-compiles macOS amd64/arm64, Linux amd64/arm64 and Windows amd64 with `CGO_ENABLED=0 -trimpath -buildvcs=false`.
4. Packages `hclaro_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows) containing the binary, `LICENSE`, `README.md`, `SECURITY.md`, `CHANGELOG.md` and `THIRD-PARTY-NOTICES.txt`, with fixed modification times and owners, plus `hclaro_<version>_checksums.txt` (SHA-256).
5. Verifies the checksum and runs `--version` natively for Linux amd64, macOS arm64 and Windows amd64. Linux arm64 and macOS amd64 are cross-compiled only.
6. Creates a **draft** GitHub release with generated notes, uploads every file, then publishes it. This job alone uses `GITHUB_RELEASE_TOKEN` from the `hclaro-release` context (fine-grained token, this repository only, **Contents: read and write**).

## After publishing

- Download one archive, verify it with the checksums file and run it on a real machine.
- Edit the release notes if the generated ones are unclear; link `docs/BETA.md`.
- If a release is broken, publish a new version. Do not move or reuse a tag.
- Delete leftover **draft** releases from the Releases page.

## Reproducibility

For a given commit, Go toolchain and target, `make cross` produces identical binaries: no cgo, no VCS stamping, trimmed paths, stripped symbols and a version passed by flag. Archives use the commit time for file times and numeric owner 0. The Vite bundle is deterministic for the same `web/package-lock.json`. To compare, rebuild at the tag with the Go version pinned in `.circleci/config.yml` (`go-version`) and compare SHA-256 sums.

## Not provided

- Code signing, macOS notarization and Windows Authenticode. No signing credentials exist; binaries are unsigned and the README says so.
- Package managers (Homebrew, Scoop, apt, …), installers and auto-update.
- SBOMs and build provenance attestations (possible future work).
