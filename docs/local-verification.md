# Fast local bank verification

Use the production Go adapter and pinned browser image locally. Rebuild only the Go binary between fixes; mount it into the existing image. AWS redeployment is reserved for infrastructure changes and the final cross-environment checks.

The runner uses your AWS CLI identity and the **new engine's** encrypted S3 sessions and DynamoDB leases. It can authenticate, fetch and renew. It has no import or notification path. It does not touch the old importer's state. Before the first cutover, keep new imports disabled. After cutover, these verification commands still have no import path and share the normal bank lease; inspect current jobs before deliberately replacing a live session.

## Setup once

Docker, Go, Python 3 and an authenticated AWS CLI are required. Use the same AWS profile as deployment. The runtime image is ARM64; other hosts need Docker's ARM64 support.

```sh
docker build --platform linux/arm64 -t transactions-engine-browser:verify .
```

## Edit, build, verify

Each command below builds the current worker and runs a separate container. Login uses the same Rod flow and Chromium/CloakBrowser choice as ECS. API and renewal checks do not launch a browser. Credentials are passed through a temporary private environment file and removed when the command ends. Never enable shell tracing or print session objects.

```sh
python3 scripts/verify-bank.py eq-bank
python3 scripts/verify-bank.py eq-bank --mode api
python3 scripts/verify-bank.py eq-bank --mode renew

python3 scripts/verify-bank.py rogers-bank
python3 scripts/verify-bank.py rogers-bank --mode api
python3 scripts/verify-bank.py rogers-bank --mode renew

python3 scripts/verify-bank.py nbdb
python3 scripts/verify-bank.py nbdb --mode api
python3 scripts/verify-bank.py nbdb --mode renew
```

API checks validate expected accounts and complete retrieval, save private snapshots, and print only account IDs, counts, expiry times and duration. Renewal uses the engine's uncertainty policy; an uncertain exchange is held instead of blindly replayed. These explicit verification commands report failures without automatically starting another login.

Avoid concurrent authentication from another application using the same bank account. During migration, the old maintenance worker can replace a session created by a local check. For an isolated lifetime check, temporarily pause the old bank's maintenance, verify no old authentication is running, and restore the original policy in a `finally` cleanup. After cutover, the old workers stay disabled; never re-enable them as verification cleanup. Record both the rotation and successful retrieval afterward; a successful early renewal response can leave the token unchanged.

After fixing a blocked login, use the normal `bank.resume` action **before** verifying again. Resume invalidates the session pointer. Do not reset credentials/challenge holds repeatedly without investigating their cause.

```sh
AWS_REGION=ca-central-1 go run ./cmd/operator -action bank.resume -bank eq-bank
```

## Build checks

```sh
go build ./...
go vet ./...
(cd cdk && go build ./... && go vet ./...)
npm --prefix admin run typecheck
npm --prefix admin run lint
npm --prefix admin run build
```

Keep the agreed restriction against mocks, new test files and test-suite runs. Real local bank checks, static checks and builds are the development loop.

## Release checks

Local success does not prove Lambda can reuse a session. After one consolidated deployment, invoke the processor from a separate Lambda execution to verify native fetch and renewal, then submit a normal dry-run workflow to check orchestration and browser callbacks.

```sh
AWS_REGION=ca-central-1 go run ./cmd/operator -function transactions-engine-processor -action verify-session -bank eq-bank
# /private/renew.json contains {"renew":true}
AWS_REGION=ca-central-1 go run ./cmd/operator -function transactions-engine-processor -action verify-session -bank eq-bank -payload /private/renew.json
```

Repeat for each bank. Compare the private normalized snapshots with browser observations before approving baselines. Verify a real ECS login once after the local selectors are stable. See [cutover](cutover.md) for the remaining gates.

For baseline changes, the operator can run the current Go code locally against a real saved snapshot and current YNAB data without redeploying. Set the normal state table/bucket, settings parameter and application secret ARN environment variables, then add `-local-preview` to `baseline.preview`. This option rejects every other action and cannot approve or import.

## Admin authentication

Copy `admin/.env.example` to the ignored `admin/.env.local`, populate the dedicated Transactions Auth0 credentials and verified-email allowlist, and generate a separate local session secret. Use the Auth0 application’s registered localhost callback. Run `npm --prefix admin run dev`; local authentication remains mandatory. Use read-only pages for login verification. Do not invoke imports or resume a financial review hold merely to verify authentication.

The initial implementation passed type checking, lint, production build, Go CDK build/vet and synthesis. Read-only local requests using the real StarRez Auth0 client in process memory confirmed that unauthenticated reads and POSTs redirect to login, deep links are preserved, the Auth0 authorization redirect uses the configured callback, transaction cookies are HttpOnly/SameSite=Lax, invalid callbacks show a safe error, and the browser access-token endpoint is disabled. No authenticated session was fabricated. A successful sign-in with the new Transactions client and the production logout round trip remain release gates.
