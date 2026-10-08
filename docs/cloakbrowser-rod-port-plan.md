# Complete CloakBrowser wrapper port to Rod

Requested 7 October 2026. The implementation and manual comparison are complete. Release evidence and remaining limits are recorded in [browser parity](browser-parity.md) and [verification results](verification-results.md). The ordered work below preserves the original plan; observations made before implementation are historical.

## Findings and evidence

The failed 17:00 Atlantic NBDB job, `scheduled-nbdb-29856720`, did start its browser fallback. Its authentication POST returned HTTP 403 with `AK000001`. The adapter returned `challenge-required` at `login-response/browser-verification`, before requesting email MFA. Health is blocked and the application job requires review, even though the surrounding Step Functions execution reports success.

National Bank's current public application maps this error to its `botmanager` route. That screen displays access restrictions, troubleshooting instructions and contact details; it does not expose an email-code verification action. The notification's generic “Verification step needed” wording does not distinguish this rejection from ordinary email MFA.

The current worker uses the same pinned CloakBrowser binary but implements only part of its JavaScript wrapper. On 7 October, an unchanged local Rod worker completed NBDB login, requested and entered the email code, and validated both expected accounts. A separate production Lambda invocation then retrieved those accounts in 628 ms using the resulting saved authentication. These checks had no import path. They establish that the credentials and email flow work, but do not isolate the cause of the AWS browser rejection. The production authentication hold was retained throughout development pending the controlled AWS release check.

## Reference to transcribe

Use the installed `cloakbrowser@0.3.25` package in the archived bank-import checkout as the authoritative source, together with its `playwright-core@1.59.1` launch/context behavior. Do not port from the latest upstream branch or copy an approximate preset from documentation.

The original bank configuration is `launchContext({ headless: false, humanize: true, humanPreset: "careful", geoip: true, timezone: "America/Halifax" })`. It does not configure a proxy. In this wrapper version, GeoIP is a no-op without a proxy; adding an IP lookup to the production path would introduce behavior the original did not use.

Keep the patched Chromium binary. Its native fingerprint changes are already inside the browser and are not JavaScript that needs rewriting in Go. Record the package source hashes, binary version, architecture, image digest and upstream license/attribution before implementation. Verify the actual binaries in the old reference image and current worker rather than inferring equality from the npm version alone.

## Source coverage ledger

Every reference module must have a recorded destination, implementation status and observed validation. Nothing is marked complete solely because its constants were copied.

| Reference module | Go destination / responsibility |
| --- | --- |
| `config.js`, `args.js` | Platform/version mapping, cache/override configuration, all defaults, argument de-duplication and precedence. |
| `playwright.js` | Browser, context and persistent-context launch; option precedence; context setup; close ownership; integration with human behavior. |
| Playwright Chromium launch/context sources | Effective launch switches, context creation, viewport, frame/input semantics and browser cleanup that the wrapper delegates to Playwright. |
| `human/config.js` | Exact default/careful presets, every parameter, overrides, random ranges and cancellable delay helpers. |
| `human/mouse.js` | Easing, cubic Bezier control points, wobble, bursts, overshoot/correction, input/button targeting, aim/hold delays and idle drift. |
| `human/keyboard.js` | ASCII/Unicode behavior, physical key mappings, Shift events, randomized timing, pauses, nearby-key mistakes and correction. |
| `human/scroll.js` | Target zones, cursor positioning, smooth wheel chunks, acceleration/deceleration, visibility checks, overshoot and settling. |
| `human/index.js` | Isolated-world lifecycle, focus/type reads, cursor state, browser/context/page integration, all page/frame operations and drag behavior. |
| `human/elementhandle.js` | Handle-level operations, nested selectors and waits, focus semantics, checked state, fallback behavior and handle lifecycle. |
| `proxy.js` | HTTP/HTTPS/SOCKS parsing, credentials, bypass rules and browser proxy configuration. |
| `geoip.js` | Conditional activation, IP resolution, country/locale mapping, database cache/update, explicit-option precedence and WebRTC argument handling. |
| `download.js` | Build/operator binary management: override, platform selection, cache/version markers, primary/fallback download, checksums, extraction and update policy. Production keeps build-time installation and disabled runtime updates. |
| `index.js`, `types.js`, declaration files | Public Go options/contracts and module exports; explicit mapping of all options and defaults. |
| `cli.js` | Equivalent operator/build commands for binary info, install, cache and update; never enable automatic production updates as a side effect. |
| `puppeteer.js`, `human-puppeteer/*` | Audit against the Playwright implementations. Record duplicate behavior once and any unique behavior explicitly; Rod requires one driver layer, not two copies of the same algorithms. |

## Implementation structure

Introduce `internal/cloak` for browser launch and configuration and `internal/cloak/human` for the translated interaction engine. Keep bank adapters responsible for their bank protocol and selectors. `cmd/browser` selects the original Cloak options for banks registered with the cloak browser; bundled Chromium remains the EQ choice.

Expose typed browser/context/page/frame/element wrappers. All production interactions go through these wrappers, including elements returned by selector lookup and waits. A shared helper that humanizes only top-level page calls would leave existing `el.Click`, focus and nested-element operations outside the port.

Each operation must preserve the reference's observable semantics, including differences between page-level and handle-level operations. Store original raw Rod/CDP primitives separately so wrapper calls cannot recurse or apply human behavior twice. Share page cursor state across frame operations; define coordinate conversion for child frames and invalidate stale contexts on navigation.

Translate the executable implementation rather than comments alone. For example, the reference ElementHandle input check describes `DOM.describeNode` but actually falls back to element evaluation. Its frame forwarding also deserves inspection against actual child-frame behavior. Document any necessary correction with its source location and observed effect; do not silently describe an improved implementation as an exact transcription.

## Ordered work

1. **Freeze and map the reference.** Capture source hashes and licenses, inspect every module/export, resolve the original effective launch options and build the coverage ledger. Record which routines are active for the original bank configuration and which are optional capabilities. Inspect Playwright delegation and Rod defaults explicitly.
2. **Port launch and context behavior.** Translate argument precedence and suppressed flags. Match headed mode, native UA/platform/GPU identity, viewport and screen geometry, browser-context isolation, timezone/locale through native flags, and process/context ownership. The wrapper chooses a random seed from 10000–99999; current Go hardcodes 54321. The original viewport is 1920×947; current Go sets a 1920×1080 window without matching its context viewport. Reproduce the original defaults first and document any subsequent deliberate change separately. Compare effective Rod and Playwright switches, including feature lists, rather than retaining unexamined Rod defaults.
3. **Port the complete configuration and motion primitives.** Copy both presets and all ranges exactly. Translate random sampling, Bezier/easing math, wobble, bursts, initial cursor, overshoot, click target selection, aim/hold and idle movement. Every wait observes the authentication context; releasing pressed keys/buttons during cancellation uses a bounded cleanup context.
4. **Port keyboard and scrolling.** Implement real CDP key-down/up and physical symbol codes, Unicode insertion, randomized pauses, mistake/correction sequences and original clear/fill/append distinctions. Verify field contents before submission. Port wheel chunking, target-zone rules, acceleration/deceleration and final settling. Do not simplify either engine to fixed sleeps.
5. **Port operation composition and DOM infrastructure.** Implement isolated execution worlds and navigation recreation; focus checks; page/frame/handle dispatch; nested-selector wrapping; `click`, `dblclick`, `hover`, `type`, `fill`, `clear`, `press`, `pressSequentially`, `check`, `uncheck`, `setChecked`, `selectOption`, `tap`, `focus`, dragging, raw mouse and keyboard wrappers. Preserve the original difference between focus and click. Cover new pages, popups and contexts rather than initializing only the first page.
6. **Port optional capabilities and management.** Implement proxy parsing/authentication/bypass and GeoIP/WebRTC with the original activation rules, override precedence and bounded fallback behavior. Map persistent contexts, user agent, color scheme, viewport and additional context/launch options. Provide build/operator equivalents for binary configuration, cache, integrity checks and deliberate updates; retain immutable production images and runtime update disablement.
7. **Integrate all cloak bank adapters.** Replace the partial bank typing/click paths with the complete wrapper. Check NBDB and Rogers selectors, banners, welcome screens, remembered-device handling, MFA and authenticated-request capture. Keep session fencing, account validation and email reader semantics. Report `AK000001` distinctly as browser access rejected rather than implying an email verification code was missed. Do not turn an unresolved rejection into an unbounded login loop.
8. **Validate locally, then release once.** Run Go formatting, builds and vet. Compare the reference wrapper and port in the same ARM64 browser image against a local event/geometry inspection page using non-secret input. Inspect trusted input events, physical codes/modifiers, mouse trajectories, wheel events, focus, frame coordinates, viewport/UA/timezone, preset ranges and cleanup. Use executable/manual inspection tools rather than mocks, new test files or test-suite runs, consistent with this repository's verification policy. Record evidence in the coverage ledger.
9. **Verify the actual banking path.** Run sequential real NBDB and Rogers logins under the existing leases, verify automatic email MFA, then use separate processes/Lambda invocations for account fetch and renewal. Compare normalized results with the approved account identities. Rebuild the immutable worker image and inspect a scoped deployment that preserves unrelated in-progress changes. Use a real read-only/session-only workflow to exercise AWS login and callback recovery. Resume NBDB's authentication hold once the revised cloud path is ready for this controlled check, not repeatedly during development.
10. **Observe natural expiry and scheduled recovery.** Confirm an expired session triggers one complete automatic login/MFA, publishes validated authentication and permits a later API retrieval. Check the durable application outcome, bank health and logs, not only Step Functions success. Restore normal healthy status only after this evidence. Preserve all financial review holds; authentication verification cannot import or retry a balance adjustment.

## Completion criteria

- Every reference module/export is mapped and its implementation or intentionally build-time equivalent is recorded.
- Original launch/context settings and every human interaction primitive are implemented with their original distributions and operation composition; no fixed-delay substitute is labeled equivalent.
- Comparison tools show actual CDP/input/geometry behavior, including child frames and cancellation cleanup.
- Real NBDB and Rogers authentication, required email MFA or remembered-device reuse, native account validation, separate Lambda fetch and renewal succeed.
- At least one revised NBDB AWS fresh login completes through the ordinary callback workflow, followed by separate API reuse and observed expiry recovery.
- Production authentication health reflects recovery; financial review state and uncertain-write markers remain governed by their existing separate process.
- Remaining unsupported bank challenges are described accurately. A complete wrapper port improves parity; it cannot guarantee that a bank will accept every future cloud session or prove that this particular rejection was caused by one of the identified differences.
