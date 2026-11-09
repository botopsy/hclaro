# Getting started with hclaro

This guide takes you from a clean machine to hclaro showing one of your Terraform or OpenTofu projects. hclaro is published by BotOpsy and is in beta: read [BETA.md](BETA.md) first.

## 1. Requirements

- macOS, Linux or Windows. Release archives exist for macOS arm64/amd64, Linux amd64/arm64 and Windows amd64; every package is run natively in CI before a release, and the test suite runs on macOS arm64, Linux amd64 and Windows amd64.
- Terraform **1.5–1.17** or OpenTofu **1.6–1.13** on `PATH`. Hclaro uses your installed engine; it does not download one.
- An initialized project (`terraform init` / `tofu init` already run), or use Hclaro's guided init from the Workspace Hub.
- A browser on the same machine. Hclaro listens only on `127.0.0.1`.

## 2. Install

### Option A: download a beta binary

1. Download the archive for your platform and `hclaro_<version>_checksums.txt` from [GitHub Releases](https://github.com/botopsy/hclaro/releases).
2. Verify it:

   ```sh
   shasum -a 256 -c hclaro_<version>_checksums.txt --ignore-missing   # macOS
   sha256sum -c hclaro_<version>_checksums.txt --ignore-missing       # Linux
   ```

   On Windows: `Get-FileHash .\hclaro_<version>_windows_amd64.zip -Algorithm SHA256` and compare with the checksums file.
   Optionally, with the GitHub CLI: `gh attestation verify <archive> -R botopsy/hclaro` checks that the file was built by hclaro's release workflow.
3. Extract it and put `hclaro` (`hclaro.exe`) in a folder on your `PATH` that only your user can write:

   ```sh
   mkdir -p ~/.local/bin && tar -xzf hclaro_<version>_<os>_<arch>.tar.gz -C ~/.local/bin hclaro
   # if needed, add to ~/.zshrc or ~/.bashrc:  export PATH="$HOME/.local/bin:$PATH"
   ```

   On Windows (PowerShell): `Expand-Archive .\hclaro_<version>_windows_amd64.zip $env:LOCALAPPDATA\Programs\hclaro`, then add that folder to your user `Path` in **Settings → System → About → Advanced system settings → Environment Variables** (user variables, not system). Avoid folders that other users or installers can write to, and do not run Hclaro as root or Administrator.
4. The binaries are not signed. On macOS, if Gatekeeper blocks the first launch, allow it in **System Settings → Privacy & Security**; on Windows, SmartScreen shows **More info → Run anyway**. Only do this for a binary whose checksum you verified.
5. Check it: `hclaro --version`.

### Option B: build from source

Requires Go 1.26+ and Make. Node.js (22.12+, 24 recommended) and npm are needed only to rebuild the web UI.

```sh
git clone https://github.com/botopsy/hclaro.git
cd hclaro
make binary          # uses the committed UI bundle; no Node needed
./bin/hclaro --version
```

To rebuild the UI as well: `make install && make build`. To run the checks: `make test && make lint`.

## 3. Try it without real infrastructure

From a source checkout, the demo uses a fake engine and synthetic state; nothing contacts a cloud or a real Terraform installation:

```sh
make demo       # one synthetic project with 316 resources
make demo-hub   # Workspace Hub with synthetic projects and workspaces
```

Open the private URL printed in the terminal. Ctrl+C stops the server.

## 4. Open your own project

```sh
cd path/to/your/terraform-root
hclaro
```

Hclaro reads the folder with `version -json`, `workspace show` and `show -json` only, then opens your browser on a private URL such as `http://127.0.0.1:<port>/#session=…`. Keep that URL private: it contains the session credential. Other ways to start:

```sh
hclaro --path ./infrastructure/prod   # another folder
hclaro --engine tofu                  # choose the engine when both are installed
hclaro --hub                          # Workspace Hub; the current folder is not read
hclaro --no-browser                   # print the URL instead of opening a browser
hclaro --strict-local                 # disable third-party features (cost estimation)
```

Reading state can contact your configured backend and load provider plugins, using your existing credentials and environment. Nothing else runs until you ask for it in the UI: plans, drift scans, graphs, discovery scans, init, workspace commands, imports and applies each show what they will do and wait for your confirmation.

## 5. Find your way around

The sidebar shows the current **project** and **workspace** at the top, then the project's views, **Plan & analyze**, **Manage**, and Git, Tools and Settings at the bottom. The README's [Navigation](../README.md#navigation) section describes each entry.

## Troubleshooting

| Problem | What to do |
| --- | --- |
| "An IaC engine is needed" | Install Terraform or OpenTofu on `PATH`, or pass `--engine`, then restart. |
| "Initialize this workspace first" | Run `init` yourself, or use **Initialize…** on the project's Hub page, then **Read again**. |
| State unavailable | Check credentials, SSO session, VPN and backend in your terminal. Hclaro withholds raw CLI errors because they can contain secrets. |
| Blank page or "session" error | Open the newest URL printed by the running `hclaro`; each start creates a new one. |
| Port in use | Omit `--port`, or choose another. |
| Plan unavailable | The Plans view says why (for example a remote execution backend). Hclaro plans and applies only locally executed workspaces. |

More cases: [Compatibility and troubleshooting](../README.md#compatibility-and-troubleshooting).

## Upgrade, go back and back up

There is no installer or auto-update: Hclaro never checks for new versions. Watch [GitHub Releases](https://github.com/botopsy/hclaro/releases) (**Watch → Custom → Releases**) and read the changelog in the release notes.

- **Upgrade:** quit every running Hclaro, then repeat steps 1–3 of Option A with the new archive, replacing the old binary.
- **Go back:** install the older archive the same way. An older Hclaro opens configuration files written by a newer one read-only and never overwrites them; [config-compatibility.md](config-compatibility.md#upgrading-and-going-back) explains how to restore a backup or start fresh.
- **Back up:** quit Hclaro and copy its configuration folder (see below). It holds no credentials or state, but it does list folder paths, resource addresses and profile names: keep the copy private.

## Uninstall and clean up

1. Delete the `hclaro` binary.
2. Delete Hclaro's configuration folder, which holds the Hub project registry (`projects.json`), execution profiles (`profiles.json`), operation history (`history.json`) and cost-estimate aggregates. Its location is your OS user configuration directory plus `hclaro`: `~/Library/Application Support/hclaro` on macOS, `~/.config/hclaro` on Linux (or `$XDG_CONFIG_HOME/hclaro`), `%AppData%\hclaro` on Windows. `HCLARO_CONFIG_DIR` overrides it.
3. Hclaro writes nothing else outside your projects except short-lived private temporary folders for saved plans and cost inputs, which it removes; leftovers from a crash are cleaned on the next start or by the OS.

Projects themselves are only changed by actions you confirmed (init, workspace commands, an import file, an apply).
