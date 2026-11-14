# M27 qualification: provider capabilities (F-1)

These are the documented behaviours that the M27.03 and M27.04 scenarios rely on, for the OD-2 provider versions: `hashicorp/random` 3.9.1 and `hashicorp/local` 2.9.1. The sources are the provider docs at the exact release tags, fetched under OD-1a in F-6 (2026-11-14, evidence `f6-2026-11-14/fetch-log.jsonl`). The real runs decide: a contradiction there is `FAIL`, never a reworded expectation.

| Behaviour | Status | Source (tag) | Confirmed in |
| --- | --- | --- | --- |
| `random_integer` import (ID `<result>,<min>,<max>`, e.g. `15390,1,50000`) | documented | `terraform-provider-random` `v3.9.1` `docs/resources/integer.md` §Import | M27.03 I1 |
| `random_string` import (ID is the string value) | documented | `v3.9.1` `docs/resources/string.md` §Import | M27.03 I1, I3 |
| `random_password` import (ID is the password value) | documented | `v3.9.1` `docs/resources/password.md` §Import | M27.03 I3 |
| `keepers` on `random_integer`, `random_string` and `random_password`: a changed value forces recreation | documented | `v3.9.1` `integer.md`, `string.md`, `password.md` (argument `keepers`) | M27.03 R1 |
| `random_password.result` and `bcrypt_hash` are sensitive | documented | `v3.9.1` `docs/resources/password.md` ("String, Sensitive") | M27.03 I3 |
| `local_file` file deleted outside the engine: detected as deleted, and a diff re-creates it | documented ("will detect the resource as having been deleted … and will generate a diff to re-create it") | `terraform-provider-local` `v2.9.1` `docs/resources/file.md` | M27.04 D1 |
| `local_file` file edited outside the engine: whether it shows as removed or updated | **unconfirmed** (not documented at `v2.9.1`) | — | M27.04 D2 decides |
| `data "local_file"` reads a file's content | documented | `v2.9.1` `docs/data-sources/file.md` | F-2 candidate 2 (used on Terraform 1.17.2) |
| `terraform_data.triggers_replace` forces replacement | **unconfirmed** here (engine documentation, not fetched under OD-1a) | — | M27.03 R1 |

Fixtures must not use `random_bytes` (ephemeral since 3.9.0) or any other feature that needs a newer engine than Terraform 1.5 or OpenTofu 1.6.

Compatibility with the oldest engines: under the M27 sandbox, `verify-mirror.sh` installs both providers and starts them (`providers schema -json`) with Terraform 1.5.5 and 1.5.7 and with OpenTofu 1.6.3 (2026-11-14, darwin_amd64).
