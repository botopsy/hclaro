# Troubleshooting

| Situation | What to do |
| --- | --- |
| Missing CLI | Install a supported engine on PATH or select `--engine`; restart. |
| Not a project | Use `--path` pointing directly to a root with `.tf`, `.tofu`, or state files. |
| Uninitialized | For a Hub project, use **Initialize…** on its page; otherwise run the engine's `init` yourself, then **Read again**. When the project opened from the terminal is initialized from the Hub, it is read again automatically once init ends. Hclaro never initializes on its own. |
| Workspace error | Check selection/initialization with the CLI; Hclaro never invents `default`. |
| State unavailable | Check credentials, SSO/VPN/backend availability and matching provider schemas manually. Raw CLI errors are withheld because they may contain secrets. |
| No snapshot / empty snapshot | These have separate states. Hclaro does not create or refresh state. |
| Locked session | Open the latest private URL from the running terminal. |
| Occupied port | Choose another `--port`, or omit the flag. |
| Loopback permission denied | Run in an environment permitting listeners on 127.0.0.1. |
| Plan unavailable | The Plans view states why: remote execution backend, unverified backend type, missing engine or workspace error. An unverified backend usually means a `backend` block that was not initialized: run `init` yourself, then **Read again**. |
| State locked | Another operation holds the lock. Wait, or investigate with your CLI. Hclaro never force-unlocks. On Windows the lock also blocks reading the state: the workspace shows **Another process holds the state lock**, and planning stays off. Hclaro reads it again on its own for about two minutes; after that, use **Read again** once the other process has finished. A non-English Windows shows the generic *State unavailable* message instead. |
| Plan credentials/network errors | The message says whether the sign-in expired (refresh SSO), no credentials were found (check the profile or environment) or access was denied (wrong identity or missing permission). Check VPN for network errors, then run the plan again. |
| `TF_CLI_ARGS_plan` / `TF_CLI_ARGS_apply` refused | The message names the variable and flag names (never values). Unset them in the shell that starts Hclaro and restart it; an execution profile cannot override them. They could change what a reviewed plan contains. |
| Hub: "TF_DATA_DIR is an absolute path" | Restart Hclaro without `TF_DATA_DIR`, or with a relative value, to read Hub workspaces. |
| Hub: workspace mismatch | The engine reported a different workspace than the one selected, so nothing was shown. Check that your engine and backend honor `TF_WORKSPACE`. |
| Hub: registry read-only | The notice says why (unreadable, or written by a newer Hclaro). Fix or move the file named there, then restart. |
| Hub: project missing or path changed | Use **Repair path** with the folder's new location, or remove the entry. |
| Redacted attributes | Values marked sensitive, secret-like attribute names (`password`, `token`, `private_key`, …) and credential-shaped or long random-looking values, including hex hashes, are masked on the server. There is no raw-state endpoint; you can reveal one hidden value at a time on request (see [Resource Explorer](../guides/resource-explorer.md)). |

Metadata (addresses, module paths, provider sources) must not be used to store secrets. Redaction relies on markers and heuristics: a secret that is not marked sensitive, has an ordinary name and does not look random can be shown. See the [security policy](../../SECURITY.md), [architecture](../architecture.md) and the [support matrix](../support-matrix.md). Version ranges and what has been run for real: [Compatibility](compatibility.md).
