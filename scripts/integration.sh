#!/usr/bin/env bash
set -euo pipefail
backend=$(cd "$(dirname "$0")/.." && pwd)
admin=${ADMIN_REPO_PATH:-"$backend/../transactions-admin"}
export AWS_ACCESS_KEY_ID=contract AWS_SECRET_ACCESS_KEY=contract AWS_REGION=ca-central-1 AWS_EC2_METADATA_DISABLED=true
export DYNAMODB_ENDPOINT=http://127.0.0.1:18000 AWS_ENDPOINT_URL_DYNAMODB=http://127.0.0.1:18000
export AWS_TRANSACTION_OVERRIDES_DYNAMODB_TABLE_NAME=TransactionOverrides
export YNAB_ACCESS_TOKEN=contract YNAB_BUDGET_ID=test YNAB_API_URL=http://127.0.0.1:18001/v1
container="transactions-contract-${RANDOM}"
mock_pid=''
cleanup(){ if [[ -n "$mock_pid" ]]; then kill "$mock_pid" 2>/dev/null || true; fi; docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run --rm -d --name "$container" -p 18000:8000 amazon/dynamodb-local:3.1.0 -jar DynamoDBLocal.jar -inMemory -sharedDb >/dev/null
for attempt in {1..60}; do if curl -s -o /dev/null "$DYNAMODB_ENDPOINT"; then break; fi; sleep 1; done
cd "$admin"
npx tsx scripts/seed-contract.ts "$backend/testdata/contract.json"
node scripts/mock-ynab.mjs "$backend/testdata/contract.json" &
mock_pid=$!
cd "$backend"
CONTRACT_CASES="$backend/testdata/contract.json" go test -count=1 -race ./internal/service -run TestAdminIntegration -v
cd "$admin"
npm run test:e2e
