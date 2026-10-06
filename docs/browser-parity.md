# CloakBrowser wrapper and Rod

The Go worker uses the same pinned CloakBrowser binary as the previous application. The wrapper's JavaScript does additional work, so selecting the binary alone is insufficient.

| Wrapper behavior | Rod implementation |
|---|---|
| Preserve the actual browser identity | `NoDefaultDevice()` disables Rod's default Chrome 114 / Mac emulation. Removing that override got NBDB past AK000001 and into MFA. |
| Suppress the automation launch flag; configure Linux fingerprint and GPU behavior | Apply the pinned wrapper's Linux launch flags, a stable fingerprint seed, and its timezone flag. |
| Set timezone through the binary | CloakBrowser receives its native timezone flag; Chromium inherits the container timezone. No page timezone override is layered over CloakBrowser. |
| Wait for visible, enabled controls | Shared Rod helpers select visible controls, wait for pointer events and enabled state, and match accessible/visible labels. |
| Enter fields and verify completion | NBDB's username uses key events; field entry checks the resulting value before submitting credentials. No values are logged. |
| Remember Rogers devices | Preserve Rogers' actual SecureLS device and username entries plus their key metadata. Restoring plain strings was incorrect. |

The wrapper's `careful` interaction preset also adds typing and mouse timing. We have not claimed equivalent behavior for that complete framework. Current verification isolates necessary differences before adding custom behavior; Rod remains the sole production browser driver.

The current NBDB UI uses “Receive by email”, `validation-code`, and optional informational welcome screens. Selectors follow that observed UI. AK000001 is explicitly recognized as a browser-verification challenge because NBDB's own application routes that code to its bot-manager screen.
