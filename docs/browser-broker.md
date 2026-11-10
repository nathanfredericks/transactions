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
routes require `Authorization: Bearer ...` and return no
browser URLs, cookies, license keys or credentials. The bearer lives in the
`BROWSER_BROKER_TOKEN` key of the Secrets Manager secret `Transactions` and the
protected server file `/srv/compose/secrets/hermes-browser-broker.env`.

## Lifecycle

The broker starts without Chromium. CDP discovery launches the persistent
Hermes browser; health checks do not. Thirty seconds after the last request or
WebSocket closes, the broker closes Chromium. Hermes's own 120-second idle
session cleanup disconnects its browser clients.

A bank reservation immediately denies new Hermes connections, closes existing
connections and stops the launcher. The oldest waiting bank is granted the
seat only once the vendor counts zero occupied seats; Hermes's browser starts
only on the same check. An unavailable or nonzero count means waiting, never a
guess that the seat is free. Banks never preempt each other.

Reservations are keyed by bank, durable job ID and bank-lease generation, so a
retried request gets the reservation it already has. Queued requests expire
after 75 seconds. Workers wait at most 60 seconds and cancel abandoned requests
with an independent cleanup context. A grant lasts ten minutes, longer than the
worker's five-minute hard limit. Nothing is stored: after a broker restart a
worker's poll gets 404 and its job waits for capacity, while the vendor's count
keeps Hermes off a seat a bank still uses. A late DELETE only releases its own
reservation. `BROWSER_BROKER_STATE` and the `broker-state` volume are no longer
used.

Bank workers reserve immediately before CloakBrowser startup, close their
browser before release, and use the same path during local verification.
A busy seat defers the job 120 seconds at a time, five times, without
consuming its authentication attempt; then the job fails as temporary and the
next schedule retries. No YNAB write is
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
