# Infrastructure audit

Scope: `cdk/cdk.go`, `cdk.json`, `scripts/*.py`, `Dockerfile`, `Dockerfile.broker`, `.github/workflows/*`, `go.mod`, `cdk/go.mod`, `.dockerignore`, `third_party/`. Judged against `docs/audit/rubric.md`. Nothing was run against AWS; live-state checks are written as read-only commands in the last section.

Decisions from Nathan that frame this report:
- The admin is removed.
- The single `Transactions` secret stays shared by every runtime. I do not propose splitting it.
- JSONLogic is dropped, Rod is replaced by playwright-go, the broker is kept but simplified, and all Go tests plus DynamoDB Local are removed.
- Every infra change below is a separate decision for Nathan, with its own risk and rollback.

Line estimates count source lines. The big `states := M{...}` and `res(...)` lines in `cdk.go` are single lines of 1,000 to 3,000 characters, so "lines" understates the real reduction there.

## Summary

1. There are two sources of truth for the stack. `cdk.go` produces a template, then `scripts/deploy-main.py` downloads the live template, rewrites it, and deploys that. CDK is never what ships. About 60 of the script's 293 lines undo, redo or stage things the CDK file does not contain or no longer needs.
2. Half the script, plus about 46 lines of `cdk.go` and the `cdk.json` admin context, exists for the admin. Removing the admin deletes it.
3. Several chains end at nothing. Alarms go to an SNS topic that this repo never subscribes to. The failure queue has no reader. `Maintain` has no schedule in the CDK. The `diagnostics/` lifecycle rule has no writer. Details are in F6, F7, F8 and F9.
4. CI lacks every tool the rubric's item 40 lists except `gofmt` and `go vet`. It pins Go 1.25.0, whose stdlib will carry known CVEs. It runs a DynamoDB service and an admin job that are both going away.

---

## A. CDK against the deploy script

### F1. The deploy script rewrites the live template instead of CDK being the source of truth (rubric 1, 3, 6; severity high)

`scripts/deploy-main.py:148-245`. `release` fetches the deployed template with `--template-stage Original`, patches it, uploads it, and builds a change set. The CDK file's output is never deployed. `cdk.json` and `cdk/cdk.go` still run in CI synth, so they can drift unnoticed.

Drift between CDK and what the script writes, or what the script assumes is live:

| What | CDK | Script | Verdict |
|---|---|---|---|
| Lambda code | S3 asset of `dist/lambda` (`cdk.go:39,101`) | zip uploaded to the CDK bootstrap bucket (`:158-168`) | Same effect, two paths. |
| `DEPLOYMENT_BRANCH` env | absent | set to `main` on both Lambdas and the task (`:169,194`) | Dead. Go reads only `DEPLOYMENT_COMMIT` (`internal/engine/learning.go:127`). |
| Browser image | `DockerImageAsset` from the repo (`cdk.go:48`) | prior release digest plus `--build-arg BROWSER_RUNTIME_IMAGE` (`:177-192`) | See F2. |
| `BROWSER_BROKER_URL` | hardcoded in a `cdk.go:129` override | hardcoded again (`:194`) | Duplicate. |
| `CapacityNotificationWorkflow` statement | `ProcessorRole` already has `states:StartExecution` on the same ARN (`cdk.go:87`) | adds a second statement (`:196-198`) | The script statement is redundant. |
| `YNABMirror` statement, `BatchGetItem`, `ynab-mirror` lifecycle rule, `ScheduleSyncYNAB` | none | removed (`:199-209`) | Nothing in CDK creates them, so this is a finished one-shot. |
| `OperatorEmail` | created only if context `alarmEmail` is set, and `cdk.json` sets none (`cdk.go:156-159`) | popped (`:223-224`) | No-op in CDK, finished one-shot. |
| `AdminBranchMain` | `cdk.go:194` | copies `AdminBranch` when `AdminBranchMain` is missing (`:219-222`) | Admin cutover staging. Delete. |
| Secret IDs | one secret via context `secretArn` | `migrate_secrets` swaps eight retired secrets for it (`:38-122`) | Finished migration, guarded by `'ApplicationSecret' in resources` (`:216-217`). |
| NoEcho parameters `GitHubToken`, `Auth0*` | created only when `repository` context is set (`cdk.go:173-178`) | `UsePreviousValue` and 1Password plumbing (`:42-62,255-266`) | Admin only. |

Finished one-shot migrations to delete, with lines:
- `:38-48` retired-secret tables, `:51-62` `admin_parameter_values`, `:65-122` `migrate_secrets`.
- `:127-132` the `cutover` and `secrets` phases and the `--admin-values-from` and `--refresh-admin` flags.
- `:199-209` the YNAB-mirror undo.
- `:210-217` the secrets phase.
- `:219-227` the admin staging and cutover branch.
- `:223-224` the `OperatorEmail` pop.
- `:196-198` the redundant processor statement.
- `:240-243` the allowed-set bookkeeping.
- The `Admin` and `Auth0` parts of `:255-266`.

After that only `release` remains, at roughly 110 lines against 293 now.

`scripts/merge-secrets.py` (86 lines) is already marked "One-off". The secret exists and `cdk.json` points at it. Delete the whole file (-86). The only live-state check is that the secret `Transactions` exists, which `cdk.json:13` already asserts.

Proposal A (decision, preferred): make CDK the only source of truth and delete `deploy-main.py` in favour of `cdk deploy`.
- Steps: run `cdk diff` against the live stack. Fold into `cdk.go` anything the script was adding and CDK lacks (only `DEPLOYMENT_BRANCH`, which is dead, so none). Replace the script with a 6-line `scripts/deploy.sh`: assert clean tree, assert pushed to main, run `TRANSACTIONS_REVISION=$(git rev-parse HEAD) cdk deploy --require-approval broadening`.
- Effect: removes about -290 lines of Python and the template-patching risk. The `git branch` and `ls-remote` guards (`:135-145`) move into the shell script, about +6.
- Prerequisite: F2, so the image builds from a pinned base rather than the previous release.
- Risk: the first `cdk deploy` may show hidden drift (`cdk diff` shows it first, nothing is applied) and, if the image asset hash differs, replaces the `BrowserTask` task definition. The script already expects this replacement (`:280`). Rollback: `aws cloudformation` rolls back automatically on failure. Keep the last `main-release-template.json` from `dist/main-release/` and its S3 copy for manual redeploy.
- If Nathan would rather keep the script, do Proposal A2: keep `release` only, delete the rest above. That is -180 lines, lower risk, and it keeps the two sources of truth.

### F2. The release image stacks on the previous release (rubric 1, 6; severity medium)

`Dockerfile:1,22` takes `ARG BROWSER_RUNTIME_IMAGE=browsers` and `FROM ${BROWSER_RUNTIME_IMAGE} AS release`. `deploy-main.py:177-182` passes the live task's image digest as that argument. Each release is therefore built on the previous release's image. The cloak and Chromium layers are never rebuilt, but the chain has three costs:
- The image depends on the history of the live stack. A fresh account, or a broken ECR image, cannot rebuild.
- `rm -rf node` and similar steps at `:27` run on top of the previous image. The build is not reproducible from the repo.
- It is the reason the script reads the live template at all.

The comment at `deploy-main.py:175-176` says refreshing browser binaries is "a separate, deliberately verified change". The Dockerfile already pins the binaries by SHA-256 (`:15,20`), so building from scratch is equally deterministic and fails safe if upstream disappears.

Proposal: drop the `ARG` and make the release stage `FROM browsers`. This is -3 lines in the Dockerfile and removes `:175-182` in the script (also gone under Proposal A). Cost: a full build per release, which downloads two browser binaries (the NBDB one needs the licence secret). Risk: low; build time and ECR storage only. Rollback: restore the `ARG`.

Stronger, tied to the Rod to playwright-go change: playwright-go drives the browser through its own bundled driver, which ships a Node binary. `Dockerfile:27` deletes `/usr/local/bin/node` and `/usr/bin/node`. That `rm` has to change when the port lands, so redo the whole image then and do not patch the stacking logic first. The image holds three browsers (stock Chromium, cloak 0.3.25 for Rogers, cloak 0.6.0 for NBDB), each with its own SHA pin (`:15,20`) and licence files (`:24-26`). The browser audit should say which can go, since each removal is -2 to -4 Dockerfile lines.

### F3. Env and secrets wiring in `cdk.go` (rubric 3, 4, 11; severity low)

- `cdk.go:127` sets `Environment` on the container with four entries. `:129` then overrides `ContainerDefinitions.0.Environment` with a different, longer list (adds `AWS_SECRET_ARN`, `DEPLOYMENT_COMMIT`, `BROWSER_BROKER_URL`). The `:127` list is dead. `:128` does the same for `Secrets`, which the L1 container-definition map supports directly. Fold both into `:127`: -2 lines, and the second list no longer silently wins.
- The index-based override `executionRole.AddPropertyOverride("Policies.0.PolicyDocument.Statement.3", ...)` (`cdk.go:91`) appends a fourth statement to a three-statement list by index. If someone adds or removes a statement at `:90` it breaks, or overwrites the wrong statement. Keeping one shared secret, replace it with a plain grant. `common` (`:85`) is already shared by processor, gateway and browser roles, and each of those also lists the secret at `:87-89`. Put `allow(secretsmanager:GetSecretValue, secretArn)` into `common` and also append it as the fourth element of the literal at `:90`. Result: -4 lines, no override, no index, no per-role duplicates. Rollback: revert; policy change only, no replacement.
- `cdk.go:92` builds `env` including `WORKFLOW_ARN`, `DEPLOYMENT_COMMIT` and others, and `:129` rebuilds a near-identical list by hand. Define the Lambda env map once and reuse it as the task environment list for the shared names. -6 lines.
- Account ID `187489282488` is typed at `cdk.go:22,59,84,107,110,129` and `deploy-main.py:15`. Use `stack.Account()` or `Aws_ACCOUNT_ID()`, or one constant. Low.
- `ecs:StopTask` and `ecs:DescribeTasks` on `*` (`cdk.go:130`): the state uses `ecs:runTask.waitForTaskToken`, not `.sync`. As far as I know, `.waitForTaskToken` needs only `ecs:RunTask` and `iam:PassRole`, so those two actions are likely unneeded (-1 statement). I could not confirm this without running it. Verify with a change set plus one dry run before removing. Low value, low risk.

### F4. `cdk.go` quality (rubric 11, 12, 17; severity low)

- `cdk.go:23` `root, _ := filepath.Abs(".")` drops an error. `:143` `b, _ := json.Marshal(input)` does the same.
- `cdk.go:152` `_ = integration` and `:169` `_ = adminRole` are dead assignments (both go: `integration` can be inlined, `adminRole` is admin).
- The file runs `go build` inside the CDK app (`:31`), then hands `dist/lambda` to an S3 asset. A synth is therefore a compile, which slows CI and makes `cdk diff` require a Go toolchain and a built binary. If Proposal A is taken, keep this. If the file is kept as a template generator only, move the build to the shell script.
- The file is a CloudFormation template written as 200+ lines of `map[string]any`. There is no type checking and no L2 reuse. Either that is accepted for exact CloudFormation control, or the stack could be a static `template.yaml`. I recommend neither a rewrite nor a swap: the raw maps are what make Proposal A cheap, because the drift is small. Revisit only if the stack grows.

---

## B. Secrets and IAM

One secret stays shared (Nathan's decision). Within that:

### F5. IAM simplifications that keep one secret (rubric 1; severity low)

- Replace the index override (F3). -4 lines.
- `APIGatewayLoggingRole` and `APIGatewayLoggingAccount` (`cdk.go:78-83`): the webhook API is an HTTP API (`ApiGatewayV2`, `:149`). The account-level CloudWatch role applies to REST API stages, not HTTP APIs. The stack carries this role only because an unrelated REST API in the account uses it. Both resources are `Retain`, so deleting them from the template leaves them in place and unmanaged. Decision for Nathan: -6 lines. Risk: none while retained; the stack just stops describing them. Confirm first that nothing in the stack depends on them: `aws cloudformation list-stack-resources --stack-name TransactionsEngine --query "StackResourceSummaries[?contains(LogicalResourceId,'Logging')]"`. Rollback: add them back.
- `s3:PutObject` for the processor role is `bucket/*` (`cdk.go:87`), which covers `incoming/`, where only SES should write. The processor writes `sessions/` and `snapshots/` only (`internal/engine/types.go:144`, `engine.go:230`). Narrow to those two prefixes, or to `sessions/*` after F8. Zero lines saved; hardening only.
- `Resource: *` on `states:SendTaskSuccess/Failure` (`cdk.go:89`) is required by that API (no resource-level permissions), so leave it.

---

## C. Mechanisms that name no live failure

These are infra pieces I could not tie to a failure that has happened or a reader that exists. Nathan asked for necessity questions; `audit-necessity` owns the app-level view.

### F6. Alarms go to an SNS topic with no subscriber (rubric 1; severity high)

`cdk.go:155-167`. Five alarms publish to `OperatorAlerts`. The only subscription is `OperatorEmail`, created if context `alarmEmail` is set (`:156-159`). `cdk.json` has no `alarmEmail`, and `deploy-main.py:224` pops `OperatorEmail` from the template (the comment says Pushover replaced AWS alarm email). If the live topic has no subscription, all five alarms fire into nothing.
- Check with the read-only command in the last section.
- If true, choose one of: (a) subscribe a real endpoint, +2 lines in `cdk.json` (restores the email); (b) delete the topic and all alarms, -12 lines, and rely on Pushover from the app; (c) point the topic at an HTTPS or Lambda subscriber that sends Pushover (new machinery, not recommended).
- Recommendation: (b) if the app already pushes failures, otherwise (a). Risk: (b) loses CloudWatch visibility of Lambda and workflow errors. Rollback: re-add.

### F7. The `DeliveryFailures` queue and its two dead-letter paths (rubric 1; severity medium)

`cdk.go:62-63,88,106,137,144,167`. The queue receives gateway async failures (`EventInvokeConfig` with `OnFailure`) and Scheduler dead letters, and drives the `UndeliveredEvents` alarm. Nothing in the Go code reads from it (grep for `sqs` finds no consumer). The visible-message alarm feeds the subscriber-less topic from F6. The failure it protects: a scheduled run or an incoming email fails twice in a row. The cost of losing it: a missed four-hour retrieval, repeated by the next schedule. Duplicate or missed transactions are visible in YNAB.
- Remove the queue, `GatewayFailureDestination`, the Scheduler `DeadLetterConfig`, `sqs:SendMessage` on both roles, and the alarm: about -8 lines.
- Keep the Scheduler `RetryPolicy` (`:144`), which is cheap.
- Risk: lose forensic records of failed async invocations; CloudWatch logs for the Lambda still exist (14-day retention). Rollback: re-add. The queue is `Retain`, so the data is not deleted by removing the resource.

### F8. Snapshot uploads and the `diagnostics/` rule (rubric 1, 6; severity medium)

- `internal/engine/engine.go:230` writes `snapshots/<bank>/<uuid>.json` on every run. `internal/engine/verify.go:66` writes one for each session verification. The only reader is the `Baseline` approval flow at `internal/engine/admin.go:340-354`, which is an admin API action. With the admin gone, this reader goes too, and the upload has no consumer. It then needs `SnapshotKey` in `Job` and `Baseline`, the `snapshots/*` read permission on the gateway role (`cdk.go:88`), and the 90-day lifecycle rule (`:60`): about -4 lines of CDK plus the Go below. The Go side belongs to `audit-necessity`.
- Rule `diagnostics/` (`cdk.go:60`): no Go code writes that prefix (grep for `diagnostics/` finds none). Delete the rule, -1 line.
- Bucket versioning (`cdk.go:60`) plus the `oldVersions` rule: all uploaded keys include a UUID (`internal/state/store.go:207`), so no object is ever overwritten. Versioning protects nothing but accidental manual edits, and the 7-day noncurrent expiry shows it was added then neutralised. Remove `VersioningConfiguration` and the `oldVersions` rule: -2 lines. Risk: none for data (retained bucket). Rollback: re-enable versioning.
- `sessions/` (7-day expiry) stays: the browser task passes the session to the processor through S3, and the DynamoDB pointer references it (`cmd/browser/main.go:62`, `internal/engine/types.go:125`).

### F9. `Maintain` has no schedule (rubric 1, 6; severity medium)

`cmd/lambda/main.go:81` handles an action `maintain` on the gateway, which starts session upkeep (`internal/engine/gateway.go:183-224`). The only schedules in `cdk.go` are `submit` jobs from `banks.All` (`:146-148`). Nothing triggers `maintain`. Either a live schedule exists outside the CDK (drift, and one the script's allowed-set would have blocked), or session upkeep never runs and the dead handler (about 45 lines of Go plus the `maintain-session` purpose branches) is removable. Check with the read-only command below.

### F10. The Step Functions workflow (rubric 1, 3; severity medium)

Why the workflow exists and what it does that Lambda alone cannot:
- `Browser` (`:134`) starts a Fargate task and waits for a callback token for up to 420 seconds. Lambda would need to start the task and then be woken by it, or poll.
- `Wait` (`:134`) sleeps for `waitSeconds` between `deferred` runs. A Lambda cannot sleep for minutes without paying; the alternative is a one-time EventBridge Scheduler entry per retry.
- Durable state across a Lambda that has a 180-second timeout.

So it is doing real work, and replacing it with Lambda plus Scheduler plus an ECS call would add custom timeout and recovery code (the 420-second `RecoverBrowser` path) to replace a state machine that is 13 states. I do not recommend dropping Step Functions. I do recommend trimming it.

Seven of the 13 states exist only for the separate notification step: `Dispatch`, `NotificationOnly`, `PreserveOutcome`, `Notify`, `NotificationOutcome`, `WaitNotification`, `FinalOutcome` (`cdk.go:134`). That step runs `Deliver` in its own Lambda invocation, with its own wait-and-retry loop and a notify-only entry point. The failure it prevents is a transient Pushover error losing a notification. The cost of that failure is a missed push for a transaction that is already in YNAB. If `audit-necessity` agrees, delivery becomes a best-effort call at the end of `Run`. The workflow becomes `Run → Outcome → (Wait | Browser → Run | RecoverBrowser | Done/Failed)`. Estimated effect:
- State machine: about -7 states (about -45% of the states line).
- `CapacityNotificationWorkflow` statement and the processor's `states:StartExecution` dependency for notify-only starts. Check whether the processor still needs `StartExecution` for another reason before dropping it.
- Go: the `Deliver` action in `cmd/lambda/main.go:74-75` and its state.
- This also removes the quiet coupling that makes the execution fail only after notification (`FinalOutcome`). The failure must move into `Run`'s `Outcome` choice (`failed → Failed`) so that `ExecutionsFailed` still fires.

Robustness items to fix in the same edit (rubric 34, 38):
- `call` tasks (`cdk.go:131-133`) have no `TimeoutSeconds`. Add `TimeoutSeconds: 200` (above the 180-second Lambda limit): +1 line.
- No `Retry` on Lambda tasks for `Lambda.ServiceException`, `Lambda.TooManyRequestsException` and `Lambda.AWSLambdaException` (SFN does not retry these by default). Add one block with `MaxAttempts: 3`, `BackoffRate: 2` and `JitterStrategy: FULL`. +1 line. The app does not retry the same call at another layer for those, so this is the one layer.
- The `Run → Outcome → Wait → Run` loop has no iteration cap, only the 86,400-second execution timeout. That is acceptable for one user and bounded by the deadline.
- Timeout order is consistent (task 300 s in `Dockerfile:30` < SFN 420 s < execution 86,400 s; Lambda 180 s). Rubric item 20 holds.

### F11. The webhook API (rubric 19, 34; severity low)

`cdk.go:149-154`. A public HTTP API, `POST /webhook`, with bearer-token auth in the Lambda (`internal/engine/gateway.go:152`). The default stage has no throttling. Add `DefaultRouteSettings` with `ThrottlingBurstLimit`/`ThrottlingRateLimit` of 5/2, about +1 line. Optional hardening. `_ = integration` is dead (F4).

---

## D. Amplify, Auth0, DNS, admin: everything to remove

Admin is dropped. Delete:

`cdk/cdk.go`
- `:168-169` `AdminComputeRole` and `_ = adminRole`.
- `:170-213` the entire `if repo != nil {...}` block: `Admin` Amplify app, `AdminBranchMain`, `AdminDomain`, the three NoEcho parameters, `OutputAdminURL`, `OutputAdminApp`.
- Imports `fmt` becomes unused once `OutputAdminURL` is gone. Check `toString` is still used (yes, for `outputs` at `:216`).
- Removed lines: about -46 plus one import.

`cdk.json`
- `repository`, `adminBranch`, `adminDomain`, `adminSubdomain`, `adminCertificateArn`, `adminBaseUrl`, `adminAuth0Domain`, `adminAllowedEmails`, `adminAuth0ClientId` (`:5-11,14`). That leaves `activate` and `secretArn`. -9 lines. Note `adminAuth0ClientId` is read by the deploy script (`deploy-main.py:214`), another reason to delete the secrets phase first.

`scripts/deploy-main.py`
- `ADMIN_PARAMETERS`, `admin_parameter_values`, the admin half of `migrate_secrets`, the `cutover` phase (`:225-236`), `AdminBranchMain` staging (`:219-222`), `Admin` and `AdminDomain` in the allowed sets (`:240-243`), and the NoEcho parameter handling (`:255-266`). (Counted in F1.)

Repo
- Whole `admin/` directory (about 45 tracked files). `admin/scripts/write-hosting-env.mjs`, `admin/package-lock.json` and the admin app's `lib/auth0.ts` go with it.
- `.dockerignore:2` (`admin`) and `.gitignore` lines `/admin/*`: -4 lines.
- CI: the Node setup and admin job (see section F).

Go (already removed `AdminURL` setting in 1a5dce2; notes for `audit-necessity`)
- `internal/engine/admin.go` is the admin's backend (`Admin` action, baseline approval). It is the gateway's `default:` branch at `cmd/lambda/main.go:88`. With no admin it has no caller except `cmd/operator`. Whether `cmd/operator` and `admin.go` stay is an app-level decision. Two parts are infra-relevant: `executionARN` at `admin.go:518-521` (uses `WORKFLOW_ARN` for admin links) and the `states:DescribeExecution` permission on the gateway role (`cdk.go:88`). If the admin API goes, drop `DescribeExecution` (-1 statement), and the gateway no longer needs a path to look up executions.
- The SFN client in the gateway is needed only to start executions; keep `StartExecution`.

Live cleanup (Retain/ordering matters; all as separate decisions)
- Removing `Admin*` from the template deletes the Amplify app, branch and domain mapping. Amplify domain removal deletes the CNAME it created, and the ACM certificate in `us-east-1` (`cdk.json:9`) is not in this stack, so it stays. Delete it manually if unused.
- Auth0: the tenant, application and `auth.fredericks.app` custom domain live outside AWS. Remove them separately (Auth0 dashboard) and remove the DNS record for `transactions-admin.fredericks.app` and `auth.fredericks.app` at the DNS host (Cloudflare per `.gitignore` and `docs/browser-broker.md`). I cannot enumerate those; Nathan must.
- 1Password item `Transactions` fields `github_token`, `auth0_client_secret`, `auth0_session_secret` can be deleted after the stack update.
- The GitHub token itself should be revoked in GitHub.
- Risk of the whole removal: Amplify deletion is not undoable; a rollback is a rebuild from git. Do it in the order: (1) deploy a template without Amplify, (2) verify gateway still works, (3) delete the external resources.

---

## E. Build coupling

### F12. `cdk` imports `internal/banks` for schedules (rubric 10, 32; severity medium)

`cdk/cdk.go:14,146`. The CDK app imports the application's bank registry only to read `b.ID` and `b.Schedule`. That import pulls `internal/bank`, `internal/banks/{eq,nbdb,rogers}`, `internal/cloak` and so go-rod (`cdk/go.mod` lists `go-rod/rod` as indirect; line `cdk/go.mod:~12`). `cdk` is a separate module, so each CDK run (and CI's `go work` synth) compiles the browser stack, and a Rod to playwright-go switch would ripple into the CDK module's `go.mod`.
- The registry also holds the strategy, the browser type and constructors: the CDK needs two strings.
- Proposal: the three schedules are infrastructure. Define them in `cdk.go` (a 3-entry `map[string]string`), -1 import, `cdk/go.mod` loses the Rod subtree (about -8 `require` lines), and the CDK module no longer depends on the application module. That also removes the `replace`-style coupling (`cdk/go.mod:~9` requires `github.com/nathanfredericks/transactions v0.0.0-00010101000000-000000000000`, a placeholder that only works through `go.work`). Cost: the bank IDs are duplicated in two places (3 lines). By rubric item 4, that is cheaper than the dependency. Risk: a new bank needs an edit in two places; `docs/adding-a-bank.md` should say so (+1 line). Rollback: revert.
- Alternative, if Nathan prefers one source: have `cmd/lambda` read `banks.All` and schedule through a gateway `maintain`-style self-scheduler. That adds machinery; not recommended.

### F13. Go version split (rubric 33; severity medium)

- `go.mod:3` and `cdk/go.mod:3`: `go 1.25.0`. `Dockerfile:2` and `Dockerfile.broker:1`: `golang:1.25.0-bookworm`. CI uses `go-version-file: go.mod`, so also 1.25.0.
- `go.work:1` says `go 1.26.5`, but `go.work` is gitignored (`.gitignore`, `git ls-files` does not list it), and CI creates a new one with `go work init` (`ci.yml:35`). The 1.26.5 version exists only on Nathan's machine. So the code is developed on 1.26.5 and built and shipped on 1.25.0. Code that compiles locally using 1.26 features (`errors.AsType`, `new(expr)`) would fail in CI and the image.
- Go 1.25.0 is the first release of the 1.25 series, so it has none of the 1.25.x security fixes. `govulncheck` will almost certainly report stdlib findings; run it to confirm (command below).
- Proposal: `go 1.26.5` in `go.mod` and `cdk/go.mod`, `golang:1.26.5-bookworm` in both Dockerfiles (or an `ARG GO_VERSION` shared by both, about +1 line each and one fewer place to edit). Delete `go.work` from `.gitignore`'s role as a hidden state: let CI use the committed `go.work` instead of `go work init`, by tracking it (it has 6 lines). Then `ci.yml:35` goes (-1 line) and the version cannot differ. Risk: the Lambda runtime is `provided.al2023`, so no Lambda-side Go version constraint; image builds change toolchain. Rollback: revert the version lines.
- `go 1.26.5` should be checked against what is installed: the `go.work` value already works locally.

---

## F. CI

`.github/workflows/ci.yml` (42 lines), `browser-broker.yml` (30 lines).

### F14. Gaps against rubric item 40 and cleanup (rubric 33, 40; severity medium)

Current: `gofmt -l cmd internal cdk/cdk.go`, `go vet`, `go build`, `go test` with DynamoDB Local, `cdk` vet and build, CDK synth, admin lint, typecheck and build.

Remove (Nathan's decisions):
- `services: dynamodb:` (`:8-12`), the `DYNAMODB_LOCAL=...` test line (`:29`), and Node setup plus the admin step (`:19-23,37-42`). About -17 lines.
- `go test ./...` goes with the tests. The comment line `**/*_test.go` in `.dockerignore:6` and the `cdk.go:48` `Exclude` entry become dead too: -2.

Add:
- `golangci-lint` v2 using the P0 `.golangci.yml` (+1 action, about +4 lines).
- `govulncheck ./...` (+2 lines).
- `go mod tidy -diff` and `go fix -diff` (+2 lines).
- `gofmt` today skips `cmd/`'s siblings and `cdk/`'s other files; use `golangci-lint`'s `gofumpt` formatter instead of the manual `test -z` line (-1).
- Scope: the `cdk` module is separate, so each tool runs twice; with a committed `go.work` (F13) `./...` covers `.` only, so keep `cd cdk && ...` for the same set. Acceptable.
- Triggers `on: [push, pull_request, workflow_dispatch]` run twice for a push to a PR branch. Use `push: branches: [main]` plus `pull_request`: ±0 lines, fewer minutes.
- Add `concurrency: {group: ${{ github.workflow }}-${{ github.ref }}, cancel-in-progress: true}`: +3 lines.
- Add `.github/dependabot.yml` for `gomod` (root and `/cdk`), `docker` and `github-actions`: new file, about 20 lines. Dependabot covers a pinned-by-digest Playwright image (`Dockerfile:10`), `cloakhq/cloakbrowser@sha256` (`Dockerfile.broker:8`) and the action tags. This is new config, but it replaces manual bumps, and the Playwright pin and Go toolchain are exactly what falls behind quietly.
- Pin `actions/*` by SHA, or accept tags. Low.

`browser-broker.yml`: builds `linux/arm64` with QEMU on an x86 runner (`:15,26`). Use `ubuntu-24.04-arm` and delete `docker/setup-qemu-action` (-1 line, much faster build). `provenance: true` and `sbom: true` add attestations nobody consumes; the image is pulled by one home machine. Remove both (-2). The workflow triggers on `internal/browserbroker/**`, which is already minimal. The broker is staying; after simplifying it, re-check the `paths` filter.

Net CI effect: about -35 lines, +12 lines.

---

## G. Scripts

### F15. `scripts/verify-bank.py` does not set `AWS_SECRET_ARN` (rubric 1; severity medium)

`verify-bank.py:36-44` builds the container environment from `STATE_TABLE`, `STATE_BUCKET`, `SETTINGS_PARAMETER` and AWS credentials. The deployed task sets `AWS_SECRET_ARN` (`cdk.go:129`) and the broker token and licence arrive as ECS `Secrets`. The local run instead reads the broker token with `aws secretsmanager get-secret-value --secret-id Transactions` (`:49-50`) and the licence from `~/.cloakbrowser/license.key` (`:55-62`). It never passes `AWS_SECRET_ARN`, so any code path that loads the full secret (bank logins) via `internal/config` fails locally, unless it reads it by another route. Check what `cmd/browser` does with `--verify-bank`: if it calls `config.LoadSecrets` it needs `AWS_SECRET_ARN`. Fix, shared with the one-secret design: `AWS_SECRET_ARN=aws('secretsmanager','describe-secret','--secret-id','Transactions')['ARN']`, +1 line, and then `:49-50` can read the broker token via the app instead of the script: -3. Net -2.
- `docs/local-verification.md` should state the same.
- Also: `values['TZ']`, `AWS_REGION` fine. `:48` hardcodes `BROWSER_BROKER_URL` a third time (after `cdk.go:129` and `deploy-main.py:194`); read it from the stack output or put it in one place. One constant: -0 lines, fewer copies.
- Line `:52` is indented with a tab inside a block of spaces (`# Explicit diagnostic override`). Python 3 tolerates a comment line, so harmless, but fix it.
- Rod to playwright-go: `go build -o dist/bank-browser ./cmd/browser` (`:34`) and the `/usr/local/bin/bank-browser` bind-mount at `:71` are tied to the image layout. After the port, check that the mounted binary still finds its driver.

### F16. `merge-secrets.py`: delete (F1). `scripts/cloak/*` and `cmd/cloak-inspect`

`scripts/cloak/binary.sh`, `inspect-reference.mjs`, `inspection.html`, and `cmd/cloak-inspect` are Rod-port reference tooling ("never used at runtime", `binary.sh:2`). `docs/cloakbrowser-rod-port-plan.md`, `docs/browser-parity.md` and `third_party/cloakbrowser-0.3.25` (46 tracked files, 328 KB) exist to support the Rod port. `Dockerfile:24-26` copies only three licence files from `third_party`; the Go sources cite it in comments. If Rod goes, these go with it, except the licence text MIT needs: keep `third_party/cloakbrowser-*/LICENSE` and `PLAYWRIGHT-LICENSE` (3 files) and delete the other 43. Estimate: -43 files, plus `cmd/cloak-inspect`, `scripts/cloak/`, and two docs. Do this only after the playwright-go port lands. If `internal/cloak/human/*` is retained because it is "derived from CloakBrowser" (`internal/cloak/human/*.go:1`), the licence files must stay.

---

## H. Dockerfiles and image context

- `.dockerignore` is a denylist. The CDK image asset uses the repo root as its directory (`cdk.go:48`), so any change to any non-ignored file (docs, scripts, `.github`) changes the asset hash and rebuilds the image. The Dockerfile only needs `go.mod`, `go.sum`, `internal/`, `cmd/browser/` and three licence files. Replace with an allowlist: `*`, then `!go.mod`, `!go.sum`, `!internal`, `!cmd/browser`, `!third_party/**/LICENSE`, `!third_party/**/PLAYWRIGHT-LICENSE` (about 8 lines, replaces 8). This also covers `**/*_test.go` and `admin`. Risk: a forgotten `COPY` source fails the build immediately. Rollback: revert.
- `Dockerfile.broker` and `Dockerfile` both pin `golang:1.25.0-bookworm` (F13). `go mod download` pulls every module dependency (AWS SDK, Rod) though the broker uses little of it. After removing Rod and the SDK-heavy pieces from the broker's import graph, this settles; at present it only costs build time. Low.
- Broker is supposed to be simplified: `Dockerfile.broker:8` pins a vendor image by digest, `CMD` wraps the vendor launcher (`:11`). Nothing to remove in the Dockerfile itself; the simplification is in `cmd/browser-broker` and `internal/browserbroker` (outside this audit). Update `docs/browser-broker.md` after.

---

## Ranked changes (value against effort)

| Rank | Change | Effort | Net lines | Risk |
|---|---|---|---|---|
| 1 | Remove admin: `cdk.go:168-213`, `cdk.json` admin context, `admin/`, CI admin job, script admin parts (D) | Medium | about -350 plus ~45 files | Medium: Amplify deletion not reversible; external Auth0/DNS cleanup by hand |
| 2 | Delete finished migrations and `merge-secrets.py` (F1) | Low | about -180 Python, -86 | Low |
| 3 | Check SNS subscribers (F6) and `Maintain` schedule (F9) with the commands below; fix what is dead | Low | -12 to -57 | Low |
| 4 | Make CDK the only template and replace `deploy-main.py` with `cdk deploy` (F1 Proposal A), after dropping image stacking (F2) | Medium | about -290 Python, +6 shell | Medium: first deploy may show hidden drift; use `cdk diff` first |
| 5 | Go 1.26.5 everywhere, commit `go.work` (F13) | Low | about 0 | Low |
| 6 | CI: drop DynamoDB, tests, Node; add golangci-lint, govulncheck, tidy and fix diffs, Dependabot (F14) | Low | -35 / +30 | Low |
| 7 | Trim the state machine, with notification inline (F10); add task timeouts and Lambda retries | Medium (needs `audit-necessity`) | -7 states, about -10 other lines | Medium |
| 8 | Drop `DeliveryFailures` queue and dead letters (F7) | Low | about -8 | Low |
| 9 | Snapshot S3 uploads, `diagnostics/` rule, versioning (F8) | Low (infra) plus Go | about -7 CDK | Low |
| 10 | Remove `cdk` to `internal/banks` import, define schedules in CDK (F12) | Low | -8 `go.mod`, +3 | Low |
| 11 | Replace the index-based IAM override and fold the duplicated env blocks (F3, F5) | Low | about -12 | Low |
| 12 | Allowlist `.dockerignore`, `verify-bank.py` `AWS_SECRET_ARN`, delete Rod-port tooling later (F15, F16, H) | Low | small | Low |

Order that avoids rework: 3 first (read-only), then 1 and 2 together, then 4 (so F2's image change happens once), then 5 and 6. Items 7 to 9 wait for `audit-necessity`. Item 12's third-party cleanup waits for the playwright-go port.

---

## Read-only commands for the lead or `audit-data`

All commands read only. Use `--region ca-central-1`.

```sh
# F6: does anything receive alarm notifications?
TOPIC=$(aws cloudformation describe-stack-resource --stack-name TransactionsEngine --logical-resource-id OperatorAlerts --query StackResourceDetail.PhysicalResourceId --output text)
aws sns list-subscriptions-by-topic --topic-arn "$TOPIC"
aws cloudwatch describe-alarms --alarm-name-prefix TransactionsEngine --query 'MetricAlarms[].[AlarmName,StateValue,AlarmActions]'
aws cloudwatch describe-alarm-history --history-item-type StateUpdate --max-records 50 --query 'AlarmHistoryItems[?contains(AlarmName,`TransactionsEngine`)].[AlarmName,Timestamp,HistorySummary]'

# F7: queue contents, to see whether failures have ever happened
Q=$(aws cloudformation describe-stack-resource --stack-name TransactionsEngine --logical-resource-id DeliveryFailures --query StackResourceDetail.PhysicalResourceId --output text)
aws sqs get-queue-attributes --queue-url "$Q" --attribute-names All --query 'Attributes.[ApproximateNumberOfMessages,ApproximateNumberOfMessagesNotVisible]'

# F9: is there a maintain schedule outside the CDK, and what is every schedule?
aws scheduler list-schedules --query 'Schedules[].[Name,State]'
aws scheduler get-schedule --name <each name from above> --query '[ScheduleExpression,Target.Input]'

# F1: drift between CDK and the live stack
aws cloudformation detect-stack-drift --stack-name TransactionsEngine          # then
aws cloudformation describe-stack-resource-drifts --stack-name TransactionsEngine --stack-resource-drift-status-filters MODIFIED DELETED
aws cloudformation get-template --stack-name TransactionsEngine --template-stage Original --query TemplateBody > /tmp/live-template.json
# from cdk/ (no deploy): CDK_OUTDIR=dist/cdk go run ./cdk, then diff the resource names against /tmp/live-template.json

# F1: confirm the retired secrets are gone from the template and still retained
aws cloudformation list-stack-resources --stack-name TransactionsEngine --query 'StackResourceSummaries[].LogicalResourceId' | tr ',' '\n' | grep -E 'ApplicationSecret|Credentials|CloakBrowserLicense|RepositoryConnection|AdminAuth0|AdminPassword|ScheduleSyncYNAB'
aws secretsmanager list-secrets --query 'SecretList[].Name'

# F5: logging role and account setting
aws apigateway get-account --query cloudwatchRoleArn
aws apigatewayv2 get-apis --query 'Items[].[Name,ApiId]'
aws apigateway get-rest-apis --query 'items[].[name,id]'

# D: what the admin removal will delete
aws amplify list-apps --query 'apps[].[name,appId,defaultDomain]'
aws amplify list-domain-associations --app-id <AppId from OutputAdminApp>
aws acm list-certificates --region us-east-1 --query 'CertificateSummaryList[].[DomainName,CertificateArn]'

# F8: bucket versioning and whether snapshots or diagnostics hold data
aws s3api get-bucket-versioning --bucket transactions-engine-private-187489282488-ca-central-1
aws s3 ls s3://transactions-engine-private-187489282488-ca-central-1/snapshots/ --summarize | tail -3
aws s3 ls s3://transactions-engine-private-187489282488-ca-central-1/diagnostics/ --summarize | tail -3

# F10: how often do Browser/Notify states fail or defer, and notification-only starts
SM=arn:aws:states:ca-central-1:187489282488:stateMachine:transactions-engine
aws stepfunctions list-executions --state-machine-arn $SM --status-filter FAILED --max-results 20 --query 'executions[].[name,startDate]'
aws stepfunctions list-executions --state-machine-arn $SM --max-results 50 --query 'executions[].[name,status]'

# F13: Go vulnerabilities (run locally; no AWS)
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

Findings that depend on these outputs: F6 (subscribers), F7 (any message ever), F9 (`maintain` schedule), the exact live drift for F1, and whether F5's logging role is used by any other API.
