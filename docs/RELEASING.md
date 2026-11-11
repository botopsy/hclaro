# Releasing hclaro (draft)

hclaro releases are published by BotOpsy on the `botopsy/hclaro` GitHub repository. They are built by GitHub Actions (`.github/workflows/release.yml`) when a version tag is pushed, uploaded to a **draft** release, and published by the maintainer after review. Distribution is **GitHub Releases**, plus a source-build Homebrew formula in the `botopsy/homebrew-tap` tap; see [Distribution roadmap](#distribution-roadmap) for what comes later and under which conditions.

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

4. Re-run the GitHub settings checklist (`docs/audits/M24-github-settings-checklist.md`): no critical row may be FAIL or NOT_VERIFIED. Check the public-beta checklist (`docs/audits/M22-public-beta-checklist.md`) for open items.
5. Check that the release notes claim no engine, backend, provider or platform beyond the PASS rows of [support-matrix.md](support-matrix.md). Green CI is the mocked tier only.

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
5. Writes `hclaro_<version>_sbom.cdx.json`, a CycloneDX SBOM of the Go toolchain and modules and the npm packages bundled into the UI (`scripts/release-sbom.mjs`, the same list as `THIRD-PARTY-NOTICES.txt`; reproducible for a commit), included in the checksums file. The npm list is `bundledPackages` in `scripts/vendor-notices.mjs`; the frontend build fails if `web/package-lock.json` has a runtime package missing from it. Go modules come from `go list -m all` (none today besides the standard library).
6. Checks the release directory with `scripts/release-check` (also in every dry run): the checksums file lists every file exactly once and matches; each archive holds exactly the binary and the five documents above, owned by 0:0, with no other file (so no `tasks/`, `.tfvars`, state, plan, credential or test file) and nothing matching common credential formats (AWS access keys, private-key blocks, GitHub, Slack and Anthropic tokens); the SBOM is CycloneDX 1.5 for this version and lists the Go toolchain and every module recorded in each binary's build info.
7. `native-smoke`: verifies the checksum, runs `--version` and starts the Hub (UI over `127.0.0.1` answers 200) natively on all five targets: Linux amd64 and arm64, macOS arm64 and amd64 (Intel), Windows amd64. The full test suite runs only on Linux amd64, macOS arm64 and Windows amd64; see [Platforms](support-matrix.md#platforms).
8. Attests build provenance for every release file with GitHub artifact attestations (Sigstore; the publish job alone gets an OIDC token) and verifies each with `gh attestation verify` (non-blocking until it has passed on one real tag, since dry runs cannot attest), then creates a **draft** release (pre-release for any `-suffix` version) with generated notes and exactly the seven files, then downloads them back and checks them against the checksums. Only this job can write, with the workflow's own `GITHUB_TOKEN` (`contents: write`); no personal access token is used.

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

The checksums file is published next to the archives, so a match proves only that the download is complete and unchanged; anyone able to replace an archive could replace the checksums too. The attestation proves the file was built by this repository's `Release` workflow from the tagged commit. Neither is a code signature; operating systems still treat the binaries as unsigned.

What the provenance records: the attestation is an [in-toto](https://in-toto.io/) statement with the SLSA v1 provenance predicate, signed through Sigstore with the workflow's identity. It names each file's SHA-256 digest, the repository, the tag and commit, the workflow file and run, and the GitHub-hosted runner. It is generated by the build platform, not by the build steps, but hclaro makes no claim of a particular SLSA build level: the build and publish run in separate jobs of one workflow, and the build job is not isolated from the repository's own scripts. Inspect it with `gh attestation verify <file> -R botopsy/hclaro --format json`.

The SBOM lists what is compiled or bundled into the binary; it does not list build-only tools (Vite, TypeScript, the test runners) or GitHub Actions used by the workflow (those are pinned by commit SHA in `.github/workflows/`).

## Not provided

- Code signing, macOS notarization and Windows Authenticode. No signing credentials exist and the release needs none (no Apple Developer membership, no certificate); binaries are unsigned and the README says so. Go's linker gives macOS arm64 binaries an ad-hoc signature, which the OS requires but which identifies no publisher. See [Future code signing](#future-code-signing).
- Other package managers (Scoop, apt, …), installers and auto-update. See below.

## Homebrew formula

`packaging/homebrew/hclaro.rb` is a source-build formula: it downloads the tag's GitHub source archive (pinned by SHA-256), builds `cmd/hclaro` with the Homebrew Go, and its test checks `hclaro --version`. The web UI is already committed in `internal/ui/dist`, so Node is not needed. A binary built by Homebrew is not marked as downloaded, so Gatekeeper does not block it; it is still unsigned.

The canonical copy lives here; the public tap [`botopsy/homebrew-tap`](https://github.com/botopsy/homebrew-tap) (created 2026-11-11 by owner decision) carries the same file as `Formula/hclaro.rb`, and users install with `brew install botopsy/tap/hclaro`. The tap's default branch has the same ruleset as `master` here except required checks (no deletion, no force-push, pull request required, no bypass), a read-only Actions token, and secret scanning with push protection.

For each release, after the tag is published:

```sh
v=0.1.0-beta.4   # the new version
curl -fsSL https://github.com/botopsy/hclaro/archive/refs/tags/v$v.tar.gz | shasum -a 256
# put the URL and that SHA-256 into packaging/homebrew/hclaro.rb, then test from a throwaway local tap:
brew tap-new --no-git hclaro-test/local && brew trust hclaro-test/local
cp packaging/homebrew/hclaro.rb "$(brew --repository)/Library/Taps/hclaro-test/homebrew-local/Formula/"
brew style hclaro-test/local/hclaro && brew audit --strict --online hclaro-test/local/hclaro
brew install --build-from-source hclaro-test/local/hclaro && brew test hclaro-test/local/hclaro
brew uninstall hclaro && brew untap hclaro-test/local
```

Then open a pull request in `botopsy/homebrew-tap` that copies the tested file to `Formula/hclaro.rb`, merge it, and check `brew install botopsy/tap/hclaro` prints the new version. Rollback: revert that commit in the tap and say so in the release notes.

Do not set `HOMEBREW_NO_INSTALL_FROM_API` for this: Homebrew would clone all of `homebrew-core` first.

## Future code signing

Not implemented: no signing job, secret or placeholder exists. Adding signing needs the owner's decision and accounts, and would change the release like this:

- **macOS Developer ID signing**: a paid Apple Developer Program membership and a Developer ID Application certificate. Signing runs on a macOS runner (`codesign --options runtime --timestamp`) because the current cross-compile on Linux cannot sign; the checksums and attestations must then cover the signed binaries, not the unsigned ones.
- **Apple notarization**: submit each signed archive with `xcrun notarytool` using an App Store Connect API key, and wait for acceptance. A bare CLI binary inside a `.tar.gz` cannot be stapled, so Gatekeeper checks the ticket online on first launch.
- **Windows Authenticode**: an OV/EV code-signing certificate, usually held in a cloud HSM or signing service, used by `signtool` (or the service's tool) with an RFC 3161 timestamp on a Windows runner.
- **Credentials in CI**: kept only in a GitHub environment with required reviewers that only the publish path can use, never available to pull requests, rotated on maintainer change, with revocation steps written down here.
- **Verification**: the release smoke test checks `codesign --verify --strict` and `spctl --assess` on macOS and `Get-AuthenticodeSignature` on Windows; the docs then replace the Gatekeeper and SmartScreen instructions.

## Distribution roadmap

Each stage starts only when the conditions listed for it are met and written down here. None is scheduled.

1. **GitHub Releases (now).** Archives, checksums, SBOM and provenance attestations; users install and upgrade by hand ([GETTING_STARTED.md](GETTING_STARTED.md#2-install)). Hclaro never checks for or downloads updates.
2. **Homebrew tap (now, since 0.1.0-beta.3).** `brew install botopsy/tap/hclaro` builds from source ([Homebrew formula](#homebrew-formula)); the owner moved this ahead of the first release without a pre-release suffix on 2026-11-11. Conditions that still apply: a named maintainer who updates the formula for every release; the formula pins the tag's source archive URL and its SHA-256 and is tested as above before it is pushed; a written rollback (revert the formula commit and say so in the release notes); the tap repository has the same protected-branch and token rules as this one.
3. **Scoop bucket, apt/rpm repositories, installers (`.pkg`, `.msi`).** Conditions: everything in stage 2, plus code signing for the platform concerned (installers in particular must not ask users to bypass Gatekeeper or SmartScreen), a key-management and rotation plan for any repository signing key, and someone able to answer platform-specific bugs.
4. **Auto-update: not planned.** It would need a trusted channel design first: signed update metadata verified against a key shipped in the binary, a user-visible opt-in, no network contact without it (Hclaro's local-first promise), and a downgrade/rollback rule consistent with [config-compatibility.md](config-compatibility.md). Until such a design is reviewed, no update check, notification or download is added.

Whatever the channel, configuration files follow [config-compatibility.md](config-compatibility.md): an older version never overwrites a file written by a newer one.
