# Pending transactions and YNAB matching

## How payee, category and memo are filled

Every import (card alert, posted retrieval or EQ reconcile) fills payee,
category and memo from the user's own approved YNAB history. Transactions stay
unapproved, nothing is flagged, and rules are no longer used: the 19 JSONLogic
rules were retired on 8 November 2026 after `cmd/evaluate rules` showed history
reproduces them (backup: `exports/rules-export-2026-11-08.json` in the state
bucket).

- **History is read live.** Each import reads the budget from YNAB. A warm
  Lambda keeps what it read and asks YNAB only for changes since then
  (`last_knowledge_of_server`). Nothing is copied or stored.
- **Payee.** If the user approved this bank text (normalized: case,
  punctuation and digit-bearing tokens ignored) for one payee, with at least
  75% of its recency-weighted approvals, that payee is written by ID. Otherwise
  the model configured in settings (`openaiModel`, `openaiEffort`) chooses one
  of the 20 most similar payees by ID, or names a new merchant. If evidence or
  the model is unavailable, the bank text is kept as the payee.
- **Category.** For a payee with history: its past categories, weighted by
  recency, closeness in amount and the same bank text. A dated event category
  moves to its newest edition ("<event> 2025" to "<event> 2026"). For a new
  payee: the model's choice. Nothing is sent when there is no evidence, and
  YNAB then applies the payee's default.
- **Memo.** In order: a scheduled transaction's memo for the same payee and
  amount; the memo every past use of the same bank text shared; the memo every
  same-amount charge shared; for same-price subscriptions, the one that recurs
  monthly on this day of the month; the memo every recent charge from the payee
  shared. Otherwise no memo.
- **Suggestion records.** Each write stores what was suggested and why
  (DynamoDB `LEARN / TX#<transaction id>`, and in the write ledger). This keeps
  the bank text when YNAB does not, so later imports learn from it.

`cmd/evaluate` replays approved history forward in time to choose and check all
of this (`deterministic`, `models`, `rules`). On the locked holdout (29 June to
7 November 2026): history decided 84% of payees, all correctly; category for
known payees was right 78% of the time (YNAB's own default: 71%); memo 76%
(always empty: 68%). `google/gemini-3.8-flash` at medium effort chose the
payee correctly 86% of the time on the cases history leaves open,
significantly ahead of `claude-haiku-5.5`, `gpt-6-luna` and
`gemini-3.1-flash-lite`.

## Pending and posted transactions

The old purchase-alert helpers (`CreateTransaction` and
`CreateTransactionWithOverride`) created uncleared transactions without
`import_id`. The replacement engine routed alerts through
`CreateEQTransaction`, which supplied an import ID for both pending and posted
transactions. YNAB therefore classified both sides as imported, preventing
matching. This affected email/webhook purchase alerts as well as EQ pending
purchases; setting `cleared: uncleared` alone does not make a transaction
user-entered.

Pending creates now omit `import_id`. Posted creates retain their existing
stable IDs, including Rogers' `YNAB:amount:date:occurrence` IDs. NBDB balance
adjustments are unchanged. Payee, category and memo come from approved history
(see below). Internal operation/ledger identities remain stable and are
separate from the ID sent to YNAB.

A confirmed pending response is recorded by its YNAB transaction ID. EQ can
recover that completed write if a process exits before saving the ledger.
An uncertain pending create remains held for review and is never inferred by
amount/date or blindly submitted again. Existing completed alert jobs remain
idempotent; legacy uncertain writes can still recover their original import ID.

YNAB documents that [API transactions with an import ID are treated as
imports](https://support.ynab.com/en_us/the-ynab-api-an-overview-BJMgQ3zAq).
Its [API reference](https://api.youneedabudget.com/v1) documents user-entered
behavior when the ID is omitted and disallows changing an existing import ID.
This release changes new pending creates. It does not recreate or delete
existing budget transactions to remove historical import IDs.

## Verification — 10 October 2026

- The focused serialization regression check passed for pending/posted creates,
  with and without overrides. Go build/vet and diff checks passed.
- A separate, unlinked YNAB test plan exercised the actual Go service and YNAB
  API. An uncleared $12.34 entry had no import ID; the corresponding cleared
  import automatically matched it into one entry and preserved its user memo.
  Submitting that cleared import again created no duplicate.
- A $23.45 pending/cleared pair deliberately outside the automatic date window
  was manually matched in the Codex in-app browser. The two entries became one
  cleared entry, and YNAB displayed its Unmatch action.
- The test plan was deleted after verification at the user's request. Both the
  browser plan list and API budget list confirmed its removal.
- Rebuilding the pre-fix working source reproduced the previously deployed
  Lambda executable hash exactly. The scoped CloudFormation release changed
  only the gateway and processor code properties, preserving the existing
  browser, hosting and schedule configuration. The stack reached
  `UPDATE_COMPLETE`; both deployed package hashes matched the release ZIP.
- Production dry runs `verify-ynab-pending-rogers-bank-20261010` and
  `verify-ynab-pending-eq-bank-20261010` both completed after fresh ECS
  authentication. A before/after API comparison found no changes to production
  YNAB transactions after normalizing subtransaction array ordering.
