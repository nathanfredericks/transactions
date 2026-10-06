# Adding a bank

Add `internal/banks/<bank>/auth.go`, `api.go`, and one entry in `internal/banks/registry.go`. Implement:

```go
type Adapter interface {
    Authenticate(context.Context, *rod.Browser) (bank.Session, error)
    Renew(context.Context, bank.Session) (bank.Session, error)
    Fetch(context.Context, bank.Session, bank.FetchRequest) (bank.Snapshot, bank.Session, error)
}
```

Choose an existing strategy (`posted`, `reconcile`, `balances`), schedule and browser. Reconciliation registrations supply an import prefix and purchase-account label. CDK iterates the registry to create credential references and schedules; it does not add a Lambda, workflow or task definition per bank.

Add configuration with its expected account UUIDs, exclusions, history start and MFA sender/subject. Populate its CDK-created bank secret with `username`, `password`, `mailToken`. Keep `enabled` false until verification. Session renewal timing is returned in the session; ordinary fetches must return rotated tokens and cookies even when a later response fails validation.

Adapters only implement bank protocols. They must not use DynamoDB/S3, notify, call YNAB or retry requests independently. Use bounded Rod operations without `Must` calls. Translate known responses to typed failures; a timeout, generic 403 or malformed response is not evidence of expiry. Never log response bodies or authentication data. Account-only fetches must validate the same identities without importing.

Prove login, independent-invocation API reads and supported renewal using real accounts. Compare complete normalized snapshots and transaction identities to the bank's displayed data. If a protocol cannot renew, return authentication-required so the shared worker signs in. Do not invent refresh-token support from an OAuth vendor name.

Email/webhook-only banks use the existing configured notification pipeline. A provider with a new kind of alert may require protocol-specific parsing; keep it inside its adapter/registration rather than adding another importer or workflow.
