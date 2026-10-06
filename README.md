# Transactions

Imports bank email and webhook notifications into YNAB. `transactions-admin` writes JSONLogic overrides to the shared DynamoDB table. The importer evaluates rules newest first, with ID as a deterministic tie breaker. A matching rule supplies the YNAB payee, optional category, and memo.

Merchant descriptors retain `APPLE.COM/BILL`. Numeric operands are numbers; calendar months are 1–12. Apple subscription rules use amount and billing windows. Bundles fall through without splitting or flags. Mullvad matches `TAILSCALE` without a fixed CAD amount. Subscription specifications and sanitized cases are in `testdata`.

Memo templates use Go syntax, with one renderer for email, webhook and EQ:

```
{{.Date}}
{{formatDate .Date "January 2006"}}
{{formatDate (subtractMonthFromDate .Date) "January 2006"}}
```

Previous-month dates clamp to the last day. EQ date-only records are interpreted in the budget timezone. Reconciliation updates bank date, amount and clearing status while preserving YNAB payee/category/memo, approval and reconciliation state. Stable import IDs recover interrupted writes. Ambiguous postings remain for review.

## Verification

```
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build ./...
(cd cdk && go vet ./... && go test ./... && go build ./...)
(cd ../transactions-admin && npm ci && npm run lint && npm run typecheck && npm run test:coverage && npm run build && npx playwright install chromium)
scripts/integration.sh
```

The integration runner needs Docker and an adjacent admin checkout (or `ADMIN_REPO_PATH`). It creates an ephemeral DynamoDB Local container, saves every fixture through admin validation/persistence, and verifies exact Go YNAB payloads using an HTTP fake. Playwright tests the built admin against the same local database and a fake YNAB lookup service. Ports 18000, 18001 and 13000 must be free. Production credentials are replaced for this process. Both repositories run the suite on pushes, pull requests and manual runs, uploading coverage, browser traces and screenshots.

## Rule repairs and deployment

`python3 scripts/repair-rules.py` backs up live overrides, verifies all payee/category references against YNAB and validates proposed rules without writing them. Add `--apply` only to apply the prepared subscription repairs. Writes use conditional expressions to avoid overwriting concurrent edits. Each run saves before/candidate/plan/after JSON under `~/.codex/backups/transactions`, with private file permissions. The tool never modifies historical YNAB transactions. No legacy templates were present in the audited original rules.

`go run ./cmd/audit-rules -rules /absolute/path/overrides-after.json` independently validates saved rules against the contract cases without network writes.

Deploy the existing Transactions CDK stack with `TRANSACTIONS_REVISION` set to the tested Git SHA. The stack tags and both Lambda environments record this SHA. The CDK app uses the root and `cdk` Go modules; create a local Go workspace with both modules if needed. Check the CloudFormation update and compare the Lambda code digest with the CDK asset uploaded to S3. Publish a version of each previous Lambda before deploying to retain rollback artifacts. The admin main branch deploys through its existing Amplify app. Check the Amplify job's exact commit, deployment result, and read-only admin pages before applying rules.

Rule rollback: restore backed-up records by ID and remove only newly added IDs identified in the repair plan, after checking for later edits. Deployment rollback: redeploy the recorded previous code artifact/configuration or previous Git revision; Amplify retains previous successful build jobs. Backups contain account identifiers and stay outside Git.
