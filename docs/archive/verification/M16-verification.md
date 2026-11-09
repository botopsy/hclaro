# M16 implementation and verification

**Status: draft.** AWS cloud discovery is implemented as a new package `internal/discovery` (allowlisted read-only AWS CLI runner, type catalogue and page parsers, scans with limits, retries and cancellation, exact matching against recorded state, context-pinned import preparation), discovery routes for the launch project, an optional `discoveryToken` on the existing M04 import route, and a connected **Discovery** view plus an import-wizard prefill in the UI.

Design and trade-offs: [architecture](../../architecture.md#cloud-discovery), [security](../../../SECURITY.md), [README](../../../README.md#cloud-discovery-aws).

## Scope decisions

- **Launch project only.** Imports (M04) run only for the project opened from the terminal, and the Hub is read-only, so discovery runs there too and matches against the launch snapshot. The task's `POST /api/projects/{id}/discovery/scans` is illustrative; routes follow the drift/graph convention: `/api/v1/discovery/…` with the tab's `profileId`.
- **AWS CLI v2, not the SDK.** The module has no third-party Go dependencies; the AWS SDK would add many. The CLI behaves like the user's terminal for SSO, `credential_process` and profiles (the M09 profile already composes `AWS_PROFILE`/`AWS_REGION`). M14 already shells out to `aws sts get-caller-identity`. CLI v1 is refused (`cli_unsupported`).
- **Five types.** `aws_vpc`, `aws_subnet`, `aws_security_group`, `aws_s3_bucket`, `aws_db_instance`, with hashicorp/aws v5 import IDs. Security group rules, descriptions, RDS endpoints and user names are never decoded.
- **Labels.** `already_tracked` / `possibly_untracked` / `unknown` / `unresolved` (the task's four). "Possibly untracked" requires every state instance of the type to have a comparable identifier in the documented shape; otherwise `unknown`. Resources shared from another account (RAM) are `unresolved`.
- **No discovery on page load.** `GET` routes never run the CLI. Verification, scan, cancel and prepare are CSRF-protected POSTs; a scan needs `acknowledgeApiCalls: true` after the preview.
- **Account pinning by STS, three times.** At verification, at scan start (fails with `identity_changed` before listing) and at preparation. Verifications, scans and preparations are bound to profile ID, context fingerprint, AWS profile/region names, workspace and engine.
- **Import link.** Preparation returns a one-time, 15-minute token. The import route re-checks it (context, import ID, resource type) before writing a file; editing the ID or type in the wizard unlinks it (it becomes a normal manual import). Unknown mappings need an explicit acknowledgement; tracked/unresolved cannot be prepared. No bulk import.
- **In memory.** Scans (10), verifications (20, 30 minutes) and preparations are never written to disk or history.

## Evidence collected

Environment: macOS 13 amd64, Go 1.27.3, Node 24.2, Playwright Chromium. No real AWS credentials, account or network were used: tests use a recording fake runner over the synthetic fixtures in `fixtures/aws` (account `111122223333`, canaries in tags, descriptions and RDS fields), and the live run used the synthetic `scripts/fake-aws` CLI first on `PATH` with `AWS_PROFILE`/`AWS_REGION` unset.

| Check | Result |
| --- | --- |
| `make test` (`go test -race`, Vitest) | Passed: all 21 Go packages with `-race` (incl. `internal/discovery`); Vitest 114/114 in 15 files. |
| New Go tests | `internal/discovery/discovery_test.go` (17): allowlist refuses create/delete/modify, `--profile`, `--endpoint-url`, `--query`, `--cli-input-json`, bad regions and duplicates; environment hardening; error classification; CLI v1 refused; identity keeps no user ID or session name; pagination with the returned token; access denied → `partial`, service marked failed; expired SSO stops the scan; throttling retried with 0.5 s/1 s backoff and bounded; multi-region duplicates merged; limit truncation; exact/conservative matching (ID, ARN, ambiguous, ARN conflict, redacted, other format, unreadable state, v4/v5 RDS); verification/scan/prepare/import check refused for another profile, workspace, account, ID, type or after expiry; call recorder sees only Describe/List/Get; per-scan and session concurrency caps, third scan refused, cancellation; scope validation; malformed pages. `internal/api/discovery_test.go` (5): end-to-end over HTTP incl. no AWS call on page load, acknowledgement, prepared import accepted once and refused for other ID/type/token, account change; sanitized errors; partial results; unavailable/no launch project; CSRF/Origin/session guard; golden contract `fixtures/discovery/scan.golden.json`. |
| New UI tests | `web/src/discovery/Discovery.test.tsx` (8): nothing posted on load; verify only on click; preview with request estimate, charges and consent; labels/filters/links and no canary; unknown needs confirmation; withheld scan of another context; denied service shown as missing; wizard prefill sends `discoveryToken` and unlinks when the ID changes. |
| Existing tests | All M01–M15 Go, Vitest and browser tests pass unchanged. |
| `make lint`, `GOOS=windows go vet`, `gofmt -l` | Passed / clean. |
| `make build` (`npm run build`), `make browser-test` | Passed. The smoke test opens Discovery unverified (no Scan offered), then a deterministic verified scan through the real Go handler: labels, no canary in the page or any API response, scan preview with consent gating, **Prepare import** into the wizard and a preview. New screenshots `cloud-discovery-dark.png`, `cloud-discovery-scan-dark.png`, `import-from-discovery-dark.png`; other screenshots changed only by the new nav item. |
| Live loopback run | `bin/hclaro --engine terraform --path fixtures/project --no-browser` with `bin/mock` (synthetic `terraform` and `aws`) first on `PATH`: options without identity and no CLI call; `POST discovery/identity` → account `111122223333`, `assumed-role/HclaroDemoReadOnly/…`, CLI 2.17.0; scan of eu-west-1 + us-east-1, all five types → `completed`, 11 AWS requests, VPC list in 2 pages, S3 listed once; 3 already tracked (`aws_s3_bucket.artifacts`, `module.storage…main[0]`, `module.database…main[0]`), 7 possibly untracked, 1 unknown (the fixture's subnets have IDs in another format), 1 unresolved (shared subnet); canary count 0 in the scan response. |

## Known limitations

- AWS only, five types, one account; no Azure/GCP, organization scans or bulk import.
- Matching compares only this workspace's state. "Possibly untracked" objects may be managed elsewhere.
- S3 bucket regions come from the CLI's `BucketRegion` field (recent CLI v2); older CLIs list every bucket with region "unknown".
- Opt-in regions that are disabled show `region_unavailable` per task. The region list is a fixed list of commercial regions (no GovCloud/China selection).
- A tag value that is secret but neither secret-named nor credential-shaped is displayed, as with state attributes.
- The suggested address is only a starting point; the import plan, not discovery, shows what an import changes (provider aliases for other regions are the user's responsibility, and the wizard warns).
- Discovery uses the AWS CLI's own credential chain; user CLI configuration (`credential_process`, aliases) runs as it would in the terminal.

## Manual verification

1. `make demo` (synthetic `terraform` and `aws` first on `PATH`; real credentials are never used) and open the printed URL.
2. **Discovery**: confirm nothing is shown as verified and no scan runs. **Verify AWS context** → account 111122223333.
3. **Scan…**: review types, regions, the request estimate and the charges note; tick consent; start. Check labels, the `redacted` tags, and the subnet shared from account 444455556666 marked Unresolved.
4. **Prepare import** on `hclaro-legacy-logs` → Import wizard with the ID filled in; change the address to `aws_s3_bucket.legacy_logs`, preview, and (optionally) plan. Change the ID and see the link removed.
5. With `HCLARO_FAKE_AWS=denied`, `expired`, `throttle` or `v1` (restart `make demo` with the variable set) check the partial, expired-session, throttled and unsupported-CLI states. (An account change between verification and scan needs a running session and is covered by `TestScanAndPrepareAreBoundToTheirContext` and `TestDiscoveryAPIEndToEnd`.)
