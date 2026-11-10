# Adding a bank

Add the bank's direct API as `internal/banks/<bank>/api.go`, implementing `bank.API`, and one entry in `internal/banks/registry.go`:

```go
type API interface {
    Renew(context.Context, bank.Session) (bank.Session, error)
    Fetch(context.Context, bank.Session, bank.FetchRequest) (bank.Snapshot, bank.Session, error)
}
```

Add its browser sign-in as `internal/signin/<bank>.go` and a case in `signin.Authenticate`. Only the browser task links `signin`; a lint rule keeps the Lambda packages free of it.

Choose an existing strategy (`posted` or `balances`) and browser. A bank whose import IDs need a prefix sets `ImportPrefix`; one with amount-only purchase alerts sets `PurchaseAccount`. Add the bank's schedule to the `schedules` map in `cdk/cdk.go`, keyed by the same registry ID. A bank adds no Lambda, workflow, task definition or secret.

Add configuration with its expected account UUIDs, exclusions, history start and MFA sender/subject. Add its login to the single `Transactions` secret: an entry under the `BANKS` map, keyed by the registry ID, with `username`, `password` and `mailToken`. All three must be non-empty. Keep `enabled` false until verification. Session renewal timing is returned in the session; ordinary fetches must return rotated tokens and cookies even when a later response fails validation.

Adapters only implement bank protocols. They must not use DynamoDB/S3, notify, call YNAB or retry requests independently. Use bounded Playwright operations without `Must` calls. Translate known responses to typed failures; a timeout, generic 403 or malformed response is not evidence of expiry. Never log response bodies or authentication data. Account-only fetches must validate the same identities without importing.

Prove login, independent-invocation API reads and supported renewal using real accounts. Compare complete normalized snapshots and transaction identities to the bank's displayed data. If a protocol cannot renew, return authentication-required so the shared worker signs in. Do not invent refresh-token support from an OAuth vendor name.

Email-only banks use the existing configured notification pipeline. A provider with a new kind of alert may require protocol-specific parsing; keep it inside its adapter/registration rather than adding another importer or workflow.
