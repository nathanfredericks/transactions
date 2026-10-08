# CloakBrowser wrapper and Rod

The runtime translates the installed `cloakbrowser@0.3.25` wrapper into Go-Rod. The frozen executable JavaScript, declarations, source hashes and licenses are in `third_party/cloakbrowser-0.3.25`; Playwright's effective Chromium arguments are frozen alongside it. The native ARM64 Chromium binary remains pinned to build `146.0.7680.177.3`, with executable SHA-256 `3cf5231598c4fd44be1ed10c58cab4a1d59cd18b05a4800c73b946e0bfc4d34f`. Node and Playwright are removed from the final runtime image.

## Source coverage

| Reference | Implementation and observed check |
| --- | --- |
| `config`, `args`, Playwright launch defaults | `internal/cloak/browser.go` and `chromium-args.json`: argument precedence, original launch switches, random seed 10000–99999, native platform, headed GPU, no Rod device emulation. Reference and Go reported the same UA/platform/timezone and geometry. |
| `playwright` | `LaunchContext`, `LaunchBrowser`, `LaunchPersistentContext`, independent contexts, target setup, cookies/storage and caller-owned profile cleanup. Real context isolation, automatic popup setup and persistent-profile reopening passed. Banking uses original headed/careful options and 1920×947 viewport. |
| `human/config` | Exact default/careful presets in `human/presets.json`, typed overrides and cancellable random delay helpers. Both reference presets were transcribed from executable source. |
| `human/mouse` | Bezier/easing/wobble, bursts, overshoot/correction, input/button targets, aim/hold and idle drift in `human/mouse.go`. Actual trusted trajectories and clicks were recorded from both drivers. |
| `human/keyboard` | Physical codes, Shift and named keys, Unicode insertion, pauses, neighboring-key mistakes and backspace correction in `human/keyboard.go`. Real mixed ASCII/symbol/Unicode entry was exact; key-down/up events balanced. |
| `human/scroll` | Cursor positioning, target zones, acceleration/deceleration, wheel chunks, overshoot and settling in `human/scroll.go`. Actual wheel events reached the far-field target. |
| `human/index`, `human/elementhandle` | Isolated worlds, cursor sharing, page/frame/handle composition, nested selectors, focus, checked/select state, type/fill/clear/press, hover/click/double-click/tap, drag and raw inputs in `human/page.go`. Full executable inspection passed, including a child frame, navigation, no-click focus and cancellation releasing held input. Element bounding boxes use the border quad, matching Playwright. |
| `proxy` | `network.go` parses HTTP/HTTPS/SOCKS configuration, credentials and bypass; `auth.go` separates proxy/origin auth, bounds repeated challenges and composes with Rod routing. Repeated real local origin-auth challenges with routing passed. External proxy/SOCKS authentication is not exercised. |
| `geoip` | Optional proxy exit-IP resolution, database caching, locale mapping, explicit overrides and WebRTC handling in `network.go`. As in the original wrapper, no proxy means no GeoIP lookup. Production uses no proxy. Optional external GeoIP paths are implemented but not operationally verified. |
| `download`, `cli` | Immutable image build and `scripts/cloak/binary.sh` use the pinned original installer/CLI for platform selection, cache/version markers, checksums, extraction and explicit updates. Both inspected images had identical native executable hashes. Runtime updates stay disabled. |
| `index`, `types`, declarations | Typed Go options and launch/context/page/element contracts expose the translated operations and raw Rod access. Arbitrary Playwright/Puppeteer option bags and their whole driver APIs are not claimed as drop-in compatible. |
| `puppeteer`, `human-puppeteer/*` | Audited counterpart of the shared wrapper algorithms; the Go runtime has one Rod driver implementation. A Puppeteer compatibility API is not introduced. |

## Manual comparison

The original wrapper and Go executable ran the same non-secret fixture in the ARM64 browser image. Both reported Windows/Win32 Chrome 146, `en-US`, `America/Halifax`, inner size 1920×947, screen 1920×1080, outer size 1920×1032, and `webdriver=false`. Both entered `Ab9@!_é🙂` exactly, clicked, checked and scrolled using trusted native input. One observed pair produced 287/300 mouse moves and 82/78 wheel events; paths and counts vary with the original random distributions. The reference generated and corrected a simulated typing mistake.

The extended Go inspection additionally exercised handles, nested focus, value-based selection, uncheck, double-click, child-frame typing, dragging, navigation, isolated context storage, new popups and cancelled typing. Cancellation left no keys pressed. Selection emits the same untrusted DOM input/change events as the underlying Playwright selector operation; mouse and keyboard events are native. Separate runs confirmed cookies/storage survive closing and reopening a caller-owned persistent profile, and origin authentication continues across challenges with a request router.

The translation intentionally forwards frame operations to the actual child frame rather than reproducing the reference forwarding defect. Cleanup is bounded and releases held keys/buttons when an action is cancelled. Missing element geometry returns an error rather than falling back to an unhumanized click. These are explicit correctness differences.

## Bank integration

All Cloak bank helper typing and clicks, including previously returned element handles, use the wrapper. EQ continues to use bundled Chromium. NBDB waits for exact field contents, repairs credential entry only when its observed browser-update dialog intercepted it, and correlates a completed authorization-code exchange with the bearer actually used on a wealth request. This avoids selecting another client's OAuth response. Expected account identities are validated before publishing authentication.

NBDB's `AK000001` is reported as browser sign-in rejected, with a distinct stage before email verification. The 7 October failure reached that bank restriction screen before requesting a code. The same native binary was already present, so missing wrapper behavior is a plausible contributor rather than a proven sole cause. Local success and the release checks are recorded in [verification results](verification-results.md). No port can guarantee that every future bank security check accepts a cloud browser.
