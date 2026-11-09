# Configuration file compatibility

Hclaro keeps a few JSON files in its configuration folder: your OS user configuration directory plus `hclaro` (`~/Library/Application Support/hclaro` on macOS, `~/.config/hclaro` or `$XDG_CONFIG_HOME/hclaro` on Linux, `%AppData%\hclaro` on Windows; `HCLARO_CONFIG_DIR` overrides it). This page is the contract for how any Hclaro version treats those files, so that switching versions, including going back to an older one, never silently corrupts or downgrades them.

**During the beta there is no automatic migration promise.** All files are at schema 1, and no release has needed an upgrade yet. If a future release changes a format, it will follow the rules below, and its changelog will say so.

## Files

| File | Holds | Schema written by this build |
| --- | --- | --- |
| `projects.json` | Workspace Hub projects: ID, absolute folder path, name, engine choice, pin, last opened time and workspace | 1 |
| `profiles.json` | Execution profiles: names, engine, workspace, variable-file paths, AWS profile and region names, names (never values) of environment variables | 1 |
| `history.json` | Operation history: redacted commands, outcomes, plan counts and addresses, sanitized log excerpts, Git commit and branch | 1 |
| `cost-estimates.json` | The last 50 cost-estimate totals (no addresses or line items) | 1 |

None of them store credentials, variable values, state or plans. They do describe your infrastructure (folder paths, resource addresses, account and profile names), so treat them as private.

## Rules

Every file has a top-level integer `version`. What happens depends on that version compared with the version this build writes:

| On disk | `projects.json`, `profiles.json`, `history.json` | `cost-estimates.json` |
| --- | --- | --- |
| **Same version** | Read and written normally. | Read and written normally. |
| **Newer version** (written by a newer Hclaro) | Never decoded, never written, never moved. The Hub, profiles or history become read-only with a notice naming the schema. | Shown as empty and never written; new totals are not saved. |
| **Older version** | Upgraded only by a migration built into that release. **The original file is copied first** to `<file>.schema-<old version>-<UTC time>` (`0600`, never replacing an existing file). If the copy cannot be written, the file is left unchanged and the store is read-only. A version with no migration is treated as unrecognized. | Treated as unrecognized. |
| **Unrecognized** (not JSON, missing, zero, negative, non-integer version, unknown fields, invalid entries) | Moved aside, byte for byte, to `<file>.corrupt-<UTC time>`, and the store starts empty with a notice. If it cannot be moved, it is left unchanged and the store is read-only. | Treated as empty and replaced on the next save (these are disposable totals). |
| **Unreadable** (permissions, not a regular file) | Left unchanged; the store is read-only. | Treated as empty and replaced on the next save. |

`history.json` also drops individual entries that fail validation and keeps the rest, and scrubs credential-shaped text from hand-edited entries when it reads them.

### While Hclaro is running

Two Hclaro processes may share the folder, for example after an upgrade while an older window is still open.

- `projects.json` and `profiles.json` are read again before every change; if the file is no longer one this build understands, the change is refused and the file is not touched.
- `history.json` is read again whenever it changed on disk. If a newer Hclaro rewrote it, history becomes read-only for the rest of the session. If an older Hclaro rewrote it with an older schema, it is not upgraded behind that process's back: history becomes read-only until restart, when the upgrade runs with a backup.
- `cost-estimates.json` is read again before every save; a newer file is never replaced.

### Writes

Every write goes to a private temporary file in the same folder, is synced, and is renamed over the old file, so a crash leaves either the old or the new file, never half of one. Files are `0600` in a `0700` folder on macOS and Linux. A crash can leave a hidden `.<name>-*.tmp` file next to them; `history.json` removes its own on the next start, and the others are safe to delete while Hclaro is not running.

## Upgrading and going back

- **Upgrade:** replace the binary. Files at the same schema are used as they are. When a release upgrades a schema, the previous file is kept next to it as `<file>.schema-<n>-<time>`.
- **Go back to an older release:** the older Hclaro refuses files written by a newer schema and opens them read-only; it does not overwrite them. To use the older version normally, quit Hclaro, then either restore the `.schema-<n>-<time>` backup over the current file or move the current file away (the older version then starts empty for that file). Releases up to and including 0.1.0-beta.3 also refuse newer `projects.json`, `profiles.json` and `history.json` at startup, but could overwrite a newer `history.json` written while they were running, and replace a newer `cost-estimates.json`; quit older versions before starting a newer one.
- **Back up:** quit Hclaro and copy the whole configuration folder. Keep the copy private: it holds folder paths, resource addresses and profile names. Do not attach these files to public issues; describe their shape instead (see [BETA.md](BETA.md#giving-feedback)).
- **Start fresh:** quit Hclaro and delete or rename the folder.

## For maintainers

- A format change increments the `SchemaVersion` of that package (`internal/projects`, `internal/profiles`, `internal/history`, `internal/cost`), adds a fixture to its `testdata/compat`, and keeps the previous fixture loading.
- Migrations are registered by the version they upgrade from (`history.DefaultMigrations`); the registry and profiles have no migration hook yet and would need one with the same backup-first rule. No migration may drop data silently or write a downgraded schema.
- Whether a release migrates automatically, asks first, or ships a separate tool is a product decision to make before the first format change; until then this page promises only the rules above.
- Tests: `TestSchema1FixtureLoadsAndStaysSchema1`, `TestNewerSchemaFixtureIsNeverChanged`, `TestNewerSchemaWrittenDuringSessionIsNotDowngraded` and `TestInvalidSchemaVersionsAreKeptAside` in each package, `TestSchemaUpgradeKeepsABackupFirst` and `TestSchemaUpgradeWithoutBackupLeavesFileUnchanged` for history, `TestStoreNeverReplacesANewerFile` for cost estimates.
