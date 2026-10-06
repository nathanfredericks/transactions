package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/state"
	"os"
	"time"
)

type Job struct {
	Version          int         `json:"version"`
	ID               string      `json:"jobId"`
	Execution        string      `json:"execution,omitempty"`
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
	Status           string      `json:"status"`
	Attempt          int         `json:"attempt"`
	MissingAttempt   int         `json:"missingAttempt"`
	BrowserDeadline  time.Time   `json:"browserDeadline"`
	BrowserAttempted bool        `json:"browserAttempted"`
	Lease            state.Lease `json:"lease"`
	SnapshotKey      string      `json:"snapshotKey,omitempty"`
	Operation        string      `json:"operation,omitempty"`
	Error            bank.Kind   `json:"error,omitempty"`
	CompletedAt      time.Time   `json:"completedAt"`
}
type Request struct {
	Action    string          `json:"action"`
	JobID     string          `json:"jobId"`
	Bank      string          `json:"bank"`
	Job       *Job            `json:"job,omitempty"`
	Lease     state.Lease     `json:"lease"`
	Execution string          `json:"execution"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}
type Outcome struct {
	Outcome     string      `json:"outcome"`
	Bank        string      `json:"bank"`
	JobID       string      `json:"jobId"`
	WaitSeconds int         `json:"waitSeconds"`
	Lease       state.Lease `json:"lease"`
}
type SessionPointer struct {
	Invalidated bool      `json:"invalidated,omitempty"`
	Key         string    `json:"key"`
	RenewAt     time.Time `json:"renewAt"`
}
type BrowserResult struct {
	Generation string        `json:"generation"`
	Error      *bank.Failure `json:"error,omitempty"`
	SessionKey string        `json:"sessionKey,omitempty"`
}
type Health struct {
	ImportReview     bool      `json:"importReview,omitempty"`
	Blocked          bool      `json:"blocked"`
	Kind             bank.Kind `json:"kind"`
	Failures         int       `json:"failures"`
	RetryAt          time.Time `json:"retryAt"`
	Episode          string    `json:"episode"`
	Notified         bool      `json:"notified"`
	MaintenanceCount int       `json:"maintenanceCount"`
}
type Engine struct {
	Store    *state.Store
	Settings *config.Settings
	SFN      *sfn.Client
	Lease    state.Lease
	Job      Job
}

func New(ctx context.Context) (*Engine, error) {
	cfg, e := config.GetAWSConfig(ctx)
	if e != nil {
		return nil, e
	}
	settings, e := config.Load(ctx)
	if e != nil {
		return nil, e
	}
	return &Engine{Store: &state.Store{DB: dynamodb.NewFromConfig(cfg), S3: s3.NewFromConfig(cfg), Table: os.Getenv("STATE_TABLE"), Bucket: os.Getenv("STATE_BUCKET")}, Settings: settings, SFN: sfn.NewFromConfig(cfg)}, nil
}
func (e *Engine) get(ctx context.Context, key string, out any) (bool, error) {
	return e.Store.Get(ctx, "BANK#"+e.Job.Bank, key, out)
}
func (e *Engine) put(ctx context.Context, key string, value any, _ string) error {
	return e.Store.Put(ctx, e.Lease, key, value)
}
func (e *Engine) saveJob(ctx context.Context) error {
	return e.Store.Put(ctx, e.Lease, "JOB#"+e.Job.ID, e.Job)
}
func (e *Engine) outcome(status string, wait int) Outcome {
	return Outcome{Outcome: status, Bank: e.Job.Bank, JobID: e.Job.ID, WaitSeconds: wait, Lease: e.Lease}
}
func (e *Engine) session(ctx context.Context) (bank.Session, error) {
	var pointer SessionPointer
	found, err := e.get(ctx, "SESSION", &pointer)
	if err != nil {
		return bank.Session{}, err
	}
	if !found || pointer.Key == "" || pointer.Invalidated {
		return bank.Session{}, bank.Fail(bank.Authentication, "session-missing")
	}
	var s bank.Session
	if err = e.Store.Download(ctx, pointer.Key, &s); err != nil {
		var missing *s3types.NoSuchKey
		if errors.As(err, &missing) {
			return s, bank.Fail(bank.Authentication, "session-missing")
		}
		return s, err
	}
	if s.Version != 1 || s.Bank != e.Job.Bank {
		return s, bank.Fail(bank.Invalid, "session-identity")
	}
	return s, nil
}
func (e *Engine) saveSession(ctx context.Context, s bank.Session) error {
	if s.Version != 1 || s.Bank != e.Job.Bank {
		return bank.Fail(bank.Invalid, "session-identity")
	}
	if s.RenewAt.IsZero() {
		bank.TokenTiming(&s)
	}
	key, err := e.Store.Upload(ctx, "sessions/"+e.Job.Bank, s)
	if err != nil {
		return err
	}
	return e.Store.Put(ctx, e.Lease, "SESSION", SessionPointer{Key: key, RenewAt: s.RenewAt})
}
func ValidateSnapshot(s bank.Snapshot, expected []string) error {
	if len(s.Accounts) == 0 {
		return bank.Fail(bank.Invalid, "empty-accounts")
	}
	seen := map[string]bool{}
	for _, a := range s.Accounts {
		if a.ID == "" || seen[a.ID] {
			return bank.Fail(bank.Invalid, "account-identity")
		}
		seen[a.ID] = true
	}
	if len(expected) != len(seen) {
		return bank.Fail(bank.Invalid, "account-count")
	}
	for _, id := range expected {
		if !seen[id] {
			return bank.Fail(bank.Invalid, "account-identity")
		}
	}
	for _, r := range s.Records {
		if !seen[r.AccountID] || r.ProvisionalID == "" || r.Description == "" || (r.Status != "posted" && r.Status != "pending") {
			return bank.Fail(bank.Invalid, "record")
		}
		if _, err := time.Parse("2006-01-02", r.Date); err != nil {
			return bank.Fail(bank.Invalid, "record-date")
		}
	}
	return nil
}
func (e *Engine) loadJob(ctx context.Context, id, bankID string) error {
	ok, err := e.Store.Get(ctx, "BANK#"+bankID, "JOB#"+id, &e.Job)
	if err != nil {
		return err
	}
	if !ok || e.Job.ID != id || e.Job.Bank != bankID {
		return fmt.Errorf("job not found")
	}
	return nil
}
