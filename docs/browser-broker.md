# Shared CloakBrowser capacity

`cmd/browser-broker` is the Go supervisor and reservation API used by Hermes,
Rogers and NBDB. EQ uses stock Chromium and does not reserve capacity. API-only
retrieval and renewal do not reserve a browser.

The broker is packaged by `Dockerfile.broker` on the pinned Hermes CloakBrowser
runtime. Its private CDP listener is port 9222; the vendor Python launcher runs
as a child, forwards on loopback port 9223, and binds Chromium to loopback 9224.
The existing persistent profile, home-egress proxy and Acadia compatibility
initialization remain with that launcher. No Docker socket is mounted.

The control API is HTTPS at https://browser-broker.fredericks.app through the
separate Cloudflare tunnel. Tunnel ingress reaches only port 8080. All control
routes require `Authorization: Bearer ...`, are rate limited, and return no
browser URLs, cookies, license keys or credentials. The bearer lives in the
`BROWSER_BROKER_TOKEN` key of the Secrets Manager secret `Transactions` and the
protected server file `/srv/compose/secrets/hermes-browser-broker.env`.

## Lifecycle

The broker starts without Chromium. CDP discovery launches the persistent
Hermes browser; health checks do not. Thirty seconds after the last request or
WebSocket closes, the broker closes Chromium. Hermes's own 120-second idle
session cleanup disconnects its browser clients.

A bank reservation immediately denies new Hermes connections, closes existing
connections, stops the launcher and confirms the vendor reports zero occupied
seats. It then grants the oldest live request. Banks never preempt each other.
A licensed-seat count that is unavailable or nonzero keeps the broker in
`releasing`; it never guesses that a seat is free.

Reservations are keyed by bank, durable job ID and bank-lease generation.
Queued requests expire after 75 seconds. Workers wait at most 60 seconds and
cancel abandoned requests with an independent cleanup context. A grant lasts ten
minutes, longer than the worker's five-minute hard limit. Explicit release,
expiry and broker restart all require a fresh zero-seat confirmation before
reusing capacity. Ownership and request history are atomically persisted under
`/srv/compose/hermes/browser/broker-state`; terminal history is retained for a day.
A late DELETE is scoped to its old reservation and cannot release a newer grant.

Bank workers reserve immediately before CloakBrowser startup, close their
browser before release, and use the same path during local verification.
Capacity failures defer with 30/60/120/240/300-second backoff, preserve financial
health, and do not consume an authentication attempt. After 15 minutes the
normal durable notification workflow sends a Pushover notice. No YNAB write is
retried by capacity handling.

## API

- `POST /v1/reservations`: JSON `bank`, `jobId`, `generation`; idempotently returns
  a reservation with `id`, `status`, `createdAt`, and `expiresAt`.
- `GET /v1/reservations/{id}`: current reservation status.
- `DELETE /v1/reservations/{id}`: idempotent cancellation/release.
- `GET /v1/status`: sanitized owner, readiness and queue count.
- Private `GET :9222/health`: liveness only, with no browser launch.

The public API has no CDP forwarding, arbitrary commands, token inspection or
browser-content operations. A reservation is trusted capacity coordination,
not a substitute for the bank engine's existing fenced data lease.

## Operations

Production uses the immutable broker image built from committed Transactions
source; the infrastructure repository pins its digest. Deploy the supervisor
and verify real lifecycle behavior before enabling `BROWSER_BROKER_URL` and
secret injection in bank tasks. Local `scripts/verify-bank.py` loads the bearer
privately from the `Transactions` secret.

Use authenticated status and the application job's capacity metadata to inspect
contention. Do not print environment files or raw Docker configuration. To roll
back, pause the affected bank schedules before reverting workers or restoring an
always-running Hermes browser; otherwise the original license contention returns.
Broker crashes retain remote ownership until its deadline and verified seat
release. The vendor may retain a killed browser's seat briefly; keep waiting
rather than granting overlapping capacity.
