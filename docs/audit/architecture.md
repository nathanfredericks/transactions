# Architecture audit

Scope: package boundaries, the `Engine` type, duplicate and dead code, types and seams, naming, and a target layout for the Go module (`internal/**`, `cmd/**`, `cdk/`). Read-only. Every finding is judged against `docs/audit/rubric.md`; rubric items are cited as `R<n>`.

These decisions are treated as already made:

- The Next.js admin is removed. `engine/admin.go` shrinks to what `cmd/operator` uses.
- JSONLogic rules are dropped, so `internal/override` and `cmd/audit-rules` go.
- Rod is replaced by playwright-go. `audit-browser` designs that, so this report stays at the interface level for browser code.
- The browser broker stays, in simplified form.
- All Go tests are removed (1,263 lines).

`audit-necessity` is still deciding whether review holds, uncertain writes, `WRITE#` intents, pending reconcile matching, baseline review, echo detection and capacity notices should exist at all. Anything in this report that depends on that decision is marked **[N]**. The package boundaries proposed below do not depend on it. Only the size of what lands in each package does.

Line counts are for non-test code. The module has 13,717 non-test lines, and `internal/engine` accounts for 2,867 of them.

---

## 1. Current package graph

| Package | Lines | Owns today | Problem |
|---|---:|---|---|
| `engine` | 2,867 | Job workflow, intake (email and webhook), YNAB import strategies, enrichment, notification delivery, admin API, session persistence, snapshot validation | Four kinds of knowledge in one package, and one `Engine` struct that carries per-job state (§2.2) |
| `service` | ~810 | YNAB HTTP client, YNAB delta sync, alert parsing, model calls, admin lookups, rule validation | "service" is a banned name (R7). It is four packages in one (§2.1) |
| `types` | 104 | YNAB wire types, enrichment decision, secrets, email and webhook routing, parameters, rule records | A grab bag (R7). Every type belongs to a specific owner |
| `override` | 163 | JSONLogic rules, memo templates, and **its own DynamoDB client** | Being deleted |
| `config` | ~240 | SSM settings, secrets, an AWS config singleton, settings and secrets cached in `context` | Reads the environment, keeps mutable globals and passes dependencies through `ctx` (R11, R31) |
| `state` | 243 | DynamoDB and S3 persistence, the lease | Mostly sound. Callers reach into its exported `DB`/`S3` fields |
| `bank` | ~870 | Failure kinds, session, record and snapshot types, HTTP helper, **Rod page helpers, JMAP mail codes**, the `Adapter` interface | The boundary package imports Rod and `cloak`, so everything that names a bank links a browser |
| `banks` + `eq`/`rogers`/`nbdb` | ~1,560 | Registry, plus API (`api.go`) and sign-in (`auth.go`) per bank | `api.go` is Rod-free but shares a package with `auth.go`, which is not |
| `learn` | ~1,590 | The pure suggestion model, the YNAB mirror, replay | Imports `types` for `YNABTransaction`, and keeps its own copies of YNAB `Payee`/`Category`/`Scheduled` |
| `llm`, `email`, `notify` | ~210 | Model call, MIME parsing, Pushover | `notify` has a side-effecting `init()` |
| `browserbroker`, `cloak`, `cloak/human` | ~2,700 | Browser capacity and the stealth browser | Covered by `audit-browser` |

What each binary links today (`go list -deps`):

| Binary | Rod | openai-go | jsonlogic | pushover | maxmind |
|---|:-:|:-:|:-:|:-:|:-:|
| `cmd/lambda` | yes | yes | yes | yes | yes |
| `cmd/browser` | yes | yes | yes | yes | yes |
| `cmd/operator` | yes | yes | yes | yes | yes |
| `cdk` | yes | – | – | – | – |
| `cmd/evaluate` | – | yes | yes | – | – |

Every binary links everything except `evaluate`. Two paths cause it:

1. `bank.Adapter.Authenticate(ctx, *rod.Browser)` (`internal/bank/bank.go:96`) puts Rod in the boundary package.
2. `cmd/browser` imports `engine`, which pulls in `service`, `learn`, `llm` and `override`.

---

## 2. Findings

Format: **ID — title** · location · rubric · severity · change · estimated lines removed or added.

### 2.1 Packages and boundaries

**P1 — The Lambda, operator and CDK binaries link Rod through `bank.Adapter`** · `internal/bank/bank.go:13,95-99`, `internal/bank/browser.go` (imports `cloak`), `internal/banks/registry.go:12-14`, `cdk/cdk.go:14` · R10, R32 · **high**

- `Authenticate` is the only method that needs a browser. `Renew` and `Fetch` live in `api.go` in each bank and are Rod-free.
- `Dependencies` is used only by `auth.go`. The engine always builds adapters as `registration.New(bank.Dependencies{})` (`engine.go:127`, `verify.go:45`).
- Change: split the interface in two.
  - `bank.API{Renew, Fetch}` stays in `bank`. It has no Rod and no `Dependencies`, and the adapters become stateless values (`Registration.API`).
  - `Authenticate` moves to a sign-in package per bank: `internal/banks/<bank>/signin`. That package imports its parent for the session-auth shape, so `type auth` becomes `type Auth`.
  - `bank/browser.go` and `bank/mail.go` move to the sign-in side. The browser-specific registry fields (`Browser`, `Dependencies`) go with them.
  - `audit-browser` decides what browser handle `Authenticate` takes.
- Effect: Lambda, operator and CDK stop linking Rod, `cloak` and maxmind.
- Lines: about +40 (two small registries, package clauses) and −15 (`Dependencies` plumbing on the API side).

**P2 — `service` is four packages in one, and builds a client on every call** · `internal/service/*.go` · R7, R10, R31 · **high**

- What is in it:
  - YNAB client and calls: `ynab.go`, `eq.go`, `sync.go`
  - alert parsing: `alerts.go`
  - model glue: `ai.go`
  - admin lookups and rule validation: `eq.go:199-286`
- Every exported function calls `newYNABClient(ctx)` (`ynab.go:71`), which re-reads secrets and settings out of `ctx`. That is 13 constructions, and a hidden dependency on `config.WithSettings` and `WithInvocationCache` having been called upstream.
- Change:
  - `internal/ynab`: one `Client{token, budgetID, *http.Client}` built in `main`, plus the YNAB wire types and a delta-sync method.
  - `internal/alert`: `alerts.go`, plus the EQ amount-only regex from `gateway.go:86-95`. It takes a model func instead of calling `config`.
  - Model glue (`ChooseWithModel`, 15 lines) moves next to enrichment.
  - Lookups and rule validation are deleted (P4).
- Lines: about −60 net, from the per-call constructor and the `ctx`-loaded config.

**P3 — `types` is a grab bag** · `internal/types/types.go` · R7, R8 · **medium**

Move each type to its owner, then delete the package:

| Type | Moves to |
|---|---|
| `YNABTransaction`, `YNABSubtransaction` | `ynab.Transaction`, `ynab.Subtransaction` (fixes the stutter, R8) |
| `PayeeChoice`, `Enrichment` | the importer, which builds the write payload |
| `EmailConfig`, `WebhookConfig`, `Secrets`, `BankCredentials` | `config` |
| `Parameters` | deleted (D4) |
| `TransactionOverride` | deleted (P4) |

Lines: −104 in `types`, about +70 at the destinations.

**P4 — `override` does its own DynamoDB access, and is going** · `internal/override/override.go:24-32` builds its own `dynamodb` client and `state.Store` from `os.Getenv("STATE_TABLE")` · R11, R31 · **high (decided)**

Delete everything that serves rules:

| What | Where | Lines |
|---|---|---:|
| `override` | whole package | 163 |
| `cmd/audit-rules` | whole command | 80 |
| `cmd/evaluate/rules.go` | whole file, plus the `rules` case in `main.go:50-51` | 180 |
| rules API and `validateRule` | `admin.go:26-69, 285-335` | 95 |
| `Lookups`, `ValidateRuleReferences` | `service/eq.go:199-286` | 88 |
| rule branch in the payload | `service/eq.go:105-118` | 14 |
| `Store.Set` (used only by `rules.save`) | `state/store.go:220-243` | 24 |
| rule seams and branches | `reconcile.go:33,35,167-170,351-360`, `import.go:209-215,297-306`, `learning.go:197-200` (`ruled`) | ~35 |
| `TransactionOverride` | `types.go:60-69` | 10 |
| the `jsonlogic` module | `go.mod` | – |

The `rule` parameter on `CreateEQTransaction` goes too. Total: about −690.

**P5 — `config` reads the environment, caches in globals and passes dependencies through `ctx`** · `config/aws.go:11-21` (`sync.Once` singleton), `settings.go:45-50` (settings in `ctx`), `invocation_cache.go` (secrets in `ctx`), `settings.go:56`, `secrets.go:36` (`os.Getenv`), `settings.go:15-16` (`SecretARN`, documented as ignored) · R11, R18, R31 · **medium**

- Change: `config.Load(ctx, ssm, name)` and `config.LoadSecrets(ctx, sm, arn)` are called once per invocation from `main`, and the values are passed as struct fields.
- Delete `WithSettings`, `WithInvocationCache`, `GetAWSConfig`, `GetParameters` and `SecretARN`.
- Keep the per-invocation settings read in `cmd/lambda`, so a change to `importsEnabled` still takes effect without a cold start. Build the clients once, outside `lambda.Start`.
- Lines: about −50.

**P6 — `notify` has a side-effecting `init()` that rewrites `http.DefaultClient`** · `internal/notify/pushover.go:13-15`, `:25` (`getSecrets` seam) · R11 · **medium**

- `llm.go:44` and `service/ynab.go:85` copy `http.DefaultClient`. Their behaviour therefore depends on whether `notify` happens to be linked.
- Change: a `notify.Pushover{token, user, client}` struct built in `main`, with `Send(ctx, msg, Options)`. Rename `NotificationOptions` to `notify.Options` (stutter, R8).
- Lines: about −5.

**P7 — Bank-specific wiring lives outside the registry** · `reconcile.go:100` (`job.Bank == "eq-bank"`), `engine.go:72`, `gateway.go:86-102` (EQ sender, subject and regex), `bank/http.go:169` (an `eqbank.ca` JWT claim inside the generic `TokenTiming`), the "EQ transaction preview" log at `reconcile.go:361` · R10 · **medium**

- `banks/registry.go:1` says it is "the only bank-specific wiring", and these break that.
- Change:
  - Replace `eq-bank` checks with a registration field. For example, `ImportPending bool` drives the posted-only filter at `reconcile.go:100-108`, with unchanged behaviour.
  - Move the EQ alert regex into `alert` as a layout with no descriptor.
  - Pass the max-lifetime claim name from the EQ adapter, or move `TokenTiming` into `eq` with a generic `exp` fallback.
  - The `engine.go:72` branch is deleted (D2).
- Lines: about ±0.

**P8 — `cmd/browser` imports `engine` for five symbols** · `cmd/browser/main.go:19,28,37,49,70,88,257,350` · R9, R10 · **low**

- The five symbols are `engine.New`, `Job`, `BrowserResult`, `SessionPointer` and `ValidateSnapshot`. Through them the browser image links `learn`, `llm` and openai-go.
- Change:
  - Move `ValidateSnapshot` to `bank.Snapshot.Validate(expected)`. It is bank knowledge.
  - Move session load and save to one exported pair (D9).
  - Once `engine` no longer imports the importer directly (target layout, §3), what remains is cheap to link.
- Lines: about −10. This is worth doing only after P1, P2 and E1.

### 2.2 The `Engine` type and the god functions

**E1 — `Engine` mixes dependencies with per-job state** · `engine/types.go:84-90` · R3, R18 (same smell), R31 · **high**

- `Store`, `Settings` and `SFN` are dependencies. `Lease` and `Job` are per-job state.
- Methods mutate `e.Job` to steer unrelated calls:
  - `Maintain` sets `e.Job = Job{Bank: ...}` purely so `e.get` hits the right partition (`gateway.go:189`).
  - `Deliver` and `VerifySession` fabricate a `Job` for the same reason (`notify.go:33`, `verify.go:24`).
  - `job.retry` swaps in an operator lease (`admin.go:160`).
- `get` scopes by `e.Job.Bank` while `put` scopes by `e.Lease.Bank` (`types.go:103-108`). `put` also takes an unused fourth parameter on all 21 calls.
- There are 43 methods on `*Engine`, with receiver names `s` in `reconcile.go` and `e` elsewhere (CRC).
- Change:
  - `Engine` keeps only dependencies.
  - `Run` creates a `run{*Engine; job Job; lease state.Lease; reg banks.Registration; scope *state.Scope}`. The import, session and notice methods move onto `*run`.
  - `state.Scope` is a bank-scoped, lease-fenced view: `Get`, `Put`, `PutMany`, `List`. It replaces 27 `"BANK#"+` concatenations and the get/put asymmetry. It is new machinery (about 30 lines) because the importer otherwise has to take the whole `Engine`.
  - Resolve the registration once per run. That replaces 10 calls to `banks.Find`, which ignore its error in four places.
- Lines: about +35 and −45.

**E2 — `Run` is 262 lines** · `engine/engine.go:14-275` · R3, R5 · **high**

- It covers load and fence, lease, the terminal check, health gating, the paused check, dispatch by source, the session state machine, fetch, validation, snapshot upload, the baseline gate and the strategy switch.
- Some of it is duplicated:
  - load, execution check and terminal-status check, twice (`:19-27`, `:51-59`)
  - "hand off to browser", twice (`:181-193`, `:212-221`)
- Change: split it into steps that each read top to bottom:
  - `begin` (load, fence, lease; about 40 lines)
  - `gate` (health, backoff, pause; about 25)
  - `retrieve` (session, renew, fetch, or browser handoff; about 60, with one `toBrowser()` helper)
  - `importSnapshot` (validate, upload, baseline, strategy; about 30)
  - `finish` (release unless held, `failure` on error)
- Lines: about −30 from the duplication. The rest moves.

**E3 — `reconcile` is 357 lines and returns a value that is always false** · `engine/reconcile.go:95-451` · R3, R6 · **high**

- `missing := false … return missing, nil` (`:443-450`). Nothing ever sets it, so the 3-step retry and `purchase-not-found` failure in `Run` are dead (`engine.go:250-262`). So is the copy at `notification_copy.go:15,49-51`.
- `created`, `adopted` and `updated` are counted and then discarded with `_ =` (`:147-151, 447-449`).
- `result := struct{ Records []Record }{…}` (`:97`) is an anonymous struct wrapping a slice. `type Record = bank.Record` (`:19`) is an alias whose only effect is to hide the owner.
- Change:
  - Delete the dead return and counters.
  - Split the body into `match(r)` (exact, then resolution **[N]**, then pending candidates **[N]**), `create(r)` (adopt or settle **[N]**, recover **[N]**, write), and `update(entry, r)`. That leaves a loop of about 50 lines.
- Lines: about −35 dead, about −20 from the split. If necessity removes holds, intents and pending matching, `reconcile` drops to about 80 lines in total.

**E4 — `Admin` is 261 lines plus a 168-line `baseline`** · `engine/admin.go:24-284, 336-503` · R3, R6 · **high (decided)**

`cmd/operator` and the docs (`docs/cutover.md:20-27`, `docs/local-verification.md:40-63`) use these actions:

| Action | Keep? |
|---|---|
| `banks.list`, `job.get`, `job.retry`, `bank.resume`, `notifications.retry` | keep |
| `baseline.preview`, `baseline.approve` | keep **[N]** |
| `submit`, `verify-session` (routed in `cmd/lambda`) | keep |
| `rules.*`, `lookups` | delete (P4) |
| `jobs.list` (`:70-93`), admin UI only | delete |
| `review.match`, `review.update`, `review.balance` (`:203-280`) | delete **[N]** |

- Change: rename the file to `ops.go`, with one small function per action and the `switch` reduced to dispatch.
- Lines: −24 from `jobs.list`. If necessity drops review holds, −78 more. If it drops baseline review, −170 more.

**E5 — `Maintain` is unreachable** · `gateway.go:183-225`, `bank.go:113` · R6 · **medium**

- It submits only for registrations with `KeepWarm`, and no registration sets `KeepWarm` (`registry.go:12-14`).
- It is invoked only by action `maintain`, which no schedule sends (`cdk.go:146-148`).
- `docs/verification-results.md:84-88` records the retirement.
- Change: delete it, with D2.
- Lines: about −45.

### 2.3 Duplicate code

**D1 — The import-ID formula is written twice, and its siblings are inline** · `reconcile.go:276-277` and `admin.go:397-400` hold the same `prefix + ":" + Hash("tx#"+acct+"#"+Hash(identity))[:32]`. The posted strategy uses `ProvisionalID` as the import ID (`import.go:101`, `admin.go:398-400`). Alerts use `"ALERT:"+Hash(jobID)[:30]` (`import.go:309`) · R36 · **medium**

- A drift between the import write and the baseline preview silently breaks adoption.
- Change: one `importID(reg, record)` function next to the strategies, and one `alertImportID(jobID)`.
- Lines: −4, and one source of truth.

**D2 — Record keys and the review-hold key are rebuilt inline** · `acct+"#"+identity(r)` at `reconcile.go:173,177,244,283,288` and `admin.go:383`; `hash(acct+"#"+identity(r))` at `reconcile.go:161,204,213` · R3 · **low**

- Change: `recordKey(r)`, plus `holdID(r)` **[N]**.
- Lines: −5.

**D3 — Ten linear scans find a YNAB transaction by ID or import ID** · by import ID: `service/eq.go:67-70`, `reconcile.go:311-318`, `import.go:116-121`, `import.go:244-248`, `admin.go:408`. By ID: `reconcile.go:290-294, 328-332, 402-406, 431-435`, `admin.go:267-271`, `import.go:428-431` · R3 · **medium**

- Change: `ynab.FindByID(rows, id)` and `ynab.FindByImportID(rows, id)`, each with `!Deleted` built in. `reconcile.go:312` deliberately looks at deleted rows **[N]**, so keep that one explicit.
- Lines: +14 and −40.

**D4 — Timezone is loaded in seven places, most of them ignoring the error** · `import.go:165,256`, `learning.go:21`, `gateway.go:101`, `reconcile.go:339-346` (fetches `Parameters` via `config` to get a timezone it already has in `s.Settings`), `override.go:80-87`, `service/ynab.go:209-218` · R12, R31 · **medium**

- `config.Load` already validates the name (`settings.go:71`).
- Change: `Settings.Location *time.Location` (tagged `json:"-"`), set once in `Load`, plus `Settings.Today()` and `Settings.Day(t)`.
- Delete `config.GetParameters`, `types.Parameters` and `PaymentProcessors`. That last one is copied around and never read (`settings.go:37`, `parameters.go:13`).
- The hard-coded `America/Halifax` in the bank adapters (`eq/api.go:78`, `rogers/api.go:78`) is the bank's own clock, so leave it. Drop the `_` on its error.
- Lines: about −35.

**D5 — Lease boilerplate is repeated ten times** · `admin.go:112-116, 137-141, 182-186, 208-212, 234-238, 251-255, 496-500`, `notify.go:34-42`, `verify.go:25-37`, `cmd/browser/main.go:329-340` · R21, R37 · **medium**

- Six sites return a lease failure as `fmt.Errorf("bank busy")`, losing the cause.
- Seven sites `defer Release(ctx, …)` on a context that may already be cancelled. `verify.go:33-37` and `cmd/browser` do it correctly.
- Change: `Store.WithLease(ctx, bank, owner, func(state.Scope) error) error`. It returns `state.ErrBusy` and releases on `context.WithoutCancel` with a 10-second timeout.
- After E4 the remaining sites are four operator actions, `Deliver`, `VerifySession` and `verifyBank`. `Run` and `RecoverBrowser` keep their explicit handling, because `Run` hands its lease to the browser.
- Lines: +15 and −35.

**D6 — Session load and save are written twice** · load: `engine/types.go:115-136` and `cmd/browser/main.go:87-104`. Save: `engine/types.go:137-149`, `cmd/browser/main.go:62-72` and `:346-350` · R3 · **medium**

- Change: one exported `LoadSession` and `SaveSession` (pointer plus S3 object plus identity check) in the session file of `engine`.
- Lines: −20.

**D7 — Recovery notices are written twice** · `engine.go:283-298` (capacity) and `:299-314` (incident) are the same "if sent, notify recovery; else mark superseded" block · R3 · **low [N]** (depends on whether capacity notices survive)

- Change: `resolveIncident(ctx, noticeKey, recoveryKey, msg, title)`.
- Lines: −14.

**D8 — `GetAccountTransactions` has three names and fetches full history repeatedly** · seams `getAccountTransactions` (`reconcile.go:32`) and `accountTransaction` (`import.go:207`), and direct calls at `import.go:112`, `admin.go:262,391`, `service/eq.go:65` · R3, R39 · **medium**

- Each call passes `last_knowledge_of_server=0`, which is the whole account history (`service/eq.go:46`).
- One job can fetch the same account three or four times: `reconcile`, the create-failure recovery, `findEchoCredit` **[N]**, and `clearEchoes` **[N]**.
- Change: one name, `ynab.Client.Transactions`, and a per-run memo on `run` that is invalidated after the run's own writes. `audit-robustness` owns the incremental-read change.
- Lines: −6.

**D9 — Text normalization: two copies of the same thing, and three different things that look alike** · `reconcile.go:46` (`merchant`) and `override.go:103` are identical. `alerts.go:55,111,113` and `email/parser.go:74` collapse whitespace only. `learn.Normalize` (`learn/text.go:14`) also drops digit tokens and punctuation · R4 · **low**

- These serve different matchers, and unifying them with `learn.Normalize` would change matching behaviour. Do not.
- Change: the `override` copy goes with P4. Add one `squash` helper inside `alert` for its three uses.
- Lines: −3.

**D10 — The "Transaction Approved" notice is built twice, and delivery matches on its title** · `import.go:335,508` build it, and `notify.go:66` picks priority with `n.Title == "Transaction Approved"` · R5 · **low**

- Change: add a `Kind` field to `Notification` and set the priority from it.
- Lines: ±0.

### 2.4 Dead code and compatibility code

**X1 — Dead YNAB helpers** · `service/ynab.go:178-287` (`CheckForRecentTransaction`, `CreateTransaction`, `CreateTransactionWithOverride`, `formatDate`) and `:67-69` (`toYNABExpenseMilliunits`) · R6 · **medium**

- Production references them nowhere. Only the tests being deleted do.
- Lines: −110.

**X2 — The retired EQ upkeep path and the `maintain-session` purpose** · `engine.go:70-74` ("Retired upkeep jobs may still be queued"). A Standard workflow times out after 24 hours (`cdk.go:135`) and the upkeep schedule was removed weeks ago, so nothing can still be queued · R6 · **medium**

- `maintain-session` is reachable only through `Maintain` (E5) or a hand-submitted `source: manual`. Manual verification uses `verify-session` instead (`README.md:53`).
- Delete the purpose's branches:
  - `engine.go:86-90, 93, 237, 279-281`
  - `gateway.go:30-35` (narrow to `retrieve`)
  - `cmd/lambda/main.go:81-82`
  - `Registration.KeepWarm`
- Keep `FetchRequest.AccountsOnly`, which the browser worker uses.
- Lines: about −30, plus E5.

**X3 — The legacy `alert#` link check** · `import.go:476-480` ("An alert the earlier bank-retrieval path already linked") · R6 · **low**

- It matters only if an old message is reprocessed, and `incoming/` objects expire after 30 days (`cdk.go:60`).
- Change: delete it after `audit-data` confirms there are no recent `alert#` rows.
- Lines: −5.

**X4 — Small unused pieces** · R6 · **low**

| Item | Location | Lines |
|---|---|---:|
| generic `decode` | `engine.go:418-422` | −5 |
| `Request.Lease`, never read | `types.go:53` | −1 |
| the post-return `if err != nil` | `gateway.go:215` (inside dead E5) | – |
| `_ = integration`, `_ = adminRole` | `cdk.go:152,169` (Amplify block is `audit-infra`'s) | – |
| `Mirror.Fill` in production (`NewMirror` already fills) | `sync.go:26` | −1 |

Keep `Fill` itself for `cmd/evaluate`, which loads mirror files.

**X5 — Untracked `dist/issue-probe` and `dist/replay-alerts` sit inside the module** · `go list ./...` shows `dist/*` packages importing `internal/service` · R9 · **low**

- They are gitignored, so CI never sees them, but a local `go build ./...` breaks as soon as `service` is split.
- Change: delete them locally, or move them out of the module tree.
- Lines: 0 tracked.

### 2.5 Types and seams

**T1 — Statuses are untyped strings** · 80 string literals across `engine/` and `cmd/` · R5 (and lint `exhaustive`) · **medium**

The string-valued sets:

| Set | Values |
|---|---|
| `Job.Status` | accepted, running, browser, waiting, paused, complete, review-required, failed |
| `Outcome.Outcome` | complete, deferred, failed, review-required, authentication-required, paused |
| `Write.Status` | started, complete, echo, reviewed-not-written **[N]** |
| `Notification.Status` | pending, sent, superseded, failed |
| `Registration.Strategy` | reconcile, posted, balances |
| `Job.Purpose` | retrieve, maintain-session (X2 removes the second) |
| `Job.Source` | scheduled, notification, alert, session, manual |
| `Record.Status` | posted, pending |

- Change: `type JobStatus string` and friends, with `const` blocks. The JSON is unchanged.
- `Outcome.Outcome` stutters. Rename the struct to `Result` and keep the field tag `json:"outcome"`, because the Step Functions definition (`cdk.go:134`) reads `$.result.outcome`.
- The CDK keeps its literals, with a comment naming the Go constants.
- Lines: about +45.

**T2 — 22 package-level function variables serve tests that are being deleted** · R11, R31 · **medium**

| File | Seams |
|---|---|
| `reconcile.go:32-37` | `getAccountTransactions`, `getOverrides`, `getParameters`, `findOverride`, `updateEQTransaction`, `hash` |
| `import.go:203-217` | `extractAlert`, `createTransaction`, `createAmountOnly`, `accountTransaction`, `deleteTransaction`, `findRules`, `submitJob` |
| `learning.go:86-115` | `loadLearning`, `chooseWithModel`, `syncMirror` |
| `service/ai.go:13-15, 42` | `getParameters`, `getSecrets`, `loadSettings`, `callModel` |
| `notify/pushover.go:25` | `getSecrets` |
| `config/secrets.go:13` | `loadSecretPayload` |

- The same function also appears under two seam names (D8), alongside direct calls.
- Change: delete them all. Call methods on the injected `ynab.Client`, `notify.Pushover` and model config.
- The `live` mirror cache (`learning.go:79-83`, a mutable package global) becomes a field on the importer. It is still per process.
- Lines: about −45.

**T3 — `Write` is one struct serving four shapes** · `import.go:28-47` · R4 · **low [N]**

- One struct holds a YNAB create intent, an update intent (`UPDATE#`), a balance adjustment (`BALANCE#`), an alert record with snapshot fields for edit detection, and a baseline settlement link (`admin.go:455`).
- Change: if intents survive necessity, split it into `writeIntent` and `alertRecord`, and give the baseline its own `Settlement`. If they do not, most of `Write` disappears and the question goes away.
- Lines: about ±0.

**T4 — Misleading names and stutter** · R8 · **low**

| Current | Location | Proposed |
|---|---|---|
| `CreateEQTransaction`, `UpdateEQTransaction`, `eqTransactionPayload` | `service/eq.go` | `Create`, `Update` (used by Rogers' posted strategy and by every alert, not just EQ) |
| `config.GetAWSConfig`, `GetSecrets`, `GetParameters` | `config` | drop the `Get` prefix |
| `service.GetAccounts`, `GetAccountTransactions` | `service` | `ynab.Client.Accounts`, `Transactions` |
| `override.GetTransactionOverrides` | `override` | deleted |
| `service.YNABAccount` | `service` | `ynab.Account` |
| `types.YNABTransaction` | `types` | `ynab.Transaction` |
| `notify.SendNotification`, `NotificationOptions` | `notify` | `Send`, `Options` |

Lines: ±0.

**T5 — The YNAB wire types are written two or three times** · `service/ynab.go:47` (`ynabPayee`) next to `learn.Payee` (`learn/mirror.go:18`); `service/eq.go:203-213` (`LookupCategory`/`LookupGroup`, deleted with P4) next to `learn.Category`; `learn.Scheduled` · R3 · **low**

- Change: `ynab` defines `Payee`, `Category`, `ScheduledTransaction`, `Transaction` and `Account`.
- `learn` imports `ynab` for these types only. `ynab` exposes `Changes(ctx, knowledge) (Delta, error)` and `learn.Mirror.Apply(Delta)`. `ynab` never imports `learn`, so there is no cycle. `learn` stays I/O-free in practice.
- Lines: −25.

### 2.6 Outside structural scope, but against a ground rule

These belong to `audit-necessity` and the verifier. They are recorded here because they show up while tracing package boundaries.

- **N1** — `reconcile.go:60-65` compares EQ descriptors after cutting at the first comma ("trailing city/country descriptor"). `admin.go:423` matches on a payee-name prefix plus `" "` or `","`. Both are tail-stripping heuristics, which the ground rules and `MEMORY.md` forbid. Both sit inside pending matching and baseline settlement **[N]**.
- **N2** — `learn/model.go:401-475` (`dated`, `year`, `edition`, `eventPrefix`, `tripCategory`) keys category behaviour on a year in the name and a `"<event> <base>"` suffix. That is a naming-convention heuristic in learning. Flag it to whoever owns learning quality. Removing it changes suggestions, so it needs a `cmd/evaluate` replay.

---

## 3. Target layout

```
cmd/
  lambda/main.go       ~60  build clients once; per invocation: config.Load, route by ROLE to engine methods
  browser/main.go      ~40  env → signin.Run (body owned by the audit-browser design)
  browser-broker/      keep (simplified per audit-browser)
  operator/main.go     keep; -local-preview calls engine ops directly
  evaluate/            minus rules.go
  cloak-inspect/       audit-browser decides
internal/
  config/      Settings (+Location, Today, Day), Secrets, BankCredentials, Email and Webhook routes.
               Load and LoadSecrets take clients and names from main. No globals, nothing cached in ctx.
  state/       Store (DynamoDB + S3), Lease, WithLease, ErrBusy, Scope (bank-scoped, fenced Get/Put/PutMany/List).
               Read(ctx, key, limit) replaces the direct S3 access in gateway.go:72.
  ynab/        Client{token, budget, http}; Transaction, Subtransaction, Account, Payee, Category, ScheduledTransaction;
               Accounts, Transactions, Create, Update, Delete, Payees, Changes; FindByID, FindByImportID.
  alert/       Parse (layouts incl. EQ amount-only), Extract(ctx, text, model func). Was service/alerts.go plus gateway.go:86-95.
  email/       MIME parsing (unchanged).
  llm/         unchanged.
  notify/      Pushover client struct.
  learn/       pure model + mirror + replay; imports ynab for types only.
  bank/        Kind/Failure, Session, Account, Record, Snapshot (+Validate), FetchRequest, HTTP helper, Hash, Milliunits,
               API interface {Renew, Fetch}. No browser, no mail.
  banks/       Registration{ID, Name, Strategy, Schedule, ImportPrefix, PurchaseAccount, ImportPending, API}; All; Find.
    eq/ rogers/ nbdb/          API adapters (today's api.go) + exported Auth shape.
    eq/signin rogers/signin nbdb/signin   Authenticate (today's auth.go), browser-only.
  signin/      browser-only: Dependencies, the sign-in registry (bank → Authenticate, browser kind), JMAP code reader
               (bank/mail.go), page and network helpers (bank/browser.go → playwright per audit-browser).
  engine/      Engine{store, settings, sfn, ynab, notifier, importer}; run{job, lease, reg, scope}.
               job.go (Job, statuses, Submit), run.go (Run, begin/gate/retrieve/finish, failure, health, capacity [N]),
               session.go (LoadSession/SaveSession, renew), intake.go (Email, Webhook), deliver.go (outbox),
               ops.go (operator actions), verify.go.
  importer/    Importer{ynab, settings, model, live mirror}; strategies: reconcile [N], posted, balances;
               notification and amount-only alert imports; echo handling [N]; enrichment (Decision, enrich, recordSuggestion);
               importID; payload construction (pending stays user-entered, posted carries import_id).
               Takes (ctx, state.Scope, importer.Job view, snapshot).
  browserbroker/, cloak/  per audit-browser.
```

What each package owns, and the dependency rules:

- `bank` imports only the standard library and uuid. `banks` imports `bank` and the per-bank API packages. Nothing under `cmd/lambda`, `cmd/operator` or `cdk` may import `signin` or `cloak`. Add a `depguard` rule (R32, rubric lint list) to enforce this.
- `importer` holds the knowledge of how bank records become YNAB transactions. `engine` holds the knowledge of how a job moves through leases, retries, the browser and notices. That is a split by knowledge, not by pipeline step (R10).
- `engine` imports `importer`. `importer` imports neither `engine` nor `config` loaders. It gets a small `importer.Job` value (bank, ID, dry run, source fields, alert fields, a missing-attempt counter it returns) and a `state.Scope`.
- `learn` remains the only place for suggestion logic, and `cmd/evaluate` keeps replaying exactly what ships.

What depends on `audit-necessity`:

| If necessity deletes… | Effect on the layout |
|---|---|
| review holds, `RESOLUTION#`, `review.*` | `importer`: `hold`, `holdID` and the resolution step go. `engine/ops.go` loses three actions. `Health.ImportReview` goes |
| uncertain writes and `WRITE#`/`UPDATE#`/`BALANCE#` intents | `importer` loses intent bookkeeping. Create relies on YNAB `import_id` plus `duplicate_import_ids` (R36). `bank.Uncertain`, `writeFailure` and `T3` go. Recovery inside the create (`service/eq.go:59-76`) goes |
| pending reconcile matching | `samePurchase`, `pendingPresent`, `claimed`, `competing` and `Entry.Authorization*` go. `reconcile` shrinks to about 80 lines, and N1 disappears |
| baseline review | `ops.go` loses `baseline` (−170) and `run.importSnapshot` loses the gate |
| echo detection | `findEchoCredit`, `clearEchoes`, `requestRetrieval` and `ECHO#` go (about −120). `importer` gets smaller, with the same boundary |
| capacity notices or `RecoverBrowser` | `run.go` shrinks. D7 is moot |
| Step Functions | `engine` keeps `Run` but loses `Outcome` and SFN plumbing. The rest of the layout is unchanged |

The package list above stays the same in every case. Only file sizes change. The one exception: if intents and pending matching both go, the `tx#` ledger (`Entry`, `ledger()`) may also go. That makes `importer/ledger.go` unnecessary and `reconcile` collapses into `posted` with a cleared-state update.

---

## 4. Sequence of moves

Each step builds on its own. The gate after every step is `go build ./... && go vet ./...`, a `cmd/evaluate deterministic` replay that must match, and, for steps that touch the import path, a live dry run per bank (`-action submit` with `dryRun: true`, then `job.get`). No tests are added.

1. **Delete what has been decided.**
   - All `_test.go` files.
   - Rules (P4): `override`, `cmd/audit-rules`, `evaluate/rules.go`, the rule branches and seams, `Store.Set`, the jsonlogic dependency.
   - Admin-only actions: `rules.*`, `lookups`, `jobs.list`.
   - About −2,000 lines including tests.
2. **Delete dead code.** X1, X2, E5, X4, the `missing` and counters in E3, and `GetParameters`/`Parameters`/`PaymentProcessors`/`SecretARN`. About −250.
3. **Delete the seams (T2).** With the tests gone, inline the 22 function variables back to direct calls. Still package functions at this point. About −45.
4. **Split `bank` (P1).** Introduce `bank.API`. Move `auth.go` files into `banks/<bank>/signin`, and `bank/browser.go` plus `bank/mail.go` into `signin`. Export `Auth`. Move `ValidateSnapshot` to `bank.Snapshot.Validate`. Check that `go list -deps ./cmd/lambda ./cmd/operator` and `cdk` show no `go-rod`. This step should land just before, or together with, the playwright port, so the sign-in files move once.
5. **Introduce `ynab` (P2, P3, T4, T5, D3).** Move the client and wire types, build `ynab.Client` once in `main`, and switch callers to methods. Move the YNAB types out of `types`. Add `FindByID`/`FindByImportID`.
6. **Introduce `alert` (P2, P7).** Move `alerts.go` and the EQ regex from `gateway.go`. Move `ChooseWithModel` next to enrichment. Delete `service`.
7. **Fix `config`, `notify` and the timezone (P5, P6, D4).** Explicit `Load`, no `ctx` cache, `Settings.Location`, `notify.Pushover`. Delete `types`.
8. **Split `Engine` (E1, D5, D6).** Add `state.Scope` and `WithLease`. Make `Engine` dependencies only, and add `run`. Resolve the registration once. Consolidate session load and save.
9. **Decompose `Run` (E2).** Introduce `begin`, `gate`, `retrieve`, `importSnapshot`, `finish` and one browser-handoff helper. Add typed statuses (T1) at the same time.
10. **Wait for `audit-necessity` verdicts, and apply the deletions first.**
11. **Extract `importer` (§3).** Move the strategies, alert imports and enrichment. Decompose whatever is left of `reconcile` (E3). Add `importID` (D1) and `recordKey` (D2).
12. **Shrink `admin.go` to `ops.go` (E4)** using the surviving actions. Move `cmd/browser`'s session code to the shared helpers (P8).

Steps 1–3 are pure deletion and can ship together. Steps 4–9 are mechanical moves with no behaviour change. Step 11 is deliberately last, because necessity decides how much is left to move.

---

## 5. Top changes, ranked by value against effort

| Rank | Change | Value | Effort | Net lines |
|---:|---|---|---|---:|
| 1 | Delete rules, admin-only actions and tests (P4, step 1) | Removes a dependency, a DynamoDB side channel and a third of `admin.go` | Low; decided | about −2,000 |
| 2 | Delete dead code: YNAB helpers, upkeep and `maintain-session`, `missing` and counters, `Parameters` (X1, X2, E5, E3, D4) | Shrinks `Run` and `gateway`; nothing to verify beyond the build | Low | about −250 |
| 3 | Delete the 22 function-variable seams (T2) | One name per operation; makes the later moves mechanical | Low | about −45 |
| 4 | Split `bank.Adapter` into `bank.API` and `signin` (P1) | Lambda, operator and CDK stop linking Rod; one boundary for the playwright port | Medium; pairs with `audit-browser` | about +25 |
| 5 | Replace `service` with `ynab` + `alert`, and dissolve `types` (P2, P3, T4, T5) | One client built once; honest names; no grab bags | Medium | about −110 |
| 6 | Split `Engine` into dependencies and per-job state, with `state.Scope`, `WithLease` and shared sessions (E1, D5, D6) | Ends the `e.Job` steering hacks; fixes lease release on cancel in one place | Medium | about −50 |
| 7 | Explicit `config`, `notify` without `init`, `Settings.Location` (P5, P6, D4) | Removes globals, `ctx`-borne dependencies and seven timezone loads | Low–medium | about −90 |
| 8 | Decompose `Run` and type the statuses (E2, T1) | `Run` readable top to bottom; `exhaustive` lint can check the state machine | Medium | about +15 |
| 9 | `ynab.FindBy*`, `importID`, `recordKey` (D1–D3) | One import-ID formula; ten loops become two helpers | Low | about −35 |
| 10 | Extract `importer` and decompose `reconcile`, after necessity (§3, E3) | Splits by knowledge; `reconcile` goes from 357 lines to 50–80 | Medium–high; wait for necessity | depends on [N] |
