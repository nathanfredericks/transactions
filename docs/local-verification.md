# Fast local bank verification

Use the production Go adapter and pinned browser image locally. Rebuild only the Go binary between fixes; mount it into the existing image. AWS redeployment is reserved for infrastructure changes and the final cross-environment checks.

The runner uses your AWS CLI identity and the **new engine's** encrypted S3 sessions and DynamoDB leases. It can authenticate, fetch and renew. It has no import or notification path. It does not touch the old importer's state. Before the first cutover, keep new imports disabled. After cutover, these verification commands still have no import path and share the normal bank lease; inspect current jobs before deliberately replacing a live session.

## Setup once

Docker, Go, Python 3, an authenticated AWS CLI and a CloakBrowser key saved privately at `~/.cloakbrowser/license.key` are required. The current NBDB build accepts a free GitHub key; its single-session allowance is isolated to NBDB. Rogers keeps its existing keyless binary. Use the same AWS profile as deployment. The runtime image is ARM64; other hosts need Docker's ARM64 support.

```sh
docker build --platform linux/arm64 --secret id=cloak_license,src="$HOME/.cloakbrowser/license.key" -t transactions-engine-browser:verify .
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

## Wrapper inspection

`cmd/cloak-inspect` uses only a local non-secret event/geometry fixture; it never loads AWS configuration or bank credentials. Build it for the ARM64 image, then mount the executable and fixture into the existing browser image:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/cloak-inspect ./cmd/cloak-inspect
docker run --rm --init --mount "type=bind,src=$PWD,dst=/workspace" --workdir /workspace --entrypoint xvfb-run transactions-engine-browser:cloak-rod -a /workspace/dist/cloak-inspect -full
# Repeat with -profiles or -auth for persistent storage and HTTP auth/routing.
docker build --platform linux/arm64 --target browsers --secret id=cloak_license,src="$HOME/.cloakbrowser/license.key" -t transactions-cloak-reference:inspect .
docker run --rm --init --mount "type=bind,src=$PWD,dst=/workspace" --workdir /workspace --entrypoint xvfb-run transactions-cloak-reference:inspect -a node scripts/cloak/inspect-reference.mjs
scripts/cloak/binary.sh info
```

The reference script imports the installed pinned wrapper from the browsers stage. `binary.sh` also supports deliberate `install`, `update` and `clear-cache`, with an optional operator cache directory. These commands are never invoked by production; do not update the pinned validation image while comparing behavior. Optional external proxy/GeoIP paths need their own real configured checks before operational use.

## NBDB browser identity

NBDB sessions record the CloakBrowser build version, platform, fingerprint seed and the browser's scoped, unexpired device/access cookies separately from cookies updated by native HTTP calls. Fresh login restores that pair; legacy sessions without a known seed establish a fresh device once. Deploy both Lambda functions with the browser worker so subsequent session saves preserve the new field. Resume invalidates API authentication while retaining the remembered device for reauthentication.

OAuth capture matches a completed authorization-code exchange to the bearer on a wealth GET, excluding the session-creation POST's separate OAuth client. Before account validation and saving cookies, it waits for the successful wealth-session POST to finish. A successful API response and both configured account identities remain the acceptance gate.

MFA email polling queries recent timestamps, checks exact sender/subject metadata, and fetches bodies only for matching messages. This avoids relying on a text-search index having updated immediately after delivery.

The 2026-10-08 incident was an authentication POST returning HTTP 403 with `AK000001`, before MFA. Bank page creation replaced the Rod context and discarded the CloakBrowser runtime, silently routing input through ordinary Rod actions despite the startup log. Page creation now retains the runtime while applying the caller’s deadline. Isolated element handles also remain in their isolated world instead of being resolved into the main world. A fixed browser identity does not guarantee bank acceptance; an explicit browser-access rejection still pauses authentication for investigation, rather than triggering repeated login attempts.

NBDB uses Chromium `154.0.8037.57.1`, downloaded by the pinned `0.6.0` wrapper with its official signature/checksum verification and an additional executable hash pin. The Rod interaction port remains based on `0.3.25`. Headed NBDB runs use a normal browser context with the actual display, without CDP viewport emulation, and Chromium’s CDP pipe transport. The temporary on-disk profile is removed after browser shutdown; only the scoped remembered-device cookies and identity are persisted. An upgrade establishes a fresh device instead of reusing cookies with changed native fingerprint profiles.

The Docker build receives the key through a BuildKit secret. CDK defaults to `~/.cloakbrowser/license.key`; set `CLOAKBROWSER_LICENSE_FILE` to another private file when needed. Populate the retained Secrets Manager secret `transactions-engine/cloakbrowser-license` privately after its initial creation. ECS injects its value at task startup using the execution role. The key does not enter Git, image layers, CloudFormation values, workflow inputs or command arguments. Local NBDB verification reads the same private key into its temporary environment file.

The upstream diagnostic confirmed the installed ARM64 build launches with a valid free license and no sessions left open. Baseline system fonts are installed; the image lacks the proprietary Windows/Office font sets recommended upstream. No font-metrics override is enabled without those fonts. IP reputation can still affect bank acceptance, so real ECS authentication is a separate release gate.

The repaired flow passed two consecutive real local NBDB logins on the licensed build, a separate native API check, and native renewal after natural access-token expiry with rotation and extended expiry. Rogers also passed a real login using the corrected shared page helper and retained its remembered device. The scoped production release completed successfully; job `nbdb-cloak-cloud-auth-20261009` completed a fresh ECS login, email MFA, wealth-session establishment and both-account validation without imports or retries.

The independent processor API check and dry-run workflow `nbdb-cloak-cloud-preview-20261009` also completed. Retrying `scheduled-nbdb-29858160` finished the original retrieval after two bounded temporary retries, committed one balance adjustment, and cleared the incident through the normal recovery path.

During the successful scheduled retry, the processor renewed the AWS-created session with a rotated access token and extended expiry. The recovery notification was delivered and all three banks reported clear health state. The browser task stopped with exit code 0 and no running tasks remained.

A final independent processor invocation after the AWS-renewed token’s natural expiry again rotated the access token, extended expiry and retrieved both configured accounts. Go build/vet, CDK build/vet and synthesis passed; no test suites were run.
