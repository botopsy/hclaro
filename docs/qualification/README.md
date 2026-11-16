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
  - `ISOLATION`: `sandbox-exec` (the macOS default), `unshare` (Linux, see below), `proxy` or `none`.
  - `HCLARO_QUAL_KEEP=1`: keep the raw run directory when a run fails.
  - `RUN_FILTER`: passed to `-test.run` (default `^TestReal`).
  - `TF_ALT`, `TOFU_ALT`: a second OD-1 binary of the same engine, another version. Only M27.05 H6 uses it, to replace the engine at the same path; without it H6's binary case is `NOT_TESTED`.
- On Windows, run `scripts/qualify/qualify.ps1 -Terraform … -Tofu … -Mirror … -Out … [-Isolation firewall|proxy|none]`. It uses `ProcessStartInfo` with a cleared environment in place of `env -i`.
- Both runners create `~/hclaro-qualify/home-read-canary` if it is missing and fail if it is not readable outside the isolation, so a failed home read inside proves the denial (OD-14).

`qualify.sh` runs these steps in order:

1. It compiles the test binary (`go test -c -tags realengine`) and `netprobe` outside the isolation, while the network is available. The commit, the dirty flag, the real home path, the repository path and the username are compiled in with `-ldflags -X`, because neither git nor Go runs inside the isolation.
2. It copies the test binary, `hclaro`, `netprobe`, the engines, the mirror and `sandbox.sb` into `/tmp/hclaro-qualify.*`, outside the home directory. This is needed because `sandbox.sb` denies reads under the home directory.
3. It runs the `netprobe control` positive control outside the isolation.
4. It starts the test binary with `env -i` under `sandbox-exec -f sandbox.sb`. `RUN_TMP` is the run directory, the only place where provider plugins may open Unix sockets.
5. It copies `run.json`, the artifacts and the control result to `OUT`. It deletes the run directory unless `HCLARO_QUAL_KEEP=1` is set and the run failed. A kept directory is unsanitised and must never be copied into the repository.

The test binary receives only `PATH=/usr/bin:/bin` (on Windows `%SystemRoot%\System32` and `SystemRoot`) plus its own inputs. None of these is passed to Hclaro:

- `HCLARO_QUAL_TERRAFORM`, `HCLARO_QUAL_TOFU`, `HCLARO_QUAL_TERRAFORM_ALT`, `HCLARO_QUAL_TOFU_ALT`
- `HCLARO_BIN`, `HCLARO_QUAL_MIRROR`, `HCLARO_QUAL_OUT`, `HCLARO_QUAL_ISOLATION`
- `HCLARO_QUAL_KEEP`

The harness's working root is a directory next to the test binary inside the run directory. It is not `TMPDIR`, so the Unix-socket allow in `sandbox.sb` covers every engine and provider process.

## Engines

- Each engine binary must match the OD-1 SHA-256 list compiled into `realengine_main_test.go`. On darwin_amd64 that list has Terraform 1.5.5, 1.5.7 and 1.17.2, and OpenTofu 1.6.3 and 1.13.9. Any other binary is refused before anything runs.
- The M27.07 platforms (Terraform 1.17.2 and OpenTofu 1.13.9 on darwin_arm64, linux_amd64, linux_arm64, windows_amd64) are on the list too. Signed `SHA256SUMS` list archive hashes only, so their binary hashes came from real-engine workflow run 43753465542, where each binary was extracted from its pinned, signature-checked archive (`fetch-engines.sh` `binary=`). That run's `preflight` records were `FAIL` ("not on the OD-1 list"), as intended.
- The run records the binary and archive SHA-256 and `provenance: verified`.
- Every Hclaro process gets its own `bin/` directory with at most one `terraform` and one `tofu`, both copies of the configured binaries. That directory is the only engine-bearing entry on its `PATH`.
- Launch mode always passes `--engine`. In Hub mode, each project is registered with an explicit `engine` preference.
- After every plan and inspection, the harness asserts that the engine name and version Hclaro reported equal the expected ones.
- One Hclaro process can hold only one version per engine name, so mixed-version Hub runs need a product change (FU-2).
- A Hub process with both engines uses one combined mirror config, `scripts/qualify/hub.tfrc`: two `filesystem_mirror` blocks, each limited by `include` to its family's registry host (`registry.terraform.io` for Terraform, `registry.opentofu.org` for OpenTofu), and `direct { exclude = ["*/*"] }`. `verify-mirror.sh` with `COMBINED=1` runs every engine's `init` and provider start with that file. Hub scenarios record its SHA-256.

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
| F-2 lock holders (OD-9): `console -no-color` with stdin held open (Terraform 1.5.x, OpenTofu); `plan -input=false -no-color` with `TF_VAR_hold=true`, blocked on a named pipe read by a gated `data "local_file"` (Terraform 1.17.2, Unix only) | M27.03 A5; long-running, started by `startHolder`, released by closing stdin or writing the pipe. Recorded as `lock holder: <command>` in `directEngineCall` |
| `plan -lock-timeout=0s` | Not used: M27.04 L1 shows the holder's lock through the lock-info file and Hclaro's own `state_locked` |

## Isolation levels

The level is derived from probe results, never from the mechanism name. `IsolationLevel` uses the same criteria as `verify-mirror.sh`:

- The control, run outside the isolation, reached the OD-5 endpoints over IPv4 TCP and UDP.
- Inside the isolation, `netprobe isolated` passed every expected check in the process and in its grandchild, both before the first scenario and after the last. The checks are loopback (with the 5 s F0-LB retry), denied IPv4, IPv6 and UDP, a failing system resolver, and a failing home read.

| Level | When |
| --- | --- |
| `enforced` | A mechanism other than `proxy`/`none`, and all of the above held. Ratified for `sandbox-exec` on darwin/amd64 (SB-1); every other platform is judged from its own probes, never inferred. |
| `weak-proxy` | `ISOLATION=proxy`, or any probe missing or failing. Functional results only, "offline not verified". |
| `none` | `ISOLATION=none`. |

Mechanisms (OD-4, OD-4-M27.07):

| `ISOLATION` | Platform | What it does |
| --- | --- | --- |
| `sandbox-exec` | macOS | `scripts/qualify/sandbox.sb`: outbound denied except loopback and Unix sockets under the run directory; network daemons' mach-lookup denied; reads under the real home denied. |
| `unshare` | Linux | `sudo -n unshare --net --mount`: a network namespace with only `lo` up, the real home covered by an empty mode-000 tmpfs, then `setpriv` back to the invoking uid and gid before `env -i` starts the test binary. Needs passwordless sudo (GitHub-hosted runners). |
| `firewall` | Windows (`qualify.ps1`) | Per-program outbound block rules, in one rule group, for every executable in the run directory; the group is deleted afterwards and its absence checked. The rules cannot name the per-process engine copies or the provider plugins created during the run, and the home stays readable, so the probes are expected to give `weak-proxy`. |

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

Before anything is written, absolute paths become `<TMP>`, `<HOME>` or `<REPO>`. The username and hostname become `<USER>` and `<HOST>`, matched as whole words, and the canary becomes `<CANARY>`. Hclaro session tokens become `<SESSION>` in two ways: every `#session=`, `?session=` or `&session=` followed by 16 or more hex digits is replaced, and each process's own token is registered when it starts, so it is replaced wherever it appears. A final scan of `OUT` for the canary, the real home path, the username and session tokens fails the run on a hit, and removes the file.

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

## M27.03 launch scenarios

`realengine_launch_test.go` holds the M27.03 scenarios (task file `tasks/M27/tasks/M27-03-LAUNCH-APPLY-IMPORT-INIT.md`), once per configured engine. A run holds at most one `terraform` and one `tofu`, so `CORE` takes two runs: Terraform 1.5.5 with OpenTofu 1.13.9, then Terraform 1.17.2.

| Test | Scenarios | Fixture |
| --- | --- | --- |
| `TestRealLaunchApply` | A1 apply, A2 destructive (typed phrase), A3 stale plan (edited before the preflight, and after the approval), A4 superseded plan, R1 replace (1, 50, and 51 refused) | `launch` |
| `TestRealLaunchImport` | I1 import, I2 collision (ID equal to the state id), I3 generated configuration | `launch-import` |
| `TestRealLaunchHub` | N1 init from the launch session (only when isolation is `enforced`; the launch view is read again), W1 workspaces with per-context reads (Hub), W3 the launch session refuses to plan after the CLI default changes, W2 inherited `TF_CLI_ARGS_workspace_delete=-force` | `providers`, `launch` |
| `TestRealLaunchOptions` | A5 apply options and lock wait | `launch-lock` |
| `TestRealLaunchGate` | A6 cancel during apply, A7 Hclaro shutdown during apply (phases P1–P5), A7b shutdown with the engine outliving the grace period | `launch-gate` (OD-8) |

Notes on how the scenarios observe things:

- **Loopback retries.** Inside `sandbox-exec`, a loopback connect can fail with EPERM for up to about a second when a process is new or has been idle (F0-LB). The client retries such a connect for up to 5 s. Nothing was sent, so the retry is safe for any request. Each Hclaro process records its retry count as an observation.
- **A5 lock wait.** The engine prints its lock-wait message only after waiting about 400 ms. A5 (b) waits for that message in the apply's event stream, then releases the holder. If the message never arrives, an apply still running after 60 s while the holder holds the lock shows the wait instead, and the missing message is recorded as a failure (defect D-3).
- **I3 echo.** For `random_password` the import ID is the password, so I3 uses the canary as the ID. The import preview has to repeat the user's own input in the block and in the manual recipe. Only those two exact forms are masked before the canary scan; the generated configuration, the plan, history and logs stay under the check.
- **No canary path.** N1 (init reads no variables) and W2 (no plan runs) record that the canary is not on their path; "canary absent" there proves nothing.
- **Gated fixture (OD-8, approved with restrictions on 2026-11-15, for A6, A7 and A7b only).** The engine's `local-exec` runs the fixed script `fixtures/qualify/launch-gate/gate.sh` as `/bin/sh ./gate.sh normal|trap`. The script is deterministic, uses no network, touches only `<project>/.qual/`, and exits on release, on its INT trap (normal mode, exit 130) or on a hard timeout (120 s normal, 900 s trap). The fixture guard refuses any `provisioner` unless the project's `main.tf` and `gate.sh` are byte-identical to the approved fixture. The Windows variant is not implemented, so Windows records `NOT_TESTED`. Interrupt delivery is observed at the gate's trap marker and, as the second source, in the engine's "Interrupt received" log line. The SSE stream is read for A6; the history excerpt for A7 and A7b, because Hclaro is gone by then.
- **History outcome names.** History stores an apply whose operation outcome is `uncertain` (or a cancelled apply) as result `partial` (`internal/history/convert.go`). A7 and A7b accept `partial` as the task's "uncertain".
- **NOT_TESTED still checks cleanup.** A scenario stopped as `NOT_TESTED` still gets the canary and cleanup checks, so a leftover process or file makes it `FAIL`.
- **N1 isolation.** N1 runs once the isolated probe before the run has passed. If the probe after the run fails, a `PASS` becomes `NOT_TESTED` (offline not verified).

## M27.04 drift, graph, lock and failure scenarios

`realengine_drift_test.go` holds the M27.04 scenarios (task file `tasks/M27/tasks/M27-04-DRIFT-GRAPH-FAILURES.md`), once per configured engine, in the same two `CORE` runs as M27.03.

| Test | Scenarios | Fixture |
| --- | --- | --- |
| `TestRealDrift` | D1 `local_file` deleted, D2 edited, D3 no drift, D4 kept scan after a `main.tf` edit | `drift` (applied through Hclaro first) |
| `TestRealGraph` | G1 graph and plan overlay of the M26.16 recording project (`fixtures/engines/project`, synthetic state with orphans) and of a module-heavy project (`for_each` modules, nested modules, keyed instances; `retired.tf` is applied, then deleted, so its objects become orphans) | `engines/project`, `graph` |
| `TestRealLocks` | L1 `state_locked` for apply, plan and drift while the F-2 holder (OD-9) holds the lock; L2 `NOT_TESTED` (F-3) | `launch-lock` |
| `TestRealFailures` | F1 `plan_failed` (`file()` on a missing path from a var file), F2 `init_required` (providers removed after init; lock file changed; a never-initialized project), F3 `plan_too_large`, F4 `unknown_actions`, F5 F1 and F2 again under `LANG`/`LC_ALL=de_DE.UTF-8` | `failures`, `forget` |

- **Every drift scan** asserts a refresh-only `plan` in the engine argv, and that the state file's serial and bytes are unchanged afterwards, including the scan that fails on the lock.
- **D2** compares Hclaro with the engine's own refresh-only plan for the same edit, recorded as `fixtures/engines/<engine>/local-drift.json` and pinned by `TestRealEngineLocalFileDrift`. hashicorp/local 2.9.1 reports an edited file as deleted on every `CORE` engine, so no approved provider yields attribute-level drift.
- **G1** copies the M26.16 project with its test secret replaced by the run canary, so the standard canary scan covers it. It passes when no node is `unknown`, every resource and data node is `exact` (mapped to state) or `configuration`, and every confirmed change of the reviewed plan sits on exactly one node, the node of its own address with instance keys removed and with the plan's action, or in the overlay's unmapped list. Only orphans may be unmapped. Terraform 1.5.x lists orphans as graph nodes mapped to state; a graph without them leaves them unmapped.
- **L1** approves the apply's plan before the holder takes the lock. While the holder runs, the lock-info file must keep its SHA-256 and the engine diagnostics must carry "Lock Info". After the release the holder, never Hclaro, removes the lock. On Windows (FU-WIN-STATE-READ) the holder's lock also blocks reading `terraform.tfstate`. After the refused apply, the workspace must therefore be `state_locked` without planning. A plan and a drift scan must be refused with `409 state_locked` before any engine starts, with no new operation and the lock-info file unchanged. After the release, without **Read again**, the workspace must be read again on its own within 150 s (Hclaro's bound of about two minutes, plus one read), and then a plan must succeed. Unix assertions are unchanged.
- **F2, never initialized:** Hclaro does not run the engine for a project that needs init. The scenario asserts the project read's `uninitialized` diagnostic, which tells the user to run init, and records `NOT_TESTED`, because no engine output was classified. The engine's own missing-init text is pinned by the `local-noinit.jsonl` recordings (`init_required`). F2 `PASS` rests on the providers-removed and lock-file variants.
- **F1** asserts the diagnostic reaches Hclaro's output with the var-file path replaced (`no file exists at "[redacted]"`). The recording's path contains "SECRET", so `TestRealEngineFailureStreams` pins the other sanitizer outcome: the line is withheld.
- **F3 peak memory.** The bound is **512 MiB**, set before the first evidence run. The method is Hclaro's own peak, read while it still runs, after the oversized plan:
  - macOS: `proc_pid_rusage` `RUSAGE_INFO_V4` `ri_lifetime_max_phys_footprint`, the peak physical footprint, which is close to but not the same as peak RSS. This needs cgo, so `qualify.sh` builds the test binary with `CGO_ENABLED=1` on macOS only.
  - Linux: `VmHWM` from `/proc/<pid>/status`.
  - Windows: not measured, so F3 is `NOT_TESTED` there.
  - `getrusage` after exit is not used, because the `wait4` usage includes the reaped engine children: one dev run read 800 MiB that way, while Hclaro alone was about 230 MiB.
- **F4** needs an engine that plans `["forget","create"]`. The trigger found is a forced replacement under `lifecycle { destroy = false }`, which Terraform 1.17.2 and OpenTofu 1.13.9 accept. Hclaro marks the plan `unknown_actions` in its apply status, and the approval is refused at the preflight (`preflight_blocked:plan.eligible,…`), not with the literal code. An engine that rejects the fixture records `NOT_TESTED`, with the engine's diagnostic in the reason.
- **Recordings.** `scripts/fixtures/record-engine.sh <engine> <out> <mirror.tfrc>` also records `fixtures/engines/local-project` (provider from the offline mirror, synthetic state, never applied): `local-drift.json[l]`, `local-missing-file.jsonl`, `local-noinit.jsonl` and `local-lockfile.jsonl`. M27.04 ran it with `env -i` under `sandbox.sb`, with `TMPDIR` inside `RUN_TMP` so that provider sockets are allowed.

## M27.05 Hub execution scenarios

`realengine_hub_test.go` holds the M27.05 scenarios (task file `tasks/M27/tasks/M27-05-HUB-EXECUTION.md`). Every Hclaro process starts with `--hub --no-browser --experimental-hub-execution`, passed explicitly; each result records `hubExecutionFlag: true`. Each process gets both configured engines and the combined mirror config. The spec's two runs are Terraform 1.17.2 + OpenTofu 1.13.9, then Terraform 1.5.5 + OpenTofu 1.13.9.

| Test | Scenarios | Fixture |
| --- | --- | --- |
| `TestRealHubEngines` | H1 two projects (`terraform`, `tofu` preference) in one Hub: engine, version, plan and apply capability per scope, lock files from the combined config; H4 both projects plan at once | `hub-hold` |
| `TestRealHubFlows` | H2: A1, A2, A3, R1, I1, D1, G1 (both projects) in a Hub workspace view, per engine; H3 `default` and `qa` of one project | `launch`, `launch-import`, `drift`, `graph`, `engines/project` |
| `TestRealHubPaths` | H5a trailing separator, `./`, `..`; H5b symlink; H5c case-only (case-insensitive volume); H5c′ and H5d recorded `SKIPPED` where they do not apply | `hub-hold` |
| `TestRealHubChanges` | H6 change after approval (configuration, profile, engine preference, engine binary), H7 busy edits, H8 capacity, H9 closing a view, H11 import without stall, H12 non-local backend | `launch`, `hub-hold`, `hub-remote` |
| `TestRealHubShutdown` | H10 shutdown with applies running in two scopes | `launch-gate` (OD-8) |

- **Views.** `Hclaro.In` returns a handle whose launch routes (`plans`, `operations`, `imports`, `drift-scans`, `graphs`, `resources`) go to `contexts/{cid}/…`. The M27.03 and M27.04 scenario bodies run unchanged through it (H2). Starts send back the `contextFingerprint` the options returned. Every scoped options and operation response has its `scope` checked: same context and workspace, and the expected engine and version.
- **Holding an operation.** `hub-hold` reads `data.local_file.gate` from the named pipe `.qual/gate.fifo` when `hold.tfvars` sets `hold = true`. A plan or import plan then stays `running` until the harness writes the pipe. H4, H5, H7, H9 and H11 use this hold, not timing. It is a plan-time read with no `local-exec`, so it is outside OD-8. Windows has no named pipe, so these scenarios are `NOT_TESTED` there.
- **H4.** Both plans are seen `running` on their SSE streams while both are held, so they overlap. Hclaro's log events are its own summaries of the engine's `-json` output. The check is therefore that every status event belongs to that project's operation and carries that engine's argv, and that no event carries the other engine's.
- **H5.** Hclaro starts in launch mode on each path form, with the Hub flag. The canonical folder is registered in its Hub, and every form is refused with `duplicate_project` naming the same project. A launch plan is held, and the Hub plan on the folder is refused with `project_busy`. Once the hold is released, the Hub plan runs. The filesystem's case behaviour is recorded from F-4 and from the run's own probe.
- **H6 codes** follow `docs/architecture.md` (scoped execution):
  - configuration edited: `409 context_changed`;
  - plan applied through a view opened with another profile: `404 plan_not_found`;
  - the view's profile edited, or the project's engine preference changed: the view is closed, `404 context_closed`;
  - engine binary replaced at the same path: `409 scope_changed`, detail `engine_changed`.
  The state's bytes are unchanged after each refusal.
- **H10** (OD-8 extended to M27.05 H10 on 2026-11-15, same restrictions) holds applies at the gate in two Hub scopes, one per engine, in one process, then interrupts Hclaro once. M27.03 A7's phases are checked per scope: one notice in all, one interrupt per engine, each engine gone before Hclaro exits, each plan's own history entry, state and lock file, and no saved plan left. It needs both engines in the run.
- **H12** runs only under `enforced` isolation. `hub-remote` is the only fixture with a non-local backend, an `s3` block that is never initialized. `GuardRemote` accepts it only when it is byte-identical. The project is registered without the inspection. The view's read must end `uninitialized`; Hclaro reports the backend as `unknown` before init, so this is the uninitialized refusal, not a remote-backend-specific one. Every execution start (plan, drift scan, graph, import preview and import) must be refused, no operation or history entry may exist, and no `.terraform` directory may appear. Init is never started; its status is only read.

## M27.06 engine version edges

No new scenarios. M27.06 runs every `TestReal*` test (M27.02 to M27.05) once per pairing of the `EDGE` set. Each run has one Terraform and one OpenTofu, both as launch engines and as the Hub pair; the `TF_ALT`/`TOFU_ALT` binaries are only the H6 swap targets:

| Run | `TF` + `TOFU` | `TF_ALT` / `TOFU_ALT` |
| --- | --- | --- |
| `edge-tf157-tofu163` | Terraform 1.5.7 + OpenTofu 1.6.3 | 1.5.5 / 1.13.9 |
| `edge-tf155-tofu1139` | Terraform 1.5.5 + OpenTofu 1.13.9 | 1.17.2 / 1.6.3 |
| `edge-tf1172-tofu163` | Terraform 1.17.2 + OpenTofu 1.6.3 | 1.5.7 / 1.13.9 |

OpenTofu 1.6.3 runs twice, with different Terraform partners. A per-version result keeps that version's own launch scenarios, plus the Hub scenarios of every run it took part in, with the run named. Terraform 1.5.7 and OpenTofu 1.6.3 output is also recorded for the parser goldens (`recorded-engine-output.md`). A version that is not in the table is not tested, and is never inferred from an edge.

## Limitations

- Enforced isolation exists for darwin/amd64 only (SB-1). Linux F-0 and Windows F-0/F-5 are `NOT_TESTED`, so runs there are `weak-proxy`.
- The Windows stop uses `CTRL_BREAK` to the Hclaro process group, which F-5 has not verified yet. `qualify.ps1` has not been run yet, and the fake-engine smoke test has not yet run in Linux or Windows CI. There are no cross-platform results; only darwin/amd64 has been exercised.
- **Timeout.** When the test binary hits `-test.timeout` (90 minutes in `qualify.sh`), Go panics and exits with status 2. `TestMain` does not finish, so there is no `run.json`. Only `run.partial.json` is left (`partial: true`, `goTestExit: -1`), holding just the scenarios that had already finished. The scenario that was running is not recorded, and its directory is not cleaned up. Nothing guarantees that its Hclaro or engine processes are stopped. `qualify.sh` then reports "no run.json written" and exits 1. M27.08 must treat such a run as invalid. Per-scenario operation timeouts (for example 15 minutes for a plan) are separate: they give that scenario `FAIL`, and the run continues.
- Leftover processes are sampled once after each scenario (`pgrep` on Unix, because macOS `ps` is setuid and the sandbox refuses it; `Win32_Process` on Windows). A process that starts and exits between samples is not seen.
- The fixture guard is a textual scan, not an HCL parser. It fails closed: JSON and override files, unknown sources, resource types and backends are refused.

## Platforms (M27.07)

`.github/workflows/real-engine.yml` (OD-7) runs the `PLATFORM` engine set, Terraform 1.17.2 and OpenTofu 1.13.9, on `ubuntu-24.04` (linux/amd64, `unshare`), `ubuntu-24.04-arm` (linux/arm64, `unshare`), `macos-15` (darwin/arm64, `sandbox-exec`) and `windows-2025` (windows/amd64, `firewall`).

- It runs only on `workflow_dispatch`, and is never a required check. Its `platform` input is `all` (default) or `windows_amd64`; the latter runs only the Windows job, so a Windows recheck leaves the other platforms' earlier results as they are. It needs no secrets: the token has `contents: read`, and checkout does not persist credentials. Actions are pinned to commit SHAs. No dependency-update tool covers the file. `TestRealEngineTagNotInCI` enforces these rules.
- Each job does the following, in order:
  1. Checks that `gpg`, `jq`, `curl` and `unzip` are on the image (nothing is installed), and records the image (`ImageOS`, `ImageVersion`), OS build, architecture and tool versions in the job summary.
  2. Downloads the engines with `fetch-engines.sh --platform`, then builds the platform's provider mirror with `PLATFORM=… mirror-providers.sh`. Both hard-fail on a pin, checksum or signature mismatch.
  3. Runs `make qualify` (Windows: `qualify.ps1`). The runner label and image are compiled into `run.json` as `runnerImage`/`runnerImageVersion`.
- Only `OUT` (`run.json`, `run.partial.json`, the sanitised artifacts and, on Windows, `f5/run.json` and `qualify/firewall-rules.txt`) is uploaded. The raw run directory never is, and `HCLARO_QUAL_KEEP` is never set.
- `verify-mirror.sh` stays macOS-only. On the runners, the harness re-hashes the providers against `mirror-manifest.json`, and the isolated probes judge the isolation level.
- The Windows job also runs F-5 (`TestF5WindowsInterrupt`, fake engine only, `continue-on-error`). It sends `CTRL_BREAK` to Hclaro's process group, both while idle in Hub mode and during a slow fake apply in launch mode. It then records whether Hclaro shut down on its own, printed the shutdown notice, and interrupted the engine once instead of killing it. If the step has no console, both parts run first without one and then again after the test process calls `AllocConsole` (`-allocconsole` records), so the result names the arrangement that works.

Platform scenarios (`TestRealPlatform`, and H5d in `TestRealHubPaths`):

| ID | Where | What |
| --- | --- | --- |
| WIN1 | windows | On Windows, A6, A7 and A7b run on `fixtures/qualify/launch-gate-windows`. Its `main.tf` is the gated fixture with a `local-exec` that runs `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File ./gate.ps1`, with `GATE_MODE` and `PSModuleAnalysisCachePath=NUL` in the environment (without the latter, PowerShell wrote its module cache into the project in run 43755588532). `gate.ps1` registers a console control handler (C# compiled locally by `Add-Type`; its build files go to `TEMP`, inside the run directory). The handler records CTRL_BREAK/CTRL_C in `.qual/interrupted` like `gate.sh`'s INT trap. It then exits 130 in `normal` mode, or keeps polling in `trap` mode. The markers and timeouts are those of `gate.sh`. The guard accepts provisioners only when `main.tf` and `gate.ps1` are byte-identical to that fixture. WIN1 is `PASS` only if A6, A7 and A7b all `PASS` for its engine. A `FAIL` among them fails it; any other status makes it `NOT_TESTED`. The owner approved the PowerShell gate for WIN1 only, so H10 stays `NOT_TESTED` on Windows. `SKIPPED` elsewhere, where A6, A7 and A7b run on `gate.sh`. |
| WIN2 (H5d) | windows | Drive-letter case, the 8.3 short name (when the volume has one) and a `mklink /J` junction are each refused as the same project and claim the same root. The claim half uses the named-pipe hold, which is Unix-only, so it is `NOT_TESTED` until there is a Windows hold. |
| WIN3 | windows | Saved-plan folder and file ACLs, parsed from `icacls`: only the owner, SYSTEM, Administrators and CREATOR OWNER may hold allow entries. Saved plans are removed after the apply and after shutdown, with the read-only attribute set. Expiry is a separate `NOT_TESTED` record (`win3-plan-expiry`): the one-hour plan age has no setting to shorten it. |
| WIN4 | every platform | `.tf` and `.tfvars` files, the canary var file included, are rewritten with CRLF line endings before init. Then the I1 import (preview, plan, apply) and the I3 generated-configuration import (redaction) run. The engine writes the generated file itself; whether it uses CRLF is recorded, not asserted. |

### Lock-file evidence

Before asserting a lock file (H1, and the launch init check), the harness saves the following as artifacts, exactly as read:
- `.terraform.lock.hcl` (`lockfile-<engine>.hcl`);
- `mirror-manifest.json`;
- each provider's mirror `<version>.json`;
- `installed-<host>_<namespace>_<name>.txt`, which lists `sha256  path` for every file of the installed package.

It also records the engine version, platform, setup command, and the CLI config file name and SHA-256. For each provider it records the hashes observed in the lock file next to the manifest's. An `h1:` hash is the SHA-256 of these lines sorted by path, so it can be recomputed offline. On darwin/amd64 the recomputation matched all four lock-file hashes.

`mirror-providers.sh` passes the hashes it reads with `jq` through `norm_hashes` (`lib.sh`). That removes only a trailing CR: native `jq` on the Windows runner ends lines with CRLF, and run 43767550235 stored `"h1:…=\r"` in `mirror-manifest.json`. It then requires each value to be exactly `h1:` plus 44 base64 characters, or `zh:` plus 64 lowercase hex characters. Any other value fails the mirror build. `TestNormHashes` covers LF, CRLF and malformed values.

### L1 diagnostics

L1 records Hclaro's `GET workspace` response (status, diagnostic, capabilities) and `GET operations` (each engine invocation's argv and status) as artifacts, with UTC timestamps, at three points:
- after the refused apply, followed by one direct `show -json` (already on the direct-call list) while the holder still holds the lock: its sanitised error text goes to `show-under-holder.txt`, and a successful read is recorded only by its size (FU-WIN-STATE-READ step 0: the exact Windows text for Hclaro's post-apply state read). It is diagnostic only and asserts nothing;
- at a plan refusal, together with the refusal body;
- after the holder's release, on every exit path, with Hclaro's stdout and stderr.

On darwin/amd64 the workspace stays `ready` with `canPlan` true after the refused apply, for both engines. This was added to explain the Windows `409 planning_unavailable` of run 43767550235.

### Dispatch manifest

After a dispatch, its artifacts are downloaded unchanged (`gh run download <id>`) into one directory per dispatch, and the job log is saved there as `run-<id>.log`. Then `scripts/qualify/dispatch-manifest.sh <dir> <id> <commit>` writes `<dir>/manifest.json`:

- The workflow run ID, the commit and the job log's SHA-256.
- One entry per platform (`qualify`, and on Windows `f5`), with:
  - the runner image and version, OS build and architecture, and filesystem case sensitivity;
  - the Hclaro binary hash;
  - each engine's version, binary and archive hash, and provenance;
  - the mirror manifest hash;
  - the isolation mechanism, level and reason, and `goTestExit`;
  - status counts, and every scenario's status and reason, grouped by engine;
  - every file of that platform's artifact, with its SHA-256.

Each platform's `run.json` and artifacts stay in their own directory; the manifest only links them. The script fails if a `run.json` is missing, partial, dirty or from another commit.

### Evidence file and matrix rows (M27.08)

`go run ./scripts/qualify/evidence-doc -root <evidence root> -runs docs/qualification/real-engine-runs.json -out docs/qualification/real-engine-<yyyymmdd>.md` validates every listed `run.json` and writes one section per run. Matrix rows go to stdout, with `?` in place of the ID. Nothing is written while any run is refused. A run is refused when it is missing a run field (checked on the raw JSON keys, so a missing `hclaroDirty` does not read as `false`), or is partial or dirty. It is also refused for a non-zero `goTestExit` without a `FAIL` scenario, a canary other than `absent`/`not_checked`, or a path into a user folder (including Windows 8.3 short forms). Finally, `ScanOut` runs over its whole result directory with the local username, home and hostname. A run that is a source of rows also needs every engine `verified`. Rows are grouped per engine version (launch scenarios) and per engine pair (scenarios with the Hub flag). Each group gives a PASS, FAIL and NOT_TESTED row as present, with SKIPPED counted as NOT_TESTED. A scenario family with more than one status in a group is listed by full scenario ID. Fake-engine F-5 scenarios never become rows. `TestValidateRun` and `TestEvidenceRows` cover these rules.
