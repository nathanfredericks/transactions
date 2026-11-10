package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/google/uuid"

	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/state"
)

// admin runs one of the four operator actions.
func (t *task) admin(ctx context.Context, r Request) (any, error) {
	switch r.Action {
	case "job.get":
		if err := t.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		return t.Job, nil
	case "job.retry":
		err := t.Store.WithLease(ctx, r.Bank, "operator-retry", func(l state.Lease) error { return t.retry(ctx, l, r) })
		return map[string]bool{"accepted": err == nil}, err
	case "banks.list":
		return t.banksList(ctx)
	case "bank.resume":
		err := t.Store.WithLease(ctx, r.Bank, "operator-resume", func(l state.Lease) error { return t.resume(ctx, l, r.Bank) })
		return map[string]bool{"resumed": err == nil}, err
	default:
		return nil, errors.New("unsupported operation")
	}
}

// retry restarts a failed or paused job from the start in a new workflow.
func (t *task) retry(ctx context.Context, l state.Lease, r Request) error {
	if err := t.loadJob(ctx, r.JobID, r.Bank); err != nil {
		return err
	}
	// Only a finished job is retried, so its earlier workflow has ended.
	if t.Job.Status == StatusComplete || !t.Job.finished() {
		return errors.New("only a failed or paused job can be retried")
	}
	t.Lease = l
	t.Job.Status = StatusAccepted
	t.Job.Attempt = 0
	t.Job.CapacityWaits = 0
	t.Job.BrowserAttempted = false
	t.Job.Error = ""
	t.Job.Operation = ""
	if err := t.saveJob(ctx); err != nil {
		return err
	}
	input, err := json.Marshal(Request{Action: "run", Bank: r.Bank, JobID: r.JobID})
	if err != nil {
		return err
	}
	_, err = t.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(t.Env.Workflow), Name: aws.String("retry-" + uuid.NewString()), Input: aws.String(string(input))})
	return err
}

// banksList reports each bank's health and whether it is enabled.
func (t *task) banksList(ctx context.Context) ([]map[string]any, error) {
	result := []map[string]any{}
	for _, registration := range banks.All {
		var h Health
		if _, err := t.Store.Get(ctx, "BANK#"+registration.ID, "HEALTH", &h); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"bank": registration.ID, "name": registration.Name, "health": h, "enabled": t.Settings.Banks[registration.ID].Enabled})
	}
	return result, nil
}

// resume clears a block and the retry time and forces a fresh sign-in,
// keeping the remembered device metadata for the browser.
func (t *task) resume(ctx context.Context, l state.Lease, bankID string) error {
	var health Health
	if _, err := t.Store.Get(ctx, "BANK#"+bankID, "HEALTH", &health); err != nil {
		return err
	}
	health.Blocked = false
	health.RetryAt = time.Time{}
	var session SessionPointer
	if _, err := t.Store.Get(ctx, "BANK#"+bankID, "SESSION", &session); err != nil {
		return err
	}
	session.Invalidated = true
	session.RenewAt = time.Time{}
	return t.Store.PutMany(ctx, l, map[string]any{"HEALTH": health, "SESSION": session})
}
