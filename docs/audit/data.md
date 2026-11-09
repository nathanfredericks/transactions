# Production data audit (read-only)

Run 2026-11-08, region ca-central-1, stack `TransactionsEngine`. Only describe, list, get and scan calls were made. Secrets Manager was not touched. The table was scanned once (1,618 items, about 1.2 MB) and analysed locally. No payee names, descriptions, account numbers or secrets appear below. The AWS identity was the account root user; no write call was issued.

Resources: state table `TransactionsEngine-State-...` (PAY_PER_REQUEST, PITR on, **TTL disabled**), state machine `transactions-engine` (STANDARD, 86400 s timeout), SNS topic `OperatorAlerts`.

Data window: the system went live on 2026-10-06 (baselines approved 23:05 to 23:15 UTC). Everything below covers 33 days, so the 90-day lookback equals "all history".

## Key shapes found

- Items are `pk`, `sk` and a JSON string `data`. No other attributes exist (no `ttl`, no GSI).
- `pk = BANK#<bank>`. Banks present: `eq-bank`, `nbdb`, `rogers-bank`, plus **one `BANK#notification-<uuid>` partition** (31 jobs, 33 notices, 31 writes; a pseudo-bank for notification-only work).
- `pk = LEARN` has `TX#` rows. `pk = RULES` has no rows.
- Not present: `ECHO#` (0 rows), `BANK#x/LEASE` (0, expected; leases are deleted), `RULES/RULE#` (0).

## Q1. Pending ledger entries

`tx#` rows total 31, all `eq-bank`. 25 are `posted` and **6 are `pending`**. rogers-bank and nbdb have no `tx#` rows.

| bank | pending | posted |
|---|---|---|
| eq-bank | 6 | 25 |
| rogers-bank | 0 | 0 |
| nbdb | 0 | 0 |

The 6 pending entries have authorization dates 2026-10-05 to 2026-10-15, so they are 24 to 34 days old. All 6 hold a `ynabId`; 5 have `adopted=false` and 1 `adopted=true`. They have never settled in the ledger. This ties to the open eq-bank hold in Q4, because eq-bank has produced no successful retrieve since 2026-10-12. A pending row is only resolved by a later import that sees the posted row, so these are frozen, not abandoned. They will stay `pending` until the hold clears or the user resolves them (an `OPERATOR_REPAIR#` record dated 2026-11-05 shows the user fixed two RTC purchases by hand).

## Q2. Record counts per key prefix

No TTL is set on the table. Timestamps only exist inside `data` for some record types.

| prefix | count | oldest | newest | note |
|---|---|---|---|---|
| `JOB#` | 762 | 2026-10-07 02:05 | 2026-11-09 00:00 | avg 1,016 bytes; the largest group (about 770 KB) |
| `NOTICE#` | 176 | 2026-10-07 04:57 | 2026-11-08 23:54 | 147 purchase, 15 incident, 14 recovery; all `sent` |
| `WRITE#` | 255 | no timestamp field | | all `complete`; 197 rogers, 27 eq, 31 notification |
| `BROWSER#<generation>` | 340 | no timestamp field | | 311 ok, 26 with `error`; 134 bytes; one per browser run |
| `tx#` | 31 | no timestamp field | | see Q1 |
| `UPDATE#` | 21 | no timestamp field | | all eq-bank, `complete` |
| `alert#` | 14 | no timestamp field | | |
| `review#` | 1 | no timestamp field | | see Q4 |
| `RESOLUTION#` | 1 | no timestamp field | | |
| `OPERATOR_REPAIR#` | 1 | 2026-11-05 | 2026-11-05 | manual repair record |
| `BALANCE#` | 1 | no timestamp field | | nbdb, `complete` |
| `BASELINE` | 3 | 2026-10-06 | 2026-10-06 | one per bank, keep |
| `HEALTH` | 3 | | | one per bank |
| `HEALTH#notifications` | 2 | | | |
| `SESSION` | 3 | | | one per bank |
| `LEARN/TX#` | 4 | 2026-11-08 18:40 | 2026-11-08 23:54 | learning only started on 2026-11-08 |
| `ECHO#` | 0 | | | prefix never written, or fully drained |

TTL planning facts:

- Growth is about 24 `JOB#` rows a day (about 700 a month). That is roughly 25 KB of table data a day plus the `BROWSER#` rows. The table holds 1.17 MB, so at this rate it reaches only about 15 MB a year. Storage cost is negligible, so TTL has no cost value. Its only value would be tidier admin listings.
- `JOB#` is read by `admin.go:87` through `List(... "JOB#")`, which loads **every** job of a bank (up to 306 now) on each admin call. NOTICE rows are listed the same way at `notify.go:43` and `admin.go:98,117`. These full-partition lists are the real reason retention matters, not storage.
- `WRITE#` and `tx#` rows are the idempotency and ledger records. They must not expire on a short clock. `tx#` is listed whole on every import (`import.go:513`).
- 164 of the 762 jobs are `maintain-session` from 2026-10-07 to 2026-10-09 only (see maintain check below). They are dead weight now. `JOB#` and `BROWSER#` are the only prefixes where a TTL (for example 60 days) would be safe.

## Q3. How often each event happened (33 days)

Step Functions: **848 executions** between 2026-10-06 and 2026-11-08 (695 in October, 153 in November). All fall inside the 90-day window, so the numbers cover the whole history. The list call returned everything; no pagination token remained.

By final status:

| status | count |
|---|---|
| SUCCEEDED | 820 |
| FAILED | 28 |
| TIMED_OUT, ABORTED, RUNNING | 0 |

By trigger (execution name): `scheduled-rogers-bank` 199, `scheduled-eq-bank` 199, `scheduled-nbdb` and similar 24, `upkeep-eq-bank` 187, `mail-` (email notification) 164, `run-` 18, `verify-nbdb-ecs` 5, `echo` 2.

By branch taken. I read the event history of all 28 failed executions and a random 60 of the 820 succeeded ones (88 of 848, so the percentages are estimates). The `Notify` state is entered by every path that did not abort early.

| path | observed |
|---|---|
| Run then Notify, no browser | 33 of 60 succeeded |
| Run, Browser (ECS login), Run, Notify | 27 of 60 succeeded (about 45%) |
| NotificationOnly (action `notify`) | 0 of the sample; 164 `mail-` executions are the notification source, and the job records show 133 `purpose=retrieve source=notification` jobs |
| Deferred Wait | **0 of 60 succeeded executions**, 16 Wait entries across the 28 failed ones |
| RecoverBrowser | 1 of 28 failed executions |

Job records back up the sample: 325 of 762 jobs (43%) have `browserAttempted=true`, so the browser branch runs on roughly four of every ten executions. That is a Fargate task each time (up to 420 s).

Failed executions: all 28 FAILED ended in the `Failed` state through the Catch. 23 went through Browser first, 3 only through Wait, 1 through Browser and RecoverBrowser, 1 immediately. The 2 and 1 failures with `Wait` are deferred loops that never recovered.

Per mechanism:

| mechanism | evidence | count |
|---|---|---|
| Browser recoveries (`RecoverBrowser`) | sample of failed runs | 1 of 28 failures (about 1 in 848 executions overall, 0 in the 60 successes) |
| Capacity deferrals | jobs with `capacitySince` set: rogers 42 complete, eq 20 review-required, nbdb 3 complete, notification 3 complete. Deferred Wait state seen in the history of 0 of 60 succeeded executions, 16 times in the failures | 68 jobs hit capacity, but no successful execution slept in `Wait`; they resolved without the loop |
| Review holds (job `review-required`) | 183 jobs: eq 181, nbdb 1, rogers 1 | but 156 are repeats of a single eq-bank block (see Q4) |
| Distinct account holds (`importReview` / blocked health) | 1 open (eq-bank). Incidents opened: 15 notices, recovered 14 | 15 incidents in 33 days: eq 6, nbdb 5, rogers 1, notification pseudo-bank about 3 |
| Review records (`review#`) | 1 | `Multiple pending or newly posted records match`, 2 candidates, eq-bank; 26 `multiple-ledger-claims` jobs belong to this one review |
| Uncertain writes (`uncertain-write`) | **0**. No job, health or write record carries that kind; all `WRITE#` rows are `complete` | 0 |
| Failures by error | `challenge-required` 156, `invalid-response` 27, `temporary` 22 (20 failed, 2 completed after retry) | |
| Fatal job operations | `browser-start` 11 (nbdb), `verification-code/email-code-timeout` 5 (eq), `account-capture/browser-response-timeout` 4, `multiple-ledger-claims` 26 (eq), others 1 each | |

Notice mix: 147 "Transaction Approved", 14 "recovered", 15 incident (EQ temporary 5, NBDB temporary 3, unexpected response 3, verification 1, sign-in rejected 1, others). All 176 are `sent`; no notice is stuck `pending`.

Retried jobs: 711 of 762 jobs have `attempt=0`, 27 have 1, 2 have 2, 22 have 3. `missingAttempt` was non-zero twice.

## Q4. Open holds and reviews now

- **Open account hold: eq-bank.** `HEALTH`: `blocked=true`, `importReview=true`, `kind=challenge-required`, `failures=28`, `retryAt=2026-10-15 22:16 UTC` (already 24 days in the past), `notified=true`. Last successful eq-bank retrieve was **2026-10-12 20:05 UTC**. Since 2026-10-13 every scheduled eq-bank job (179, every four hours) has ended `review-required/challenge-required`; 154 of them stopped before the browser and 25 tried it. The hold is not auto-clearing.
- **Open review: 1** (`review#`, eq-bank), with 2 candidates and reason "Multiple pending or newly posted records match". A `RESOLUTION#` row (1) also exists for eq-bank. I cannot tell from the data alone whether the review is still blocking; the 26 `multiple-ledger-claims` jobs ended in October.
- nbdb and rogers-bank: `blocked=false`, 0 failures, no retry. nbdb has had 14 failed jobs overall (11 `browser-start`) but recovers each time and last completed 2026-11-08; rogers-bank completed 2026-11-09 00:00 UTC.
- No Step Functions execution is RUNNING.

## Added checks (coordinator request)

### 1. SNS alarm topic and subscribers

All 5 CloudWatch alarms (`GatewayErrors`, `ProcessorErrors`, `UndeliveredEvents`, `WorkflowFailures`, `WorkflowTimeouts`) are enabled, currently `OK`, and publish only to the one topic `OperatorAlerts`.

That topic has **zero subscriptions**: 0 confirmed, 0 pending, 0 deleted, and the list call returns no rows. `cdk/cdk.go` only creates the email subscription when the `alarmEmail` context value is set, and it is not set in the deployed stack. **No alarm reaches any subscriber.** This matters because the 28 FAILED executions would have fired `WorkflowFailures` and nobody was told. The only alerting path in production is the app's own notifications.

### 2. EventBridge schedules and rules invoking `maintain`

- Three EventBridge Scheduler schedules belong to this stack (default group, all ENABLED): rogers-bank `cron(0 0/4 * * ? *)`, eq-bank `cron(0 0/4 * * ? *)`, nbdb `cron(0 17 ? * MON-FRI *)`. Each target's input action is **`submit`** with `purpose=retrieve`, `source=scheduled`.
- **No schedule or rule invokes `maintain`.** `events list-rules` returns only two AWS-managed rules (ECS task events to Step Functions and the capacity provider rule). The other schedules in the account belong to unrelated Amplify apps and a Questrade and a Menu schedule, and were not inspected beyond their names.
- `Maintain` (`cmd/lambda/main.go:81`, `gateway.go:185`) is therefore reachable only by a manual invoke. The data agrees: the 164 `maintain-session` jobs and the `upkeep-eq-bank` executions all date from 2026-10-07 to 2026-10-09, with none after. The retired schedule is dead code in production, supporting a rubric item 6 deletion of `Maintain` and `KeepWarm` handling, subject to `audit-necessity`.

## Findings against the rubric

1. **Alarm topic has no subscriber** (`cdk/cdk.go:157-160`, rubric 1). Medium. The mechanism names a failure it prevents, yet delivers nothing; 28 workflow failures went unreported. Propose: either set `alarmEmail` or delete the topic, alarms and email subscription code (about 10 lines removed). Do not add a new alert path.
2. **`Maintain` and its `maintain-session` purpose are unreachable** (`cmd/lambda/main.go:81`, `internal/engine/gateway.go:30-33,185-221`, rubric 6). Medium. No schedule calls it; last job was 2026-10-09. Propose deleting the `maintain` case, `Maintain`, the `maintain-session` purpose, `KeepWarm`, `SessionPointer` renewal checks if nothing else uses them, and `Source == "session"` handling (about 60 to 90 lines removed). Check `SESSION` (3 rows), `verify-session` and the browser `RenewalUncertain` path in the same pass.
3. **eq-bank block repeats forever** (`engine.go:338-364`, rubric 1 and 2). High. Blocked since 2026-10-15 and still scheduled every four hours: 154 Lambda plus Step Functions runs with no work, 25 browser tasks, each re-notifying nothing. The existing `retryAt` is already in the past and ignored, so the hold never expires. Propose having the scheduled submit return early when `health.Blocked` (one `if`, about 3 lines added) rather than start an execution, instead of a new state or flag.
4. **Frozen pending `tx#` entries** (`reconcile.go:135-139,236`, rubric 2). Medium. 6 eq-bank rows pending for 24 to 34 days. No change needed unless the hold in item 3 becomes permanent; then the pending-expiry rule must be thought through. No new code proposed.
5. **Capacity and RecoverBrowser paths are rare** (`cdk/cdk.go:132-136` `Wait`, `RecoverBrowser`, `engine.go` `capacitySince`, rubric 1). Medium. Wait was never entered in 60 succeeded executions and only inside failures; RecoverBrowser appears once in 848 executions. Candidate for `audit-necessity` to weigh. The 68 `capacitySince` jobs show the condition itself happens, but the loop did not resolve any of them in the sample. Savings if removed: about 15 lines of workflow plus engine code, unproven.
6. **Uncertain-write machinery has no occurrences** (`bank.go:28`, `engine.go:338,364`, `notification_copy.go:43-45`, rubric 1). Low to medium. Zero events in 33 days. It guards a duplicate-payment style failure, and YNAB duplicates are easy to delete (rubric ground rule), so keep only if `audit-necessity` judges the pending-POST case worth it.
7. **Whole-partition lists** (`admin.go:87,98,117`, `notify.go:43`, `import.go:402,513`, rubric 1). Low now (306 jobs per bank). With no TTL, `JOB#` and `BROWSER#` rows only grow; a TTL attribute on those two prefixes is the smallest fix (about 4 lines added), or limit the admin list. Leave `WRITE#`, `tx#`, `BASELINE` and `LEARN` untouched.
8. **Stray pseudo-bank partition** (`BANK#notification-<uuid>`, rubric 2). Low. One partition holds 31 jobs and 33 notices from `alert`-style notification jobs. If it is one per notification, it fragments the data; check `gateway.go` naming rather than adding code.

## Ranked top changes (value against effort)

1. Fix the alarm topic: set `alarmEmail` or delete the alarms. About 10 lines removed or one config value; ends silent failure.
2. Skip scheduled submits for a blocked bank (item 3). About 3 lines added; stops about 150 pointless runs a month and ends the eq-bank noise.
3. Delete the unreachable `Maintain` and `maintain-session` path (item 2). 60 to 90 lines removed.
4. Ask `audit-necessity` to decide on the Wait loop, RecoverBrowser and uncertain-write branches using the counts above (items 5 and 6).
5. Add a TTL to `JOB#` and `BROWSER#` only (item 7), optional.

## Caveats

- Branch percentages come from a sample of 88 of 848 execution histories; status counts and job counts are exact.
- The `WRITE#`, `BROWSER#`, `tx#` and similar rows have no timestamp field, so oldest/newest is shown only where one exists.
- I did not read bank, YNAB or Secrets Manager data, and did not verify whether the eq-bank challenge is still failing right now beyond the job records.
