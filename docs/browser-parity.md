# CloakBrowser wrapper and Rod

The Go worker uses the same pinned CloakBrowser binary as the previous application. The wrapper's JavaScript does additional work, so selecting the binary alone is insufficient.

| Wrapper behavior | Rod implementation |
|---|---|
| Preserve the actual browser identity | `NoDefaultDevice()` disables Rod's default Chrome 114 / Mac emulation. Removing that override got NBDB past AK000001 and into MFA. |
| Suppress the automation launch flag; configure Linux fingerprint and GPU behavior | Apply the pinned wrapper's Linux launch flags, a stable fingerprint seed, and its timezone flag. |
| Set timezone through the binary | CloakBrowser receives its native timezone flag; Chromium inherits the container timezone. No page timezone override is layered over CloakBrowser. |
| Wait for visible, enabled controls | Shared Rod helpers select visible controls, wait for pointer events and enabled state, and match accessible/visible labels. |
| Enter fields and verify completion | NBDB's username, password and MFA code use Rod key-down/key-up events, explicit Shift events and context-bounded timing within the wrapper's careful ranges. Non-ASCII input uses native text insertion; field entry checks the resulting value before submitting credentials. No values are logged. |
| Remember Rogers devices | Preserve Rogers' actual SecureLS device and username entries plus their key metadata. Restoring plain strings was incorrect. |

The keyboard helper uses fixed durations within the wrapper's ranges rather than implementing its randomized pauses, simulated mistakes and mouse-path framework. We have not claimed equivalent behavior for that complete framework. Current verification isolates necessary differences before adding custom behavior; Rod remains the sole production browser driver.

The current NBDB UI uses “Receive by email”, `validation-code`, and optional informational welcome screens. Selectors follow that observed UI. Authentication can be captured from any authenticated request to the observed wealth API; the worker then fetches and validates the expected accounts directly. It does not depend on the web application issuing one particular portfolio-summary request. AK000001 is explicitly recognized as a browser-verification challenge because NBDB's own application routes that code to its bot-manager screen.

Local success does not establish AWS compatibility. The latest ECS run received AK000001 and remains blocked. The old wrapper also recorded intermittent HTTP 403 responses, so the failure cannot yet be attributed solely to Rod.
