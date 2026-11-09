package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfnt "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/google/uuid"

	"github.com/nathanfredericks/transactions/internal/banks"
)

func (e *Engine) Admin(ctx context.Context, r Request) (any, error) {
	switch r.Action {
	case "job.get":
		if err := e.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		notices, err := e.Store.List(ctx, "BANK#"+r.Bank, "NOTICE#")
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": e.Job, "notifications": notices}, nil
	case "notifications.retry":
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "notification-retry")
		if err != nil || !ok {
			return nil, errors.New("bank busy")
		}
		defer e.Store.Release(ctx, l)
		rows, err := e.Store.List(ctx, "BANK#"+r.Bank, "NOTICE#")
		if err != nil {
			return nil, err
		}
		for _, raw := range rows {
			var n Notification
			_ = json.Unmarshal(raw, &n)
			if n.JobID == r.JobID && n.Status != "sent" && n.Status != "superseded" {
				n.Attempts = 0
				n.Status = "pending"
				if err = e.Store.Put(ctx, l, "NOTICE#"+n.ID, n); err != nil {
					return nil, err
				}
			}
		}
		if err = e.Store.Release(ctx, l); err != nil {
			return nil, err
		}
		return e.startOperation(ctx, r, "notify")
	case "job.retry":
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "operator-retry")
		if err != nil || !ok {
			return nil, errors.New("bank busy")
		}
		defer e.Store.Release(ctx, l)
		if err = e.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		if e.Job.Status == "complete" {
			return nil, errors.New("completed job cannot be retried")
		}
		execution := e.Job.Execution
		if execution == "" {
			execution = executionARN(e.Job.ID)
		}
		prior, err := e.SFN.DescribeExecution(ctx, &sfn.DescribeExecutionInput{ExecutionArn: &execution})
		var absent *sfnt.ExecutionDoesNotExist
		if err != nil && !errors.As(err, &absent) {
			return nil, err
		}
		if prior != nil && (prior.Status == sfnt.ExecutionStatusRunning || prior.Status == sfnt.ExecutionStatusPendingRedrive) {
			return nil, errors.New("job workflow is still active")
		}
		e.Lease = l
		e.Job.Status = "accepted"
		e.Job.Attempt = 0
		e.Job.BrowserAttempted = false
		e.Job.Error = ""
		e.Job.Operation = ""
		return e.startOperation(ctx, r, "run")
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

func (e *Engine) startOperation(ctx context.Context, r Request, action string) (any, error) {
	name := action + "-" + uuid.NewString()
	if action == "run" {
		// Claim the new execution before starting it; late older workers are fenced out.
		e.Job.Execution = executionARN(name)
		if err := e.saveJob(ctx); err != nil {
			return nil, err
		}
	}
	input, _ := json.Marshal(Request{Action: action, Bank: r.Bank, JobID: r.JobID})
	_, err := e.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(os.Getenv("WORKFLOW_ARN")), Name: &name, Input: aws.String(string(input))})
	return map[string]bool{"accepted": err == nil}, err
}

func executionARN(name string) string {
	return strings.Replace(os.Getenv("WORKFLOW_ARN"), ":stateMachine:", ":execution:", 1) + ":" + name
}
