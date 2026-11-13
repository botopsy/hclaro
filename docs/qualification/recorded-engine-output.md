# Recorded real engine output

Evidence for support matrix rows RE-08 to RE-10, recorded on 2026-11-13 (M26.16). The parsers are tested in CI against output from real engines, not only against generated fixtures. Hclaro itself was not run, and nothing was applied.

## Recording

`scripts/fixtures/record-engine.sh <engine binary> fixtures/engines/<engine>-<version>` copies `fixtures/engines/project` to a temporary directory and runs the engine there with an empty environment (`TZ=UTC`, `PATH`, `HOME` set to the temporary directory, `CHECKPOINT_DISABLE=1`). It uses the same argv as Hclaro: `init`, `version -json`, `plan -input=false -json -out=<abs> -var-file=prod.tfvars`, `show -json <plan>`, the same plan with `-refresh-only`, `show -json` for state, and `graph`. The project uses only the built-in `terraform_data` resource and the `terraform_remote_state` data source with the default local backend, so `init` downloads nothing. The state is a hand-written synthetic file; the canary `DO_NOT_LEAK_HCLARO_TEST_SECRET` is planted in the sensitive variable, the recorded state and the remote state. The script fails if a recording contains the temporary directory's path.

The configuration covers update, no-op, `count` creates, a sensitive input update, both replacement orders, a `moved` block, deleted resources (only in state: one plain, one with `count` keys, one in a removed module instance), a module with `for_each`, a deferred data read, and unknown, changed and sensitive outputs.

| Engine | Version | Platform | Source |
| --- | --- | --- | --- |
| Terraform | 1.5.5 | darwin/amd64 | Installed on the maintainer machine |
| Terraform | 1.17.2 | darwin/amd64 | releases.hashicorp.com zip, SHA-256 checked against `SHA256SUMS` (signature not checked) |
| OpenTofu | 1.13.9 | darwin/amd64 | GitHub release zip, SHA-256 checked against `SHA256SUMS` (signature not checked) |

## Tests

`TestRealEnginePlans`, `TestRealEngineRefreshOnlyPlans` (`internal/plan`), `TestRealEngineScans` (`internal/drift`), `TestRealEngineGraphs` (`internal/graph`), `TestRealEngineStates` (`internal/state`) and `TestRealEngineStreams` (`internal/operations`) parse every recording and pin the same result for all three engines: 15 changes (3 create, 3 update, 2 replace in both orders, 4 delete, 1 read with `read_because_dependency_pending`, 2 no-op including the move), outputs, refusal of a normal plan as a drift scan, the resource nodes, the declared dependency and the state mapping of state-only resources in both graph formats, the state index, and every stream line recognized. The canary never appears in parsed output.

Found by these recordings and fixed: Terraform 1.5 and OpenTofu name resources that exist only in state `(orphan)`, and OpenTofu splits module variables into `(expand, reference)` and `(expand, input)` nodes. The graph showed all of them as unknown nodes; they are now merged into their node like `(expand)`. Orphans are named per instance, so a resource in a removed module instance (`module.old["x"].terraform_data.this (orphan)`) has a module key but none of its own; it was treated as a configuration address and never mapped to state. A key on any segment now makes a node an instance, mapped to its exact address.

## Not covered

- Built-in resources never drift, so no real `resource_drift` record with changed attributes is recorded; drift classification of such records is tested with synthetic documents only.
- Apply, import, init and workspace output, error diagnostics and lock messages are not recorded here.
- One platform (darwin/amd64) and one version per engine line; other versions in the accepted ranges are not recorded.
