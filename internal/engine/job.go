package engine

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfnt "github.com/aws/aws-sdk-go-v2/service/sfn/types"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/state"
)

type Job struct {
	Version          int         `json:"version"`
	ID               string      `json:"jobId"`
	Bank             string      `json:"bank"`
	Purpose          string      `json:"purpose"`
	Source           string      `json:"source"`
	DryRun           bool        `json:"dryRun"`
	ReceivedAt       time.Time   `json:"receivedAt"`
	MessageID        string      `json:"messageId,omitempty"`
	AlertAmount      int64       `json:"alertAmount,omitempty"`
	AlertDate        string      `json:"alertDate,omitempty"`
	AccountID        string      `json:"accountId,omitempty"`
	Text             string      `json:"text,omitempty"`
	OccurredAt       time.Time   `json:"occurredAt"`
	Status           Status      `json:"status"`
	Attempt          int         `json:"attempt"`
	MissingAttempt   int         `json:"missingAttempt"`
	BrowserDeadline  time.Time   `json:"browserDeadline"`
	CapacityWaits    int         `json:"capacityWaits,omitempty"`
	BrowserAttempted bool        `json:"browserAttempted"`
	Lease            state.Lease `json:"lease"`
	Operation        string      `json:"operation,omitempty"`
	Error            bank.Kind   `json:"error,omitempty"`
	CompletedAt      time.Time   `json:"completedAt"`
}

type Request struct {
	Action  string          `json:"action"`
	JobID   string          `json:"jobId"`
	Bank    string          `json:"bank"`
	Job     *Job            `json:"job,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Status is where a job stands.
type Status string

const (
	StatusAccepted Status = "accepted"
	StatusRunning  Status = "running"
	StatusWaiting  Status = "waiting"
	StatusBrowser  Status = "browser"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
	StatusPaused   Status = "paused"
	// statusReviewRequired is the status earlier releases gave failed jobs.
	statusReviewRequired Status = "review-required"
)

// Next tells the workflow what to do after a run.
type Next string

const (
	NextComplete Next = "complete"
	NextPaused   Next = "paused"
	NextFailed   Next = "failed"
	NextDeferred Next = "deferred"
	NextBrowser  Next = "authentication-required"
)

type Outcome struct {
	Outcome     Next        `json:"outcome"`
	Bank        string      `json:"bank"`
	JobID       string      `json:"jobId"`
	WaitSeconds int         `json:"waitSeconds"`
	Lease       state.Lease `json:"lease"`
}

// finished reports a job that no run should resume.
func (j Job) finished() bool {
	return j.Status == StatusComplete || j.Status == StatusFailed || j.Status == StatusPaused || j.Status == statusReviewRequired
}

func (t *task) loadJob(ctx context.Context, id, bankID string) error {
	ok, err := t.Store.Get(ctx, "BANK#"+bankID, "JOB#"+id, &t.Job)
	if err != nil {
		return err
	}
	if !ok || t.Job.ID != id || t.Job.Bank != bankID {
		return errors.New("job not found")
	}
	return nil
}

func (t *task) saveJob(ctx context.Context) error {
	return t.Store.Put(ctx, t.Lease, "JOB#"+t.Job.ID, t.Job)
}

func (t *task) get(ctx context.Context, key string, out any) (bool, error) {
	return t.Store.Get(ctx, "BANK#"+t.Job.Bank, key, out)
}

func (t *task) put(ctx context.Context, key string, value any, _ string) error {
	return t.Store.Put(ctx, t.Lease, key, value)
}

func (t *task) outcome(next Next, wait int) Outcome {
	return Outcome{Outcome: next, Bank: t.Job.Bank, JobID: t.Job.ID, WaitSeconds: wait, Lease: t.Lease}
}

func (e *Engine) Submit(ctx context.Context, j Job) (string, error) {
	if j.Version != 1 || j.ID == "" || j.Bank == "" || len(j.ID) > 80 || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(j.ID) {
		return "", bank.Fail(bank.Invalid, "job")
	}
	if j.Purpose != "retrieve" {
		return "", bank.Fail(bank.Invalid, "job-purpose")
	}
	if j.ReceivedAt.IsZero() {
		j.ReceivedAt = time.Now()
	}
	j.Status = StatusAccepted
	created, err := e.Store.Create(ctx, "BANK#"+j.Bank, "JOB#"+j.ID, j)
	if err != nil {
		return "", err
	}
	if !created {
		var existing Job
		_, err = e.Store.Get(ctx, "BANK#"+j.Bank, "JOB#"+j.ID, &existing)
		if err != nil {
			return "", err
		}
		if existing.Bank != j.Bank || existing.Source != j.Source || existing.Purpose != j.Purpose || existing.DryRun != j.DryRun || existing.MessageID != j.MessageID || existing.AlertAmount != j.AlertAmount || existing.AlertDate != j.AlertDate || existing.Text != j.Text || existing.AccountID != j.AccountID || !existing.OccurredAt.Equal(j.OccurredAt) {
			return "", bank.Fail(bank.Invalid, "event-id-reused")
		}
		if existing.finished() {
			return j.ID, nil
		}
	}
	b, _ := json.Marshal(Request{Action: "run", JobID: j.ID, Bank: j.Bank})
	_, err = e.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(e.Env.Workflow), Name: &j.ID, Input: aws.String(string(b))})
	if _, ok := errors.AsType[*sfnt.ExecutionAlreadyExists](err); ok {
		err = nil
	}
	return j.ID, err
}
