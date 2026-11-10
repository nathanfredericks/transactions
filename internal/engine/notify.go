package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/notify"
)

// send pushes a notification, trying three times. A lost push is logged and
// never fails the job: the transaction is already in YNAB's approval list.
func (t *task) send(ctx context.Context, title, message string, purchase bool) {
	if t.Job.DryRun {
		return
	}
	options := notify.NotificationOptions{Title: title}
	if purchase {
		options.Priority = 1
		options.Sound = "cashregister"
		options.URL = "ynab://"
		options.URLTitle = "Open YNAB"
	}
	var err error
	for attempt := range 3 {
		if err = t.Push.Send(ctx, message, options); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			slog.Warn("notification not sent", "job", t.Job.ID, "error", ctx.Err())
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	slog.Warn("notification not sent", "job", t.Job.ID, "error", err)
}

// Error copy reviewed against recovery policy and rewritten with the Claude rephrase skill.
// Only safe display names enter these templates; bank response details never do.
func failureNotice(name string, f *bank.Failure) (title, message string) {
	key := string(f.Kind)
	if strings.HasSuffix(f.Operation, "/browser-access-rejected") {
		key = "browser-access-rejected"
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
	default:
		title = "{bank}: Job did not complete"
		message = "A job for {bank} could not finish. Inspect the linked job for details before retrying. [bank-error]"
	}
	return strings.ReplaceAll(title, "{bank}", name), strings.ReplaceAll(message, "{bank}", name)
}
