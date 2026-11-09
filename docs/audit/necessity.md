# Necessity audit

Question for every mechanism: should it exist at all? The context is one person, three banks (EQ, Rogers, NBDB), one notification-only account (BMO) and one YNAB budget. YNAB shows every imported transaction for approval, so a stray duplicate is easy to see and delete. Hours of downtime cost almost nothing. A lost or wrong transaction costs more, and YNAB reconciliation catches it.

Judged against `docs/audit/rubric.md`. Taken as given: the Next.js admin is removed, JSONLogic rules are dropped, Rod becomes playwright-go, the browser broker stays in simplified form, and all Go tests are removed. The production counts come from `docs/audit/data.md`: 33 days, 848 executions, 762 jobs, and one full scan of the table. Line counts are approximate Go lines.

## Short answer: do we need a review hold?

**No.** The evidence says the review machinery has caused more harm than it prevented.

- **What it was built for never happened.** 0 uncertain writes in 33 days. All 255 `WRITE#` rows, 21 `UPDATE#` rows and the 1 `BALANCE#` row are `complete` (data.md Q2, Q3). Earlier docs agree: "uncertain YNAB writes were reviewed in code rather than deliberately induced" (`docs/verification-results.md:114`).
- **Every hold that did fire was a false alarm about legitimate data or user edits:**
  - `ambiguous-posting`: two genuine RTC purchases on consecutive days. It produced 1 `review#`, 1 `RESOLUTION#` and **26 `multiple-ledger-claims` failed jobs** (data.md Q3; `verification-results.md:201`).
  - `previous-import-missing`: the user had deleted a charge in YNAB (`verification-results.md:138`).
  - `ledger-transaction-removed`: the user had deleted or merged entries in YNAB. On 5 November the doc says "EQ remains on its separate financial review hold", with "two new interest rows" waiting behind it (`verification-results.md:70-78`).
- **The hold itself loses transactions.** EQ has not completed a retrieve since **2026-10-12**. It has been `blocked` on `challenge-required` since 2026-10-15. 179 scheduled runs since then ended `review-required`, 154 of them without even trying the browser. One incident notice was sent and none since. The 5 CloudWatch alarms have no subscriber (data.md Q4, added check 1). For 27 days the system did exactly the thing it exists to prevent: EQ transactions are missing from YNAB, and nothing has said so for over three weeks.
- **Some holds cannot be released at all.** No operator action clears a `WRITE#` "started" record. `review.update` accepts only `UPDATE#` (`internal/engine/admin.go:231`), and `review.balance` accepts only `BALANCE#` (`admin.go:257`). The only way out is hand-editing DynamoDB.

YNAB already provides both safety nets the holds try to rebuild: an idempotency key (`import_id`, which returns `duplicate_import_ids` instead of writing twice) and a human approval step for every transaction. Retrying is safe, and an occasional duplicate shows up in the approval queue.

## The core bug that makes holds permanent

`internal/engine/import.go:49-54`:

```go
func writeFailure(cause error, operation string) error {
	if bank.Classify(cause).Kind == bank.Invalid {
		return cause
	}
	return bank.Fail(bank.Uncertain, operation)
}
```

Every non-`Invalid` YNAB error becomes `Uncertain`, including a 400 (definitely not written), a 429 (definitely not written) and `errDuplicateImport` (definitely already written). For posted writes, `CreateEQTransaction` has already re-read the account by import ID after the error (`internal/service/eq.go:63-72`), so "not found" means "not written". But the `started` intent stays behind, and every later job reads it, fails to find the transaction and holds again (`import.go:111-124`, `reconcile.go:320-336`). One failed POST stops that bank's imports for good.

---

## Inventory

Each entry gives: what it does and where, the failure it prevents, how often that failure happened, the cost of deleting it, and a verdict. Rubric items are in brackets.

### Writes

#### N1. `Uncertain` write outcome (delete) — high [1, 2, 36]

- **Where:** `internal/bank/bank.go:28`; `writeFailure` at `import.go:49-54`; Uncertain returns at `import.go:123,178,191,249,490`, `reconcile.go:335,465,473`; branches in `failure()` at `internal/engine/engine.go:338,364`; notice copy at `internal/engine/notification_copy.go:43-45`. About 25 lines directly, and it is the reason the intent records below exist.
- **Prevents:** writing the same transaction twice after a lost YNAB response.
- **Happened:** 0 times in 33 days (data.md Q3).
- **If deleted:** writes that carry an `import_id` (all Rogers and EQ posted writes) are deduplicated by YNAB, so a retry costs nothing. Writes without one (card alerts, which must not carry an ID or YNAB won't match them to the cleared import, per `docs/ynab-matching.md`) could create one unapproved duplicate. That only happens if YNAB committed the write and the response was then lost inside the 20 s client timeout. It has never been observed, and the result would be visible in YNAB's approval list.
- **Verdict: delete.** A create error returns as is: `Temporary` (retried in the job, then by the next schedule) or `Invalid`. Treat `errDuplicateImport` as success, with no hold and no lookup. That also means a transaction the user deleted in YNAB stays deleted instead of raising `previous-import-missing` (`eq.go:73-75`, `reconcile.go:311-314`).
- **Lines:** −25, +3.

#### N2. `WRITE#` intent, started and complete records, and their recovery lookups (delete started, keep complete only where it carries data) — high [2, 36]

- **Where:**
  - `posted`: `import.go:101-131,137-146`, about 40 lines.
  - Notification: `import.go:226-250,313-316`, about 25 lines.
  - Amount-only alert: `import.go:481-491,497`, about 15 lines.
  - `reconcile`: `reconcile.go:320-337,362-391`, about 45 lines.
  - `UPDATE#` in `updateTransaction`: `reconcile.go:455-478`, about 22 lines.
- **Prevents:** a crash between the YNAB POST and saving the ledger, followed by a blind re-POST.
- **Happened:** 0 started records left over (all 255 `WRITE#` rows are `complete`). The notification recovery loop at `import.go:244-247` compares against `previous.ImportID`. That ID is never sent to YNAB for pending writes (`eq.go:102-104`), so the loop cannot match anything. It is dead and always ends in Uncertain.
- **If deleted:**
  - **Rogers:** read the account's transactions once (deleted ones included, which `GetAccountTransactions` already returns via `last_knowledge_of_server=0`) and skip records whose `import_id` exists. Create the rest. YNAB dedupes any race. This replaces the per-record `WRITE#` get, put and put.
  - **Notification and amount-only alerts:** write to YNAB first, then save one `complete` row. Keep the notification `complete` row, because echo detection reads it (`import.go:402-419`). The amount-only `WRITE#alert#` row can go: `Submit` already returns early for a completed job (`internal/engine/gateway.go:53-55`).
  - **`UPDATE#`:** a YNAB update sets absolute values, so re-sending it is idempotent. The intent adds nothing.
- **Verdict: delete** the `started` state everywhere, the recovery lookups, `UPDATE#` and `WRITE#alert#`. Keep `WRITE#notification#…{complete}` as the echo source.
- **Lines:** about −140, +15.

#### N3. NBDB account hold after an uncertain adjustment (`BALANCE#`) (delete) — high [1, 2]

- **Where:** `import.go:172-179,184-187,193-197`; `review.balance` at `admin.go:246-280`; `balanceWrites` in `job.get` at `admin.go:106-110`. About 60 lines.
- **Prevents:** posting an adjustment twice if the first response was lost.
- **Happened:** never (1 `BALANCE#` row, `complete`).
- **If deleted:** the balance strategy already converges. Each run writes `bank balance − YNAB balance`. If an earlier adjustment landed, the difference is 0. If it didn't, this run writes it. A double write would need YNAB to accept the write but not yet show it in the account balance on the next weekday's run, and even then the following run writes the opposite adjustment. That is the textbook "define errors out of existence" case.
- **Verdict: delete.** Compute, write, and return any error as `Temporary`.
- **Lines:** −60.

#### N4. Review holds: `review#`, `RESOLUTION#`, `review.match` and pending-ledger matching (delete) — high [1, 2, 6]

- **Where:**
  - `samePurchase`: `reconcile.go:55-68`.
  - `pendingPresent`, `claimed` and the `hold` closure: `reconcile.go:130-166`.
  - `RESOLUTION#` lookup, pending candidates and competing posted rows: `reconcile.go:200-274`.
  - `ambiguous-posting`: `reconcile.go:443-446`.
  - `review.match`: `admin.go:203-228`.
  - About 160 lines.
- **Prevents:** settling a pending YNAB entry against the wrong posted row.
- **Happened:** 1 review in 33 days. It was a false alarm (two real purchases) and cost 26 failed jobs plus a hand repair (`OPERATOR_REPAIR#`, data.md Q2).
- **Now unreachable going forward:** since `7a23bb6`, EQ retrievals keep only `posted` rows (`reconcile.go:100-108`), and EQ alerts are amount-only writes that YNAB matches itself (`import.go:464-510`). Nothing creates pending `tx#` rows any more. The only candidates left are the 6 legacy pending rows from 5 to 15 October (data.md Q1).
- **If deleted:** when EQ comes back, the posted rows for those 6 legacy entries are imported with their own `import_id`. YNAB auto-matches the ones created without an import ID (after 10 October). Any that don't match appear as visible, unapproved duplicates.
- **Verdict: delete.** At most 6 duplicates, one time. Nathan can check those 6 by hand when EQ is unblocked.
- **Lines:** −160.

#### N5. The `reconcile` strategy and the `tx#` ledger as a whole (replace with the `posted` strategy) — high [1, 3, 4]

- **Where:** all of `internal/engine/reconcile.go` (478 lines), plus `Entry` and `ledger()` at `import.go:512-526`.
- **Prevents:** tracking a purchase from pending to posted and keeping YNAB in step.
- **Happened (useful work):** the 21 `UPDATE#` settlements and the 31 `tx#` rows, all from before EQ stopped importing pending rows.
- **If deleted:** EQ becomes Rogers. Each posted record gets `import_id = "EQ:" + hash("tx#"+account+"#"+hash(identity))[:32]`, the same formula as today (`reconcile.go:276-277`), so existing EQ imports dedupe in YNAB.
  - For one release, keep a read-only skip: "if any legacy `tx#` row `exactMatch`es this record, skip it". That covers the 25 posted legacy entries whose YNAB transactions were adopted or settled without that ID, until they leave EQ's fetch window.
  - Gone with it: `posted-regressed`, `multiple-ledger-claims`, `conflicting-ledger-references`, `ledger-transaction-removed`, `review-link-missing` and `previous-import-missing`. These are six hold conditions, two of which actually fired (26 jobs, and the November EQ block).
- **Verdict: replace** with the shared `posted` path from N2. The difference between banks shrinks to an import-ID function on `bank.Registration`.
- **Lines:** about −470, +25.

#### N6. `ECHO#` and echo detection (keep) — low [1]

- **Where:** `import.go:262-291,346-454`, about 120 lines.
- **Prevents:** importing a Rogers or BMO alert that repeats a refund as a new purchase.
- **Happened:** added 7 November at Nathan's request. 0 `ECHO#` rows so far, and `missingAttempt` was non-zero twice.
- **Verdict: keep.** It is importer behaviour, which the ground rules freeze. It also depends on `WRITE#notification#` `complete` rows (N2). The `alert#` legacy check at `import.go:476-480` is not part of it. That check guards against an earlier retrieval path removed in `7a23bb6`; its 14 rows are from before then. **Delete the check** (−5).

### Data and auth

#### N7. Baseline review and approval before imports (delete) — high [1, 6]

- **Where:**
  - `Baseline` type: `import.go:19-27`.
  - `baseline()`: `admin.go:336-503`, 168 lines.
  - Gate: `engine.go:237-247`.
  - `Seen`, `Links` and `Settlements` use: `reconcile.go:177,244,283-307`, `import.go:98`.
  - `-local-preview`: `cmd/operator/main.go:23,43-56`.
  - `baselineApproved` in `banks.list`.
  - About 235 lines.
- **Prevents:** a new importer re-importing transactions already in YNAB at cutover.
- **Happened:** a one-shot cutover gate, used once (3 `BASELINE` rows, all 2026-10-06).
- **If deleted:**
  - Rogers fetches a rolling 10 days (`internal/banks/rogers/api.go:79`), so every `Seen` row is a month outside its window.
  - NBDB never reads `Seen`.
  - EQ's `Links` and `Settlements` were consumed into `tx#` rows on the first import, which N5's legacy skip covers.
  - Adding a bank later goes through a dry run and YNAB's approval list.
- **Verdict: delete** (rubric 6, a finished one-shot migration). Set EQ's `startDate` no later than 2026-10-12 so the catch-up after the block is complete.
- **Lines:** −235.

#### N8. The imports-enabled pause (keep) — low [1]

- **Where:** `engine.go:93-97`, `internal/config/settings.go:38`, and the coupling to baseline approval at `admin.go:490-492`.
- **Prevents:** writes during an incident. It is the only switch that also covers the notification-only BMO account.
- **Verdict: keep** the five lines as the one global kill switch. The baseline coupling goes with N7.

#### N9. `RenewalUncertain` and `ExchangeUncertain` (delete) — medium [1, 2]

- **Where:** `engine.go:159-161,424-451`; `internal/engine/verify.go:42-44`; `bank.go:36,53`; `internal/bank/http.go:47,93,129`. About 25 lines.
- **Prevents:** replaying a refresh token the bank may already have rotated.
- **Happened:** no recorded occurrence.
- **Today's behaviour:** `Invalid "renewal-outcome-unknown"`, a hold that needs `bank.resume`.
- **If deleted:** a lost renewal response is treated as `Authentication`. The job falls through to the browser login it already allows once (`engine.go:181-193`). The cost is one extra login, and replaying a rotated token at worst also ends in that login.
- **Verdict: delete.** The hold is strictly worse than the login it avoids.
- **Lines:** −25, +2.

#### N10. Snapshot validation against `ExpectedAccounts` (replace) — low [1]

- **Where:** `internal/engine/types.go:161-168`, `settings.go:78-92`.
- **Prevents:** importing into the wrong account.
- **Duplicates:** `mapping()` (`import.go:57-86`) already requires every non-excluded account to map to exactly one open YNAB account by note.
- **Verdict: replace.** Keep the non-empty and record-shape checks, and drop the exact account-count equality and the `ExpectedAccounts` setting.
- **Lines:** −20.

### Coordination

#### N11. Lease generations and fencing (keep) — low [37]

- **Where:** `internal/state/store.go:65-166`, about 100 lines, and the `e.Lease` plumbing.
- **Prevents:** two jobs for one bank renewing or refreshing the same session, or both launching a browser and colliding on MFA codes.
- **Happens:** Rogers scheduled jobs overlap echo retrievals and alert jobs on the same lease; the Oct 12 lease conflict is evidence of that contention. Concurrent auth costs browser logins and can trip bank defences.
- **Verdict: keep.** It is a deep module behind a small interface (`Acquire`, `Put`/`PutMany`, `Release`). After N1 to N7 it fences far fewer rows: `JOB#`, `SESSION`, `HEALTH`, notification `WRITE#` and `ECHO#`. Keep `retryConflict`: it fixed a real incident (`645a96f`, `verification-results.md:199`).

#### N12. Health and incident tracking: `Blocked`, `RetryAt`, `ImportReview`, `Episode`, `Notified`, `MaintenanceCount` (replace with something simpler) — high [1, 2, 34]

- **Where:** `failure()` at `engine.go:331-394`; `recovered()` at `engine.go:277-325`; the `Health` type at `types.go:74-83`; `notifyOnce` at `internal/engine/notify.go:21-31`; `bank.resume` at `admin.go:181-200`. About 150 lines.
- **Prevents:**
  - `Blocked`: repeated logins after rejected credentials, which can lock the account.
  - `Episode`/`Notified`: one notification per incident.
  - `RetryAt`: backoff across jobs.
- **Happened:** 15 incidents in 33 days, 14 recovered. The one still open is EQ, `blocked` on `challenge-required` since 2026-10-15. Its `retryAt` is 24 days in the past and ignored, because `Blocked` is checked first (`engine.go:79-84`). One notice was sent for 179 failed runs.
- **Dead parts:**
  - `ImportReview` is written for every `Invalid`/`Uncertain` retrieve (`engine.go:338-340`) but read only for `maintain-session` jobs (`engine.go:279`), which nothing creates (N19).
  - The cross-job `RetryAt` backoff caps at 30·2⁷ s ≈ 64 min (`engine.go:352`), shorter than the 4-hour schedule, so for scheduled jobs it never matters.
- **Verdict: replace** with `Health{Blocked bool, Failing bool, RetryAt time.Time}`:
  - `Credentials` sets `Blocked`. It is cleared by `bank.resume`, the only manual step left. The lockout risk is real and wrong credentials don't fix themselves.
  - `Challenge` sets `RetryAt = now + 24h` instead of `Blocked`. While `RetryAt` is in the future, a scheduled job completes quietly instead of deferring (`engine.go:85-92`, change `deferred` to `complete` for `source == scheduled`). Each daily attempt that fails sends its notice again. This is the same idea as data.md item 3 without a new flag. EQ would have retried daily and reminded Nathan daily instead of going dark.
  - `RetryAt` is otherwise set only from a bank's `Retry-After` or maintenance window, not by exponential backoff, so retries live in one layer (rubric 34): the in-job wait, then the schedule.
  - Notify when `Failing` goes false→true and true→false. Delete `Episode`, `Notified`, `ImportReview`, the `NOTICE#`-superseded logic and the dry-run notice preservation. Keep the two-strikes rule for maintenance on scheduled jobs (3 lines).
- **Lines:** about −110, +35.

#### N13. Browser recovery path: `RecoverBrowser` and `BROWSER#<generation>` (merge into `Run`) — low [1, 3]

- **Where:** `engine.go:395-417`; result handling at `engine.go:128-153`; `recover-browser` at `cmd/lambda/main.go:72-73`; the SFN `RecoverBrowser` state at `cdk/cdk.go:134`.
- **Prevents:** losing a saved login because the task-token callback failed.
- **Happened:** about 1 in 848 executions (data.md Q3).
- **Verdict: replace.** Point the Browser state's `Catch` at `Run`. In `Run`, a `browser`-status job with no result already becomes `Temporary` (`engine.go:134-136`); set `Attempt = 2` there, as `RecoverBrowser` does. Keep `BROWSER#` (it carries the error kind, such as capacity versus challenge) unless the broker rework moves it into the job record.
- **Lines:** −30, +3.

#### N14. Browser-capacity backoff and capacity notifications (simplify) — medium [1, 34]

- **Where:** `deferCapacity` at `engine.go:461-486`; capacity recovery notice at `engine.go:283-298`; `CapacitySince`, `CapacityWaits` and `CapacityNotified` at `types.go:38-40`; the `startOperation(…"notify")` workaround at `engine.go:472`. About 50 lines.
- **Prevents:** treating a busy browser seat as a failed login, and staying silent if the seat stays busy.
- **Happened:** 68 jobs hit capacity (8%). It is real, so keep a defer. No capacity notice was ever sent: all 176 notices are purchase, incident or recovery (data.md Q3).
- **Verdict: simplify.** A `Capacity` result defers a fixed 120 s, up to 5 times (one counter on the job, reusing `Attempt`), then fails as `Temporary` through the normal path. That gives the normal incident notice and a retry on the next schedule. Delete the 15-minute capacity notice, the capacity recovery notice and the three job fields.
- **Lines:** −45, +8.

### Infrastructure and delivery

#### N15. The Step Functions workflow (keep, cut from 13 states to 6) — medium [1, 38]

- **Where:** `cdk/cdk.go:131-136`.
- **Could one Lambda plus a Fargate task do it?** Not more simply. Three durable waits are needed:
  1. Echo waits of 60, 300 and 900 s (importer behaviour).
  2. Capacity and temporary retries for alert jobs, which have no "next schedule".
  3. A browser task with a 420 s timeout and crash detection.

  Without SFN those become an SQS delay queue, an ECS-to-Lambda invoke and a watchdog for a dead task: the same states in code.
- **Verdict: keep,** reduced to `Run → Outcome(Choice) → Browser (Catch→Run) | Wait → Run | Done | Failed`. Delete `Dispatch`, `NotificationOnly`, `PreserveOutcome`, `Notify`, `NotificationOutcome`, `WaitNotification`, `FinalOutcome` (N16) and `RecoverBrowser` (N13).
- **Lines:** about −8 CDK, plus the engine code counted under N13 and N16.

#### N16. Separate notification delivery with bounded retries (replace with an inline send) — medium [1, 3, 34]

- **Where:**
  - `Deliver`: `notify.go:32-95`, 63 lines.
  - `Notification` type and `notifyOnce`: `notify.go:11-31`.
  - `NOTICE#` rows written with financial results: `import.go:335-336,508-509`.
  - `notifications.retry`: `admin.go:111-135`.
  - `notify` action: `cmd/lambda/main.go:74-75`.
  - 7 SFN states.
  - About 140 lines.
- **Prevents:** a Pushover failure being confused with a financial failure, and a retry re-running an import.
- **Happened:** all 176 notices are `sent`, and none is stuck `pending` or `failed` (data.md Q3). No Pushover outage is recorded anywhere.
- **If deleted:** send inline after the YNAB write and the `complete` record, with 3 tries in-process, and log on failure. A failed send cannot re-run an import because the write is already recorded. A lost "Transaction Approved" push costs nothing, since the transaction is in YNAB's approval list.
- **Verdict: replace.** `notifyOnce` becomes a direct call.
- **Lines:** about −140, +15.

#### N17. Per-invocation caches (replace) — low [3, 11, 31]

- **Where:** `internal/config/invocation_cache.go` (31 lines); cache reads and writes in `GetSecrets` (`internal/config/secrets.go:33-35,45-47`); `WithSettings`/`Load` context plumbing (`settings.go:43-51`); call sites at `cmd/lambda/main.go:24` and `cmd/operator/main.go:49`.
- **Prevents:** fetching the secret more than once per invocation.
- **Verdict: replace.** Load `Settings` and `Secrets` once in `main` and pass them in through constructors. Secret rotation is picked up on the next cold start, which is fine for one user. The warm YNAB read in `internal/engine/learning.go:79-115` is a different cache with a measured benefit; leave it.
- **Lines:** −40, +10.

#### N18. Snapshot uploads to S3 on every run (delete from `Run`) — low [1]

- **Where:** `engine.go:230-236`, `Job.SnapshotKey` at `types.go:43`, the gateway's `snapshots/*` read permission at `cdk/cdk.go:88`.
- **Prevents:** nothing. The only reader was `baseline()` (N7).
- **Verdict: delete** from `Run`. Keep the upload in `VerifySession` (`verify.go:66`), which manual checks use.
- **Lines:** −10.

#### N19. Dead code: `Maintain`, `KeepWarm`, the upkeep shim, `purchase-not-found` and the webhook (delete) — medium [6]

- **`Maintain` and `maintain-session`:** `gateway.go:183-225` (43 lines), `bank.go:113`, `cmd/lambda/main.go:81-82`, the shim at `engine.go:70-74`, `ImportReview` at `engine.go:279-281`, and the purpose check at `gateway.go:30-35`. No registration sets `KeepWarm`, and nothing invokes `maintain` (data.md added check 2). The last `maintain-session` job was 2026-10-09.
- **`purchase-not-found`:** `reconcile()` always returns `missing = false` (`reconcile.go:443-450`), so `engine.go:251-262` and `notification_copy.go:49-51` are unreachable.
- **Webhook:** `gateway.go:139-182` (44 lines), the `WebhookConfig` and `WebhookToken` types, admin `jobs.list` at `admin.go:83-85`, and 6 CDK resources at `cdk/cdk.go:149-154`. "The user confirmed on 6 October 2026 that the webhook is no longer used" (`docs/cutover.md:44`).
- **`Execution` fencing:** `engine.go:22-24,54-56`, `startOperation` at `admin.go:507-513`. Only operator `job.retry` starts a second execution, and it already refuses while the earlier one is running (`admin.go:152-159`). Delete.
- **Verdict: delete.**
- **Lines:** about −130 Go and −6 CDK resources.

#### N20. CloudWatch alarms, `OperatorAlerts` and the `DeliveryFailures` queue (keep, but give them a subscriber) — high [1]

- **Where:** `cdk/cdk.go:62-63,106,155-167`.
- **Prevents:** silent infrastructure failure.
- **Happened:** 28 failed executions, and nobody was told (data.md added check 1). After N16, a Lambda crash caught by SFN is the one failure the app cannot report itself.
- **Verdict: keep, and fix with configuration.** Set `alarmEmail` to Pushover's email-to-push address. No code. Deleting the alarms instead would leave crashes completely invisible.
- **Lines:** 0.

### Operations

#### N21. The operator CLI and gateway `Admin` surface (shrink) — medium [1, 3]

- **Where:** `admin.go` (521 lines) and `cmd/operator/main.go` (75 lines).
- **After the decisions above:**
  - Rules (`rules.*`, `validateRule`) go with the JSONLogic decision.
  - `lookups` and `jobs.list` served only the web admin.
  - Baseline and review actions go with N4 and N7. `notifications.retry` goes with N16.
- **Keep four actions:**
  - `banks.list`: health per bank.
  - `bank.resume`: clears `Blocked` and invalidates the session.
  - `job.get`: without the reviews and balances lists.
  - `job.retry`.
- **Verdict: shrink.**
- **Lines:** `admin.go` drops to about 90. The operator drops `-local-preview` (−14). Total about −440 including the rules code.

---

## Ranked top changes (value against effort)

1. **Delete `Uncertain` and the `started` intents (N1, N2, N3), and treat a duplicate import as done.** This removes every permanent hold, including the ones nobody can clear. About −225 lines, low effort, high value.
2. **Stop a blocked bank from going dark (N12).** Challenges retry daily and re-notify, credentials still pause, and the dead `ImportReview` and backoff go. This would have caught the 27-day EQ outage. About −75 net, low effort.
3. **Subscribe the alarms (N20).** One context value, and it ends 28 unreported failures.
4. **Replace `reconcile` with the `posted` strategy and delete pending matching and review records (N4, N5).** About −600 lines, medium effort. The one-time risk is up to 6 visible duplicates from the legacy pending rows.
5. **Delete baseline review (N7)** once N5 lands. About −235, low effort.
6. **Inline notifications and cut the workflow to 6 states (N15, N16).** About −135, low-to-medium effort, touching CDK and engine together.
7. **Delete dead code: `Maintain`, upkeep, `purchase-not-found`, webhook, `Execution` fencing, `alert#` (N6, N19).** About −135 plus 6 CDK resources, low effort.
8. **Simplify capacity backoff (N14) and merge `RecoverBrowser` into `Run` (N13).** About −65, low effort.
9. **Delete `RenewalUncertain` (N9).** −23, low effort.
10. **Shrink the operator and `Admin` surface (N21), load config once (N17), drop snapshot uploads (N18), drop the `ExpectedAccounts` equality (N10).** About −500 in total, mostly falling out of the admin removal.

Leftover DynamoDB rows (`review#`, `RESOLUTION#`, `UPDATE#`, `BALANCE#`, `alert#`, `BASELINE`, and `WRITE#` rows for Rogers and alerts) need no migration. Nothing reads them after these changes, and they can be deleted at leisure.

## Closing table

| Mechanism | Verdict | Lines saved (approx.) | Risk accepted |
|---|---|---|---|
| `Uncertain` write outcome (N1) | Delete | 22 | One possible unapproved duplicate for an alert write whose response is lost after YNAB commits it (never observed) |
| `WRITE#` started, recovery lookups, `UPDATE#`, `WRITE#alert#` (N2) | Delete; keep notification `complete` rows | 125 | Same as N1; a user-deleted import stays deleted instead of raising a review |
| NBDB `BALANCE#` account hold (N3) | Delete | 60 | At worst one doubled adjustment, corrected by the next run |
| `review#`, `RESOLUTION#`, `review.match`, pending matching (N4) | Delete | 160 | Up to 6 one-time duplicates from legacy pending rows, visible in YNAB |
| `reconcile` strategy and `tx#` ledger (N5) | Replace with `posted` and a temporary legacy skip | 445 | As N4; depends on EQ identities staying stable, as the ledger already did |
| Echo detection and `ECHO#` (N6) | Keep; delete the `alert#` legacy check | 5 | None |
| Baseline review (N7) | Delete | 235 | A future new bank relies on a dry run and YNAB approval, not a review screen |
| Imports-enabled pause (N8) | Keep | 0 | None |
| `RenewalUncertain` / `ExchangeUncertain` (N9) | Delete | 23 | One extra browser login after a lost token-exchange response |
| `ExpectedAccounts` equality (N10) | Replace with non-empty check and note mapping | 20 | A newly added bank account fails at mapping instead of at validation |
| Lease and fencing (N11) | Keep | 0 | None |
| Health and incidents (N12) | Replace with `Blocked` (credentials only), `Failing`, `RetryAt` | 75 | One login attempt a day against a bank that is challenging; one notice a day while it fails |
| `RecoverBrowser` (N13) | Merge into `Run` | 27 | None |
| Capacity backoff and notices (N14) | Simplify to fixed defer, then normal failure | 37 | Contention over about 10 min fails the job, and the next schedule retries |
| Step Functions (N15) | Keep, 13 states down to 6 | 8 CDK | None |
| Separate notification delivery (N16) | Replace with inline send and 3 tries | 125 | A Pushover outage drops a push; YNAB still shows the transaction |
| Per-invocation caches (N17) | Load once in `main` | 30 | Secret rotation needs a cold start |
| Snapshot uploads in `Run` (N18) | Delete (keep in verify) | 10 | No stored snapshot for post-mortems of scheduled runs |
| `Maintain`, upkeep shim, `purchase-not-found`, webhook, `Execution` fencing (N19) | Delete | 130 and 6 CDK resources | Webhook must be re-added if ever needed again |
| Alarms, SNS and failure queue (N20) | Keep; set `alarmEmail` | 0 | None |
| Operator and `Admin` surface (N21) | Shrink to four actions | 440 (including rules) | Inspecting jobs beyond `job.get` uses the AWS console |
| **Total** | | **about 1,800 Go lines** (N4's 160 sit inside N5's 445 and are counted once), out of about 2,870 in `internal/engine` plus the admin and operator code | |
