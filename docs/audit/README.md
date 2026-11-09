# Audit roadmap

This merges six read-only audits, each judged against [the rubric](rubric.md):

| Report | What it covers |
|---|---|
| [necessity](necessity.md) | Whether each mechanism should exist at all |
| [robustness](robustness.md) | Failure behaviour |
| [architecture](architecture.md) | Packages and layout |
| [browser](browser.md) | Rod to playwright-go, and the broker |
| [infra](infra.md) | CDK, deploy, CI, images |
| [data](data.md) | Read-only production evidence, 2026-10-06 to 2026-11-09 |

Decisions already made by Nathan:
- remove the Next.js admin;
- drop rules (already deleted in production);
- move to playwright-go;
- keep the broker but simplify it;
- remove all Go tests;
- no secret split;
- infrastructure changes are allowed, each as its own decision;
- importer behaviour stays the same, with no flags and no new review flows.

## What the evidence says

Over 848 workflow runs in 33 days:
- **0 uncertain writes.** Every `WRITE#`, `UPDATE#` and `BALANCE#` record is `complete`.
- **Only 1 review record.** Every hold that fired was a false alarm: two genuine identical purchases, or entries Nathan had deleted in YNAB.
- **EQ has not imported since 2026-10-12.** It has been blocked on `challenge-required` since 10-15. 179 scheduled runs ended "review required", one notice went out, and its `retryAt` is ignored because `Blocked` is checked first.
- **The five CloudWatch alarms have no SNS subscriber.** The 28 failed executions told nobody.
- **Nothing invokes `Maintain`.** The webhook has been unused since 2026-10-06.
- **Rod and openai-go are linked into every binary,** the CDK app included.

The machinery built to prevent rare failures has never caught one. It has caused the failures that did happen. The roadmap removes about 1,800 lines from the engine, about 2,800 from the browser stack, the 2.3k-line admin and about 2,300 lines of tests and seams, and adds back a few hundred.

## Do now (operations, no code)

| # | Action | Why |
|---|---|---|
| O1 | ~~Subscribe the alarms~~ Nathan chose to keep them silent (2026-11-08); P2 deletes the unused alarms, `OperatorAlerts` and `DeliveryFailures` | Alarms reach nobody by choice |
| O2 | Done 2026-11-08: `startDate` was already 2026-10-02; `bank.resume` ran for eq-bank | EQ had been dark for 27 days |
| O3 | Stop using the AWS account root user locally; use an IAM user or SSO role | `audit-data` ran as root |

## Decisions for Nathan

**Approved 2026-11-08:** D1–D11 and D13. D12's learning heuristics stay with the learning session.

| # | Decision | Recommendation | Risk accepted | Phase |
|---|---|---|---|---|
| D1 | Delete `Uncertain`, the `started` intents, `UPDATE#`, the `BALANCE#` hold and `RenewalUncertain`; treat YNAB's duplicate-`import_id` reply as done | Delete | A rare unapproved duplicate if YNAB commits but the response is lost (never seen); one extra login after a lost renewal | P3 |
| D2 | Replace EQ `reconcile` and the `tx#` ledger with the Rogers `posted` path, using the same import-ID formula plus a one-release legacy skip | Replace | Up to 6 one-time duplicates from the legacy pending rows, visible and unapproved | P3 |
| D3 | Delete baseline review | Delete | A future bank relies on a dry run plus YNAB approval | P3 |
| D4 | Health becomes `Blocked` (credentials only), `Failing` and `RetryAt`. A challenge retries daily and notifies each time; non-bank errors don't count against the bank | Replace | One login attempt a day against a challenging bank | P3 |
| D5 | Inline notifications with 3 tries; the workflow goes from 13 states to 6; drop `DeliveryFailures` and `notifications.retry` | Replace | A Pushover outage drops a push | P3 |
| D6 | Capacity becomes a fixed 120 s wait ×5, then a normal failure; merge `RecoverBrowser` into `Run` | Simplify | Contention over about 10 min fails the job until the next schedule | P3 |
| D7 | Delete the webhook API (6 CDK resources) | Delete | Re-add if ever needed | P1 |
| D8 | Delete per-run snapshot uploads and the `diagnostics/` rule; keep uploads in `verify` | Delete | No stored snapshot for post-mortems | P3 |
| D9 | Operator surface shrinks to `banks.list`, `bank.resume`, `job.get` and `job.retry` | Shrink | Anything else goes through the AWS console | P1/P3 |
| D10 | Run CloakBrowser 154 for both Rogers and NBDB, inside the existing single seat | One build | If Rogers rejects 154, Rogers stays on 146 with no reservation | P6 |
| D11 | Humanizer for NBDB and Rogers only, not EQ | Yes | None; EQ never used it | P6 |
| D12 | Existing code that breaks the "no naming heuristics" rule: the EQ comma cut (`reconcile.go:60-65`), the payee-prefix match (`admin.go:423`), and category-name heuristics in `learn/model.go:401-475` | Comma cut and prefix match disappear with D2/D9. The learn heuristics are the learning session's call | — | P3 / separate |
| D13 | Make CDK the only deploy path (`cdk deploy`) and delete the template patching in `deploy-main.py` | Yes | The first `cdk diff` must be reviewed carefully against the live stack | P2 |

## Phases

Every phase is one branch and one PR, with the gates below and a check-in with Nathan before the next phase. P2 comes before any phase that changes CDK, because today `deploy-main.py` patches the live template and CDK edits would never reach production.

### P0: Tooling baseline
- Go 1.26.x in `go.mod`, `cdk/go.mod`, both Dockerfiles and CI.
- `.golangci.yml` (v2, per the rubric), `govulncheck`, `go mod tidy -diff`, `go fix -diff`.
- Dependabot for Go modules and Actions. CI runs on pull requests plus pushes to `main` only.
- Delete every `*_test.go`, the `go test` step and DynamoDB Local. Delete the 22 test-only function-variable seams.
- `cdk` stops needing `go.work`: run `cd cdk && go run .`.
- Fix the README testing section and `docs/adding-a-bank.md`.

### P1: Deletion pass (code only; no CDK deploy yet)
- **Admin:** remove `admin/` and the CI admin job.
- **Rules:** `internal/override`, `cmd/audit-rules`, jsonlogic, `findRules`, `rules.*`, `validateRule`.
- **Dead code:**
  - `Maintain`, `KeepWarm`, `maintain-session` and the upkeep shim;
  - `purchase-not-found`, `Execution` fencing and the legacy `alert#` check;
  - `service/ynab.go:178-287`, `decode[T]`, `types.Parameters`/`config.GetParameters` and unused settings fields;
  - the webhook handler (D7).
- **Operator and admin actions:** keep only what the four operator actions use (D9).
- **Browser dead code:** `cloak/network.go`, proxy auth, `maxminddb`, `x/net/proxy`.
- **Fix** `nbdb/auth.go:193` (lost humanization), and make a humanization fallback on a Cloak bank an error.
- **Scripts:** `scripts/merge-secrets.py`; fix `scripts/verify-bank.py` (`AWS_SECRET_ARN`).

### P2: One deploy path (D13)
- `cdk/cdk.go` becomes the source of truth.
  - Fold in the drift, for example `DEPLOYMENT_BRANCH` (unused, so delete it) and the duplicate `CapacityNotificationWorkflow`.
  - Delete the finished migrations.
  - Remove Amplify, Auth0 and `OutputAdminURL`, the webhook resources, the subscriber-less alarms with `OperatorAlerts` and `DeliveryFailures` (O1), the duplicate env block and the index-based IAM override (use a plain grant).
- `cdk` no longer imports `internal/banks`; the three schedules move into `cdk.go`. Centralize the account ID.
- Images stop stacking: each release builds from a pinned base by digest.
- `deploy-main.py` shrinks to a clean-`main` check, the image build, `cdk diff`, and `cdk deploy` after Nathan approves.
- **After deploy, by hand** (the order is in [infra.md §D](infra.md)): Auth0, DNS, the ACM certificate, the GitHub token and the 1Password fields.

### P3: Necessity cuts (D1–D6, D8, D9)
- The write path:
  - delete `Uncertain` and the intents;
  - YNAB errors carry the HTTP status, so a 4xx means not written and a duplicate import means done;
  - EQ moves to `posted` with the legacy skip;
  - delete review records, baseline and `BALANCE#`.
- The new `Health` (D4).
- Inline notifications and the 6-state workflow (D5).
- Capacity and `RecoverBrowser` (D6).
- Delete snapshot uploads (D8).

### P4: Remaining robustness
- **Deadline:** stop work 20 s before the Lambda deadline, and run cleanup on `context.WithoutCancel` with a short timeout. The lease TTL tracks the timeouts.
- **Step Functions:** a `Retry` for Lambda service errors with `JitterStrategy: FULL`, and `TimeoutSeconds` on every task.
- **Email intake:** require DMARC pass in the SES `Authentication-Results` header. Check the format against a stored message first.
- **Reads:** `since_date` on YNAB reads and a rolling EQ start, instead of full history.
- **Retention:** TTL on `JOB#` and `BROWSER#`.
- **Timezone:** one `Settings.Location()` plus `time/tzdata`.
- **Echoes:** `clearEchoes` becomes best-effort.
- **Notify:** replace `gregdel/pushover` with a context-aware POST, and delete `notify.init()`.

### P5: Readability restructure (target layout in [architecture.md §3](architecture.md))
- Replace `service` and `types` with `ynab` (one client built in `main`) and `alert`. `config` loads once in `main`, with no globals and no ctx values.
- Split `bank.Adapter` into a Rod-free `bank.API` plus a browser-only `banks/<bank>/signin`, so the Lambda no longer links the browser stack.
- **Engine:** `Engine` holds dependencies only, and each job runs as a per-job struct. Split the files into job, run, session, intake, deliver and ops. `Run` gets decomposed. An `importer` package takes the strategies and enrichment.
- **Cleanup:** typed status constants, one `importID`, one find helper, `state.WithLease`, a JSON slog handler built in `main`, and logging of `Failure.Kind` and `Operation`.

### P6: playwright-go and the broker ([browser.md](browser.md))
- Pin `github.com/mxschmitt/playwright-go v0.6401.0`. That is the module path its go.mod declares; its driver is 1.64.1 and it bundles Node.
- One `LaunchPersistentContext` path for all three banks, matching the vendor 0.6.0 launch, with CloakBrowser 154 (D10).
- **New code** (about 70 lines for the recorder):
  - `internal/chromium` and `internal/human` (about 400 lines, Click, Fill and PressSequentially only);
  - an `OnResponse` recorder in place of `bank.Observer`;
  - an `AfterFunc` watchdog, `signal.NotifyContext`, and a discard logger;
  - the licence status file for exit handling. Never log Playwright error text.
- **Delete:** `internal/cloak/**`, `pipe.go`, `chromium-args.json`, `cmd/cloak-inspect`, `scripts/cloak`, the vendored 0.3.25 JavaScript (keep the licences) and the parity and port-plan docs.
- **Image:** a slim Ubuntu base with the same font set as today.
- **Broker:** delete disk persistence, the uncertain/releasing states, history, the rate limiter, the allowlist and `Running()` (about −330 lines). Build its image on a native ARM runner.

## Gates (verifier, every phase)

No tests, by decision.
1. **Static checks**, for the root module and `cdk`: `GOWORK=off go build ./...`, `go vet ./...`, `golangci-lint run`, `govulncheck ./...`, `go mod tidy -diff`.
2. **CDK:** `cdk synth`, compared with `main`. From P2 on, `cdk diff` against production, reviewed by Nathan before any deploy.
3. **Images:** `docker build` for the images the phase touches.
4. **Parity:** a `cmd/evaluate` replay against approved YNAB history, which must not regress against `main`. A dry-run fetch per bank through the [local verification loop](../local-verification.md). For P6, a real sign-in for EQ, Rogers and NBDB, following browser.md's live verification steps.
5. **Diff review:** no change to YNAB write payloads, import IDs, dates or amounts beyond the decided ones. No new flags or review flows. No secrets or PII in logs.
