# Releasing hclaro (draft)

hclaro releases are published by BotOpsy on the `botopsy/hclaro` GitHub repository. They are built by GitHub Actions (`.github/workflows/release.yml`) when a version tag is pushed, uploaded to a **draft** release, and published by the maintainer after review. Distribution is **GitHub Releases only**; a Homebrew tap is deferred until the first non-pre-release. Nothing in this document has been run for a beta tag yet.

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
   make cross VERSION=0.1.0-beta.2
   ./dist/hclaro-$(go env GOOS)-$(go env GOARCH) --version
   ```

4. Check the public-beta checklist (`docs/audits/M22-public-beta-checklist.md`) for open items.

## Tag and publish

Only the BotOpsy maintainer does this, after approving the release:

```sh
git tag -a v0.1.0-beta.2 -m "Hclaro 0.1.0-beta.2"
git push origin v0.1.0-beta.2
```

The `Release` workflow then:

1. **Validates** (read-only): the tag is annotated and a semantic version; it points to a commit on `master`; `Makefile` and `cmd/hclaro/main.go` carry the same version; the latest `CI` runs of `linux`, `windows` and `macos` on that commit succeeded (the release does not rerun the suite); and no release exists for the tag. Any failure stops the release before anything is built.
2. Rebuilds the web UI from empty caches and checks it matches the committed `internal/ui/dist`.
3. Cross-compiles macOS amd64/arm64, Linux amd64/arm64 and Windows amd64 with `CGO_ENABLED=0 -trimpath -buildvcs=false`.
4. Packages `hclaro_<version>_<os>_<arch>.tar.gz` (`.zip` for Windows) containing the binary, `LICENSE`, `README.md`, `SECURITY.md`, `CHANGELOG.md` and `THIRD-PARTY-NOTICES.txt`, with fixed modification times and owners, plus `hclaro_<version>_checksums.txt` (SHA-256). The job summary lists the toolchain, commit and checksums.
5. Writes `hclaro_<version>_sbom.cdx.json`, a CycloneDX SBOM of the Go toolchain and modules and the npm packages bundled into the UI (`scripts/release-sbom.mjs`, the same list as `THIRD-PARTY-NOTICES.txt`; reproducible for a commit), included in the checksums file.
6. Verifies the checksum and runs `--version` natively on all five targets: Linux amd64 and arm64, macOS arm64 and amd64 (Intel), Windows amd64.
7. Attests build provenance for every release file with GitHub artifact attestations (Sigstore; the publish job alone gets an OIDC token), then creates a **draft** release (pre-release for any `-suffix` version) with generated notes and exactly the seven files, then downloads them back and checks them against the checksums. Only this job can write, with the workflow's own `GITHUB_TOKEN` (`contents: write`); no personal access token is used.

Then review the draft on the Releases page and publish it:

```sh
gh release edit v0.1.0-beta.2 --draft=false
```

A release is never overwritten. If the publish job fails after creating the draft, delete the draft on the Releases page and rerun the failed job. A dry run (`gh workflow run release.yml -f version=0.0.0-dryrun`, or any pull request that changes the workflow) validates, builds and smoke-tests without publishing.

## After publishing

- Download one archive, verify it with the checksums file and run it on a real machine.
- Edit the release notes if the generated ones are unclear; link `docs/BETA.md`.
- If a release is broken, publish a new version. Do not move or reuse a tag.
- Delete leftover **draft** releases from the Releases page.

## Reproducibility

For a given commit, Go toolchain and target, `make cross` produces identical binaries: no cgo, no VCS stamping, trimmed paths, stripped symbols and a version passed by flag. Archives use the commit time for file times and numeric owner 0. The Vite bundle is deterministic for the same `web/package-lock.json`. To compare, rebuild at the tag with the Go version pinned in `.github/workflows/release.yml` (`go-version`) and compare SHA-256 sums.

## Verifying a download

```sh
shasum -a 256 -c hclaro_<version>_checksums.txt --ignore-missing      # sha256sum on Linux
gh attestation verify hclaro_<version>_darwin_arm64.tar.gz -R botopsy/hclaro
```

The attestation proves the file was built by this repository's `Release` workflow from the tagged commit. It is not a code signature; operating systems still treat the binaries as unsigned.

## Not provided

- Code signing, macOS notarization and Windows Authenticode. No signing credentials exist; binaries are unsigned and the README says so.
- Package managers (Homebrew, Scoop, apt, …), installers and auto-update.
