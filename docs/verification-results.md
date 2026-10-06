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
| Rogers session lifetime | Initial results were confounded by the old maintenance worker performing competing logins. An isolated renewal-window check is in progress; no bank lifetime limit has been established |
| NBDB local Rod + CloakBrowser login | Removed Rod’s default Chrome 114 Mac identity override, resolving the observed AK000001 rejection. MFA and informational welcome screens reached; final isolated retrieval and renewal checks are in progress |
| Go build/vet, CDK build/vet, admin typecheck/lint/build | Passed; rerun relevant checks after further edits |
| Pushed base implementation CI | Passed on commit 475aab4 |

The timings measure local retrieval operations, not end-to-end notification latency. The under-15-second notification target remains unverified. No production cutover is justified by these checks alone.

Local checks fixed button readiness, Rogers' MFA selector, CDP request-body capture and preflight filtering. The runner makes subsequent fixes possible without AWS deployment. A malformed fresh NBDB exclusion setting was also corrected.

Still required: successful NBDB browser authentication; Lambda-origin reuse and renewal for each bank; observed Rogers token rotation; real ECS login/callback checks; browser/native data comparison; baseline and rules review; supported failure/recovery cases; operator SNS confirmation; and one controlled cutover after old writers are paused.
