package engine

import (
	"context"
	"encoding/json"
	"github.com/nathanfredericks/transactions/internal/notify"

	"net/url"
	"time"
)

type Notification struct {
	ID       string    `json:"id"`
	JobID    string    `json:"jobId"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	Status   string    `json:"status"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

func (e *Engine) notifyOnce(ctx context.Context, id, message, title string) error {
	if e.Job.DryRun {
		return nil
	}
	var previous Notification
	found, err := e.get(ctx, "NOTICE#"+id, &previous)
	if err != nil || found {
		return err
	}
	return e.put(ctx, "NOTICE#"+id, Notification{ID: id, JobID: e.Job.ID, Title: title, Message: message, Status: "pending", At: time.Now()}, e.Job.ID)
}
func (e *Engine) Deliver(ctx context.Context, r Request) (Outcome, error) {
	e.Job = Job{ID: r.JobID, Bank: r.Bank}
	l, ok, err := e.Store.Acquire(ctx, r.Bank, "notify#"+r.JobID)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return e.outcome("deferred", 5), nil
	}
	e.Lease = l
	defer e.Store.Release(ctx, l)
	rows, err := e.Store.List(ctx, "BANK#"+r.Bank, "NOTICE#")
	if err != nil {
		return Outcome{}, err
	}
	pending := false
	for _, raw := range rows {
		var n Notification
		if json.Unmarshal(raw, &n) != nil {
			return Outcome{}, bankStateError()
		}
		if n.JobID != r.JobID || n.Status == "sent" || n.Status == "superseded" {
			continue
		}
		if n.Attempts >= 4 {
			if n.Status != "failed" {
				n.Status = "failed"
				if err = e.Store.Put(ctx, l, "NOTICE#"+n.ID, n); err != nil {
					return Outcome{}, err
				}
			}
			continue
		}
		options := notify.NotificationOptions{Title: n.Title, URL: e.Settings.AdminURL + "/jobs/" + url.PathEscape(r.JobID) + "?bank=" + url.QueryEscape(r.Bank), URLTitle: "View job"}
		if n.Title == "Transaction Approved" {
			options.Priority = 1
			options.Sound = "cashregister"
			options.URL = "ynab://"
			options.URLTitle = "Open YNAB"
		}
		n.Attempts++
		if err = e.Store.Put(ctx, l, "NOTICE#"+n.ID, n); err != nil {
			return Outcome{}, err
		}
		sendErr := notify.SendNotification(ctx, n.Message, options)
		if sendErr == nil {
			n.Status = "sent"
		} else {
			n.Status = "pending"
			if n.Attempts >= 4 {
				n.Status = "failed"
			} else {
				pending = true
			}
		}
		if err = e.Store.Put(ctx, l, "NOTICE#"+n.ID, n); err != nil {
			return Outcome{}, err
		}
	}
	if pending {
		return e.outcome("deferred", 60), nil
	}
	return e.outcome("complete", 0), nil
}
