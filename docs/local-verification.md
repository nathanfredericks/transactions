# Fast local bank verification

Use the production Go adapter and pinned browser image locally. Rebuild only the Go binary between fixes; mount it into the existing image. AWS redeployment is reserved for infrastructure changes and the final cross-environment checks.

The runner uses your AWS CLI identity and the **new engine's** encrypted S3 sessions and DynamoDB leases. It can authenticate, fetch and renew. It has no import or notification path. It does not touch the old importer's state. Before the first cutover, keep new imports disabled. After cutover, these verification commands still have no import path and share the normal bank lease; inspect current jobs before deliberately replacing a live session.

## Setup once

Docker, Go, Python 3, an authenticated AWS CLI and a CloakBrowser key saved privately at `~/.cloakbrowser/license.key` are required. The current NBDB build accepts a free GitHub key; its single-session allowance is shared with Hermes through the Go broker. Rogers reserves through the same broker and keeps its existing keyless binary. Use the same AWS profile as deployment. The runtime image is ARM64; other hosts need Docker's ARM64 support.

```sh
docker build --platform linux/arm64 --secret id=cloak_license,src="$HOME/.cloakbrowser/license.key" -t transactions-engine-browser:verify .
```

## Edit, build, verify

Each command below builds the current worker and runs a separate container. Login uses the same Playwright flow and Chromium/CloakBrowser choice as ECS. API and renewal checks do not launch a browser. Credentials are passed through a temporary private environment file and removed when the command ends. Never enable shell tracing or print session objects.

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
```

Keep the agreed restriction against mocks, new test files and test-suite runs. Real local bank checks, static checks and builds are the development loop.

## Release checks

Local success does not prove Lambda can reuse a session. After one consolidated deployment, invoke the processor from a separate Lambda execution to verify native fetch and renewal, then submit a normal dry-run workflow to check orchestration and browser callbacks.

```sh
AWS_REGION=ca-central-1 go run ./cmd/operator -function transactions-engine-processor -action verify-session -bank eq-bank
# /private/renew.json contains {"renew":true}
AWS_REGION=ca-central-1 go run ./cmd/operator -function transactions-engine-processor -action verify-session -bank eq-bank -payload /private/renew.json
```

Repeat for each bank. Compare the private normalized snapshots with browser observations. Verify a real ECS login once after the local selectors are stable. See [operations](cutover.md) for the operator commands.

## Browser stack

The worker drives the browser with playwright-go (driver 1.64.1 on Node 24.21.0). EQ runs the image's stock Chromium with plain Playwright input. Rogers and NBDB run CloakBrowser `154.0.8037.57.1` from the pinned runtime image, with its stealth arguments, a temporary profile, the window's own size and `en-CA`, and `internal/human` input: the vendor's careful preset, sent as the same CDP mouse and key events the earlier Rod port sent. Control labels are read as the page renders them (aria-label, else rendered text), in Playwright's isolated world. Playwright's error text is never logged, since it can carry URLs, arguments and browser output.

On 9 November 2026 each bank passed a real local login and a separate native API check on this stack: EQ (email code, 2 accounts), Rogers (first run on 154, email code, 1 account) and NBDB (remembered device restored, email code, wealth session correlated, 2 accounts).

## NBDB browser identity

NBDB sessions record the CloakBrowser build version, platform, fingerprint seed and the browser's scoped, unexpired device/access cookies separately from cookies updated by native HTTP calls. Fresh login restores that pair; legacy sessions without a known seed establish a fresh device once. Deploy both Lambda functions with the browser worker so subsequent session saves preserve the new field. Resume invalidates API authentication while retaining the remembered device for reauthentication.

OAuth capture matches a completed authorization-code exchange to the bearer on a wealth GET, excluding the session-creation POST's separate OAuth client. Before account validation and saving cookies, it waits for the successful wealth-session POST to finish. A successful API response and both configured account identities remain the acceptance gate.

MFA email polling queries recent timestamps, checks exact sender/subject metadata, and fetches bodies only for matching messages. This avoids relying on a text-search index having updated immediately after delivery.

The 2026-10-08 incident was an authentication POST returning HTTP 403 with `AK000001`, before MFA, while input had silently bypassed the humanizer. With playwright-go the humanizer is a field of the sign-in's browser, not a context value, so that cannot recur. A fixed browser identity does not guarantee bank acceptance; an explicit browser-access rejection still pauses authentication for investigation, rather than triggering repeated login attempts.

Rogers and NBDB use CloakBrowser `154.0.8037.57.1`, which the pinned runtime image carries; the vendor's download no longer serves that build, so changing it is a deliberate runtime rebuild and live verification. Headed runs use a normal browser context with the actual display, without viewport emulation, over Playwright's pipe. The temporary on-disk profile is removed after browser shutdown; only the scoped remembered-device cookies and identity are persisted. An upgrade establishes a fresh device instead of reusing cookies with changed native fingerprint profiles.

The Docker build receives the key through a BuildKit secret. CDK defaults to `~/.cloakbrowser/license.key`; set `CLOAKBROWSER_LICENSE_FILE` to another private file when needed. The running task's copy is the `CLOAKBROWSER_LICENSE_KEY` key of the Secrets Manager secret `Transactions`; ECS injects it at task startup using the execution role. The key does not enter Git, image layers, CloudFormation values, workflow inputs or command arguments. Local NBDB verification reads the same private key into its temporary environment file.

The upstream diagnostic confirmed the installed ARM64 build launches with a valid free license and no sessions left open. Baseline system fonts are installed; the image lacks the proprietary Windows/Office font sets recommended upstream. No font-metrics override is enabled without those fonts. IP reputation can still affect bank acceptance, so real ECS authentication is a separate release gate.

The repaired flow passed two consecutive real local NBDB logins on the licensed build, a separate native API check, and native renewal after natural access-token expiry with rotation and extended expiry. Rogers also passed a real login using the corrected shared page helper and retained its remembered device. The scoped production release completed successfully; job `nbdb-cloak-cloud-auth-20261009` completed a fresh ECS login, email MFA, wealth-session establishment and both-account validation without imports or retries.

The independent processor API check and dry-run workflow `nbdb-cloak-cloud-preview-20261009` also completed. Retrying `scheduled-nbdb-29858160` finished the original retrieval after two bounded temporary retries, committed one balance adjustment, and cleared the incident through the normal recovery path.

During the successful scheduled retry, the processor renewed the AWS-created session with a rotated access token and extended expiry. The recovery notification was delivered and all three banks reported clear health state. The browser task stopped with exit code 0 and no running tasks remained.

A final independent processor invocation after the AWS-renewed token’s natural expiry again rotated the access token, extended expiry and retrieved both configured accounts. Go build/vet, CDK build/vet and synthesis passed; no test suites were run.
