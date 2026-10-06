# Verification record — 6 October 2026

The new importer and schedules remain disabled. These are real account checks, not mocks. No YNAB writes were made.

| Check | Observed result |
|---|---|
| EQ local Rod + pinned Chromium login | Passed; MFA completed, both expected accounts validated, encrypted session saved |
| EQ separate-process native fetch | Passed; both expected accounts, seven records; 4.639 seconds including state access |
| EQ separate-process renewal + fetch | Passed; seven records; 5.212 seconds |
| EQ expiry metadata | Renewal extended access-token expiry; the reported 30-minute maximum session expiry did not move |
| EQ ECS login and callback | Passed; expired session launched the browser, both accounts validated, callback resumed retrieval and the dry-run job completed |
| EQ separate Lambda fetch and renewal | Passed; eight current records, 4.133 seconds for fetch and 4.289 seconds for renewal plus fetch |
| Rogers local Rod + pinned CloakBrowser login | Passed; email MFA completed, expected account validated, encrypted session saved |
| Rogers separate-process native fetch | Passed; 29 posted records over the configured ten-day window; 2.767 seconds |
| Rogers early renewal + fetch | Passed; 29 records; 3.028 seconds. Bank returned the existing expiry, so actual later token rotation remains to verify |
| Rogers renewal-window check | Passed with old maintenance temporarily paused: access token rotated, expiry extended from 18:46:37 to 18:54:44 Atlantic, refresh token unchanged; subsequent native retrieval returned 29 records in 2.839 seconds |
| Rogers Lambda-origin reuse | Passed; reused the locally renewed session and retrieved 29 records in 2.416 seconds |
| Rogers ECS login and callback | Passed; remembered device reused, expected account validated, dry-run workflow completed |
| Rogers Lambda-origin token rotation | Passed at the observed renewal window; access token rotated, expiry extended, 29 records retrieved in 2.311 seconds |
| Concurrent Rogers maintenance jobs | Both completed using saved authentication, no browser launches, notifications or financial-write rows |
| Rogers session lifetime | Initial results were confounded by the old maintenance worker performing competing logins. Controlled renewal works; no maximum bank lifetime has been established |
| NBDB local Rod + CloakBrowser login | Passed; native browser identity preserved, MFA completed, both expected accounts validated and session saved |
| NBDB separate-process native fetch and renewal | Passed; fetch 0.967 seconds, renewal plus fetch 1.917 seconds; access token rotated and expiry extended |
| NBDB Lambda-origin reuse and renewal | Passed in separate invocations; fetch 0.504 seconds, renewal plus fetch 1.308 seconds; both account identities validated, expiry extended again |
| NBDB intermittent local portfolio stall | Observed successful MFA/token exchange followed by a failed browser portfolio request. Authentication now captures the request, then the worker validates accounts through native Fetch before saving. Latest local login, fetch (1.061 seconds) and renewal (2.026 seconds) passed |
| NBDB ECS login | First cloud run stopped at Sign in before an authentication request. Added a second check for the late-loading update notice and safe control diagnostics; cloud recheck pending |
| NBDB timeout cleanup | A later AWS run reached MFA but stalled before account capture; the task exited without a callback and workflow recovery ended the dry run as a temporary failure. Fixed cleanup with a fresh deadline and process termination, reserved time to publish errors, and replaced sequential welcome-screen timeouts with a bounded wait on the portfolio request. Latest local login/fetch/renewal passed; updated AWS check in progress |
| Existing rule recreation | All 19 rules validated against current YNAB references and recreated with fresh IDs; contents and precedence verified unchanged. User explicitly confirmed preserving the Mullvad rule's TAILSCALE match |
| Independent alarms and admin | SNS email subscription confirmed; Amplify hosting built successfully with password protection; job links configured |
| Baseline decisions and admin | Real-data proposals validated without approval: EQ four existing links plus four missing records, Rogers 29 existing links. New screen rendered against the live backend; Go build/vet and admin typecheck/lint/build passed |
| Go build/vet, CDK build/vet, admin typecheck/lint/build | Passed; rerun relevant checks after further edits |
| Pushed implementation CI | Passed on commits 475aab4 and ab3947a |

The timings measure local retrieval operations, not end-to-end notification latency. The under-15-second notification target remains unverified. No production cutover is justified by these checks alone.

Local checks fixed button readiness, Rogers' MFA selector, CDP request-body capture and preflight filtering. The runner makes subsequent fixes possible without AWS deployment. A malformed fresh NBDB exclusion setting was also corrected.

Old maintenance policies are restored after each isolated check. The latest local NBDB run encountered its first welcome screen and still completed. New schedules and imports remain disabled; new-bank partitions contain no transaction-write, balance-write or imported-ledger rows.

The initial real-data comparison found four EQ entries without same-amount YNAB candidates, including two pending records. Rogers had 24 exact import-ID/amount/date matches, four existing import IDs with differences and one record without its import ID. Final decisions belong in the baseline review; no baseline has been approved.

The delivery-failure queue contains an Amazon SES setup notification without a Message-ID, not a lost bank alert. Mail filtering now precedes message-ID validation for recognized alerts; live reprocessing of this setup event is pending. The active SES receipt set still routes mail to the old application's bucket.

Still required: successful NBDB ECS login/callback, browser/native data comparison, final baseline review, remaining failure/recovery cases, and one controlled cutover after old writers are paused. Cross-environment renewal does not establish session longevity across scheduled intervals. Callback loss, stale-worker publication and all error categories have not yet been exercised live.
