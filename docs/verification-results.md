# Verification record — 6 October 2026

Verification uses real accounts and AWS resources, local production browser runs, builds and static checks. No mocks, new test files or test-suite runs were used.

## EQ authentication on demand (7 October 2026)

The operator requested allowing EQ sessions to expire between four-hour imports and purchase alerts, accepting the additional lookup delay. EQ no longer keeps authentication warm. The live one-minute upkeep schedule was disabled and then removed from CloudFormation; the three bank import schedules remain enabled, with EQ retaining `cron(0 0/4 * * ? *)` in `America/Halifax`. Explicit manual verification remains available.

Go application and CDK build/vet, formatting checks and CDK synthesis passed. The scoped deployment updated only the two Lambda code packages and removed upkeep; existing admin authentication and browser resources were preserved. Both deployed Lambda package hashes match the final build, and CloudFormation reports `UPDATE_COMPLETE`.

A real gateway maintenance invocation started zero jobs. Dry-run job `verify-retired-eq-upkeep-0bcb1f40a0b0443fb5f56691aa9cab72`, submitted with the retired upkeep source and purpose, succeeded and recorded its completion time. Its workflow never entered Browser or RecoverBrowser, created no bank snapshot, and left the saved session pointer and bank health unchanged. No synthetic purchase or financial import was submitted. Scheduled imports and purchase alerts still use the existing on-demand renewal/login path; behavior after this deployment awaits the next genuine scheduled job or alert.

The sections below record the earlier cutover behavior; references to EQ background maintenance describe the configuration before this change.

## Authentication and retrieval

| Bank | Verified behavior |
|---|---|
| EQ | Local Rod/Chromium and ECS login, both account identities, complete configured history, separate-process and Lambda retrieval, and renewal extending token expiry. Warm Lambda fetch/renewal takes approximately 4–5 seconds. The observed maximum session lifetime remains 30 minutes; background maintenance renews before access-token expiry and authenticates again at hard expiry. |
| Rogers | Local Rod/CloakBrowser and ECS login, remembered device reuse, 29 posted transactions matching the old ten-day result and import IDs, separate Lambda retrieval, and actual access-token rotation at the renewal window. Renewal plus retrieval takes approximately 2–3 seconds. Old maintenance previously performed competing logins; those workers are now stopped. Four-hour idle session survival is not established. |
| NBDB | Local Rod/CloakBrowser and repeated ECS login, both expected account identities, separate Lambda retrieval and renewal with token rotation. Latest independent renewal plus retrieval took 1.120 seconds. CAD balances match the old importer; the configured cash account is excluded. No adjustment was needed in the final preview. |

NBDB fixes preserve the native browser identity, use Rod keyboard events, capture authenticated wealth requests without waiting for the web application's portfolio display, and prefer the completed OAuth token response over an earlier request's bearer. The browser still validates the expected accounts through native Fetch before publishing authentication. A bank-issued security challenge remains a reason to pause, not to repeat logins indefinitely.

The EQ cutover check found an expired bearer being sent to renewal and held as an unknown outcome. EQ now starts fresh authentication after access-token expiry; renewal remains available while it is valid. Safe renewal logs retain the underlying category and operation without bodies or tokens. A later MFA timeout exposed a timestamp precision mismatch: Fastmail timestamps have whole-second precision, so the shared reader now compares at that precision. Delayed email is a temporary failure rather than an unsupported challenge. The updated local EQ login passed, followed by Lambda reuse and renewal.

## Recovery and financial safety

- A real EQ ECS worker published validated authentication but could not deliver its deliberately invalid callback. Normal workflow recovery consumed the saved result and completed with one browser login. Replaying the terminal operation did not authenticate again.
- Real DynamoDB conditional operations in an isolated temporary partition rejected a concurrent lease, expired-generation publication and stale overwrite/release. The successor's state remained intact; temporary verification rows were removed. This exercised the production conditions, not a unit-test substitute for every Store method.
- Concurrent Rogers maintenance jobs completed using saved authentication, without browser launches, purchase notifications or financial-write rows.
- Renewal uncertainty is fenced before the token exchange. A crash cannot leave the old token marked as safely reusable. Unexpected exchanges are held rather than blindly replayed.
- Notification delivery has separate state. EQ's observed incident was delivered once, and a subsequent successful maintenance job delivered one recovery notice. Dry runs preserve any recovery notice owed to the operator. Notification imports also clear their own resolved incident state.
- The failure queue's old message was an SES setup notification, not a lost purchase. Its real event was reprocessed successfully, archived privately and acknowledged. Sender filtering precedes Message-ID validation for unrelated mail.
- Before cutover, all three bank partitions had zero financial-write and imported-ledger rows. Background maintenance never enters an import strategy.

Rejected credentials, deliberate throttling, all possible bank maintenance responses and uncertain YNAB writes were reviewed in code rather than deliberately induced against live financial accounts. These scenarios must not be described as all having passed live fault injection. No software change guarantees that banks will never reject a session or issue a new challenge.

## Baseline review

All 19 existing rules were recreated with fresh IDs, with contents and precedence verified unchanged. The user confirmed preserving the rule named Mullvad VPN that matches TAILSCALE.

The final EQ baseline retains three existing posted entries, settles three existing pending entries, and imports two missing pending entries. The settlement review covers two eBay foreign-exchange differences and QuizSolver posting. Legacy bank evidence ties the old authorizations to their posted purchases; the new backend checks the selected YNAB amount/date again before updating only bank fields. It preserves payee, category, memo and approval. The two new pending entries offset one another. Earlier proposals treating the three settlements as new transactions were rejected before any writes.

Rogers retains all 29 existing entries, including explicitly reviewed date edits and the uniquely matched entry without an import ID. NBDB's reviewed portfolio balance matched YNAB. Baseline approvals themselves made no YNAB changes.

## Deployment and cutover

Go build/vet, Go CDK build/vet, admin typecheck/lint/build, CDK inspection and pushed CI passed for the corresponding changes. The settlement review screen was inspected locally against the deployed backend. Amplify hosting is password protected; the independent SNS subscription is confirmed.

The deployed architecture has two application Lambdas, one Step Functions workflow, one on-demand browser task definition, four schedules and no NAT gateway. The repository's CDK context now retains production activation so a later normal deployment does not silently disable schedules.

Old bank schedules and maintenance were paused. At 23:04:31 UTC, SES routing switched to the new private email bucket with imports disabled, preserving incoming messages as durable paused jobs. Old workflows drained before the five old runtime Lambdas were disabled. Original settings were retained privately for pre-import rollback, then deleted with the recovery assets at the user’s request; after new writes, any rollback requires reconciliation.

The webhook is no longer used, as confirmed by the user. No sender migration is required. The temporary Codex health-check automation was deleted at the user's request; no recurring Codex monitor remains.

## Final cutover observations

The real EQ alert dry run completed in 6.268 seconds from durable receipt to completion, without a browser. This includes retrieval and reconciliation; it is not a measurement of external email transport. The final ECS EQ login with the MFA precision fix also passed.

The first live import created the $1 refund and then correctly stopped when the $1 authorization could not be confirmed. Investigation found the old authorization in YNAB's deleted-transaction delta, with its original import ID retained. The user explicitly chose to restore the charge and keep the refund. A one-off reviewed restoration reused the production lease, persisted a write intent and used a stable restoration ID. Its completed result was linked through the baseline to the original bank identity. No duplicate or unconfirmed financial write was blindly repeated.

The shared client now includes deleted entries in its account reads, and baseline approval rejects treating a known deleted import as a new transaction. A duplicate import whose live transaction cannot be found is an explicit review condition. EQ/Rogers creation intents now remain held across later jobs unless the existing stable ID proves completion. Successful session maintenance cannot clear an unresolved financial review. YNAB documents that deleted transactions are included only in delta requests in its [official transaction schema](https://github.com/ynab/ynab-sdk-python/blob/main/docs/TransactionDetail.md).

The cutover completed with imports enabled and the old writers still stopped. EQ updated exactly the three reviewed settlements; the restored charge and refund are the only two added entries. Payee, category, memo and approval fields on the existing purchases were unchanged. Rogers and NBDB completed without additional entries or adjustments.

A second real retrieval for every bank, followed by real session-maintenance jobs for every bank, produced **zero YNAB changes**. All six jobs used saved authentication without a browser. At the final check, all three banks were healthy, all recorded writes were complete, notification delivery had no pending/failed rows, all five CloudWatch alarms were OK, and the failure queue had zero visible or in-flight messages. Scheduled EQ maintenance also renewed and rotated its access token successfully.

The deployed application commit is `38e7142`; its CI and Amplify builds passed. Subsequent documentation-only commits describe these observed results. The original schedule times remain in place, and no extra Codex automation is active.

## Legacy retirement and admin domain (6 October, Atlantic time)

The custom admin domain is defined in Go CDK with the existing wildcard certificate and existing username/password. DNS was changed with the Cloudflare `cf` CLI. Moving directly between Amplify distributions failed because AWS observed the previous CloudFront target; clearing only the admin CNAME before recreating the association allowed the certificate and alias to attach. Authenticated HTTPS returned 200 from the rebuilt admin. Runtime notification links now use `https://transactions-admin.fredericks.app`.

`BankImportStack` and `TransactionsStack` were deleted after the cutover. Before deletion, the old SES activation custom resource was marked Retain to prevent its delete hook from disabling the new receipt set. The account-wide API Gateway logging role was transferred to the new CDK stack so unrelated APIs retain logging.

Removed the legacy Amplify app, unused webhook domain and DNS record, old databases, buckets, logs, schedules, task-definition revisions, AppConfig configuration, SSM settings, dedicated IAM roles/policies and the old bank-import user/access key. The two legacy secrets were initially scheduled for deletion; the user subsequently requested permanent deletion without the recovery window. The `bank-import` and `transactions-admin` GitHub repositories are archived. Shared CDK asset storage, unrelated applications and the wildcard certificate remain intact.

Old table records, job history, email and configuration were initially archived under the encrypted private bucket's `retired/2026-10-07/` prefix. At the user’s subsequent request, all versions and delete markers under `retired/` and the local recovery copies were permanently removed. The dedicated retirement lifecycle rule was removed from CDK. Old session object folders were not copied. Removed the obsolete SAM template, Makefile/email event and unused legacy configuration fields from the active repository.

The latest observed failure was EQ session-maintenance verification-email timeout at 23:51 UTC. The following scheduled job authenticated successfully by 23:56 UTC; subsequent maintenance jobs completed in roughly 2–3 seconds. The email arrived within the expected window, so the original polling miss remains unexplained; no speculative authentication change was made. All three banks were unblocked and the five infrastructure alarms were OK during retirement verification.

At the user's request, a Codex thread monitor runs every 30 minutes to inspect failures, repair recoverable issues and verify the admin. It remains quiet for unchanged/non-actionable state and never retries an uncertain financial write or removes a financial review hold.

Recovery deletion exception: AWS created a `SYSTEM` backup of the deleted `TransactionsEQState` table. A direct deletion request was rejected because AWS does not allow deleting this backup. AWS reports automatic expiry at **2026-11-11 00:21:18 UTC** (10 November, 8:21 p.m. Atlantic). No other DynamoDB backups or AWS Backup recovery points were listed. All 606 retirement S3 versions, 34 local recovery copies and both legacy secrets were permanently deleted; the retired-prefix lifecycle rule is absent from the deployed bucket.

## CloakBrowser port and cleared-only EQ retrieval (7 October, Atlantic time)

The failed 17:00 NBDB job was rejected with HTTP 403 / `AK000001` before email MFA. The port now translates the pinned original wrapper's launch/context behavior and complete human interaction algorithms to Rod, with source hashes and licenses retained. It also fixes a late update dialog intercepting credential entry, correlates the completed OAuth response with the actual wealth bearer, and distinguishes browser-access rejection in notifications. [Browser parity](browser-parity.md) records module coverage, intentional corrections and optional paths that remain unverified.

Go build/vet, Go CDK build/vet and synthesis passed. The non-secret executable fixture compared the original wrapper and port in the pinned ARM64 image; geometry/identity matched and trusted mouse, keyboard and wheel input produced correct values. Extended handle/frame/navigation/context/popup/cancellation checks, persistent profile reopening and repeated HTTP authentication with routing passed. No mocks, new test files or test suites were introduced or run.

Local NBDB login automatically entered email MFA and validated both configured accounts at 22:11. Separate Lambda reuse succeeded in 572 ms, followed by successful renewal/retrieval after natural access-token expiry in 1834 ms. Local Rogers reused its remembered device at 22:12 and validated its configured account; separate Lambda fetch returned 37 records in 2253 ms, and renewal plus fetch succeeded in 2303 ms. This Rogers login did not require a new email code. EQ's unchanged Chromium path completed automatic email MFA at 22:23 and returned both configured accounts and eight rows from a separate Lambda invocation.

The scoped CloudFormation release preserved the deployed hosting and schedule configuration and replaced the browser task with the pinned ECR digest. Both application Lambdas were rebuilt from the release source. A normal read-only AWS callback workflow, `verify-cloak-rod-nbdb-20261008-0140`, started fresh authentication after the earlier session's natural expiry and the one-time resume. It accepted credentials at 22:35:49, submitted email verification at 22:36:04, correlated wealth authentication and validated both accounts at 22:36:07, then completed its durable job and Step Functions execution at 22:36:09. NBDB authentication health is unblocked. Separate deployed Lambda reuse succeeded in 488 ms.

The real scheduled-source EQ dry run, `verify-cloak-rod-eq-bank-20261008-0140`, completed fresh ECS authentication and reconciliation at 22:35:46. Production logs recorded `posted=6 pendingSkipped=2`: routine retrievals filter pending rows before any matching or write. Purchase-email (`source=alert`) behavior is retained. There is no amount-specific rule, and this release does not delete existing YNAB entries. Both read-only workflow checks left financial ledger/write/review rows unchanged. All five infrastructure alarms were OK after these checks.

The same released ECS image also completed Rogers' normal callback workflow at 22:38:59 using the remembered device. The dedicated NBDB session-maintenance recovery job completed without another browser, clearing the incident and delivering the ordinary recovery notification. All three banks were healthy and unblocked afterward. Admin type checking, lint and production build passed; the public admin root retained its expected sign-in redirect.

Release artifacts: browser image digest `sha256:2f1c5c2f17e140333a9cda94ea1e78a7e55b62a96e5dba88e0b7059b5807a5a4`; final Lambda ZIP SHA-256 `07a8c733b298622e8fa695b8224ede53bc0abf030785b9b1956e86c705a27304`. The deployed workflow references `TransactionsEngine-BrowserTask-KJZthJMMyogy:1`. Final Lambda hashes were compared against the uploaded ZIP, and both scoped stack updates completed successfully.
