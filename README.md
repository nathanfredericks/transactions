# Transactions

One Go application imports bank transactions and balances into YNAB. Bank protocols live in adapters; a shared engine owns authentication state, leases, reconciliation, writes and notification delivery. The Next.js admin is in `admin/` and calls the Go gateway through IAM-authenticated Lambda invocation.

```mermaid
flowchart TD
  Email[SES email] --> S3[Private S3: incoming email]
  S3 --> Gateway[Gateway Lambda]
  Webhook[Authenticated webhook + stable event ID] --> Gateway
  Schedule[EventBridge schedules] --> Gateway
  Admin[Next.js admin / operator command] --> Gateway
  Gateway --> State[(DynamoDB: jobs, leases, incidents, rules, ledger)]
  Gateway --> Workflow[One Step Functions workflow]
  Workflow --> Engine[Processor Lambda: shared engine]
  Engine --> Lease[Acquire bank lease and generation]
  Lease --> Session[Load encrypted saved session]
  Session --> Adapter[Bank adapter: renew and fetch]
  Adapter -->|Authentication required| Browser[One Fargate task: Rod + bank-selected Chromium]
  Browser -->|Validate accounts, save session, clean up, callback| Workflow
  Adapter -->|Validated normalized snapshot| Strategy[Posted / reconciliation / balances]
  Strategy -->|Approved baseline + imports enabled| YNAB[Shared YNAB client]
  Strategy -->|Dry run| Preview[Saved snapshot and preview]
  Engine --> State
  Engine --> Private[(Private S3: sessions and snapshots)]
  YNAB --> Intent[Persist financial result and notification intent]
  Intent --> Delivery[Separate notification delivery step]
  Delivery --> Pushover[Pushover]
  Delivery -->|Failure| Retry[Bounded notification-only retry]
  Upkeep[EQ session maintenance] --> Gateway
  Upkeep -.->|Authentication only: no YNAB path| Adapter
  AWS[Lambda / workflow / delivery alarms] --> SNS[Independent SNS email]
```

## Repository

- `internal/bank`: adapter contract, typed failures, narrow HTTP/browser/MFA helpers.
- `internal/banks`: EQ, Rogers and NBDB adapters; registry owns schedules, browser selection and import strategy.
- `internal/engine`: one orchestration, retry, baseline, import and notification implementation.
- `internal/state`: one DynamoDB fencing and S3 persistence implementation.
- `internal/service`, `internal/override`, `internal/email`, `internal/notify`: reused YNAB, AI, JSONLogic, MIME and Pushover code.
- `cmd/lambda`: gateway and processor roles from the same Go binary.
- `cmd/browser`: bounded Rod authentication worker; no YNAB secret permission.
- `cmd/operator`: administrative API client; no session-reading operation.
- `cdk`: Go CDK definition for the complete runtime. No NAT gateway or always-running worker.
- `admin`: thin Next.js rule editor, job delivery status, authentication reset and baseline review.

## Operational rules

Authentication expiry permits renewal and one browser attempt per job. Credentials and unsupported challenges pause authentication until an operator resumes it. Maintenance, throttling and temporary read failures defer through the workflow. Arbitrary 403s and malformed bank responses are review failures, not a reason to log in repeatedly.

A bank lease fences every state change with a generation. Session objects are immutable; publishing their pointer requires the current lease. Browser results are saved before callbacks and recovered if the callback is lost. Browser work has an absolute deadline shorter than the callback timeout. EQ missing-activity waits release the lease for 1, 5 and 15 minutes.

Session maintenance has no import path. EQ maintains its renewable session; hard expiry launches browser authentication. Rogers/NBDB authenticate when their scheduled retrieval needs it. Bank-enforced session limits still apply.

YNAB writes have no generic retry wrapper. Transaction creation uses stable import IDs; updates check the identified transaction after uncertain completion. An uncertain NBDB adjustment leaves an account hold that later jobs cannot bypass. Notifications have separate status and bounded delivery attempts; retrying delivery cannot import anything.

## Configuration and deployment

The one configuration document is `/transactions-engine/settings`. Application credentials and each bank's credentials use separate Secrets Manager resources. Do not put tokens in CDK context, workflow payloads, Git or logs. S3 objects are private and encrypted; browser IAM cannot read the application secret.

Initial CDK deployment disables all schedules and creates a settings placeholder with imports disabled. Complete configuration before invoking it. Admin hosting is enabled with the `repository` and `githubTokenSecretArn` CDK contexts, using a Secrets Manager JSON `token` field. Its generated basic-auth password is in `transactions-engine/admin-password`. Set the `alarmEmail` context to the operator address and confirm the SNS subscription. Infrastructure email is independent of Pushover.

Use the [local verification loop](docs/local-verification.md) to fix authentication and exercise real API calls without redeploying.

See [cutover and verification](docs/cutover.md) and [adding a bank](docs/adding-a-bank.md). Do not enable production imports until the real-account verification and reviewed baseline gates have passed.

## Build and inspect

No mocks, new test files or test-suite runs are used for this rebuild. CI performs only static checks and builds.

```sh
GOWORK=off go build ./...
go vet ./...
(cd cdk && go build ./... && go vet ./...)
npm --prefix admin ci --ignore-scripts
npm --prefix admin run typecheck
npm --prefix admin run lint
npm --prefix admin run build
# If a local workspace does not exist: go work init . ./cdk
npx --yes aws-cdk@2.1144.0 synth --output dist/cdk
docker build --platform linux/arm64 -t transactions-engine-browser:verify .
```

Existing source-level tests in retained packages are historical, and are not the release evidence for this rebuild. The retired coordinator/handler implementations and their tests were removed with the old runtime.
