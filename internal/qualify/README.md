The harness itself (`make qualify`, environment allowlist, direct calls, isolation levels, results) is documented in [`docs/qualification/README.md`](../../docs/qualification/README.md).

# M27 qualification: provider capabilities (F-1)

These are the documented behaviours that the M27.03 and M27.04 scenarios rely on, for the OD-2 provider versions: `hashicorp/random` 3.9.1 and `hashicorp/local` 2.9.1. The sources are the provider docs at the exact release tags, fetched under OD-1a in F-6 (2026-11-14, evidence `f6-2026-11-14/fetch-log.jsonl`). The "Confirmed in" column follows the checklist's grouping (M27.03 I1, I3, R1; M27.04 D1, D2); only I1 (`random_integer` import), D1 (deleted) and D2 (edited) name a single behaviour in their task files. The real runs decide: a contradiction there is `FAIL`, never a reworded expectation.

| Behaviour | Status | Source (tag) | Confirmed in |
| --- | --- | --- | --- |
| `random_integer` import (ID `<result>,<min>,<max>`, e.g. `15390,1,50000`) | documented | `terraform-provider-random` `v3.9.1` `docs/resources/integer.md` §Import | M27.03 I1 |
| `random_string` import (ID is the string value) | documented | `v3.9.1` `docs/resources/string.md` §Import | M27.03 (I1, I3, R1 group) |
| `random_password` import (ID is the password value) | documented | `v3.9.1` `docs/resources/password.md` §Import | M27.03 (I1, I3, R1 group) |
| `keepers` on `random_integer`, `random_string` and `random_password`: a changed value forces recreation | documented | `v3.9.1` `integer.md`, `string.md`, `password.md` (argument `keepers`) | M27.03 (I1, I3, R1 group) |
| `random_password.result` and `bcrypt_hash` are sensitive | documented | `v3.9.1` `docs/resources/password.md` ("String, Sensitive") | M27.03 (I1, I3, R1 group) |
| `local_file` file deleted outside the engine: detected as deleted, and a diff re-creates it | documented ("will detect the resource as having been deleted … and will generate a diff to re-create it") | `terraform-provider-local` `v2.9.1` `docs/resources/file.md` | M27.04 D1 |
| `local_file` file edited outside the engine: whether it shows as removed or updated | **unconfirmed** (not documented at `v2.9.1`) | — | M27.04 D2 decides |
| `data "local_file"` reads a file's content | documented | `v2.9.1` `docs/data-sources/file.md` | F-2 candidate 2 (used on Terraform 1.17.2) |
| `terraform_data.triggers_replace` forces replacement | **unconfirmed** here (engine documentation, not fetched under OD-1a) | — | M27.03 (I1, I3, R1 group) |

Fixtures must not use `random_bytes` (new in 3.9.0) or any other feature that needs a newer engine than Terraform 1.5 or OpenTofu 1.6.

Compatibility with the oldest engines: under the M27 sandbox, `verify-mirror.sh` installs both providers and starts them (`providers schema -json`; this shows they install and load, not that every resource works) with Terraform 1.5.5 and 1.5.7 and with OpenTofu 1.6.3 (2026-11-14, darwin_amd64).

# M27 qualification: unlock-failure reachability (F-3)

Source analysis, 2026-11-14, of the local backend at the exact release tags. It used a read-only sparse checkout and downloaded no binaries. No engine was run, and no recipe was executed.

| Engine | Tag commit | F-3 result | M27.04 L2 |
|---|---|---|---|
| Terraform 1.5.5 | `c10eb21b390627a6259eef9cd883e6ddeccc5716` | `NOT_REPRODUCIBLE` | `NOT_TESTED`, reason "unlock failure not reproducible on Terraform 1.5.5" |
| Terraform 1.5.7 | `ee58ac1851c8a433005df9863ed47796a9f6b5e7` | `NOT_REPRODUCIBLE` | `NOT_TESTED`, reason "unlock failure not reproducible on Terraform 1.5.7" |
| Terraform 1.17.2 | `ae094fc0ee6d4f3d2a990b40ac48a6d2dbaeb53e` | `NOT_REPRODUCIBLE` | `NOT_TESTED`, reason "unlock failure not reproducible on Terraform 1.17.2" |
| OpenTofu 1.6.3 | `7df1a351d8763bc7ed8fc712ca28f166f59b769c` | `NOT_REPRODUCIBLE` | `NOT_TESTED`, reason "unlock failure not reproducible on OpenTofu 1.6.3" |
| OpenTofu 1.13.9 | `c83e7e50daff10ac704cb0811e9ffd0ad2523759` | `NOT_REPRODUCIBLE` | `NOT_TESTED`, reason "unlock failure not reproducible on OpenTofu 1.13.9" |

`NOT_REPRODUCIBLE` is the F-3 feasibility finding: no recipe was found in the inspected code. It is not a scenario status. L2 itself stays `NOT_TESTED` (`tasks/M27/README.md` vocabulary).

**Scope of the conclusion.** It covers the default `local` backend only, through apply and import, on a local filesystem (APFS, ext4, NTFS). It rests on the inspected packages: `internal/states/statemgr`, `internal/backend/local`, `internal/command`, and `internal/flock` in OpenTofu 1.13.9. Out of scope: network filesystems (NFS, SMB/CIFS), where `fcntl` can return `ENOLCK`; every other backend (remote-state backends, `cloud`/`remote`); kernel or fault injection; uninspected engine code. This does **not** show that an unlock failure is impossible in general. It shows only that no external condition, created after the state write, was found that makes these code paths emit the diagnostic.

**Coverage that remains.** The synthetic `state_unlock_failed` tests stay as unit and integration coverage of Hclaro's classifier and outcome handling: `internal/engine/planner_test.go` and `internal/operations/apply_test.go`. They are not real-engine qualification evidence.

Findings, the same behaviour in all five:

- Where the diagnostic comes from: in the inspected packages, only `clistate.locker.Unlock` (`internal/command/clistate/state.go`) emits "Error releasing the state lock", when `statemgr.Filesystem.Unlock` returns an error. "Failed to unlock state" comes only from `force-unlock` (`internal/command/unlock.go`), which M27 forbids. "error unlocking state" does not occur in these packages.
- Callers: apply (`backend/local/backend_apply.go`, `opApply`) and import (`command/import.go`) release the lock in a deferred call. The state is persisted (`PersistState`) before that call runs. The `localRun` error path (`backend/local/backend_local.go`) unlocks only on errors before the context exists, and its caller returns before registering the deferred unlock. So no inspected path unlocks twice after a state write. A double unlock would give "LocalState not locked".
- `Filesystem.Unlock` (`internal/states/statemgr/filesystem.go`):
  - The lock ID check compares the locker's own in-memory ID, so it cannot fail from outside the process.
  - The `os.Remove` of `.terraform.tfstate.lock.info` is ignored. Terraform 1.5.5, 1.5.7 and 1.17.2 drop the error; OpenTofu 1.6.3 and 1.13.9 only log it at `[ERROR]`.
  - The function returns only the OS unlock result:
    - Unix: `fcntl(F_SETLK, F_UNLCK)` on the still-open state file descriptor (`filesystem_lock_unix.go` `unlock`; OpenTofu 1.13.9 `internal/flock.Unlock`). No condition was found that makes this fail from outside the process on a local filesystem.
    - Windows: returns `nil`, because the lock is released by `Close` (`filesystem_lock_windows.go` `unlock`; OpenTofu 1.13.9 `internal/flock.Unlock`).
- Making the state directory read-only after the state write (the condition L2 forbids before the write) is predicted from source, not executed. The prediction is that it produces **no** diagnostic: the apply exits successfully and `.terraform.tfstate.lock.info` is left behind. That is a stale lock-info file, not `state_unlock_failed`.
