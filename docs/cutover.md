# Operations

The 6 October 2026 cutover from the old importers is complete; see [the verification record](verification-results.md) for its history. Stop new writes by setting `importsEnabled` false in settings; use `-c activate=false` for a CDK-defined schedule pause.

## Operator API

Use IAM credentials and `AWS_REGION=ca-central-1`. Payloads are JSON files kept outside Git with private permissions.

```sh
go run ./cmd/operator -action banks.list
go run ./cmd/operator -action submit -payload /private/dry-run.json
go run ./cmd/operator -action job.get -bank eq-bank -job JOB_ID
go run ./cmd/operator -action job.retry -bank rogers-bank -job JOB_ID
go run ./cmd/operator -action bank.resume -bank rogers-bank
```

- `banks.list` shows each bank's health: `blocked` (credentials rejected), `failing` (you were told it is failing) and `retryAt`.
- `job.retry` restarts a failed or paused job from the start.
- `bank.resume` clears a block and the retry time and forces a fresh login. Use it after fixing credentials or a challenge.

Dry-run payload: `{"version":1,"jobId":"unique-stable-id","bank":"eq-bank","source":"manual","purpose":"retrieve","dryRun":true}`. A dry run logs what it would import and writes nothing to YNAB.

## Measure

CloudWatch structured `bank-job` records contain bank, purpose, outcome, browser use and invocation duration. `session-renewal` records contain success/failure. Compare workflow start/completion for end-to-end latency; Lambda duration alone excludes the orchestration and queueing. A notification that could not be sent after three tries is logged as `notification not sent`.

Account transaction reads include YNAB deletion history, so an import you deleted in YNAB stays deleted.
