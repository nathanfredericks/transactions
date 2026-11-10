# Transactions

One Go application imports bank transactions and balances into YNAB. Bank protocols live in adapters; a shared engine owns authentication state, leases, imports and notifications.

```mermaid
flowchart TD
  Email[SES email] --> S3[Private S3: incoming email]
  S3 --> Gateway[Gateway Lambda]
  Schedule[EventBridge schedules] --> Gateway
  Operator[Operator command] --> Gateway
  Gateway --> State[(DynamoDB: jobs, leases, bank health)]
  Gateway --> Workflow[One Step Functions workflow]
  Workflow --> Engine[Processor Lambda: shared engine]
  Engine --> Lease[Acquire bank lease and generation]
  Lease --> Session[Load encrypted saved session]
  Session --> Adapter[Bank API: renew and fetch]
  Adapter -->|Authentication required| Browser[One Fargate task: browser sign-in]
  Browser -->|Validate accounts, save session, clean up, callback| Workflow
  Adapter -->|Validated normalized snapshot| Strategy[Posted / balances]
  Strategy -->|Imports enabled| YNAB[Shared YNAB client]
  Strategy -->|Dry run| Preview[Logged preview]
  Engine --> State
  Engine --> Private[(Private S3: sessions)]
  YNAB --> Pushover[Pushover, sent inline]
```

## Repository

- `internal/bank`: the bank API contract, typed failures, sessions, snapshots and the HTTP helper. No browser code.
- `internal/banks`: EQ, Rogers and NBDB APIs; the registry owns browser selection and import strategy.
- `internal/signin`: browser-only bank sign-ins, page helpers and the email code reader.
- `internal/engine`: jobs (`job.go`), runs (`run.go`), sessions, email intake, imports, learning, notifications and operator actions.
- `internal/state`: one DynamoDB fencing and S3 persistence implementation, including `WithLease`.
- `internal/ynab`, `internal/alert`, `internal/email`, `internal/notify`, `internal/llm`, `internal/learn`: the YNAB client, purchase-alert parsing, MIME parsing, Pushover, the model client and the learned enrichment.
- `internal/config`: settings and secrets, loaded by each binary's `main` and passed in.
- `cmd/lambda`: gateway and processor roles from the same Go binary.
- `cmd/browser`: bounded Rod authentication worker; no YNAB secret permission.
- `cmd/operator`: administrative API client; no session-reading operation.
- `cdk`: Go CDK definition for the complete runtime. No NAT gateway or always-running worker.

## Operational rules

Authentication expiry permits renewal and one browser attempt per job. Rejected credentials block the bank until `bank.resume`. A challenge holds off scheduled runs for a day and is reported each time it fails. Maintenance, throttling and temporary failures are retried once within the job, then by the next schedule. A malformed bank response fails the job without a login. You are told when a bank starts failing or fails differently, and when it recovers.

A bank lease fences every state change with a generation. Session objects are immutable; publishing their pointer requires the current lease. Browser results are saved before callbacks; if the browser task fails or loses its callback, the workflow returns to the processor, which ends the job. A busy browser seat is waited for 120 seconds at a time, five times. Browser work has an absolute deadline shorter than the callback timeout. Late alerts wait 1, 5 and 15 minutes for their bank credit. Mail is acted on only when SES verified the sender with DMARC. Retrievals fetch the last 60 days, never before the bank's `startDate`. The processor stops work 20 seconds before its Lambda deadline so the job is saved and re-run.

EQ four-hour imports process cleared transactions only. Purchase-alert jobs retain the pending authorization import path.

EQ authentication runs on demand during four-hour imports and purchase-alert lookups. These jobs reuse usable sessions and renew or launch browser authentication when needed; sessions may expire between jobs, so purchase alerts can take longer to resolve. There is no background EQ upkeep schedule. Explicit manual session verification remains available and has no import path. Rogers/NBDB authenticate when their scheduled retrieval needs it. Bank-enforced session limits still apply.

YNAB writes have no generic retry wrapper. Posted imports skip any import ID YNAB already holds, deleted transactions included, and a duplicate import ID counts as done. A YNAB 4xx other than 429 fails the job; other errors are retried. Alert writes carry no import ID so YNAB can match them to the cleared import. NBDB balance adjustments write the remaining difference each run. Notifications are sent inline after the write, three tries, and a lost one is only logged.

## Configuration and deployment

The one configuration document is `/transactions-engine/settings`. All runtime credentials live in one Secrets Manager secret, `Transactions`, created outside the stack and passed to CDK as the `secretArn` context. Its JSON holds the application keys (`YNAB_ACCESS_TOKEN`, `OPENAI_API_KEY`, `PUSHOVER_TOKEN`, `PUSHOVER_USER`), `CLOAKBROWSER_LICENSE_KEY`, `BROWSER_BROKER_TOKEN` and `BANKS` (one `{username, password, mailToken}` per bank ID). The Lambdas and the browser task can both read it. Do not put tokens in CDK context, workflow payloads, Git or logs. S3 objects are private and encrypted.

The committed CDK context records this production deployment with schedules active. For a fresh installation or a deliberate schedule pause, synthesize/deploy with `-c activate=false`; the initial settings placeholder always has imports disabled. Check a dry run before enabling imports.

`cdk/cdk.go` is the whole stack, including the three bank schedules. There are no CloudWatch alarms; the application reports failures through Pushover.

Use the [local verification loop](docs/local-verification.md) to fix authentication and exercise real API calls without redeploying.

See [operations](docs/cutover.md) and [adding a bank](docs/adding-a-bank.md). Do not enable a new bank's imports until a real dry run looks right.

## Build and inspect

The repository has no tests. CI runs static checks and builds on pull requests and pushes to `main`: `golangci-lint` (with the `gofumpt` and `goimports` formatters), `govulncheck`, `go mod tidy -diff`, `go fix -diff`, `go vet`, `go build` and `cdk synth`, for the root module and the `cdk` module. Release evidence is the `cmd/evaluate` replay and live dry runs through the [local verification loop](docs/local-verification.md).

The `cdk` module is separate and does not import the application, so no `go.work` is needed; delete any local one, or keep `GOWORK=off` set. The CDK app builds from `cdk/`, runs from the repository root, and compiles the Lambda binary itself.

```sh
export GOWORK=off
go build ./... && go vet ./... && golangci-lint run && govulncheck ./... && go mod tidy -diff && go fix -diff ./...
(cd cdk && go build ./... && go vet ./... && golangci-lint run && govulncheck ./... && go mod tidy -diff && go fix -diff ./...)
npx --yes aws-cdk@2.1144.0 synth --output dist/cdk
docker build --platform linux/arm64 --secret id=cloak_license,src="$HOME/.cloakbrowser/license.key" -t transactions-engine-browser:verify .
```

Production releases use committed, pushed `main` source. From a clean `main`
checkout, `python3 scripts/deploy-main.py diff` shows what CDK would change.
`python3 scripts/deploy-main.py deploy` shows the same diff, asks for
confirmation and runs `cdk deploy`, which builds both Lambda packages and the
browser image (from its pinned base, with the CloakBrowser licence file as a
build secret). Docker must be running. The script refuses another branch,
uncommitted changes, or a revision that is not the remote `main` head.

## Shared browser capacity

Hermes, Rogers and NBDB share CloakBrowser capacity through the Go
[browser broker](docs/browser-broker.md). Hermes launches its persistent browser
on demand; bank authentication immediately reserves exclusive access. The
public HTTPS control API requires a bearer token, while CDP stays private.
