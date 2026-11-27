# Command line

```sh
hclaro [flags]
```

| Flag | Meaning |
| --- | --- |
| `--path DIR` | Terraform/OpenTofu project directory (default `.`). |
| `--engine terraform\|tofu` | Choose the engine. Terraform is preferred when both are on `PATH`. |
| `--port N` | Loopback port (default: an available port). |
| `--no-browser` | Print the URL instead of opening a browser. |
| `--hub` | Open the [Workspace Hub](../guides/workspace-hub.md); the current directory is not read unless `--path` is given. |
| `--no-hub-execution` | Keep Hub workspaces read-only: plans, applies and imports run only for the launch project. (`--experimental-hub-execution` is still accepted and does nothing.) |
| `--strict-local` | Disable features that send data to third parties: [cost estimation](../guides/cost-estimation.md) and AI requests sent by Hclaro. |
| `--claude-path FILE`, `--codex-path FILE` | Executables for [AI assistance](../guides/ai-assistance.md) (default: `claude` and `codex` on `PATH`). |
| `--tflint-path FILE`, `--trivy-path FILE`, `--checkov-path FILE` | Executables for the optional [scanner cards](../guides/scanners.md) (default: `tflint`, `trivy` and `checkov` on `PATH`). |
| `--export-history FILE` | Write the redacted [operation history](../guides/history-and-git.md#operation-history) to a new `.json` or `.md` file and exit. |
| `--version`, `--help` | Print the version or usage and exit. |

Exit codes: 0 success, help or version; 1 startup failure; 2 invalid arguments. `hclaro --help` is the authoritative list.

## Opening a project

Hclaro inspects only the selected directory; it never searches parent directories. Terraform is preferred when both CLIs are on `PATH`. The port is dynamically allocated unless supplied. The browser opens automatically; if it does not, use the printed URL. Exit codes: 0 success/help/version, 1 startup failure, 2 invalid arguments.

Launch runs only `version -json`, `workspace show`, and `show -json`. It never runs init (init runs only from a Hub project page after you confirm it; see [Initialize a project](../guides/workspace-hub.md#initialize-a-project)), plan, refresh, apply, destroy, import, replace, or state modifications. A plan runs only when you confirm it in the UI (see [Plans](../guides/plans-and-apply.md#plans)). Hclaro never runs `terraform import`/`tofu import`; imports use import blocks and a reviewed plan (see [Import](../guides/import-and-replace.md#import)). Reading state can contact the configured remote backend and load installed provider plugins. Use an initialized project and your existing credentials/environment; Hclaro does not resolve or store credentials itself.

The UI displays **one recorded snapshot**, not live provider inventory or drift detection. Browsing, searching, filtering, sorting, and opening details read process memory. Use **Read again** in the context bar (or restart Hclaro) to inspect the folder and reread state after an external change, such as running `init` in a terminal; it runs no init, plan or refresh and is unavailable while an operation runs. Inherited `TF_CLI_ARGS` settings are accepted only when they contain `-no-color`; other injected flags fail explicitly because they can redirect raw output or alter reads. `TF_DATA_DIR`, `TF_WORKSPACE`, CLI configuration, and credential settings are inherited. `TF_LOG*` and update checks are disabled for child reads to prevent secret-bearing log files and update telemetry.

The private URL contains a random launch credential in its **fragment**, which is removed by the app before API use. Keep that URL private. The browser stores the credential in sessionStorage for reloads; state is never placed in localStorage. Only display preferences (theme, density, grouping, editor, sidebar mode) and the details-drawer width use localStorage. A stopped/restarted server requires its new URL.
