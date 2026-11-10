package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/nathanfredericks/transactions/internal/llm"
	"github.com/nathanfredericks/transactions/internal/notify"
	"github.com/nathanfredericks/transactions/internal/ynab"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/config"
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
	Status           string      `json:"status"`
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

// Health is a bank's standing across jobs. Blocked waits for bank.resume.
// Failing means the operator has been told the bank is failing, so a later
// success reports its recovery. RetryAt holds off scheduled runs.
type Health struct {
	Blocked     bool      `json:"blocked"`
	Failing     bool      `json:"failing"`
	RetryAt     time.Time `json:"retryAt"`
	Kind        bank.Kind `json:"kind,omitempty"`
	Maintenance int       `json:"maintenance,omitempty"`
}

// Engine runs jobs. Its dependencies are built once per invocation by main.
type Engine struct {
	Store    *state.Store
	Settings *config.Settings
	SFN      *sfn.Client
	YNAB     *ynab.Client
	Push     notify.Pushover
	Model    llm.Config
	Env      Env
}

// task is one job being run: the engine's dependencies plus the job and the
// bank lease it holds.
type task struct {
	*Engine
	Job   Job
	Lease state.Lease
}

// Run runs a job until it completes, fails, waits or needs the browser.
func (e *Engine) Run(ctx context.Context, request Request) (Outcome, error) {
	return (&task{Engine: e}).run(ctx, request)
}

// Admin runs an operator action.
func (e *Engine) Admin(ctx context.Context, r Request) (any, error) {
	return (&task{Engine: e}).admin(ctx, r)
}

// VerifySession exercises real API access in a separate process, using the
// normal lease, renewal policy and encrypted session store. It never imports.
func (e *Engine) VerifySession(ctx context.Context, bankID string, renew bool) (any, error) {
	return (&task{Engine: e}).verifySession(ctx, bankID, renew)
}

// Env is the deployment the engine runs in.
type Env struct {
	Table, Bucket string
	// Workflow is the Step Functions state machine ARN.
	Workflow string
	// Commit is the deployed revision, recorded with each suggestion.
	Commit string
}

// New builds an engine from the AWS configuration, settings and secrets.
func New(cfg aws.Config, env Env, settings *config.Settings, secrets *config.Secrets) *Engine {
	return &Engine{
		Store:    &state.Store{DB: dynamodb.NewFromConfig(cfg), S3: s3.NewFromConfig(cfg), Table: env.Table, Bucket: env.Bucket},
		Settings: settings,
		SFN:      sfn.NewFromConfig(cfg),
		YNAB:     ynab.New(secrets.YNABAccessToken, settings.BudgetID),
		Push:     notify.Pushover{Token: secrets.PushoverToken, User: secrets.PushoverUser},
		Model:    llm.Config{Endpoint: settings.OpenAIEndpoint, Key: secrets.OpenAIAPIKey, Model: settings.OpenAIModel, Effort: settings.OpenAIEffort, Timeout: 45 * time.Second},
		Env:      env,
	}
}

func (t *task) get(ctx context.Context, key string, out any) (bool, error) {
	return t.Store.Get(ctx, "BANK#"+t.Job.Bank, key, out)
}

func (t *task) put(ctx context.Context, key string, value any, _ string) error {
	return t.Store.Put(ctx, t.Lease, key, value)
}

func (t *task) saveJob(ctx context.Context) error {
	return t.Store.Put(ctx, t.Lease, "JOB#"+t.Job.ID, t.Job)
}

func (t *task) outcome(status string, wait int) Outcome {
	return Outcome{Outcome: status, Bank: t.Job.Bank, JobID: t.Job.ID, WaitSeconds: wait, Lease: t.Lease}
}

func (t *task) session(ctx context.Context) (bank.Session, error) {
	var pointer SessionPointer
	found, err := t.get(ctx, "SESSION", &pointer)
	if err != nil {
		return bank.Session{}, err
	}
	if !found || pointer.Key == "" || pointer.Invalidated {
		return bank.Session{}, bank.Fail(bank.Authentication, "session-missing")
	}
	var s bank.Session
	if err = t.Store.Download(ctx, pointer.Key, &s); err != nil {
		if _, ok := errors.AsType[*s3types.NoSuchKey](err); ok {
			return s, bank.Fail(bank.Authentication, "session-missing")
		}
		return s, err
	}
	if s.Version != 1 || s.Bank != t.Job.Bank {
		return s, bank.Fail(bank.Invalid, "session-identity")
	}
	return s, nil
}

func (t *task) saveSession(ctx context.Context, s bank.Session) error {
	if s.Version != 1 || s.Bank != t.Job.Bank {
		return bank.Fail(bank.Invalid, "session-identity")
	}
	if s.RenewAt.IsZero() {
		bank.TokenTiming(&s)
	}
	key, err := t.Store.Upload(ctx, "sessions/"+t.Job.Bank, s)
	if err != nil {
		return err
	}
	return t.Store.Put(ctx, t.Lease, "SESSION", SessionPointer{Key: key, RenewAt: s.RenewAt})
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

// finished reports a job that no run should resume. review-required is the
// status earlier releases gave failed jobs.
func (j Job) finished() bool {
	return j.Status == "complete" || j.Status == "failed" || j.Status == "paused" || j.Status == "review-required"
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
