package engine

import (
	"github.com/nathanfredericks/transactions/internal/bank"
	"strings"
)

// Error copy reviewed against recovery policy and rewritten with the Claude rephrase skill.
// Only safe display names enter these templates; bank response details never do.
func failureNotice(name string, f *bank.Failure) (title, message string) {
	key := string(f.Kind)
	if strings.HasSuffix(f.Operation, "/browser-access-rejected") {
		key = "browser-access-rejected"
	}
	if f.Operation == "previous-import-missing" || f.Operation == "purchase-not-found" {
		key = f.Operation
	}
	switch key {
	case "authentication-required":
		title = "{bank}: Could not log in automatically"
		message = "Automatic login for {bank} could not be completed, so this job has stopped. Scheduled checks will continue attempting to connect. Open the linked job to review or retry. [authentication-required]"
	case "credentials-rejected":
		title = "{bank}: Saved credentials were rejected"
		message = "{bank} rejected the saved credentials. Automatic login is paused until the credentials are updated and this bank is resumed. [credentials-rejected]"
	case "browser-access-rejected":
		title = "{bank}: Browser sign-in rejected"
		message = "{bank} blocked the browser before email verification could begin. Automatic login is paused while browser access is reviewed. [challenge-required]"
	case "challenge-required":
		title = "{bank}: Verification step needed"
		message = "{bank} requires a verification step that cannot be completed automatically. Automatic login is paused until the challenge is resolved and this bank is resumed. [challenge-required]"
	case "bank-maintenance":
		title = "{bank}: Maintenance detected"
		message = "{bank} appears to be undergoing maintenance. This job has been deferred to avoid repeated login attempts. A later scheduled check will try again. [bank-maintenance]"
	case "throttled":
		title = "{bank}: Too many requests"
		message = "Too many requests were sent to {bank}. This job has stopped after several retry attempts. A future scheduled check can try again. Open the linked job for details. [throttled]"
	case "temporary":
		title = "{bank}: Temporary error"
		message = "A temporary error occurred during a {bank} job. This job has stopped after several retry attempts. A future scheduled check can try again. Open the linked job for details. [temporary]"
	case "invalid-response":
		title = "{bank}: Unexpected response received"
		message = "An unexpected response was received during a {bank} job. This job has stopped and needs review. Open the linked job to inspect the issue. [invalid-response]"
	case "uncertain-write":
		title = "{bank}: YNAB update not confirmed"
		message = "A YNAB update for {bank} may have already been accepted, but confirmation was lost. Further updates for this operation are blocked until the outcome is reviewed. [uncertain-write]"
	case "previous-import-missing":
		title = "{bank}: Previously imported transaction missing"
		message = "YNAB recognizes a previously imported transaction, but it has been deleted or is no longer present. Review whether to restore or exclude it before importing again. [previous-import-missing]"
	case "purchase-not-found":
		title = "{bank}: Purchase not found"
		message = "A transaction alert from {bank} was received, but no matching purchase appeared after repeated checks. Further imports are blocked until reviewed. Open the linked job to inspect the issue. [purchase-not-found]"
	default:
		title = "{bank}: Job did not complete"
		message = "A job for {bank} could not finish. Inspect the linked job for details before retrying. [bank-error]"
	}
	return strings.ReplaceAll(title, "{bank}", name), strings.ReplaceAll(message, "{bank}", name)
}
