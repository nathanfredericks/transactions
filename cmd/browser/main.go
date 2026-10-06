package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/smithy-go"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/engine"
	"github.com/nathanfredericks/transactions/internal/state"
	"log/slog"
	"os"
	"time"
)

func run(ctx context.Context) error {
	app, err := engine.New(ctx)
	if err != nil {
		return err
	}
	bankID, jobID, generation := os.Getenv("BANK"), os.Getenv("JOB_ID"), os.Getenv("GENERATION")
	registration, ok := banks.Find(bankID)
	if !ok {
		return bank.Fail(bank.Invalid, "bank")
	}
	var job engine.Job
	ok, err = app.Store.Get(ctx, "BANK#"+bankID, "JOB#"+jobID, &job)
	if err != nil {
		return err
	}
	if !ok || job.Lease.Generation != generation || job.Status != "browser" || !job.BrowserDeadline.After(time.Now().Add(15*time.Second)) {
		return bank.Fail(bank.Invalid, "browser-job")
	}
	lease := job.Lease
	if err = app.Store.Assert(ctx, lease); err != nil {
		return err
	}
	result := engine.BrowserResult{Generation: generation}
	authCtx, cancel := context.WithDeadline(ctx, job.BrowserDeadline)
	defer cancel()
	session, authErr := authenticate(authCtx, app, registration, lease)
	if authErr != nil {
		result.Error = bank.Classify(authErr)
	} else {
		result.SessionKey, err = app.Store.Upload(ctx, "sessions/"+bankID, session)
		if err != nil {
			return err
		}

	}
	values := map[string]any{"BROWSER#" + generation: result}
	if result.Error == nil {
		values["SESSION"] = engine.SessionPointer{Key: result.SessionKey, RenewAt: session.RenewAt}
	}
	if err = app.Store.PutMany(ctx, lease, values); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"jobId": jobID, "bank": bankID, "generation": generation})
	token := os.Getenv("TASK_TOKEN")
	_, err = app.SFN.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{TaskToken: &token, Output: ptr(string(payload))})
	return err
}
func authenticate(ctx context.Context, app *engine.Engine, r bank.Registration, l state.Lease) (session bank.Session, err error) {
	settings := app.Settings.Banks[r.ID]
	cfg, err := config.GetAWSConfig(ctx)
	if err != nil {
		return session, err
	}
	secret, err := secretsmanager.NewFromConfig(cfg).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &settings.SecretARN})
	if err != nil {
		return session, bank.Fail(bank.Invalid, "credentials-config")
	}
	var credentials bank.CredentialsData
	if secret.SecretString == nil || json.Unmarshal([]byte(*secret.SecretString), &credentials) != nil || credentials.Username == "" || credentials.Password == "" || credentials.MailToken == "" {
		return session, bank.Fail(bank.Invalid, "credentials-config")
	}
	binary := os.Getenv("CHROMIUM_PATH")
	if r.Browser == "cloak" {
		binary = os.Getenv("CLOAK_PATH")
	}
	if binary == "" {
		return session, bank.Fail(bank.Invalid, "browser-binary")
	}
	launch := launcher.New().Context(ctx).Bin(binary).Headless(false).NoSandbox(true).Set("disable-dev-shm-usage")
	if r.Browser == "cloak" {
		// Match the pinned CloakBrowser wrapper's documented Linux launch defaults.
		launch.Delete("enable-automation").Delete("enable-unsafe-swiftshader").
			Set("fingerprint", "54321").Set("fingerprint-platform", "windows").
			Set("fingerprint-timezone", "America/Halifax").Set("ignore-gpu-blocklist").Set("window-size", "1920,1080")
	}
	control, err := launch.Launch()
	if err != nil {
		return session, bank.Fail(bank.Temporary, "browser-start")
	}
	defer launch.Cleanup()
	// Rod defaults to a Chrome 114 Mac device; preserve the actual Chromium identity.
	browser := rod.New().NoDefaultDevice().ControlURL(control).Context(ctx)
	if err = browser.Connect(); err != nil {
		return session, bank.Fail(bank.Temporary, "browser-connect")
	}
	defer func() {
		closed := make(chan error, 1)
		go func() { closed <- browser.Close() }()
		select {
		case closeErr := <-closed:
			if err == nil && closeErr != nil {
				err = bank.Fail(bank.Temporary, "browser-cleanup")
			}
		case <-time.After(8 * time.Second):
			launch.Kill()
		}
	}()
	var previous *bank.Session
	var pointer engine.SessionPointer
	if found, e := app.Store.Get(ctx, "BANK#"+r.ID, "SESSION", &pointer); e != nil {
		return session, e
	} else if found && pointer.Key != "" {
		var saved bank.Session
		if e = app.Store.Download(ctx, pointer.Key, &saved); e != nil {
			var missing *s3types.NoSuchKey
			if !errors.As(e, &missing) {
				return session, e
			}
		} else {
			if saved.Bank != r.ID {
				return session, bank.Fail(bank.Invalid, "saved-bank")
			}
			previous = &saved
		}
	}
	adapter := r.New(bank.Dependencies{Previous: previous, Credentials: credentials, EmailSender: settings.EmailSender, EmailSubject: settings.EmailSubject, EmailCodeLength: settings.EmailCodeLength})
	session, err = adapter.Authenticate(ctx, browser)
	if err != nil {
		return session, err
	}
	snapshot, updated, err := adapter.Fetch(ctx, session, bank.FetchRequest{AccountsOnly: true})
	session = updated
	if err != nil {
		return session, err
	}
	if err = engine.ValidateSnapshot(snapshot, settings.ExpectedAccounts); err != nil {
		return session, err
	}
	if err = app.Store.Assert(ctx, l); err != nil {
		return session, err
	}
	fields := []any{"bank", r.ID, "accounts", len(snapshot.Accounts)}
	if previous != nil && previous.Headers["deviceid"] != "" {
		fields = append(fields, "rememberedDeviceReused", previous.Headers["deviceid"] == session.Headers["deviceid"])
	}
	slog.Info("browser authentication validated", fields...)
	return session, nil
}
func ptr[T any](value T) *T { return &value }
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 270*time.Second)
	defer cancel()
	operation := run
	if len(os.Args) > 1 {
		if len(os.Args) != 3 {
			slog.Error("expected verification mode and bank ID")
			os.Exit(2)
		}
		switch os.Args[1] {
		case "--verify-bank":
			operation = func(ctx context.Context) error { return verifyBank(ctx, os.Args[2]) }
		case "--verify-api", "--verify-renew":
			operation = func(ctx context.Context) error {
				app, err := engine.New(ctx)
				if err != nil {
					return err
				}
				result, err := app.VerifySession(ctx, os.Args[2], os.Args[1] == "--verify-renew")
				if err != nil {
					return err
				}
				return json.NewEncoder(os.Stdout).Encode(result)
			}
		default:
			slog.Error("unknown verification mode")
			os.Exit(2)
		}
	}
	if err := operation(ctx); err != nil {
		fields := []any{"category", bank.Classify(err).Kind, "operation", bank.Classify(err).Operation, "type", fmt.Sprintf("%T", err)}
		var apiError smithy.APIError
		if errors.As(err, &apiError) {
			fields = append(fields, "awsCode", apiError.ErrorCode())
		}
		var operationError *smithy.OperationError
		if errors.As(err, &operationError) {
			fields = append(fields, "awsService", operationError.Service(), "awsOperation", operationError.Operation())
		}
		slog.Error("browser failed", fields...)
		os.Exit(1)
	}
	fmt.Println("browser result published")
}

// verifyBank is an operator-only real-browser check using the same worker and lease.
// It validates account access and saves authentication, with no import or notification path.
func verifyBank(ctx context.Context, bankID string) error {
	app, err := engine.New(ctx)
	if err != nil {
		// Configuration validation errors contain only fixed messages or timezone names.
		slog.Error("verification setup failed", "error", err)
		return err
	}
	registration, ok := banks.Find(bankID)
	if !ok {
		return bank.Fail(bank.Invalid, "bank")
	}
	l, ok, err := app.Store.Acquire(ctx, bankID, "verify-"+uuid.NewString())
	if err != nil {
		return err
	}
	if !ok {
		return bank.Fail(bank.Temporary, "bank-busy")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Store.Release(cleanup, l)
	}()
	slog.Info("verification browser starting", "bank", bankID)
	s, err := authenticate(ctx, app, registration, l)
	if err != nil {
		return err
	}
	key, err := app.Store.Upload(ctx, "sessions/"+bankID, s)
	if err != nil {
		return err
	}
	return app.Store.Put(ctx, l, "SESSION", engine.SessionPointer{Key: key, RenewAt: s.RenewAt})
}
