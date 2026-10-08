# Frozen wrapper reference

Copied from the installed `cloakbrowser@0.3.25` and `playwright-core@1.59.1`
packages in the archived bank-import checkout on 7 October 2026. The JavaScript
and declarations are the authoritative translation source; source maps are omitted.
`SHA256.json` records source-file hashes. CloakBrowser code is MIT; its original
license and the Apache-2.0 Playwright license are included and copied into the
release image.

The Go translation lives in `internal/cloak` and `internal/cloak/human`. Patched
Chromium stays native. The pinned npm installer remains a build/operator component
for platform mapping, archive checksums/extraction, version markers and cache
management; Node/Playwright are removed from the final runtime image.

Both inspected ARM64 images contain Chromium `146.0.7680.177.3`:
`chrome` SHA-256 `3cf5231598c4fd44be1ed10c58cab4a1d59cd18b05a4800c73b946e0bfc4d34f`.
The executable reports `Chromium 146.0.7680.177`. Wrapper auto-update is disabled
in the images. A source/image update must be explicit and re-verified.
