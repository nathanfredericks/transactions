# Verification and one cutover

This is a fresh engine, not a migration of old sessions or ledgers. Existing YNAB data is retained. Old writers continue until the verified replacement is ready; new dry runs do not write to YNAB.

## Gates

1. Build Go, admin and browser image; synthesize and inspect CDK. Validate the workflow with AWS. Confirm exactly two application Lambdas, one workflow and one browser task definition, no NAT and disabled schedules.
2. Populate the fresh settings and separate Secrets Manager secrets. Recreate reviewed rules through `rules.save`; do not copy operational rows or sessions.
3. For each bank, submit a real dry-run job. Verify the browser's account-only validation, saved authentication and direct retrieval from a separate Lambda invocation. Capture only metadata: request operation/status, token expiry, account IDs and record counts, never tokens or raw authentication exchanges.
4. Renew from a separate invocation and fetch again. Check EQ identities, full history and pagination; Rogers posted ten-day window and import IDs; NBDB account identities, exclusions and CAD balances. Chromium must really log into EQ; the CloakBrowser binary controlled through Rod must really log into Rogers and NBDB.
5. Observe expiry, concurrent triggers, fencing, callback recovery, maintenance and unsupported challenges. Maintenance and delivery-only jobs must produce zero YNAB writes. Do not deliberately submit wrong credentials to a real bank to test lockouts; record unexercised cases as unverified.
6. Pause old writers and drain in-flight workflows. Fetch final fresh dry runs. Compare the proposed baseline with existing YNAB data; explicitly resolve pending links. Approve each baseline only after review. No new importer is enabled while old writers can write.
7. Enable imports in settings, activate the new schedules and switch email/webhook routing once. Observe one authorized real operation per strategy. Keep old writers paused. Disable the new schedules/imports to stop; restoring old writers requires reconciliation of any writes already made.

## Operator API

Use IAM credentials and `AWS_REGION=ca-central-1`. Payloads are JSON files kept outside Git with private permissions.

```sh
go run ./cmd/operator -action banks.list
go run ./cmd/operator -action submit -payload /private/dry-run.json
go run ./cmd/operator -action job.get -bank eq-bank -job JOB_ID
go run ./cmd/operator -action baseline.preview -bank eq-bank -job JOB_ID
go run ./cmd/operator -action baseline.approve -bank eq-bank -job JOB_ID -payload /private/approval.json
go run ./cmd/operator -action notifications.retry -bank eq-bank -job JOB_ID
go run ./cmd/operator -action bank.resume -bank rogers-bank
go run ./cmd/operator -action job.retry -bank rogers-bank -job JOB_ID
```

Dry-run payload: `{"version":1,"jobId":"unique-stable-id","bank":"eq-bank","source":"manual","purpose":"retrieve","dryRun":true}`.

Baseline approval: `{"approved":true,"links":{"BANK_ACCOUNT_UUID#BANK_RECORD_ID":"YNAB_TRANSACTION_ID"}}`. The backend revalidates chosen identities, amounts and dates. Posted baseline records are deliberately treated as already handled; the reviewer must check these against YNAB. Approval is not an import.

NBDB review: action `review.balance`, payload `{"accountId":"BANK_ACCOUNT_UUID","decision":"confirmed","transactionId":"YNAB_TRANSACTION_ID"}` or an explicitly reviewed `not-written` decision. A bank authentication reset never clears this hold.

A webhook POST to `/webhook` requires the configured bearer token and `bank`, `notification`, stable `eventId`, and `occurredAt` timestamp. Success returns `202` and a durable `jobId`. Reusing an event ID with a different payload is rejected. Do not resend with new IDs to work around uncertain outcomes.

## Measure

CloudWatch structured `bank-job` records contain bank, purpose, outcome, browser use and invocation duration. `session-renewal` records contain success/failure. Compare workflow start/completion for end-to-end latency; Lambda duration alone excludes the orchestration and queueing. Track browser jobs per bank, renewal success and failures needing review. The EQ target is under 15 seconds for alert-to-completion with usable authentication; fresh-login cases are a separate population. No latency claim is valid until measured in the deployed engine.

Confirm SNS email delivery independently, and inspect the delivery-failure queue and failed application notification rows. Successful scheduled retrieval and routine renewal are quiet. A lost Pushover response may duplicate a notification, but never retries a financial write.
