package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/google/uuid"

	"github.com/nathanfredericks/transactions/internal/banks"
)

func (e *Engine) Admin(ctx context.Context, r Request) (any, error) {
	switch r.Action {
	case "job.get":
		if err := e.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		return e.Job, nil
	case "job.retry":
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "operator-retry")
		if err != nil || !ok {
			return nil, errors.New("bank busy")
		}
		defer e.Store.Release(ctx, l)
		if err = e.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		// Only a finished job is retried, so its earlier workflow has ended.
		if e.Job.Status == "complete" || !e.Job.finished() {
			return nil, errors.New("only a failed or paused job can be retried")
		}
		e.Lease = l
		e.Job.Status = "accepted"
		e.Job.Attempt = 0
		e.Job.CapacityWaits = 0
		e.Job.BrowserAttempted = false
		e.Job.Error = ""
		e.Job.Operation = ""
		if err = e.saveJob(ctx); err != nil {
			return nil, err
		}
		input, err := json.Marshal(Request{Action: "run", Bank: r.Bank, JobID: r.JobID})
		if err != nil {
			return nil, err
		}
		_, err = e.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(os.Getenv("WORKFLOW_ARN")), Name: aws.String("retry-" + uuid.NewString()), Input: aws.String(string(input))})
		return map[string]bool{"accepted": err == nil}, err
	case "banks.list":
		result := []map[string]any{}
		for _, registration := range banks.All {
			var h Health
			if _, err := e.Store.Get(ctx, "BANK#"+registration.ID, "HEALTH", &h); err != nil {
				return nil, err
			}
			result = append(result, map[string]any{"bank": registration.ID, "name": registration.Name, "health": h, "enabled": e.Settings.Banks[registration.ID].Enabled})
		}
		return result, nil
	case "bank.resume":
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "operator-resume")
		if err != nil || !ok {
			return nil, errors.New("bank busy")
		}
		defer e.Store.Release(ctx, l)
		var health Health
		if _, err = e.Store.Get(ctx, "BANK#"+r.Bank, "HEALTH", &health); err != nil {
			return nil, err
		}
		health.Blocked = false
		health.RetryAt = time.Time{}
		var session SessionPointer
		if _, err = e.Store.Get(ctx, "BANK#"+r.Bank, "SESSION", &session); err != nil {
			return nil, err
		}
		// Force authentication while retaining remembered device metadata for the browser.
		session.Invalidated = true
		session.RenewAt = time.Time{}
		return map[string]bool{"resumed": true}, e.Store.PutMany(ctx, l, map[string]any{"HEALTH": health, "SESSION": session})
	default:
		return nil, errors.New("unsupported operation")
	}
}
