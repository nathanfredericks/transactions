package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/nathanfredericks/transactions/internal/notify"
)

// send pushes a notification, trying three times. A lost push is logged and
// never fails the job: the transaction is already in YNAB's approval list.
func (e *Engine) send(ctx context.Context, title, message string, purchase bool) {
	if e.Job.DryRun {
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
		if err = e.Push.Send(ctx, message, options); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			slog.Warn("notification not sent", "job", e.Job.ID, "error", ctx.Err())
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	slog.Warn("notification not sent", "job", e.Job.ID, "error", err)
}
