# Verification record — 6 October 2026

The new importer and schedules remain disabled. These are real account checks, not mocks. No YNAB writes were made.

| Check | Observed result |
|---|---|
| EQ local Rod + pinned Chromium login | Passed; MFA completed, both expected accounts validated, encrypted session saved |
| EQ separate-process native fetch | Passed; both expected accounts, seven records; 4.639 seconds including state access |
| EQ separate-process renewal + fetch | Passed; seven records; 5.212 seconds |
| EQ expiry metadata | Renewal extended access-token expiry; the reported 30-minute maximum session expiry did not move |
| Rogers local Rod + pinned CloakBrowser login | Passed; email MFA completed, expected account validated, encrypted session saved |
| Rogers separate-process native fetch | Passed; 29 posted records over the configured ten-day window; 2.767 seconds |
| Rogers early renewal + fetch | Passed; 29 records; 3.028 seconds. Bank returned the existing expiry, so actual later token rotation remains to verify |
| Rogers renewal-window check | Passed with old maintenance temporarily paused: access token rotated, expiry extended from 18:46:37 to 18:54:44 Atlantic, refresh token unchanged; subsequent native retrieval returned 29 records in 2.839 seconds |
| Rogers Lambda-origin reuse | Passed; reused the locally renewed session and retrieved 29 records in 2.416 seconds |
| Rogers session lifetime | Initial results were confounded by the old maintenance worker performing competing logins. Controlled renewal works; no maximum bank lifetime has been established |
| NBDB local Rod + CloakBrowser login | Passed; native browser identity preserved, MFA completed, both expected accounts validated and session saved |
| NBDB separate-process native fetch and renewal | Passed; fetch 0.967 seconds, renewal plus fetch 1.917 seconds; access token rotated and expiry extended |
| NBDB Lambda-origin reuse and renewal | Passed in separate invocations; fetch 0.504 seconds, renewal plus fetch 1.308 seconds; both account identities validated, expiry extended again |
| Go build/vet, CDK build/vet, admin typecheck/lint/build | Passed; rerun relevant checks after further edits |
| Pushed implementation CI | Passed on commits 475aab4 and ab3947a |

The timings measure local retrieval operations, not end-to-end notification latency. The under-15-second notification target remains unverified. No production cutover is justified by these checks alone.

Local checks fixed button readiness, Rogers' MFA selector, CDP request-body capture and preflight filtering. The runner makes subsequent fixes possible without AWS deployment. A malformed fresh NBDB exclusion setting was also corrected.

Both old maintenance policies were restored after the isolated checks. One earlier isolated NBDB attempt stalled after a successful token response; a subsequent attempt completed. This intermittent post-MFA behavior still needs observation before release. The successful run did not require either optional welcome screen.

Still required: Lambda-origin reuse and renewal for EQ, Lambda-origin token rotation for Rogers, real ECS login/callback checks, browser/native data comparison, baseline and rules review, supported failure/recovery cases, operator SNS confirmation, and one controlled cutover after old writers are paused. Local success and NBDB cross-environment renewal do not establish session longevity across scheduled intervals.
