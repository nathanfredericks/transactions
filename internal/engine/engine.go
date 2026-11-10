package engine

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/llm"
	"github.com/nathanfredericks/transactions/internal/notify"
	"github.com/nathanfredericks/transactions/internal/state"
	"github.com/nathanfredericks/transactions/internal/ynab"
)

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

// Env is the deployment the engine runs in.
type Env struct {
	Table, Bucket string
	// Workflow is the Step Functions state machine ARN.
	Workflow string
	// Commit is the deployed revision, recorded with each suggestion.
	Commit string
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
