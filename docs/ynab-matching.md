# Pending transactions and YNAB matching

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
adjustments are unchanged. Overrides still set payee, category and rendered
memo. Internal operation/ledger identities remain stable and are separate from
the ID sent to YNAB.

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
