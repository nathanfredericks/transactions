# Browser worker audit: Rod to playwright-go, and a simpler broker

Scope: `internal/cloak/**`, `internal/bank/browser.go`, `internal/banks/*/auth.go`, `cmd/browser`, `cmd/browser-broker`, `internal/browserbroker`, both Dockerfiles, `third_party/cloakbrowser-*`, and the browser docs. This audit is read-only and judges everything against `docs/audit/rubric.md`. Rubric items are cited as `R<n>`.

Decisions already made by Nathan, and not reopened here: Rod is replaced by playwright-go; the broker stays but gets simpler, with no extra licence seats; Go tests and the admin site are gone.

## Summary

- **Net effect:** about **2,800 fewer Go lines**. About 2,690 lines are deleted outright (`internal/cloak` 1,003, `internal/cloak/human` 1,338, `cmd/cloak-inspect` 352). About 530 new lines replace them (`internal/chromium` ~130, `internal/human` ~400), and the remaining files shrink further. The broker loses about 330 of its 784 lines. About 5,250 lines of vendored JavaScript under `third_party/cloakbrowser-0.3.25` also go; only its `LICENSE` stays.
- **Why the migration pays off:** CloakBrowser is written for Playwright. Today the Rod port re-implements things Playwright already does: the Chromium switch list, the pipe transport, headed window insets, viewport emulation, isolated-world queries, request routing, and proxy auth. With playwright-go, the launch becomes the vendor's own driver call, and the humanizer becomes a near line-for-line transcription of `human/*.js` on top of `Mouse`, `Keyboard` and `Locator`.
- **Recommendation: run one CloakBrowser build, the licensed Chromium 154, for both Rogers and NBDB.** The licence permits it: the free key gives one concurrent session, and Rogers already reserves that seat through the broker today. Doing this removes the second binary, the `NBDB_CLOAK_PATH` branching and a second launch shape.
- **Broker:** delete its disk persistence, rate limiter, bank allowlist and its `releasing`/`uncertain` state machine. The vendor's seat count already decides when capacity is free; the broker only needs to ask for it before every hand-over.
- **NBDB bug at `nbdb/auth.go:193`:** this is a real bug. Under the new design, the whole class of bug becomes impossible to write.

## Prior notes, checked

| Note | Verdict | Evidence |
|---|---|---|
| Rogers uses CloakBrowser 0.3.25 (Chromium 146); NBDB uses licensed 0.6.0 (Chromium 154) via `NBDB_CLOAK_PATH` | Confirmed | `cmd/browser/main.go:105-111`; `Dockerfile:13-20`. The 146 build is `146.0.7680.177.3` (sha256 `3cf52315…`); the 154 build is `154.0.8037.57.1` (sha256 `8eba2a33…`). |
| `internal/cloak/human` is ~1,338 lines, much of it unused | Confirmed, with one correction | 652+286+155+127+118 = 1,338. Unused: `page.go:289-405` and `page.go:464-652`, plus most of `keyboard.go:172-266`. Lines 406-463 (`Click`, `Fill`, `PressSequentially`) **are** used. |
| `network.go` and proxy auth are unused | Confirmed | `Options.Proxy` is never set. `resolveNetwork` (`network.go:202`) does nothing without a proxy, as in the vendor. `auth.go:30` returns early. Deleting `network.go` drops `maxminddb` and `golang.org/x/net` from `go.mod`; nothing else imports them. |
| `pipe.go` exists for `--remote-debugging-pipe` | Confirmed | Only NBDB uses it (`main.go:183`). Playwright always uses a pipe. |
| `bank.Observer` records CDP Network events | Confirmed | `internal/bank/browser.go:43-95` |
| NBDB humanization bug at `nbdb/auth.go:193` | Confirmed | `p.Context(captureCtx)` replaces the Rod context and drops the `runtimeKey{}` value. `cloak.ForPage` (`cloak/browser.go:392`) then returns false, so the welcome-screen clicks go through plain Rod. Rod's `p.Timeout()` keeps values; only `Context()` drops them. |
| playwright-go latest is ~v0.6401.0 with driver 1.64.1 | Confirmed, with a module-path catch | The Go proxy lists `v0.6401.0` (2026-11-04), and its `run.go` has `playwrightCliVersion = "1.64.1"`. **The module path is `github.com/mxschmitt/playwright-go`** (see its `go.mod`). Import and require it under that path; the `playwright-community` URL only redirects. |
| Runtime base is `mcr.microsoft.com/playwright:v1.59.1-noble` | Confirmed | `Dockerfile:10` |
| The release stage's `rm -rf node` does not shrink the image | Confirmed | `Dockerfile:27` deletes files in a new layer on top of the base image's layers, so the bytes stay. It is also wrong after the migration, because playwright-go needs Node at runtime. |

## Decisions

### 1. Versions

**Pin `github.com/mxschmitt/playwright-go v0.6401.0`.** It uses driver `playwright-core 1.64.1` and Node `24.21.0`.

- CloakBrowser's peer range is `playwright-core >=1.40` for 0.3.25 and `>=1.53` for 0.6.0. The vendor tested 0.6.0 against 1.63 and 0.7.1 (2026-11-07) against 1.64. There is no playwright-go release on 1.63: the series goes 1.62.1 (`v0.6201.1`), then 1.64.1.
- Playwright 1.64.1 bundles Chromium 156. Driving 154 is two majors back, which is the pairing the vendor itself ships in 0.7.1. Driving 146 is ten majors back. That gap is a second reason to move Rogers off 146.
- Since 1.59.1, the driver's Chromium switches changed (`chromiumSwitches.ts`). 1.64.1 disables `BlockOriginHeaderModificationOnRedirect` and `AvoidCorsURLLoaderRestartOnRedirect` for request interception on Chromium 149+ and 155+, which is relevant because both banks route requests. It no longer lists `--enable-automation` in its switch table. Keep `IgnoreDefaultArgs` anyway, because that is the vendor contract.
- Fallback if 1.64.1 regresses: `v0.6201.1` (driver 1.62.1, released 2026-08-17).
- The wrapper used at build time stays `cloakbrowser@0.6.0`, and only for the signed download of `154.0.8037.57.1`, which is validated live. No CloakBrowser JavaScript runs at runtime.

### 2. One CloakBrowser version for both banks

What the licence allows, from the vendor README (0.6.0): "v146 — the older build stays free on GitHub Releases". The free Chromium 154 build requires a GitHub-issued key and allows one concurrent session.

| Option | Licence | Code and image | Risk |
|---|---|---|---|
| **A. Both banks on 154 (recommended)** | Rogers uses the seat. It already reserves through the broker today (`main.go:118`), so contention with Hermes and NBDB does not change. No extra seats. | Delete the 146 install and pin (`Dockerfile:13-16`), the 0.3.25 npm wrapper, `NBDB_CLOAK_PATH` and the `CLOAK_PATH`/`NBDB_CLOAK_PATH` choice (`main.go:105-111`). One launch shape. | Rogers gets a new native fingerprint. Rogers already uses a random seed on every launch (`cloak/browser.go:111`) and remembers the device through `localStorage` `deviceId`, not cookies, so the worst case is one automated email MFA. Verify live. |
| B. Keep 146 for Rogers and 154 for NBDB | Rogers needs **no** reservation (the build is keyless). The broker then serves only NBDB and Hermes. | Two binaries, two launch shapes, two env paths. | Driver 1.64.1 against Chromium 146. The 146 build "ages fast as detection evolves" (vendor). |

**Choose A.** If Rogers' live check rejects 154, fall back to B. B's only code cost is `Registration.Browser` gaining a third value that means "keyless, so no reservation".

### 3. Launch parity

Parity target: cloakbrowser 0.6.0 `launchPersistentContext`, which ships the 154 build. NBDB already uses that shape (`main.go:173-184`: temporary profile, `NoViewport`, `en-CA`, pipe). Under option A, Rogers and EQ use it too. Rod's launcher also gave EQ a temporary non-incognito profile, so this keeps EQ's behaviour.

```go
// internal/chromium: the one launch call for every bank.
ctx, err := pw.Chromium.LaunchPersistentContext(profileDir, playwright.BrowserTypeLaunchPersistentContextOptions{
    ExecutablePath:    exe,              // nil for EQ: Playwright's own Chromium via PLAYWRIGHT_BROWSERS_PATH
    Headless:          playwright.Bool(false),
    Args:              args,             // see table
    IgnoreDefaultArgs: ignore,           // cloak only: --enable-automation, --enable-unsafe-swiftshader
    NoViewport:        playwright.Bool(true),
    Env:               env,              // see section 7
})
```

| Item | Vendor 0.6.0 | Today (Rod) | New |
|---|---|---|---|
| Executable | `ensureBinary()` (154) | `NBDB_CLOAK_PATH` or `CLOAK_PATH` | `CLOAK_PATH` for cloak banks, Playwright's Chromium for EQ |
| Default switches | Playwright's `chromiumSwitches` plus `--no-sandbox` | Frozen 1.59.1 copy in `chromium-args.json` | Playwright supplies them; delete the JSON |
| Stealth args | `--no-sandbox --fingerprint=<seed> --fingerprint-platform=windows` | Same (`cloak/browser.go:109-117`) | Same. Seed from the session for NBDB (`main.go:168-172`), random for Rogers |
| Headed GPU | `--ignore-gpu-blocklist` | Same | Same |
| Timezone/locale | `--fingerprint-timezone=America/Halifax`; `--lang`/`--fingerprint-locale` when set | NBDB sets `en-CA`; Rogers sets none | `en-CA` for both cloak banks (one rule) |
| `--start-maximized` | Added automatically for builds ≥ `148.0.7778.215.4` when no viewport is given (`args.js`) | **Missing** (port based on 0.3.25) | Add it. It is the vendor behaviour for this exact build. Check that the live window and screen sizes stay coherent (section 9). |
| `IgnoreDefaultArgs` | `--enable-automation`, `--enable-unsafe-swiftshader` | Emulated by deleting Rod flags (`cloak/browser.go:186-190`) | Pass it directly |
| Viewport | `viewport: null` when headed | NBDB: `NoViewport`. Rogers: 1920×947 plus manual window bounds (`cloak/browser.go:400-437`) | `NoViewport: true` for every bank |
| Colour scheme | Playwright default (`light`) | Manual `Emulation.setEmulatedMedia` (`cloak/browser.go:443-449`) | Playwright default; delete |
| Widevine hint | `seedWidevineHint`: writes `<profile>/WidevineCdm/latest-component-updated-widevine-cdm` = `{"Path":"<dir>"}` if `WidevineCdm/manifest.json` sits next to the binary | Missing | Add 10 lines **only if** the 154 archive contains `WidevineCdm` (check at image build) |
| License env | `CLOAKBROWSER_LICENSE_KEY` and `CLOAKBROWSER_LICENSE_STATUS_FILE` | Key inherited; no status file | Both (section 7) |

### 4. The humanizer

**Operations the banks actually use** (call sites: `internal/bank/browser.go:240-366`):

| Bank helper | Humanizer operation | Used by |
|---|---|---|
| `bank.Input` | `Page.Fill` (field-switch pause, click with scroll, select all, Backspace, type) | EQ (plain), Rogers, NBDB |
| `bank.Type` | `Page.PressSequentially` (click if not focused, then type) | NBDB username |
| `bank.Click` / `ClickIfPresent` | `Page.ElementLabel` lookup, then `Page.Click` | all |
| `bank.ClickElement` | `Element.Click` | Rogers (remember-me checkbox, email radio) |
| value check | `Element(...).Raw.Property("value")` | all |

Nothing else is used. That covers `DoubleClick`, `Hover`, `Tap`, `Focus`, `Clear`, `Press`, `SetChecked`, `Select*`, `Drag`, `Move`, `MouseClick`, `Keyboard*`, `Frame`, nested `Element(s)`, `Navigate`, the cleanup of held keys on cancel, the 30-key `shortcutHold` table, the `default` preset and `Resolve`'s override validation.

**Is the humanizer needed?**
- **EQ: no.** EQ has run on stock Chromium with plain input since cutover, with no recorded bot rejection (`docs/verification-results.md` §"Authentication and retrieval" and the 12 Oct EQ runs). Do not add it.
- **NBDB: keep it.** `deviceCookies` restores Akamai Bot Manager cookies (`_abck`, `bm_sz`, `ak_bmsc`, `bm_sv`; see `nbdb/auth.go:303`), and Akamai scores input telemetry. The 8 October `AK000001` rejection happened while input had silently bypassed the humanizer (`docs/local-verification.md:99`), and logins passed after it was restored. That is correlation, not proof, but it is the only evidence available, and a rejection costs a paused bank.
- **Rogers: unproven.** Rogers logged in on 6 October with plain Rod input, before the port. Its login strips `recaptchaToken` and rewrites `channel` (`rogers/auth.go:43-62`), which suggests the risk is server-side rather than behavioural. Turning humanization off for Rogers saves no code, because NBDB keeps the module, and saves about 15 s per login. Keep one rule (cloak bank means humanize) unless a later A/B run shows otherwise.

**Design on playwright-go.** `internal/human` is a leaf package that depends only on playwright-go.

```go
type Cursor struct {
    page playwright.Page
    cdp  playwright.CDPSession // shift symbols and viewport size, as the vendor does
    cfg  Config                // careful preset as a Go literal
    x, y float64
    init bool
}
func New(ctx playwright.BrowserContext, p playwright.Page) (*Cursor, error)
func (c *Cursor) Click(ctx context.Context, l playwright.Locator) error
func (c *Cursor) Fill(ctx context.Context, l playwright.Locator, value string) error
func (c *Cursor) PressSequentially(ctx context.Context, l playwright.Locator, value string) error
```

How each piece maps onto Playwright:
- **Mouse:** `page.Mouse().Move(x, y)`, `Down/Up(ClickCount)` and `Wheel(0, dy)` replace raw `Input.dispatchMouseEvent`. Playwright tracks buttons and modifiers itself; this is exactly what `human/mouse.js` and `scroll.js` call.
- **Keyboard:** `Keyboard().Down/Up/InsertText/Press("Control+a")`. Shift symbols keep the vendor's CDP `Input.dispatchKeyEvent` through `CDPSession.Send` (`keyboard.js` `typeShiftSymbol`).
- **Element geometry:** `Locator.BoundingBox()` replaces `DOM.getBoxModel`.
- **Viewport size:** `Page.getLayoutMetrics` over the CDP session. `page.ViewportSize()` is nil under `NoViewport`, and the vendor's `scrollToElement` throws in that case.
- **DOM reads in Playwright's isolated utility world:** this replaces the hand-made isolated world (`page.go:103-182`).
  - Is it an input: `l.And(page.Locator("input,textarea,[contenteditable=true]")).Count()`.
  - Is it focused: `l.And(page.Locator(":focus")).Count()`.
  - Entered value: `l.InputValue()`.
- **Actionability:** before moving the cursor, call `l.Click(playwright.LocatorClickOptions{Trial: playwright.Bool(true)})`. It waits for the element to be visible, stable, enabled and not covered, and dispatches no input. Order: humanized wheel scroll into the target zone, then trial, then `BoundingBox`, then move, then down/up. That order means the trial never needs its own programmatic scroll.
- **Deadlines:** every wait takes `ctx` (R18) instead of reading a stored Rod context. That removes the `runtimeKey` mechanism, and with it the bug class behind `nbdb/auth.go:193`.
- **Deliberate simplification:** page-level and handle-level clicks share one path, with scroll if needed. The port kept the vendor's distinction (`page.go:281-288`), but no bank depends on it (R4).

Size: about 400 lines (mouse ~110, keyboard ~100, scroll ~70, operations ~70, config ~50), down from 1,338 Go lines plus 256 lines of preset JSON.

### 5. Network observation

`page.ExpectResponse` alone is not enough. EQ waits on two responses from one click. NBDB correlates every token response with a later wealth request (`nbdb/auth.go:224-264`). A small recorder is still needed, but Playwright's `Request` and `Response` objects already carry what `Exchange` copies by hand.

```go
type Exchange struct {
    Sequence int
    Request  playwright.Request
    Response playwright.Response // nil when the request failed before a response
    Failed   bool
}
func Record(p playwright.Page) *Recorder // p.OnResponse + p.OnRequestFailed; handlers only append under a mutex
func (r *Recorder) Wait(ctx context.Context, match func(Exchange) bool) (Exchange, error)
func (r *Recorder) Latest(match func(Exchange) bool) (Exchange, bool)
func (r *Recorder) Matches(match func(Exchange) bool) []Exchange
func (r *Recorder) Diagnostics(hostSuffix string) []map[string]any
```

How the current `Observer` maps:
- `Observer.PostData` becomes `Request.PostData()`, and `Observer.Body` becomes `Response.Body()` plus `json.Unmarshal`. `Body()` waits for the response to finish, so the `Finished` predicates in NBDB go away, except for the wealth-session wait, which calls `Response.Finished()`.
- **Use `Request.Headers()`, not `AllHeaders()`.** `AllHeaders()` merges the network stack's extra info (`accept-language`, `sec-fetch-*`, `cookie`). Those would then be saved into `Session.Headers` and replayed from Lambda, which would change API behaviour. `Headers()` matches the CDP `requestWillBeSent` headers that are saved today.
- Event handlers run on playwright-go's dispatcher goroutine, so they must not make RPC calls. `URL()`, `Method()`, `Headers()` and `Status()` are local; call `Body()` only after `Wait` returns.
- Diagnostics lose `corsError` (Playwright does not expose it) but keep the `net::ERR_*` text from `Request.Failure()`.

Routing:
- Rogers: `p.Route("https://selfserve.apis.rogersbank.com/**", …)` with `route.Continue(RouteContinueOptions{Headers, PostData})`. **The glob must be `**`**: Rod's Fetch `*` matched slashes, but Playwright's `*` does not.
- NBDB: `p.Route("https://sdk.privacy-center.org/**", func(r) { r.Abort("blockedbyclient") })`.
- Enabling routing makes Playwright disable the HTTP cache. Check this in the live runs.

The `cloak.NewRequestRouter`/`Start`/`Stop` composition (`cloak/auth.go:101-131`) is deleted.

### 6. Cancellation

playwright-go has no `context.Context`. Use one watchdog per launch, plus timeouts derived from the deadline:

```go
pw, err := playwright.Run(&playwright.RunOptions{
    SkipInstallBrowsers: true, Verbose: false,
    Stdout: io.Discard, Stderr: io.Discard,
    Logger: slog.New(slog.DiscardHandler), // otherwise Run calls log.SetOutput(Stderr): a global side effect
})
stop := context.AfterFunc(ctx, func() { stopDriver(pw) }) // closes stdin; SIGKILLs pw.Pid() after 10 s
bctx.SetDefaultTimeout(float64(time.Until(deadline).Milliseconds()))
```

- When `ctx` expires, the driver exits. Chromium exits on pipe EOF, which also releases the licence seat. Every blocked Playwright call then returns `playwright.ErrTargetClosed`, and the adapter maps it to `Temporary` exactly as `Observer.Wait` does today (`bank/browser.go:134-136`).
- `Close()` order: `stop()`; `bctx.Close()` with a bounded timer; then `pw.Stop()`, bounded the same way. All of it runs under `context.WithoutCancel` plus a timeout (R21). The deferred broker release stays registered first so that it runs last (`main.go:127-134`).
- `cmd/browser/main.go:272` builds its context from `context.Background()`, so an ECS `SIGTERM` kills the process without releasing the reservation, which then expires after 10 minutes. Use `signal.NotifyContext(ctx, syscall.SIGTERM)` (R21, R23).
- `ClickIfPresent` uses `Locator.WaitFor(Timeout)` and checks `errors.Is(err, playwright.ErrTimeout)` (R15), replacing the `context.DeadlineExceeded` check at `bank/browser.go:329`.

### 7. Privacy and licence exit codes

**What leaks.** A Playwright launch error contains `<launching> <exe> <args>` (including `--fingerprint=<seed>`), each Chromium stderr line (`[pid=N][err] …`) and the exit code (`packages/utils/processLauncher.ts`; `browserType.ts`: "Failed to launch the browser process.\nBrowser logs:"). Action timeouts carry call logs with navigated URLs, and OAuth redirects contain authorization codes. Rules:
- Never log `err.Error()` from playwright-go. Log only a fixed kind, using a helper of about 8 lines that maps `ErrTimeout`, `ErrTargetClosed` and `ErrPlaywright` to strings. Replace every `fmt.Sprintf("%T", e)` (`main.go:233,301`; `bank/browser.go:304`). With playwright-go these would all print `*fmt.wrapError` (R28).
- The driver's stderr goes to `io.Discard`. The browser's stdio goes to the driver and only reaches output with `DEBUG=pw:browser`, so `DEBUG` must never be forwarded.
- Chromium's env comes from `LaunchOptions.Env`. Build it in `main` from `os.Environ()` minus `TASK_TOKEN` and `BROWSER_BROKER_TOKEN`, keep `CLOAKBROWSER_LICENSE_KEY` for cloak banks only, and add the status file below (R11, R28).

**Exit codes 76-79.** Today's mapping (`main.go:193-202`) only works on the Rod pipe path (`cloak/browser.go:235-240`). On the WebSocket path the exit code is always -1. Two ways exist to read it:
1. **Preferred: the vendor's status file.** Set `CLOAKBROWSER_LICENSE_STATUS_FILE=<tmp>/licence-status` in the browser env. The Pro/free-key binary writes the integer before exiting, including for a refusal after the handshake (an over-cap seat), which surfaces as `TargetClosedError` on the first navigation (vendor `license.js`). After any launch or auth failure, read the file once and map 76→Temporary, 77→Invalid, 78→Temporary, 79→Invalid, keeping today's operation names. No string matching (R15).
2. If the live check shows that `154.0.8037.57.1` predates the status file, the vendor's own fallback is one anchored match, `/process did exit:\s*exitCode=(\d+)/`, against Playwright's launch message (`license.js` `EXIT_CODE_PATTERNS`). Keep it in one function that never logs the text, and treat it as the single documented exception to R15. The simpler alternative is to drop the mapping and report every launch failure as `Temporary browser-start`, so a bad key is retried and then notified as generic. Pick that if the exception is unwelcome.

### 8. The Docker image

Today's image is the full `mcr.microsoft.com/playwright` image (Chromium, Firefox, WebKit, their system deps, global Node/npm) with two CloakBrowser builds on top, followed by an `rm` that frees nothing. Proposed:

```dockerfile
FROM golang:1.25.0-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY cmd/browser ./cmd/browser
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o /browser ./cmd/browser
# The driver version follows go.mod: playwright-go assembles playwright-core 1.64.1,
# Node 24.21.0 and its coreBundle.js patch exactly as Run() expects.
RUN PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    go run github.com/mxschmitt/playwright-go/cmd/playwright install --no-shell chromium
RUN printf '%s  %s\n' '<node sha>' /opt/playwright-driver/node '<cli sha>' /opt/playwright-driver/package/cli.js | sha256sum -c -

FROM node:24-bookworm-slim@sha256:<digest> AS cloak
# Unchanged official signed download (Dockerfile:19-20 today); the key exists only in this step.
RUN --mount=type=secret,id=cloak_license,required=true npm install --ignore-scripts --save-exact --prefix /tmp/cb cloakbrowser@0.6.0 && \
    CLOAKBROWSER_LICENSE_KEY="$(cat /run/secrets/cloak_license)" CLOAKBROWSER_CACHE_DIR=/opt/cloakbrowser node --input-type=module -e '…ensureBinary…154.0.8037.57.1…' && \
    printf '%s  %s\n' '8eba2a33…' "$(cat /opt/cloak-path)" | sha256sum -c - && rm -f /opt/cloakbrowser/.license_cache

FROM ubuntu:24.04@sha256:<digest> AS release
COPY --from=build /opt/playwright-driver /opt/playwright-driver
COPY --from=build /opt/ms-playwright /opt/ms-playwright
RUN /opt/playwright-driver/node /opt/playwright-driver/package/cli.js install-deps chromium && \
    apt-get install -y --no-install-recommends <font packages copied from the current image> && \
    rm -rf /var/lib/apt/lists/* && useradd -m pwuser
COPY --from=cloak /opt/cloakbrowser /opt/cloakbrowser
COPY --from=build /browser /usr/local/bin/bank-browser
COPY third_party/cloakbrowser-0.3.25/LICENSE /usr/share/licenses/cloakbrowser/LICENSE
COPY third_party/cloakbrowser-0.6.0/LICENSE  /usr/share/licenses/cloakbrowser/LICENSE-0.6.0
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    CLOAK_PATH=/opt/browsers/cloak CLOAK_VERSION=154.0.8037.57.1 TZ=America/Halifax
USER pwuser
ENTRYPOINT ["timeout","--signal=TERM","--kill-after=15s","300s","xvfb-run","-a","/usr/local/bin/bank-browser"]
```

- Node is supplied by playwright-go's own installer, which downloads the Node release it is aligned with (a linux-arm64 prebuilt exists). The `<node sha>` and `<cli sha>` pins match the existing practice for the browser binaries. The npm-based `cloak` stage never ships.
- **Fonts:** take the font package list from `dpkg -l 'fonts-*'` in the current image. Dropping the Firefox/WebKit dependencies must not change the font fingerprint that NBDB's Akamai check sees.
- Delete `CHROMIUM_PATH`, `NBDB_CLOAK_PATH`, `NBDB_BROWSER_VERSION` (rename it `CLOAK_VERSION`), `/opt/browsers/chromium`, the 146 install (`Dockerfile:13-16`) and `Dockerfile:27`.
- Measure the size change at build time (`docker image inspect -f '{{.Size}}'`, before and after). No number is claimed here.

### 9. The broker

**What it protects:** one free-key seat shared by Hermes, NBDB and (under option A) Rogers. Seat ownership is defined by the vendor's count (`VendorSeats`, `server.go:442`). Everything else is a guess about that count.

| Mechanism | `file:line` | Failure it prevents | Verdict |
|---|---|---|---|
| Atomic, fsynced disk state | `server.go:57-115`, `main.go:29-32` | Broker restart forgets a grant, so Hermes starts over a running bank | **Delete.** Every hand-over already requires `seats == 0` (`server.go:48-56,177-185`), so a forgotten grant blocks Hermes anyway. A client polling a lost ID gets 404, which becomes `ErrCapacity`, which defers the job with backoff, and no login attempt is consumed. Residual window: a restart within seconds of a grant, before the vendor counts the seat; that costs one exit-76 retry. |
| `Uncertain` flag, `releasing` owner, `present()` | `server.go:66,141-171,214-229,316-317` | Granting the next bank before the previous seat is released | **Collapse.** Check seats before each grant and drop the extra state. Owner becomes `idle`/`hermes`/`bank`. |
| Reservation history (24 h), 100-reservation cap, `expired`/`released` statuses | `server.go:203-210,273-282` | Unbounded growth | **Delete** with the disk state. In-memory waiters expire after 75 s and grants after 10 min (keep both; R37). |
| Rate limiter (100 req/s) | `server.go:334-346` | Abuse of the control API | **Delete.** It sits behind Cloudflare Tunnel with a 32-character-plus bearer token, and the constant-time compare stays. |
| Bank allowlist `nbdb`/`rogers-bank` | `server.go:257` | Unknown callers | **Delete; do not derive it.** The broker image builds only `internal/browserbroker`. Deriving the list from `internal/banks` would link every adapter and playwright-go into it. The bearer already authenticates callers, and the worker decides from the registry whether to reserve (`r.Browser == "cloak"`). |
| Idempotent POST keyed by (bank, job, generation) | `server.go:267-271` | Duplicate reservations on retry | Keep it, keyed by `jobId+generation`. |
| `Supervisor.Running` | `supervisor.go:30-42` | — | Dead code; delete. |
| Lazy launcher, 30 s idle stop, hijacked-connection tracking, process-group kill | `supervisor.go`, `server.go:357-439` | Hermes holding the seat | Keep. This is the broker's purpose. |
| `FromEnv` reading env inside the package | `client.go:23-31` | — | Build the client in `cmd/browser/main.go` (R11). |

### 10. Do we need the browser recovery paths?

There are two "recovery" mechanisms in scope.

1. **The `RecoverBrowser` Lambda action and the Step Functions `Catch`** (`engine.go:395-418`, `cmd/lambda/main.go:72-73`, `cdk/cdk.go:134`). It covers a browser task that crashes, times out (420 s) or loses its `SendTaskSuccess`. That has happened at least once: a deployment interrupted a worker (`verification-results.md`, 5 Nov). Something must end the job and release the lease, so the purpose stays. The separate action does not need to. `Run` already reads `BROWSER#<generation>`, treats a missing result as `browser-result-missing` (`engine.go:128-136`), checks execution staleness (`engine.go:53-55`) and releases the lease (`engine.go:44-48`). Point the `Catch` at `Run`, set `Attempt = 2` with operation `browser-task-incomplete` in the `!found` branch, and delete `RecoverBrowser` (about 25 lines, a Lambda case and a state machine state). This is outside my files; the engine auditor should confirm it.
2. **NBDB's credential-entry repair** (`nbdb/auth.go:82-98,324-344`). It handles a browser-update dialog that covers the fields; this was observed on cold browsers. Keep the purpose and replace the mechanism with Playwright's own handler:

   ```go
   p.AddLocatorHandler(p.Locator("button,a").Filter(playwright.LocatorFilterOptions{HasText: regexp.MustCompile(`Ignore the update`)}),
       func(l playwright.Locator) { _ = human.Click(ctx, l) }) // runs before every actionability check, including the humanizer's trial click
   ```

   That deletes `enterCredentials` and both `ClickIfPresent("Ignore the update")` calls (about 25 lines). It relies on section 4's trial click to fire the handler. Verify live (section 12).

## Findings

| # | `file:line` | Rubric | Severity | Proposed change | Lines |
|---|---|---|---|---|---|
| 1 | `internal/banks/nbdb/auth.go:193` | 3, 18 | **high** | `p.Context(captureCtx)` drops the humanizer, so welcome screens are clicked by plain Rod. New API: `bank.ClickIfPresent(captureCtx, b, …)`. The humanizer is a field of `bank.Browser`, not a context value. | −1/+1 (class removed) |
| 2 | `internal/cloak/` (all) | 1, 3, 6, 32 | high | Delete. Launch becomes one `LaunchPersistentContext` call in `internal/chromium`. `network.go` (proxy/GeoIP, unused), `auth.go` (proxy/HTTP auth plus router composition, unused), `pipe.go` (Playwright pipes natively), `chromium-args.json` (Playwright supplies it), plus `setup()` and `watchPages` (Playwright does windows, emulation and popups). | −1,039 / +130 |
| 3 | `internal/cloak/human/` (all) | 1, 6 | high | Rebuild as `internal/human` (~400 lines) with only Fill, PressSequentially and Click. The careful preset becomes a Go literal; the `default` preset, `Resolve` validation, the isolated-world code, cleanup on cancel and 20 unused operations go. | −1,594 / +400 |
| 4 | `Dockerfile:10,13-16,27` | 1, 32 | high | Slim Ubuntu base, driver from playwright-go, one 154 build, no `rm` layer (section 8). | ~−8 / +10 |
| 5 | `cmd/browser/main.go:105-111,173-184,136-241` | 1, 4 | high | One binary (option A), one launch path for all banks. Delete `NBDB_CLOAK_PATH`, the `Pipe` choice and the EQ Rod launcher with its close goroutine (`main.go:210-240`). | −105 / +25 |
| 6 | `internal/browserbroker/server.go:57-115,141-229,257,334-346` | 1, 2, 37 | high | Remove disk state, `Uncertain`/`releasing`, history, rate limiter and allowlist; check seats before every grant (section 9). | −200 / +20 |
| 7 | `internal/bank/browser.go:23-199` | 3, 5 | medium | Replace `Observer` with the `Recorder` over `OnResponse`/`OnRequestFailed` (section 5). Delete `PostData` and `Body`'s base64 handling. | −180 / +70 |
| 8 | `internal/bank/browser.go:237-367` | 4, 5 | medium | Make `Input`, `Type`, `Click`, `ClickIfPresent` and `ClickElement` take `(ctx, *Browser, playwright.Locator)`. When the humanizer is absent, use `Locator.Fill`/`Click`. Labels use `Locator(sel).Filter({HasText: re, Visible: true}).First()` in place of `visibleButtonJS`. | −130 / +60 |
| 9 | `cmd/browser/main.go:189-203` | 15, 28 | medium | Read licence denials from `CLOAKBROWSER_LICENSE_STATUS_FILE` (section 7). The current mapping never fires on the non-pipe path. | −10 / +15 |
| 10 | `cmd/browser/main.go:233,301`; `internal/bank/browser.go:304` | 28 | medium | Replace `%T` with a fixed error kind; never log playwright-go error text (launch errors contain the argv, the seed and Chromium stderr). | ±8 |
| 11 | `cmd/browser/main.go:272` | 21, 23 | medium | `signal.NotifyContext(…, SIGTERM)` so that an ECS stop closes the browser and releases the reservation. | +2 |
| 12 | `internal/banks/nbdb/auth.go:41-49` | 2 | medium | `AddCookies` rejects a cookie that has both `URL` and `Domain`/`Path`. Set `URL` only when `Domain` is empty, or Playwright will refuse NBDB's device restore. | ±3 |
| 13 | `internal/banks/nbdb/auth.go:82-98,324-344` | 1, 2 | medium | Replace the dialog repair with `AddLocatorHandler` (section 10). | −25 / +4 |
| 14 | `engine.go:395-418`, `cmd/lambda/main.go:72`, `cdk/cdk.go:134` | 1, 3 | medium | Fold `RecoverBrowser` into `Run` (section 10; engine auditor to confirm). | −30 / +2 |
| 15 | `internal/banks/nbdb/auth.go:73-80` | 2 | low | `Goto` waits for `load` by default, so the separate `WaitLoad` goes. EQ and Rogers gain the same wait; their next locator wait already covered it. | −3 |
| 16 | `cmd/browser/main.go:143-153` | 1, 11 | low | Delete the `NBDB_BROWSER_PLATFORM` override and the stale `"146.0.7680.177.3"` defaults. A missing `CLOAK_VERSION` should fail with `Invalid`. Also delete `scripts/verify-bank.py --stock-browser`. | −15 |
| 17 | `cmd/browser/main.go:142,247` | 10 | low | Optional: apply the stable-seed/version pairing to every cloak bank instead of `r.ID == "nbdb"`, which removes the bank-ID branches. | −6 |
| 18 | `cmd/cloak-inspect/`, `scripts/cloak/*` | 6 | low | Delete. They compare the Rod port with the 0.3.25 reference, which no longer exists. | −398 |
| 19 | `third_party/cloakbrowser-0.3.25/` | 6 | low | Keep `LICENSE` (the Go humanizer is derived from `human/*.js`); delete `dist/`, `README.md`, `SHA256.json`, `package.json`, `PLAYWRIGHT-LICENSE` and `playwright-1.59.1-chromiumSwitches.js`. | −5,246 |
| 20 | `internal/browserbroker/supervisor.go:30-42`; `client.go:23-31` | 6, 11 | low | Delete the dead `Running()`; construct the client in `main`. | −15 |
| 21 | `internal/bank/bank.go:13,96` | 29 | low | `Adapter.Authenticate(ctx, *rod.Browser)` becomes `(ctx, *bank.Browser)`. The Lambda binary links playwright-go code but no driver, the same as with Rod today. | ±2 |

## Per-file migration plan

**Deleted**

| Path | Lines |
|---|---|
| `internal/cloak/browser.go`, `auth.go`, `network.go`, `pipe.go`, `chromium-args.json` | 1,039 |
| `internal/cloak/human/page.go`, `keyboard.go`, `mouse.go`, `config.go`, `scroll.go`, `presets.json` | 1,594 |
| `cmd/cloak-inspect/main.go` | 352 |
| `scripts/cloak/binary.sh`, `inspect-reference.mjs`, `inspection.html` | 46 |
| `third_party/cloakbrowser-0.3.25/*` except `LICENSE` | ~5,246 |
| `go.mod`: `go-rod/rod`, `ysmood/{fetchup,goob,got,gson,leakless}`, `oschwald/maxminddb-golang`, direct `golang.org/x/net` | — |

**New**

| Path | Size | Contents |
|---|---|---|
| `internal/chromium/chromium.go` | ~130 | `Launch(ctx, Options) (*Instance, error)`: `playwright.Run` with quiet options, temporary profile, args (stealth, timezone, locale, `--start-maximized`), optional Widevine hint, `LaunchPersistentContext`, `context.AfterFunc` watchdog, bounded `Close`, `LicenceCode() (int, bool)` from the status file. No `bank` import. |
| `internal/human/human.go` (+ `keyboard.go`) | ~400 | Section 4. |
| `go.mod` | — | `github.com/mxschmitt/playwright-go v0.6401.0` |

**Changed**

| Path | Change | Lines |
|---|---|---|
| `internal/bank/browser.go` | Remove the rod/cloak imports. Add `Browser{Context, Page, human}`, `NewBrowser`, `Recorder` (section 5), `ResponseJSON(resp, out)`, `SafeHeaders(map[string]string)`. `BrowserSession` reads `Context.Cookies()`, where a session cookie has `Expires == -1`. Locator-based `Input`/`Type`/`Click`/`ClickIfPresent`/`ClickLocator`; `LogControls` via `Page.Evaluate`. | 367 → ~210 |
| `internal/bank/bank.go` | `Adapter.Authenticate` signature; drop the rod import. | ±2 |
| `cmd/browser/main.go` | Read all env once; one `chromium.Launch` call; reserve iff `r.Browser == "cloak"`; licence code via the status file; `NotifyContext`; fixed error kinds. | 351 → ~260 |
| `internal/browserbroker/server.go`, `types.go`, `supervisor.go`, `client.go`; `cmd/browser-broker/main.go` | Section 9. | 784 → ~450 |
| `Dockerfile` | Section 8. | 30 → ~35 |
| `scripts/verify-bank.py` | Drop `--stock-browser` and `NBDB_BROWSER_PLATFORM`. Send the broker token to every cloak bank via the registry list (unchanged under option A). | −12 |
| Docs | Rewrite `docs/browser-parity.md` as a one-page parity table (section 3). Delete `docs/cloakbrowser-rod-port-plan.md`, which is historical. Update `docs/local-verification.md:9,78-89,99-105` and `docs/browser-broker.md` (no state file; seat check per grant). | — |

## Changes in each adapter

**EQ** (`internal/banks/eq/auth.go`)
- `bank.NewPage`/`bank.Observe` become `b.Page`/`bank.Record(b.Page)`.
- `p.Navigate` becomes `b.Page.Goto(url)`.
- `p.Timeout(25s).Element(otp)` becomes `b.Page.Locator(otp).WaitFor(…Timeout 25000)`.
- `fail()` reads `b.Page.Locator("body").InnerText(Timeout 2000)`, in place of `text, _ :=` (R12).
- `waitResponse` uses `v.Response.Status()` and `bank.SafeHeaders(v.Response.Headers())`.
- `o.PostData(token)` becomes `token.Request.PostData()`; `BrowserSession(b, …)`.
- No humanizer. The EQ launch moves from Rod's default profile to a Playwright temporary profile with `NoViewport`, so there is still no device emulation (`main.go:216`).

**Rogers** (`internal/banks/rogers/auth.go`)
- `EvalOnNewDocument` becomes `b.Page.AddInitScript(playwright.Script{Content: …})`.
- The router becomes `b.Page.Route("…/**", …)` with `Continue(Headers, PostData)`. Build `Headers` from `route.Request().Headers()`, and the body from `PostDataBuffer()`.
- Remember-me: `loc.IsChecked()`, then `bank.ClickLocator`.
- The email option becomes `Locator("mat-radio-button,label").Filter({HasText: re}).First()`.
- `p.Eval(localStorage…)` becomes `b.Page.Evaluate`, still in the main world as today.
- Delete the `cloak.*Router` calls and the `defer`.

**NBDB** (`internal/banks/nbdb/auth.go`)
- Cookie restore uses `b.Context.AddCookies`, following finding 12.
- `Route(…privacy-center…/**)` with `Abort("blockedbyclient")`.
- `Goto` (drop `WaitLoad`).
- `AddLocatorHandler` for the update dialog, which deletes `enterCredentials` and both `ClickIfPresent` calls.
- `o.Body` becomes `bank.ResponseJSON(login.Response, &state)`.
- Token correlation keeps its logic over `rec.Matches`/`rec.Latest`. `v.Finished` predicates go away; the wealth-session wait keeps `Response.Finished()`.
- Line 193 becomes `bank.ClickIfPresent(captureCtx, b, …)`.

## Live verification, per bank

There are no tests, by rule. The gates are builds, static checks and real runs. Run every command from the repository root. All bank runs use `scripts/verify-bank.py`, which takes a lease, uses the real broker for cloak banks, saves the session and never imports.

**0. Static checks and image**

```sh
gofmt -l . && go vet ./... && go build ./... && golangci-lint run && govulncheck ./... && go mod tidy -diff
docker build --platform linux/arm64 --secret id=cloak_license,src="$HOME/.cloakbrowser/license.key" -t transactions-engine-browser:verify .
docker run --rm --entrypoint /opt/playwright-driver/node transactions-engine-browser:verify /opt/playwright-driver/package/cli.js --version   # expect 1.64.1
docker run --rm --entrypoint /opt/browsers/cloak transactions-engine-browser:verify --version                                               # expect 154.0.8037.57.1
docker run --rm --entrypoint sh transactions-engine-browser:verify -c 'ls "$(dirname "$(readlink -f /opt/browsers/cloak)")"/WidevineCdm 2>/dev/null; dpkg -l "fonts-*" | sort'
docker image inspect -f '{{.Size}}' transactions-engine-browser:verify   # compare with the current image
```

**1. Licence handling** (safe: it fails before any bank contact)

Run the worker once with a deliberately wrong key, through a private env file built the way `verify-bank.py` builds one, but with `CLOAKBROWSER_LICENSE_KEY=invalid`, and `--verify-bank nbdb`. Expect `category=invalid-response operation=browser-license-invalid`, then a broker release. Expect no URL, `--fingerprint`, `pid=` or `[err]` text in the output. If the status file was not written, decide on section 7 option 2.

**2. EQ**

```sh
python3 scripts/verify-bank.py eq-bank             # fresh login + automatic email MFA
python3 scripts/verify-bank.py eq-bank --mode api   # separate process, no browser
python3 scripts/verify-bank.py eq-bank --mode renew
```

Expect `browser authentication validated bank=eq-bank accounts=2`. Then `--mode api` returns both accounts. Watch the account-capture stage log; it should show no `browser-response-timeout`. To check the header choice in section 5, an api-mode retrieval must succeed with the saved `Session.Headers`.

**3. Rogers** (154 under option A)

```sh
curl -sH "Authorization: Bearer $TOKEN" https://browser-broker.fredericks.app/v1/status   # owner idle
python3 scripts/verify-bank.py rogers-bank        # watch status: owner=bank during the run, idle within ~10 s after
python3 scripts/verify-bank.py rogers-bank --mode api
python3 scripts/verify-bank.py rogers-bank --mode renew
```

- Expect the login POST to return 200 or 412, followed by automated email MFA on the first 154 run.
- Expect `channel` to be `201` in the saved headers.
- Expect the second login, a day later, to reuse the device without email MFA.
- The api check returns posted records with the import IDs unchanged.

**4. NBDB**

```sh
python3 scripts/verify-bank.py nbdb               # expect "NBDB remembered browser restored cookies>0", MFA, "welcome screen clicked=true", "wealth authentication correlated"
python3 scripts/verify-bank.py nbdb --mode api
python3 scripts/verify-bank.py nbdb               # second login: same seed/version, device reused
python3 scripts/verify-bank.py nbdb --mode renew  # after natural access-token expiry
```

During one NBDB run, record the geometry through a temporary local build that logs `Page.getLayoutMetrics` and `Browser.getWindowForTarget` bounds through the CDP session. Never use page JavaScript for this. The window must fit inside the screen and the inner size must be smaller than the outer size. If `--start-maximized` under `xvfb-run -a` produces incoherent sizes, remove it, or give `xvfb-run` `-s "-screen 0 1920x1080x24"`. Decide on this evidence.

**5. Cancellation**

Start `verify-bank.py nbdb` and run `docker kill --signal=TERM <container>` once the log says the page loaded. Expect:
- the broker owner returns to `idle` within seconds, not after the 10-minute grant expiry;
- `docker ps` shows no container left;
- the next status call shows `pending=0`.

**6. Broker** (Hermes host, after deploying the new broker image)
- **Restart:** `docker restart` the broker while a bank holds a grant. The worker's next poll gets 404 and the job defers with capacity backoff. Hermes stays refused while the vendor count is 1.
- **Hermes:** lazy start, a 30 s idle stop, and a bank reservation that pre-empts Hermes. Each of these was checked on 5 Nov; repeat each check once.

**7. AWS** (operator, after deploy)

Run one read-only dry-run workflow per bank, as in `verification-results.md` 7-8 Oct. Then run a separate Lambda api invocation per bank.

## Risks and open questions

- The licensed build gets a newer driver (1.64.1, against 1.59.1 today). Its behaviour should improve, because the vendor tests that pairing, but only the live runs settle it.
- Rogers on 154 is the one auth behaviour change in option A. The fallback is option B.
- Whether `154.0.8037.57.1` writes the licence status file is decided by check 1.
- `--start-maximized` and the Xvfb screen size are decided by the geometry check in step 4.
- Playwright disables the HTTP cache while routing is on. Both cloak banks route, so check that their page loads stay within the existing timeouts.
- The `RecoverBrowser` fold-in depends on engine semantics owned by another audit.

## Top changes, ranked by value against effort

1. **Delete `internal/cloak` and `internal/cloak/human`; add `internal/chromium` and `internal/human` on playwright-go** (findings 2, 3). About −2,100 Go lines. It also removes the class of bug behind `nbdb/auth.go:193`. Effort: medium.
2. **Both banks on the one licensed 154 build, with one launch path** (findings 5, 16). Deletes a binary, env branching and an install step, and stops driving Chromium 146 with a 156-era driver. Effort: low, plus one live Rogers check.
3. **Fix `nbdb/auth.go:193`** (finding 1). It is free with change 1. As a one-line stop-gap on Rod, replace `p.Context(captureCtx)` with `p.Timeout(time.Until(deadline))`, where `deadline` comes from `captureCtx.Deadline()`. Rod's `Timeout` derives from the page's existing context, so it keeps the humanizer value. Effort: trivial.
4. **Simplify the broker's state around the vendor seat count** (finding 6). About −330 lines and no durable state to repair by hand (the 5 Nov lost-release incident). Effort: low.
5. **Slim the image and supply the driver from playwright-go** (finding 4). Drops Firefox/WebKit and stops the `rm` layer that frees nothing. Effort: low.
6. **Licence codes from the status file, plus redacted error logging and `NotifyContext`** (findings 9-11). Small, and closes real privacy and cleanup gaps. Effort: low.
7. **Replace `Observer` with `Recorder`** (finding 7). −110 lines. Effort: low; it comes with change 1.
8. **Delete the inspection tooling and the vendored 0.3.25 JavaScript** (findings 18, 19). About −5,600 lines in total. Effort: trivial.
9. **NBDB dialog via `AddLocatorHandler`; fold `RecoverBrowser` into `Run`** (findings 13, 14). Moderate deletions, each needing a live check or another auditor's confirmation. Effort: low to medium.
