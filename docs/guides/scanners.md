# Scanners: tflint, Trivy and Checkov

The Tools page of a Hub workspace can run three optional static scanners next to Validate and Format check, so their findings sit beside the plan you are about to apply. Hclaro never installs them. A card that finds no installed scanner says **Not installed. Hclaro never installs scanners.** and links to the scanner's own documentation.

## What runs

| Scanner | Command (fixed) | Supported |
| --- | --- | --- |
| tflint | `tflint --format=json --no-color` (`--recursive` when **Include subfolders** is ticked) | 0.47 and later |
| Trivy | `trivy config --format json --quiet --skip-check-update .` | 0.57 and later |
| Checkov | `checkov -d . -o json --framework terraform --quiet --compact --skip-download` | 3.x |

- Nothing runs until you click **Run**. Opening the page only reads each scanner's `--version`. A version outside the range disables the card and says why. The three version checks run together and each may take up to 30 seconds (Checkov is slow to start).
- The scanner runs in the project folder with a minimal environment: `PATH`, `HOME`, temp folders, proxy and CA settings. It gets none of the execution profile's variables, no `AWS_*`, `ARM_*`, `GOOGLE_*`, `TF_VAR_*` or tokens. tflint's deep-check mode is not enabled.
- The project's own `.tflint.hcl`, `.trivyignore`/`trivy.yaml` and `.checkov.yaml` are used by the scanner as usual; the card lists which it found.
- Use `--tflint-path`, `--trivy-path` and `--checkov-path` to pick an executable that is not first on `PATH`.
- One run per scanner per workspace, at most eight tools at once, a 5-minute limit and a 16 MiB output limit. **Cancel** interrupts the scanner; a cancelled or timed-out run shows no result.

## Nothing is downloaded

Hclaro does not run `tflint --init`, which downloads plugins. If tflint needs plugins the result says so and shows the command for you to run yourself. Trivy and Checkov run with their offline flags; if Trivy still needs data, the result says to run it yourself once. Hclaro does not guarantee a scanner makes no network call beyond those flags: check the scanner's own documentation if that matters, and keep proxy settings in mind (they are passed through).

## Results

Each finding shows severity, rule ID, a message of at most 300 characters (redacted like engine diagnostics), file and line, grouped by file. tflint's `error`, `warning` and `notice` appear as high, medium and low; Trivy's severities are kept; Checkov's open-source rules carry none and show as unrated. Code snippets, resource values and links in the scanner's output are never read. The card summary reads `✓ No findings` or for example `3 high · 5 medium · 2 low`. A scan that could not complete (plugins missing, a failure, unreadable output) shows **Not scanned**, never "clean".

In **Plan Review** of a Hub workspace, a line such as *Security scan: 2 high (trivy, 12:04)* appears when a finished scan of the same workspace still matches the configuration (it is hidden after any configuration, variable or lock file changes, and when the scan was clean). It never blocks or gates an apply.

## Limits

- Static scans of configuration files only; scanning plan files is not offered.
- Findings are not fixed by Hclaro. The AI "Suggest improvements" action can propose changes.
- File names are shown as text; they do not open an editor.
- Not yet run against real Trivy or Checkov binaries, and no scanner is gated under `--strict-local`; see [limitations](../reference/limitations.md).
