package engine

import (
	"context"
	"log/slog"
	"time"

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
