# Robustness audit

Scope: how the transactions app behaves when things fail. Judged against `docs/audit/rubric.md`. Taken as given: admin removed, rules dropped, Rod replaced by playwright-go, broker kept but simplified, all Go tests removed.

Each finding gives `file:line`, the rubric item, a severity, a verdict (**harden** or **delete the mechanism**), the proposed change and a line estimate. "Overlap" marks a mechanism that `audit-necessity` also covers, using the same names: *review holds*, *uncertain writes*, *account holds*, *intents*, *leases*, *Step Functions*.

## The one idea that matters most

YNAB's `import_id` is an idempotency key (rubric 36, BL). Every write with an import ID (all Rogers `posted` writes, all EQ `reconcile` writes) can be re-sent safely: YNAB returns `duplicate_import_ids` and writes nothing. The code doesn't use that. It wraps each write in a DynamoDB `started` intent, re-reads the account's full history to recover, and turns anything it can't prove into an Uncertain *review hold*. Nothing clears that hold for `WRITE#` keys: the operator's `review.update` accepts only `UPDATE#`, `review.balance` accepts only `BALANCE#`, and admin is being removed. So **a single YNAB 400/429/5xx or a Lambda timeout during a write can stop a bank's imports until someone edits DynamoDB by hand.** If writes are re-sent instead, most of the *intents* and *uncertain writes* machinery can go, and the system becomes more robust as a result.

---

## Findings

### R1. A `started` write intent can block a bank's imports for good (high)

- `internal/engine/import.go:111-131` (posted): a `started` record whose import ID isn't found in YNAB returns `Uncertain "transaction-write"`. Every later Rogers job hits the same record first and fails the same way.
- `internal/engine/reconcile.go:320-337` (EQ): same pattern. `previous.Status != ""` with no YNAB match gives Uncertain on every run.
- `internal/engine/import.go:140-143` and `reconcile.go:367-370`: any create error goes through `writeFailure` and becomes Uncertain. The `started` record stays behind, so the next run lands in the branch above.
- `internal/service/eq.go:59-76`: after a failed POST, `CreateEQTransaction` re-reads full history to look for the import ID. On `errDuplicateImport` with no live match, it raises `Invalid "previous-import-missing"`, another permanent hold (`reconcile.go:312-313` does the same).
- No operator action clears a `WRITE#` hold (`internal/engine/admin.go:229-245` accepts only `UPDATE#`).

How it triggers: YNAB returns 429 (200 requests/hour limit) or 400 (for example a model-named payee that is too long) on a POST, or the Lambda dies between `put(started)` and the POST leaving. Cost: the bank's imports stop silently after one "review" notification.

Rubric 2, 35, 36. Verdict: **delete the mechanism** (overlap: *intents*, *uncertain writes*, *review holds*).

Change:
- Drop the `started` state for writes that carry an import ID. Write only `WRITE#…{status: complete}` after success, which is kept as the cheap "already done" cache that saves enrichment and YNAB calls.
- In `createTransaction` (`internal/service/ynab.go:170-174`), treat a `duplicate_import_ids` hit on our own ID as success with no transaction. The caller records `complete` with an empty `TransactionID` and skips `clearEchoes`/`recordSuggestion`. This also respects a user's deletion in YNAB instead of holding for review.
- Delete the post-failure lookup in `CreateEQTransaction` (`eq.go:64-76`), the recovery branch in `posted` (`import.go:111-131`), and `reconcile.go:320-337` together with the `started`/`complete` bookkeeping around the create (`reconcile.go:362-366, 373-376, 381-391`). EQ imports only posted rows now (`reconcile.go:100-108`), so every EQ create carries an import ID. The import-ID scan of `snapshots` at `reconcile.go:311-318` already covers "written but ledger not saved".
- A create error then becomes Temporary (retry next attempt) rather than Uncertain.

Estimate: about −70 lines, +5.

### R2. No headroom before the Lambda deadline, and cleanup uses the same ctx (high)

- `cdk/cdk.go:101`: processor `Timeout: 180`.
- Per record, `enrich` can call the model with a 45 s client timeout (`internal/service/ai.go:27`), then a YNAB POST with 20 s (`internal/service/ynab.go:85`). A cold start also syncs 2 years of the mirror through 4 YNAB calls (`internal/service/sync.go:41-85`). Four model-routed records plus a cold sync can pass 180 s, and the first run after a pause or a new baseline easily does.
- `cmd/lambda/main.go:18-71`: the handler passes the raw Lambda ctx straight through and sets no soft deadline (rubric 20).
- `internal/engine/engine.go:44-49` (`Release(ctx, …)`, line 46) and `engine.go:65-69` (`failure(ctx, …)`) run on the same ctx. After a deadline-driven error they fail too. On a hard timeout no defers run at all: the lease stays held for 15 min (`internal/state/store.go:66`), the job stays `running` forever, and any in-flight write leaves a `started` intent (R1).

Rubric 20, 21, 22. Verdict: **harden**.

Change:
- In `handle`, for the processor role, derive `work, cancel := context.WithDeadline(ctx, deadline.Add(-20*time.Second))` and defer `cancel()`.
- In `Run`, build `cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)` and use it for `failure` and `Release`.

With R1 in place, hitting the soft deadline produces a Temporary failure, then a `deferred` Wait, then a fresh invocation that skips the records already marked `complete`. The loop makes progress instead of corrupting state. Optional: if logs show deadline deferrals often, raise the processor timeout to 600 s. The lease TTL must then go up too (see R13).

Estimate: +10 lines.

### R3. Email intake trusts the `From` header (medium)

- `internal/engine/gateway.go:86` (`alert@eqbank.ca`) and `gateway.go:104-105` (configured senders) decide on `parsed.From` alone. `internal/email/parser.go:25-37` never looks at authentication headers.
- The SES rule has `ScanEnabled: true` (`cdk/cdk.go:113`), so SES prepends its verdicts to the stored message: `X-SES-Spam-Verdict`, `X-SES-Virus-Verdict`, and an `Authentication-Results: amazonses.com; spf=…; dkim=…; dmarc=…` header. Nobody reads them. The S3 event that reaches the gateway carries no verdict of its own.
- Impact: anyone who learns `transactions@fredericks.app` (it's in `cdk/cdk.go`) can send a forged EQ alert, which creates an amount-only YNAB transaction and a "Transaction Approved" push. They can also send a forged card alert whose free text goes to the extraction model (`internal/service/alerts.go:96-100`).

Rubric 1 (cost is a bogus YNAB entry plus a push, low but real). Verdict: **harden**.

Change: in `email.Parse`, return the first `Authentication-Results` header (SES's own is topmost; any sender-supplied copies come below it). In `Email`, skip (log and `continue`) unless it contains `dmarc=pass` with `header.from=` equal to the `From` domain. Check the exact format against a stored message under `incoming/` before coding. Don't add a spam-verdict check; DMARC covers it.

Estimate: +10 lines.

### R4. YNAB errors carry no HTTP status, so a definite rejection reads as "may have been written" (medium)

- `internal/service/ynab.go:32-45`: `ynabErrorResponse` has no status field. `ynab.go:121-127` throws `res.StatusCode` away, except in a plain string error.
- `internal/engine/import.go:49-54` (`writeFailure`) treats only `bank.Invalid` as a definite outcome, so a 400/401/404/409/429 on a POST becomes Uncertain. `import.go:189-191` (balances) and `reconcile.go:472-474` (updates) map every error to Uncertain unconditionally.

After R1, R5 and R6, only the writes without an import ID go through `writeFailure`: notification pending writes (`import.go:317-319`) and amount-only alerts (`import.go:500-502`). Their holds are per job, so nothing else is blocked, but a 429 still throws the purchase away.

Rubric 15, 35. Verdict: **harden**.

Change: add `Status int` to the error returned from `do` (wrap both branches in one `*YNABError{Status, ID, Name}`, which also fixes rubric 15 naming). In `writeFailure`, a YNAB 4xx means nothing was written, so return `bank.Fail(bank.Temporary, operation)` (a retry is safe). Only transport errors and 5xx stay Uncertain.

Estimate: +8 lines, −4.

### R5. Balance adjustments: the `BALANCE#` intent and per-account hold (medium)

- `internal/engine/import.go:172-197`: a `started` record returns `Uncertain "balance-adjustment"` for that account on every run until the operator runs `review.balance` (`admin.go:246-293`, which goes away with admin).
- Balance reconciliation already converges on its own: each run writes `bank balance − YNAB balance`. If the last adjustment landed, the difference is 0 and nothing is written. If it didn't, it's written now. The worst case, a read-after-write lag, gives one visible extra adjustment that the next run reverses.

Rubric 1, 2. Verdict: **delete the mechanism** (overlap: *account holds*, *intents*).

Change: delete the `BALANCE#` get/put and the hold. Compute the difference, write it, return any error as Temporary.

Estimate: −20 lines (plus about −45 in `review.balance` if admin.go survives in any form).

### R6. Transaction updates: the `UPDATE#` intent (medium)

- `internal/engine/reconcile.go:457-478`: a `started` record that doesn't match gives Uncertain on every EQ run, and the only release is the operator's `review.update`.
- `UpdateEQTransaction` (`internal/service/eq.go:147-168`) is a PUT with complete values, so it is idempotent by nature (BL).

Rubric 2, 36. Verdict: **delete the mechanism** (overlap: *intents*, *review holds*).

Change: if the transaction doesn't match, PUT it and return errors as Temporary. Drop the operation record.

Estimate: −12 lines.

### R7. The notification `started` recovery can never succeed (low)

- `internal/engine/import.go:234-249` looks for `tx.ImportID == previous.ImportID` ("ALERT:…"). But `eqTransactionPayload` sends an import ID only when `status == "posted"` (`internal/service/eq.go:102-104`), and notification writes are `"pending"` (`import.go:317`). The loop is dead code that costs a full-history read and always ends in Uncertain.

Rubric 6. Verdict: **delete the mechanism** (overlap: *uncertain writes*).

Change: `case "started": return 0, bank.Fail(bank.Uncertain, "notification-import")`. The hold is per job and the cleared bank import supplies the transaction later. Drop the now-unused `ImportID` on the intent (`import.go:309, 313`).

Estimate: −10 lines.

### R8. Bank health counts YNAB, AWS and model failures as bank failures (medium)

- `internal/engine/engine.go:331-394`: `failure()` runs for every error from `Run`. `bank.Classify` (`internal/bank/bank.go:41-47`) maps anything that isn't a `*bank.Failure` (YNAB errors, DynamoDB/S3 errors, `ValidateAdjustmentPayee`'s plain error at `eq.go:188`) to `Temporary "operation"`. That error then bumps `health.Failures`, sets `health.RetryAt` with exponential backoff up to about 64 min (`engine.go:352`), and sends "{bank}: Temporary error" (`internal/engine/notification_copy.go:37-39`).
- Model failures are swallowed by `enrich`'s fallback on the bank paths (`internal/engine/learning.go:140-165`), which is fine. On the notification path, `extractAlert` failing becomes `Temporary "notification-extract"` (`import.go:251-254`) and backs off all of `HEALTH#notifications`.
- Engine-side config errors also use `bank.Fail(Invalid, …)` (for example `account-mapping`, `import.go:75,82`) and get reported as "Unexpected response received" from the bank.

Rubric 13, 15. Verdict: **harden**, in the smallest way (overlap: *review holds*, since health is where `Blocked`/`ImportReview` live).

Change: in `failure()`, when `errors.As(cause, &f)` is false (not a bank failure), set the job to `waiting`/`failed` by the same attempt rule but leave `HEALTH` alone (no `Failures++`, no `RetryAt`, no incident). The Lambda/Step Functions alarms already page for infrastructure. The default-to-Temporary in `Classify` is the right direction for reads, so keep it.

Estimate: +6 lines.

### R9. No Step Functions retry for Lambda service errors, and a failed execution is never re-run (medium)

- `cdk/cdk.go:131-133`: the processor Task (`call`) has `Catch → Failed` and no `Retry`. `Lambda.ServiceException`, `Lambda.TooManyRequestsException` and `Lambda.SdkClientException` are transient (rubric 35), but they fail the execution at once.
- `internal/engine/gateway.go:53-63`: re-submitting a non-terminal job hits `ExecutionAlreadyExists` (names are unique for 90 days), which is treated as success. A purchase alert whose execution failed this way is therefore never imported. For a `notification-<account>` sender with no bank import behind it, the transaction is lost. The same goes for `Notify` (`cdk.go:134`): a transient error drops the push notification.
- "Retries have no jitter": there are no Step Functions retries at all. The code-driven waits (`engine.go:352`, `engine.go:481`, `import.go:221`) have no jitter, but with one client there is nothing to de-correlate (BL), so don't add jitter there.

Rubric 34, 35, 38. Verdict: **harden** (overlap: *Step Functions*).

Change: add one line to `call`:
`"Retry": [{"ErrorEquals": ["Lambda.ServiceException","Lambda.AWSLambdaException","Lambda.SdkClientException","Lambda.TooManyRequestsException"], "IntervalSeconds": 2, "MaxAttempts": 3, "BackoffRate": 2, "JitterStrategy": "FULL"}]`.
`Run`/`Deliver` are already safe to re-invoke (execution check, lease, terminal statuses). A Lambda `TimeoutSeconds` on these Tasks adds nothing while the function timeout is lower, so skip it.

Estimate: +1 line.

### R10. `Catch → Failed` leaves the lease held (low)

- `cdk/cdk.go:132`. Verified, but it bites only when the Lambda crashes or times out. Whenever `Run` returns an error itself, the deferred `Release` has already run (`engine.go:44-49`). On a crash no Go code can release anything, so the 15-minute lease expiry is the designed release (rubric 37 satisfied).

Verdict: **no new machinery**. Don't add a release state to the workflow. R2 makes the timeout case rare and clean. Two small fixes:
- Stop joining the `Release` error into the result (`engine.go:46-47`). A failed release after a successful job currently turns into a Lambda error, then `Catch → Failed` and a false alarm, while the lease expires anyway. Log it instead.
- Log the dropped `Release` errors at `engine.go:414` and `internal/engine/notify.go:42` (rubric 12).

Estimate: ±3 lines.

### R11. `clearEchoes` can fail after the credit is committed (low)

- `internal/engine/import.go:144-151`: the credit's `WRITE#` is saved as `complete` before `clearEchoes` runs. If `clearEchoes` fails (a YNAB read or delete, or the `ECHO#` put), the job fails and is retried, but the retry `continue`s past the completed credit (`import.go:108-110`). The echo is never cleaned up.
- Result: a leftover duplicate alert transaction in YNAB, plus a spurious "Temporary error" incident. No double import and no data loss.

Rubric 1, 13. Verdict: **delete the mechanism**, or at minimum stop it failing the job (overlap: necessity should weigh the whole echo feature: `findEchoCredit`, `echoWaits`, `requestRetrieval`, `clearEchoes`, the `ECHO#` claims, and the `Stale`/`AlertDate` fields, about 150 lines). The rubric's own cost model ("YNAB makes an occasional duplicate easy to see and delete") argues for deletion.

Minimal change if kept: log the `clearEchoes` error like `recordSuggestion` does and return nil (±2 lines).

### R12. Unbounded growth: no DynamoDB TTL, and scans that grow forever (low)

- `cdk/cdk.go:57`: the table has no `TimeToLiveSpecification`.
- Scanned on hot paths, growing without bound:
  - `NOTICE#` in full on every delivery (`notify.go:43`).
  - `LEARN/TX#` on every learning load (`learning.go:99`).
  - `WRITE#notification#` on every positive Rogers credit (`import.go:402`).
  - `tx#` ledger on every EQ run (`import.go:513`; that one is real state).
- Also accumulating with no reader: `JOB#`, `BROWSER#<generation>`, `ECHO#`, `review#`, `UPDATE#`.
- Rubric 39. Verdict: **harden** (TTL is close to free).
- Change: enable TTL on a `ttl` attribute. In `engine.put`, set `ttl` = now + 90 days for keys with the prefixes `JOB#`, `NOTICE#`, `BROWSER#`, `ECHO#`, `WRITE#notification#`, `WRITE#alert#`. In `recordSuggestion`, set `ttl` = now + 2 years (matching `MirrorYears`, `internal/service/sync.go:15`). Keep Rogers/EQ `WRITE#` and `tx#` without TTL. Rogers looks back only 10 days (`internal/banks/rogers/api.go:80`), so 90 days would be safe there too, but there's no need.
- Estimate: +12 lines (store: an optional `ttl` on `Put`/`PutPlain`; engine: a prefix table; cdk: 1 line).

### R13. Full-history reads from YNAB and from EQ (low)

- `internal/service/eq.go:46`: `GetAccountTransactions` sends `last_knowledge_of_server=0`, which returns the whole account history every time.
- After R1 and R7, the remaining callers are `reconcile.go:121` (each mapped EQ account, every run), `findEchoCredit` (`import.go:350`) and `clearEchoes` (`import.go:424`). Each one costs one of the 200 requests/hour and a response that grows for years against a 20 s timeout.
- `internal/banks/eq/api.go:75-85`: EQ fetches from the fixed `settings.StartDate` every 4 hours, so bank-side paging grows with the same history. That means more requests to a bot-sensitive bank and more Lambda time.
- Rubric 39. Verdict: **harden**.
- Change: give `GetAccountTransactions` a `since` parameter. `reconcile` passes the earliest date among the snapshot records that are not `baseline.Seen`, minus 30 days. The echo callers pass `alert.Date` or `credit.Date − 5d`. For EQ, consider fetching from `max(StartDate, today − 60d)`. That's a behaviour change for very late postings, so it's Nathan's call.
- Estimate: +6 lines.

### R14. Leases: fencing is right, and the TTL is tied to timeouts (low)

- `internal/state/store.go:100` fences every DynamoDB commit with `generation = :g AND expires > :now`, so storage checks the fencing token (rubric 37 ✓). `Release` is conditional on the generation (`store.go:116-126`) ✓. Expiry handles crashes ✓. YNAB writes can't be fenced, which is one more reason R1's re-sendable writes are the right model.
- The fixed 15-minute TTL (`store.go:66`) must cover Run (180 s) + browser (420 s, `cdk.go:134`) + Run (180 s) = 780 s. If R2's optional timeout increase happens, raise the TTL to 2×Lambda + 420 s, or the second Run loses its lease mid-job.
- `store.go:89-90` returns `fmt.Errorf("lease lost")` with no sentinel. It's classified as Temporary and retried (rubric 15). Add `var ErrLeaseLost`.
- Verdict: **harden** (minor). Overlap: *leases*. A single conditional lease per bank is the simplest correct mutual exclusion, so keep it. Taking the bank lease to deliver pushes (`notify.go:34-42`) is a separate question for necessity: notices could be written outside the lease, which would also remove the `deferred 5` contention with imports.
- Estimate: +3 lines.

### R15. `notify.init` replaces `http.DefaultClient` (low)

- `internal/notify/pushover.go:13-15` mutates a process global from `init()` because `gregdel/pushover` takes no ctx. Every user of `DefaultClient` inherits a 15 s timeout and no redirects. `internal/llm/llm.go:44` copies it and `internal/service/ynab.go:85` borrows its `Transport`, so behaviour depends on import order.
- Rubric 11, 19, 32. Verdict: **delete the mechanism**.
- Change: replace the library with one `http.NewRequestWithContext` POST of a form to `https://api.pushover.net/1/messages.json` on a package-level `&http.Client{Timeout: 15s}`. In `llm.go`, build a fresh `http.Client`, and in `ynab.go` drop the `Transport` line.
- Estimate: −1 dependency, about −30 lines, +18.

### R16. Small error-handling defects in failure paths (low)

- `internal/engine/import.go:165` and `internal/engine/gateway.go:101`: `zone, _ := time.LoadLocation(...)`. A bad timezone gives a nil `*Location`, and `time.Now().In(nil)` panics. In the processor that is a crash with the lease held. Return the error (rubric 12, 17). ±4 lines.
- `internal/engine/gateway.go:81-83`: `email.Parse` errors on unrelated mail (spam with no `Date` header, for example) fail the S3 event, so the event is retried 2× (`cdk.go:106`), lands in the DLQ and trips the `UndeliveredEvents` alarm. Parse the headers first and drop mail from unknown senders before parsing the body. ±5 lines.
- `internal/engine/admin.go:114,139,184,210,236,253,498`: `"bank busy"` hides DynamoDB errors behind a busy message. It goes away with admin; don't carry the pattern into `cmd/operator`.
- Dead failure branches to delete with R1 (overlap: necessity, dead code, rubric 6):
  - `reconcile` always returns `missing == false` (`reconcile.go:443-450`), so `engine.go:250-262` (`purchase-not-found` retries) and `notification_copy.go:15,49-51` are unreachable. About −15 lines.
  - No registration sets `KeepWarm` (`internal/banks/registry.go:11-13`) and no schedule invokes `maintain`, so `Maintain` (`gateway.go:183-225`) and the `ImportReview` short-circuit in `recovered` (`engine.go:279-281`) are dead. About −45 lines.

---

## Verified prior notes

| Note | Status |
|---|---|
| `bank.Classify` maps unknown errors to Temporary (`bank.go:41-47`) | Confirmed. Correct for reads. The harm is in health accounting (R8). |
| YNAB error type has no status (`service/ynab.go:32-45`) | Confirmed, and `do` discards `StatusCode` at 121-127 (R4). |
| `writeFailure` treats only `bank.Invalid` as definite (`import.go:49-54`) | Confirmed. Worse than noted: the leftover `started` intent turns it into a permanent hold (R1). |
| `Run` releases the lease with a possibly expired ctx (`engine.go:46`) | Confirmed, as is `failure` at 67 (R2). |
| SFN `Catch → Failed` without releasing the lease (`cdk.go:132`) | Confirmed, but only on crash or timeout, and TTL covers it (R10). |
| Processor timeout 180 s (`cdk.go:101`) vs per-record enrichment | Confirmed (R2). |
| `clearEchoes` partial failure after the credit is committed (`import.go:144-151`) | Confirmed. Leaves a duplicate, never loses data (R11). |
| Email trusts `From` (`gateway.go:86`) | Confirmed, also at 104-105 (R3). |
| `GetAccountTransactions` reads full history (`service/eq.go:46`) | Confirmed (R13). |
| No DynamoDB TTL | Confirmed (R12). |
| Health conflates YNAB/AWS failures (`engine.go:331-393`) | Confirmed, and notification-path model failures too (R8). |
| `notify.init` replaces `http.DefaultClient` | Confirmed (R15). |

## Ranked changes (value against effort)

1. **R1: re-send import-ID writes; drop the `started` intents and the recovery reads.** Removes the only failure mode that silently stops a bank for good. About −70 lines.
2. **R9: Step Functions `Retry` with `JitterStrategy: FULL` for Lambda service errors.** +1 line, and alerts stop vanishing.
3. **R2: soft deadline in the handler plus `WithoutCancel` cleanup in `Run`.** +10 lines. With R1, turns timeouts into progress.
4. **R5 and R6: delete the `BALANCE#` and `UPDATE#` intents.** About −32 lines, and two more permanent holds are gone.
5. **R7 and R16 dead code** (notification recovery, `purchase-not-found`, `Maintain`). About −70 lines, zero risk.
6. **R3: check SES's DMARC verdict before trusting `From`.** +10 lines.
7. **R4: put the HTTP status on YNAB errors, so a 4xx is "not written".** About +4 lines net.
8. **R8: keep non-bank errors out of bank health.** +6 lines.
9. **R15: replace `gregdel/pushover` and delete the `init()`.** One fewer dependency, net negative lines.
10. **R12: DynamoDB TTL on transient records.** +12 lines.
11. **R11: make `clearEchoes` best-effort now; let necessity decide on deleting the echo feature.**
12. **R13, R14, R10:** `since_date` on YNAB reads, an `ErrLeaseLost` sentinel, logging release errors instead of joining them.

Net if everything above lands: roughly −200 lines, with ~60 added, and no new mechanism.
