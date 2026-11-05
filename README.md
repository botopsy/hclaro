# Hclaro

A local, read-only workspace for Terraform and OpenTofu. Browse recorded resource instances, nested modules, and provider sources; search canonical addresses and inspect redacted attributes. No accounts, telemetry, external runtime assets, or infrastructure mutations.

**Milestone 4 alpha (draft).** Browse state, run a plan and review every proposed change, plan the replacement of selected instances, import an existing object through a reviewed import block, and apply a reviewed saved plan after explicit confirmation (local execution only). Start with the synthetic demo. See verification status for [M1](docs/M01-verification.md), [M2](docs/M02-verification.md), [M3](docs/M03-verification.md) and [M4](docs/M04-verification.md). Apply and import have not yet been run against a real engine. Real Terraform/OpenTofu certification is still open, so treat this as an alpha, not a release.

## Run the isolated demo

Go 1.26 or newer and Make are sufficient; the frontend is already built and embedded:

```sh
make demo
```

Open the private session URL printed in the terminal. This builds a fake CLI in `bin/mock`, selects it explicitly, and uses only the synthetic project, backend metadata, and 316-resource fixture. It never runs an installed Terraform/OpenTofu executable. Ctrl+C stops the server. Your environment must permit local TCP listeners and a browser.

Do not install the fake executable on your normal PATH.

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

## Use with an existing project

```sh
./bin/hclaro
./bin/hclaro --path ./infrastructure/prod
./bin/hclaro --port 8080
./bin/hclaro --engine terraform
./bin/hclaro --engine tofu
./bin/hclaro --no-browser
./bin/hclaro --help
./bin/hclaro --version
```

Hclaro inspects only the selected directory; it never searches parent directories. Terraform is preferred when both CLIs are on PATH. The port is dynamically allocated unless supplied. The browser opens automatically; if it does not, use the printed URL. Exit codes: 0 success/help/version, 1 startup failure, 2 invalid arguments.

Launch runs only `version -json`, `workspace show`, and `show -json`. It never runs init, plan, refresh, apply, destroy, import, replace, or state modifications. A plan runs only when you confirm it in the UI (see [Plans](#plans)). Hclaro never runs `terraform import`/`tofu import`; imports use import blocks and a reviewed plan (see [Import](#import)). Reading state can contact the configured remote backend and load installed provider plugins. Use an initialized project and your existing credentials/environment; Hclaro does not resolve or store credentials itself.

The UI displays **one recorded snapshot**, not live provider inventory or drift detection. Browsing, searching, filtering, sorting, and opening details read process memory. Restart Hclaro to reread state after an external change. Inherited `TF_CLI_ARGS` settings are accepted only when they contain `-no-color`; other injected flags fail explicitly because they can redirect raw output or alter reads. `TF_DATA_DIR`, `TF_WORKSPACE`, CLI configuration, and credential settings are inherited. `TF_LOG*` and update checks are disabled for child reads to prevent secret-bearing log files and update telemetry.

The private URL contains a random launch credential in its **fragment**, which is removed by the app before API use. Keep that URL private. The browser stores the credential in sessionStorage for reloads; state is never placed in localStorage. Only theme, density, and grouping preferences use localStorage. A stopped/restarted server requires its new URL.

## Explorer

- Resource table with 50-row pages, address sorting, and composable provider/type/mode/module filters.
- Hierarchical modules with direct resource counts; data sources remain distinct from managed resources.
- Inspector with canonical address, provider source, module, instance key, nested JSON, and copy actions.
- Overview with resource, data source, child-module and provider counts and distribution by type.
- Light/dark/system themes, compact density, and flat/module grouping. Cmd/Ctrl+K focuses resource search; arrow keys navigate resource buttons; Escape closes details. Tab and native disclosure controls navigate modules and JSON.


## Plans

Open **Plans** and choose **Run plan**. A preflight dialog explains that the engine will contact providers and the backend with your existing credentials, may lock the state, and will not change infrastructure. You can add optional variable files from the project root; `terraform.tfvars` and `*.auto.tfvars` load automatically as usual.

Hclaro then runs `plan -input=false -json -out=<private temp file>` and streams a sanitized progress log. You can cancel at any time: the engine is interrupted so it can release its lock. Afterwards Hclaro reads the saved plan with `show -json`, records its SHA-256, and deletes it.

The review shows:

- Counts per action (create, update, replace, delete, read, forget, unknown, no change), which also filter the list. Unknown action combinations are flagged as potentially destructive.
- Every resource change in engine order, including collateral deletions, data reads, moves, imports and deposed objects. Unchanged resources are behind a toggle.
- Per change: the full address, the replacement order (destroy-then-create or create-then-destroy), the reason, and an attribute diff marking redacted, sensitive, `known after apply`, and `forces replacement` values. A change to a redacted value is still listed.
- Output changes and changes made outside Terraform/OpenTofu (drift).
- Metadata: engine, workspace, variable files, plan SHA-256, and a warning when configuration changed since the plan.

Planning is available only for local execution (remote/cloud backends are refused) and one operation runs at a time. **Operations** lists the session's plans and applies with status, duration, exit code and their logs. History lives in memory only.

## Replace and apply

In **Resources**, tick the checkbox of one or more managed instances (up to 50; data sources cannot be selected) and choose **Plan replacement…**. Hclaro runs a full plan with one `-replace=<address>` argument per instance, using the exact address from state. It never adds `-target`. The review lists the requested instances, flags any the plan does not actually replace, and counts every other change in the plan, including deletions and replacements you did not select. Replacing destroys and recreates objects; Hclaro does not promise zero downtime.

Any plan, not only a replacement, can be applied when all of these hold:

- the workspace uses local execution (remote/cloud execution stays plan-only);
- the plan is not errored, incomplete or deferred, and has changes;
- it is the most recent applicable plan, less than one hour old;
- configuration, variable files, the lock file, the engine version and the workspace are unchanged since planning.

Only then is the saved plan kept, in a private `0700` temp directory. **Review and apply…** asks the server for a one-time confirmation valid for five minutes, then shows the action counts, the exact command and the plan digest. Plans that delete, replace or forget anything require typing `apply <workspace>`. Hclaro then re-checks the workspace and the file's SHA-256 and runs `apply -input=false -json <saved plan>`. A saved plan never prompts, so this dialog is the only confirmation. Closing it applies nothing.

The saved plan is deleted after one apply attempt, whatever the result, and also when it expires, when a newer plan replaces it, and when Hclaro stops. There is no automatic re-plan: if the engine reports the plan as stale, run a new one. After an apply, Hclaro re-reads state and reports the outcome separately from the process status: **applied**, **not applied**, or **uncertain** when an apply failed or was interrupted after starting.

## Import

**Import** brings an object that already exists outside Terraform/OpenTofu into state. Enter the resource instance address (managed resources only, with its count index or for_each key if the block uses one), the provider's import ID, an optional provider reference such as `aws.west`, and the name of a **new** file in the root module. **Check and preview** writes nothing and runs nothing. It reports:

- the address is already in state, or an object of the same type with that `id` is already managed elsewhere (both block the import);
- a missing resource block (blocks the import unless the engine generates one), or a block that could not be verified;
- an instance key that does not match the block's `count`/`for_each` (warning; the check is textual, the plan is authoritative);
- an existing import or moved block for the same address, an existing destination file, override file names, and import IDs that look like credentials (all block the import).

The preview shows the exact new file. **Write file and plan import** needs a separate consent checkbox. Hclaro then writes the file without overwriting anything (temporary file, then a hard link that fails if the name exists; symlinks and a swapped project directory are refused) and runs a full plan, exactly like **Run plan**: you review the import together with every other change the plan contains. Applying uses the normal apply dialog. Because an import writes state, it always requires typing `apply <workspace>`.

The engine can generate the resource block (`-generate-config-out`) for root-module addresses without an instance key when no block exists. It writes a second new file, `<name>_generated.tf`, during the plan. The review shows a redacted copy of it before anything can be applied; the file on disk contains the values the engine read from the object, so review it before committing it.

Hclaro never deletes files you approved. After a failed plan or apply the review says how to retry (fix the file, then run any plan: plans include import blocks in your configuration) or back out (delete the file). When importing through Hclaro is unavailable, for example for remote execution backends, the wizard still checks the request and shows a manual recipe: the import block plus `plan -out`, `show` and `apply` of the saved plan. The legacy `import` command is shown only as a labeled alternative because it writes state without a reviewable plan.

## Development

```sh
make install
make dev
```

This starts the **synthetic** backend at `127.0.0.1:9273` and Vite at `127.0.0.1:5173`; open the printed private URL. Vite proxies only same-machine, validated read-only requests; the backend still requires authorization. Neither server enables wildcard CORS. Production uses only the Go server and embedded assets.

`make browser-test` builds an isolated HTTP fixture helper and runs Playwright against the real Go handler using intercepted requests. Install a browser first (`cd web && npx playwright install chromium`; Playwright is held at 1.58 because newer releases do not support macOS 13), or set `HCLARO_CHROME` to an installed Chrome executable. It checks rendering, copying, filters, themes, tablet overflow, secret boundaries, and a 1,000-instance search, and writes screenshots into `docs/screenshots/`. It never reaches a real engine or cloud account.

`scripts/build-offline-preview.mjs` is a legacy fallback for environments without npm access. It produces a labeled preview with substitute adapters, not the production build; do not commit its output.

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

Cross-compilation is not OS runtime validation. Release signing, installers, and package-manager distribution are not provided.

### Releases

Pushing a tag such as `v0.2.0` or `v0.2.0-alpha.1` runs `.github/workflows/release.yml`. It reruns the full CI suite on all three OSes, rebuilds the frontend and checks it matches the committed embed, cross-compiles with the tag's version, and packages each target as `hclaro_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) with the README, changelog, security policy and third-party notices, plus a SHA-256 checksums file. Linux amd64, macOS arm64 and Windows amd64 packages are then checksum-verified and run with `--version` on native runners; Linux arm64 and macOS amd64 are cross-compiled only. Finally it creates a GitHub release with the archives attached. Tags with a suffix (`-alpha.1`, `-rc.1`) are marked as pre-releases.

```sh
git tag v0.1.0-alpha.2 && git push origin v0.1.0-alpha.2
```

Binaries are unsigned: macOS Gatekeeper and Windows SmartScreen will warn on first launch. Verify downloads with `shasum -a 256 -c hclaro_<version>_checksums.txt --ignore-missing`.

## Compatibility and troubleshooting

The adapter accepts stable Terraform **1.5–1.17** and OpenTofu **1.6–1.13**, using the documented common `show -json` interface and JSON state format major version 1. Prereleases, other engine major versions, plan JSON, duplicates, and malformed records fail closed. Tests use synthetic official-format output and fake executables. Terraform 1.5.5 has also been run for real against a disposable local project (local backend, built-in resources only); no cloud provider, remote backend or OpenTofu release has been certified. Remote/cloud backends are read-only, with execution mode shown explicitly. An initialized project with no `backend` or `cloud` block uses the default local backend; otherwise absent metadata remains unknown.

| Situation | What to do |
| --- | --- |
| Missing CLI | Install a supported engine on PATH or select `--engine`; restart. |
| Not a project | Use `--path` pointing directly to a root with `.tf`, `.tofu`, or state files. |
| Uninitialized | Run the engine's `init` yourself after reviewing the project; restart. Hclaro never initializes it. |
| Workspace error | Check selection/initialization with the CLI; Hclaro never invents `default`. |
| State unavailable | Check credentials, SSO/VPN/backend availability and matching provider schemas manually. Raw CLI errors are withheld because they may contain secrets. |
| No snapshot / empty snapshot | These have separate states. Hclaro does not create or refresh state. |
| Locked session | Open the latest private URL from the running terminal. |
| Occupied port | Choose another `--port`, or omit the flag. |
| Loopback permission denied | Run in an environment permitting listeners on 127.0.0.1. |
| Plan unavailable | The Plans view states why: remote execution backend, unverified backend type, missing engine or workspace error. An unverified backend usually means a `backend` block that was not initialized: run `init` yourself, then restart. |
| State locked | Another operation holds the lock. Wait, or investigate with your CLI. Hclaro never force-unlocks. |
| Plan credentials/network errors | Refresh SSO or credentials in your terminal, check VPN, then run the plan again. |
| `TF_CLI_ARGS_plan` refused | Remove injected flags other than `-no-color`; they could change what a reviewed plan contains. |
| Redacted attributes | Values marked sensitive, secret-like attribute names (`password`, `token`, `private_key`, …) and credential-shaped or long random-looking values, including hex hashes, are masked on the server. There is no reveal/raw-state endpoint. |

Metadata (addresses, module paths, provider sources) must not be used to store secrets. Redaction relies on markers and heuristics: a secret that is not marked sensitive, has an ordinary name and does not look random can be shown. See [security policy](SECURITY.md), [architecture](docs/architecture.md), and [M01 evidence and unresolved gates](docs/M01-verification.md).
