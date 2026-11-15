# Real-engine qualification harness (M27)

`internal/qualify` runs the real `bin/hclaro` against real Terraform and OpenTofu binaries in temporary directories. It drives Hclaro over its HTTP API the same way the UI does. Only the real-engine scenario files (`realengine_*_test.go`) carry the `realengine` build tag. The harness core and the fake-engine smoke test are untagged, so normal CI compiles and runs them.

Nothing here applies, imports, destroys or refreshes by itself. A scenario that changes state asks Hclaro, which requires the reviewed saved-plan path. The fixtures use only the local backend, the built-in `terraform` provider, `hashicorp/random` and `hashicorp/local`. Engines and provider mirrors come from the M27.01 setup (`scripts/qualify/README.md`) and stay outside the repository.

## Running it

```sh
make qualify TF=/abs/terraform TOFU=/abs/tofu MIRROR=<mirror dir> OUT=<result dir>
```

- `make qualify` builds `bin/hclaro`, then runs `scripts/qualify/qualify.sh`.
- Pass at least one of `TF` and `TOFU`. With neither, the run is `NOT_TESTED` ("no engine configured").
- Optional variables:
  - `HCLARO_BIN`: the binary under test (default `bin/hclaro`).
  - `ISOLATION`: `sandbox-exec` (the macOS default), `proxy` or `none`.
  - `HCLARO_QUAL_KEEP=1`: keep the raw run directory when a run fails.
  - `RUN_FILTER`: passed to `-test.run` (default `^TestReal`).
- On Windows, run `scripts/qualify/qualify.ps1 -Terraform … -Tofu … -Mirror … -Out …`. It uses `ProcessStartInfo` with a cleared environment in place of `env -i`.

`qualify.sh` runs these steps in order:

1. It compiles the test binary (`go test -c -tags realengine`) and `netprobe` outside the isolation, while the network is available. The commit, the dirty flag, the real home path, the repository path and the username are compiled in with `-ldflags -X`, because neither git nor Go runs inside the isolation.
2. It copies the test binary, `hclaro`, `netprobe`, the engines, the mirror and `sandbox.sb` into `/tmp/hclaro-qualify.*`, outside the home directory. This is needed because `sandbox.sb` denies reads under the home directory.
3. It runs the `netprobe control` positive control outside the isolation.
4. It starts the test binary with `env -i` under `sandbox-exec -f sandbox.sb`. `RUN_TMP` is the run directory, the only place where provider plugins may open Unix sockets.
5. It copies `run.json`, the artifacts and the control result to `OUT`. It deletes the run directory unless `HCLARO_QUAL_KEEP=1` is set and the run failed. A kept directory is unsanitised and must never be copied into the repository.

The test binary receives only `PATH=/usr/bin:/bin` (on Windows `%SystemRoot%\System32` and `SystemRoot`) plus its own inputs. None of these is passed to Hclaro:

- `HCLARO_QUAL_TERRAFORM`, `HCLARO_QUAL_TOFU`
- `HCLARO_BIN`, `HCLARO_QUAL_MIRROR`, `HCLARO_QUAL_OUT`, `HCLARO_QUAL_ISOLATION`
- `HCLARO_QUAL_KEEP`

The harness's working root is a directory next to the test binary inside the run directory. It is not `TMPDIR`, so the Unix-socket allow in `sandbox.sb` covers every engine and provider process.

## Engines

- Each engine binary must match the OD-1 SHA-256 list compiled into `realengine_main_test.go`. Today that list has darwin_amd64 only: Terraform 1.5.5, 1.5.7 and 1.17.2, and OpenTofu 1.6.3 and 1.13.9. Any other binary is refused before anything runs.
- The run records the binary and archive SHA-256 and `provenance: verified`.
- Every Hclaro process gets its own `bin/` directory with at most one `terraform` and one `tofu`, both copies of the configured binaries. That directory is the only engine-bearing entry on its `PATH`.
- Launch mode always passes `--engine`. In Hub mode, each project is registered with an explicit `engine` preference.
- After every plan and inspection, the harness asserts that the engine name and version Hclaro reported equal the expected ones.
- One Hclaro process can hold only one version per engine name, so mixed-version Hub runs need a product change (FU-2).
- A Hub process with both engines needs one combined mirror config. It is not built yet (M27.05), and the harness refuses such a process.

## Environment allowlist

Hclaro and every direct engine call get exactly the variables below. Nothing is inherited from the shell. `CheckEnvironment` rejects any other name before a process starts, as well as any missing name and any credential-like name.

| Variable | Value |
| --- | --- |
| `PATH` | `<process>/bin:/usr/bin:/bin`; on Windows `<process>\bin;%SystemRoot%\System32;%SystemRoot%;…\WindowsPowerShell\v1.0` |
| `HOME` | `<process>/home` (empty) |
| `XDG_CONFIG_HOME`, `XDG_CACHE_HOME` (Unix) | under `<process>/home` |
| `USERPROFILE`, `APPDATA`, `LOCALAPPDATA` (Windows) | under `<process>\home` |
| `TMPDIR` (Unix); `TMP`, `TEMP` (Windows) | `<process>/tmp` |
| `HCLARO_CONFIG_DIR` | `<process>/config` |
| `TF_CLI_CONFIG_FILE` | the M27.01 mirror config for the engine family (`direct { exclude = ["*/*"] }`) |
| `CHECKPOINT_DISABLE` | `1` |
| `LANG`, `LC_ALL` | `C.UTF-8` |
| `TZ` | `UTC` |
| `SystemRoot`, `ComSpec`, `PATHEXT` (Windows) | copied from the host |
| `HTTPS_PROXY`, `HTTP_PROXY` | `http://127.0.0.1:9` (defence in depth only, never evidence of offline execution) |

Scenario-specific additions are declared when Hclaro starts and are recorded by name in `extraEnvironment`.

The harness fails fast, as `FAIL` with reason `environment`, if its own environment holds a name matching either of these patterns:

- `^(AWS_|ARM_|AZURE_|GOOGLE_|GCLOUD_|CLOUDSDK_|TF_TOKEN_|TFE_|VAULT_|DIGITALOCEAN_|HCLOUD_|OCI_|ALICLOUD_)`
- `(?i)(TOKEN|SECRET|PASSWORD|CREDENTIAL)`

## Direct engine calls

Everything else goes through Hclaro. `internal/qualify/direct.go` holds the only engine calls, and `TestDirectCallsAllowlisted` fails if a mutating subcommand appears there or if another file starts an engine.

| Allowed direct call | Purpose |
| --- | --- |
| `version -json` | Record the engine version |
| `init -input=false -no-color` (mirror config) | Setup when the scenario does not test init; recorded as `setupInit: direct` |
| `show -json`, `show -json <statefile>`, `state list`, `workspace show`, `workspace list` | Read-only verification |
| The F-2 lock holder (OD-9) and `plan -lock-timeout=0s` | Added with M27.03 A5 and M27.04 L1; not present yet |

## Isolation levels

The level is derived from probe results, never from the mechanism name. `IsolationLevel` uses the same criteria as `verify-mirror.sh`:

- The control, run outside the isolation, reached the OD-5 endpoints over IPv4 TCP and UDP.
- Inside the isolation, `netprobe isolated` passed every expected check in the process and in its grandchild, both before the first scenario and after the last. The checks are loopback (with the 5 s F0-LB retry), denied IPv4, IPv6 and UDP, a failing system resolver, and a failing home read.

| Level | When |
| --- | --- |
| `enforced` | `sandbox-exec` and all of the above held. darwin/amd64 only (SB-1). |
| `weak-proxy` | `ISOLATION=proxy`, or any probe missing or failing. Functional results only, "offline not verified". |
| `none` | `ISOLATION=none`. |

`credentialFilesReachable` comes from the home-read check (OD-14). It is `null` when no isolated probe ran.

## Results

`OUT/run.json` (`schemaVersion: 1`) is written atomically at the end. `run.partial.json` is updated after each scenario. M27.08 consumes this file.

- **Run fields:**
  - the run ID and times;
  - the Hclaro commit, dirty flag, version and binary SHA-256;
  - the OS, architecture, OS version, runner image and whether the filesystem is case-sensitive;
  - the engines;
  - the providers, re-hashed against `mirror-manifest.json`, and the manifest SHA-256;
  - the isolation mechanism, level and probe results (`control`, `isolatedBefore`, `isolatedAfter`);
  - `credentialFilesReachable`, the allowlist names, `goTestExit`, and `differences` from a reference run.
- **Scenario fields:**
  - `id`, `engine`, `mode` and `hubExecutionFlag`;
  - `status` and `reason`;
  - `steps`: the route, HTTP status, Hclaro error code and duration of each step;
  - `reviewedPlanPath`, `directEngineCalls`, `setupInit`;
  - `canary`, `cleanup` (temp dirs, processes, lock files), `artifacts`;
  - `projectEngines` and `observations`.

A scenario starts as `NOT_TESTED`. It becomes `PASS` only after it returns and its canary and cleanup checks hold. A panic, a timeout or `t.Fatal` gives `FAIL`, and so does `t.Skip` without a recorded reason. The test binary exits non-zero when any scenario is `FAIL`. A run stopped before any scenario still writes `run.json`, with one `preflight` record that is `FAIL` with reason `environment` or `setup`.

Before anything is written, absolute paths become `<TMP>`, `<HOME>` or `<REPO>`. The username and hostname become `<USER>` and `<HOST>`, matched as whole words, and the canary becomes `<CANARY>`. A final scan of `OUT` for the canary, the real home path and the username fails the run on a hit.

## Canary and filesystem checks

Each run plants `HCLARO-QUAL-CANARY-<random>` in the fixtures, in places Hclaro must never show:

- a sensitive variable and its var file;
- recorded-state values that the engine marks sensitive;
- a remote-state file.

A value that the engine reports as not sensitive may legitimately be displayed, so the canary is never put there.

After every scenario, the canary must be absent from:

- every response body and SSE event (`responses.raw`);
- Hclaro's stdout and stderr;
- every file under its config directory (`history.json`, operation logs, the registry).

Files the engine owns, such as state, saved plans and var files, are not scanned.

The scenario then passes only if all of these hold:

- No process runs from under the scenario directory. Engines and providers run from there, so this covers orphans.
- No `hclaro-plan-*` or `hclaro-drift-*` directory is left in Hclaro's `TMPDIR`.
- No `.terraform.tfstate.lock.info` file is left.
- No project file outside `.terraform/` changed unless the scenario allowed it.

On Unix, saved-plan directories must be `0700` and their files `0600` while they exist. On Windows, Hclaro sets no ACL of its own (`os.Chmod` only toggles the read-only bit). It relies on the per-user `TEMP` ACL, so the Windows check is only that nothing is left after shutdown.

## Self-test and deliberate faults

- `TestSmokeFakeEngine` runs one launch-mode plan and one Hub registration against `scripts/fake-engine` in normal CI on Linux, macOS and Windows. It produces no qualification evidence.
- `TestHarnessReportsFaults` re-runs the test binary as a child for each M27.02 fault and checks that the child exits non-zero with `FAIL` and the expected reason. The faults are:
  - a panicking scenario;
  - a project registered with the wrong engine preference;
  - a fixture with `backend "s3"`;
  - a response that echoes the canary;
  - `AWS_SECRET_ACCESS_KEY` in the harness environment.
- A leaking Hclaro build is checked by hand on a scratch branch that is never merged: patch a response to echo the var files, build it, and run `HCLARO_BIN=<leaky build> make qualify …`. The expected result is `FAIL`, "canary found in responses".

## RE-01 reproduction

`TestRealRE01Plan` plans `fixtures/qualify/re01` with `prod.tfvars` through Hclaro. It runs once per configured engine. The fixture is a reconstruction from the [RE-01 description](terraform-1.5.5-local.md), because the original files were not kept.

The scenario asserts:

- 13 changes: 5 create, 2 update, 2 replace, 1 delete, 1 read and 2 no-op;
- both replacement orders;
- `delete_because_no_resource_config` and `read_because_dependency_pending`;
- the move kept as `previousAddress`;
- the canary is absent.

The recorded differences from RE-01 are the reconstruction itself and the missing `random_password` keeper canary. RE-01 used built-in resources only, and a provider resource would change the 13-change summary.

**What RE-01 tests here.** It tests that values the engine marks sensitive stay redacted: the sensitive variable and its var file, sensitive state attributes, sensitive outputs and the remote-state file. Hclaro's own independent secret detection stays active. A value the engine does not mark sensitive, and that Hclaro's detection does not recognise, is displayed. That is intended: the owner decision of 2026-11-15 is explicit reveal, not automatic visibility, and unmarked attributes are not hidden by default. A missing sensitivity flag is still never taken as proof that a value is safe, and the future classification work (provider schema sensitivity, explicit reveal) is out of M27.02.

**Fixture correction, kept distinguishable.**
- The first real run (2026-11-15, Terraform 1.5.5, `enforced`) was `FAIL`, "canary found in responses". Its evidence is preserved unchanged at `~/hclaro-qualify/evidence/m27-02-dev/run2/`, with `run.json` sha256 `71f63bb8…b13c`. The cause was the fixture: the deleted resource `terraform_data.retired` held the canary in a state value that was not marked sensitive, so Hclaro showed it in that change's detail. The raw response transcript of that run was not kept; only the sanitised results are.
- The fixture now marks that value sensitive (`sensitive_attributes`), which reads RE-01's "sensitive, deleted and output values" as a sensitive deleted value. Later runs with the corrected fixture pass, for example `~/hclaro-qualify/evidence/m27-02-2026-11-15/ref-tf155/` from a clean commit. Each run records the correction in `differences`.

## Limitations

- Enforced isolation exists for darwin/amd64 only (SB-1). Linux F-0 and Windows F-0/F-5 are `NOT_TESTED`, so runs there are `weak-proxy`.
- The Windows stop uses `CTRL_BREAK` to the Hclaro process group, which F-5 has not verified yet. `qualify.ps1` has not been run yet, and the fake-engine smoke test has not yet run in Linux or Windows CI. There are no cross-platform results; only darwin/amd64 has been exercised.
- **Timeout.** When the test binary hits `-test.timeout` (90 minutes in `qualify.sh`), Go panics and exits with status 2. `TestMain` does not finish, so there is no `run.json`. Only `run.partial.json` is left (`partial: true`, `goTestExit: -1`), holding just the scenarios that had already finished. The scenario that was running is not recorded, and its directory is not cleaned up. Nothing guarantees that its Hclaro or engine processes are stopped. `qualify.sh` then reports "no run.json written" and exits 1. M27.08 must treat such a run as invalid. Per-scenario operation timeouts (for example 15 minutes for a plan) are separate: they give that scenario `FAIL`, and the run continues.
- Leftover processes are sampled once after each scenario (`pgrep` on Unix, because macOS `ps` is setuid and the sandbox refuses it; `Win32_Process` on Windows). A process that starts and exits between samples is not seen.
- The fixture guard is a textual scan, not an HCL parser. It fails closed: JSON and override files, unknown sources, resource types and backends are refused.
