# AWS authentication

Part of [Execution profiles](execution-profiles.md).

Hclaro orchestrates the AWS CLI and, optionally, aws-vault; it is not a credential wallet. It never reads, stores, shows or exports access keys, session tokens, SSO tokens, sign-in URLs or codes, and never runs `aws configure list`, `aws configure export-credentials`, `aws-vault export`, `add`, `remove` or `clear`.

**Binding.** Edit an execution profile (**Manage** next to the profile selector) and fill in **AWS authentication**:

| Field | Example | Meaning |
| --- | --- | --- |
| Method | AWS CLI profile | `aws-cli` sets `AWS_PROFILE=<profile>` for every command; `aws-vault` runs every command as `aws-vault exec <profile> -- <command>`. Never chosen automatically. |
| AWS profile | `prod-sso` | A profile name from your AWS configuration (`~/.aws/config`, or `AWS_CONFIG_FILE`). **Find profiles** runs `aws configure list-profiles` (local, names only). |
| AWS region | `eu-west-1` | Optional; passed as `AWS_REGION` and `AWS_DEFAULT_REGION`. |
| Expected account ID | `111122223333` | Optional guard: apply is blocked unless STS verifies this account. |
| Expected role ARN | `arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_Deploy_*/*` | Optional guard. `*` stands for part of one segment and never spans `:` or `/`, so `Deploy/*` does not match `DeployAdmin/…`. |

**Status.** The header shows the AWS state of the tab's profile in words: *No AWS profile*, *Missing tool*, *Not checked*, *Verified*, *Authentication required*, *Error*, *Mismatch* or *Unknown*. Click it (or **AWS** next to a profile in **Manage**) for the panel. Reading the status runs only local commands (`aws --version`, `aws login help`, `aws-vault --version`, `aws configure get sso_session|sso_start_url --profile <name>`, whose values are discarded) and never contacts AWS.

- **Verify identity** runs `aws sts get-caller-identity --output json` with the profile's environment, or `aws-vault exec <profile> -- aws sts get-caller-identity --output json`, the same path the engine uses. It contacts AWS and may run a configured `credential_process`, so it runs only when you click; nothing polls STS. The panel shows the account, ARN and **Last checked** time. STS reports no expiry, so Hclaro shows no countdown: *Verified* means it worked at that moment. This is the **CLI execution identity**: provider aliases, `assume_role`, web identity or a `profile` set in the Terraform configuration can make the provider use another principal or account.
- **Sign in with AWS (SSO)** appears for profiles with `sso_session` or `sso_start_url` and runs `aws sso login --profile <name>`; the AWS CLI opens your browser itself. **Sign in with aws login** appears only when the installed AWS CLI offers `aws login`; it is a separate method and never replaces SSO. Sign-in output is not shown or kept. When it ends, the panel says *Login process completed — verify identity*; nothing is verified, and no plan or apply resumes. A sign-in can be cancelled and stops after 10 minutes.
- **Terminal commands** lists copyable commands for when the browser cannot open or a prompt is needed: `aws sso login --profile <name>`, `aws sso login --profile <name> --use-device-code`, `aws login --profile <name>` and `aws sts get-caller-identity --profile <name>`. For aws-vault it lists `aws-vault exec <name> -- aws sts get-caller-identity`. Run one, then **Verify again**. Hclaro never asks for passwords or MFA codes.

**aws-vault.** Hclaro runs aws-vault with empty stdin and cannot show its prompts. aws-vault 6.3.1 reads an MFA code from stdin, so a profile that needs one fails at once (*Authentication required*, `AUTH_INTERACTIVE_UNSUPPORTED`) instead of waiting. Run the terminal command once (aws-vault caches the session), then verify again. A GUI prompt driver such as `AWS_VAULT_PROMPT=osascript`, or the macOS dialog for a locked aws-vault keychain, can still appear when a command runs. Commands that may need credentials run through aws-vault, and the reviewed command shows the `aws-vault exec <profile> --` prefix: plan, apply, import, init, drift, graph, providers, outputs, workspace commands and state reads. The version query, `workspace show`, `fmt` and `validate` never need credentials and run without it.

**Opening a workspace never prompts.** With an aws-vault profile, a Hub workspace with a **local** backend is read without aws-vault: its state and workspace list are files, so no AWS call is made. With a **remote** (or unrecognized) backend, the automatic read is deferred: the workspace shows *Read with aws-vault when you are ready*, and **Read again** reads it through aws-vault. Nothing falls back to other credentials. aws-vault keeps a region Hclaro sets (checked with aws-vault 6.3.1); without one it uses the profile's configured region. Cancelling a plan, apply or init interrupts the process Hclaro started. Checked on macOS with aws-vault 6.3.1 (file backend, synthetic credentials, no AWS call): aws-vault replaces itself with the engine (same process), so the engine receives exactly one interrupt and stops gracefully; a cancel before that point stops aws-vault and the engine never starts. Windows and aws-vault 7 were not checked: on Windows aws-vault keeps running and starts the engine as a child, so whether it stops gracefully depends on the interrupt reaching it.

**Apply.** The approval is bound to the AWS identity you verify in the preflight:

- When the execution profile names an AWS profile, the blocking check *AWS identity verified for this review* is unknown until you choose **Verify AWS identity** in the preflight.
- Whenever the preflight identity was verified (also without a profile), STS is asked again immediately before the apply. Another account or principal, or a failed check, refuses the apply with `AUTH_CONTEXT_STALE`, whatever caused it: a sign-in in Hclaro, `aws sso login` in a terminal, another tool. An assumed-role session name is ignored (aws-vault and SSO name every session differently); the role is not.
- With an expected account or role, *AWS identity matches the profile's expected account* also blocks a different account (`AUTH_ACCOUNT_MISMATCH`) or role.
- A sign-in from the AWS panel after the preflight refuses its approval too. Changing the profile changes its revision, so plans made with the old binding cannot be applied. Nothing is retried or applied automatically after a sign-in.

Remaining risks: the engine resolves credentials itself after the final STS check, so a change in the second between them is not caught. A sign-in started while an apply runs is not blocked. A plan without an execution profile whose identity you did not verify is not bound to an identity (as before M20).

**Residual risks.**

- **AWS CLI v1:** it can verify identity but cannot sign in.
- **Cancelling a sign-in:** it is interrupted first, so the AWS CLI can finish writing its SSO cache, and killed after 3 seconds (killed at once on Windows). A process the AWS CLI itself started could outlive it; this was not verified with the real CLI.
- **aws login button:** shown only when `aws login help` succeeds, which may fail on a machine where help rendering is unavailable; the button is then hidden.
- **Withheld ARN:** if STS returns an ARN Hclaro cannot read safely (shown as *ARN withheld*), the preflight check *AWS identity verified for this review* blocks the apply, because a change of principal could not be detected. Check the profile with `aws sts get-caller-identity` in a terminal.
- **EC2 instance role:** on an EC2 instance, a profile that exists but has no credentials can fall back to the instance role. STS shows that identity, and an expected-account guard blocks it.

**Known limitations.** Security limitations (they affect which identity or secret is used):

- **Endpoint and CA variables are inherited** (`AWS_ENDPOINT_URL`, `AWS_ENDPOINT_URL_STS`, `AWS_CA_BUNDLE`): they could point the STS check at another endpoint. Changing them requires control of the environment Hclaro starts in.
- **aws-vault settings reach the engine:** aws-vault needs its own settings (`AWS_VAULT_BACKEND`, `AWS_VAULT_PROMPT`, `AWS_VAULT_KEYCHAIN_NAME`, `AWS_VAULT_FILE_PASSPHRASE`, …) and passes its whole environment to the command it runs, so Terraform/OpenTofu and providers can read them, including a file-backend passphrase (observed with aws-vault 6.3.1). Hclaro cannot remove them from that command without running another program inside aws-vault, and does not remove them from AWS CLI-method commands because a `credential_process` that calls aws-vault needs them. Prefer the OS keychain backend over `AWS_VAULT_FILE_PASSPHRASE`. Hclaro never reads, logs or shows these values.
- **No default profile with aws-vault:** commands that never need credentials (version, `workspace show`, `fmt`, `validate`, local-backend reads) run without aws-vault and without `AWS_PROFILE`; if one of them did contact AWS it would use the `[default]` profile. None of them should.

Operational limitations (they affect convenience, not which identity is used):

- **Concurrent checks:** only the panel's **Verify identity** is limited to one at a time per profile; a preflight or apply check can run alongside it, so aws-vault can prompt twice.
- **GUI prompts:** only prompts read from stdin fail at once. A keychain dialog or `AWS_VAULT_PROMPT=osascript` waits until the 20-second identity timeout or the engine timeout.
- **Cancelled sign-in:** the AWS CLI is interrupted, then killed after 3 seconds; a helper process it started could outlive it (not checked with the real CLI).

**Troubleshooting.** *Missing tool*: install AWS CLI v2 (or aws-vault) and choose **Detect tools**. *Authentication required*: sign in, then verify. *Mismatch*: signing in again to the same profile does not help; change the AWS profile or sign in to the expected account. *Unknown* (`AUTH_NETWORK`): STS was unreachable or slow (check VPN). *Error*: the profile is missing or AWS denied the call.
