package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/browserbroker"
	"github.com/nathanfredericks/transactions/internal/chromium"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/engine"
	"github.com/nathanfredericks/transactions/internal/human"
	"github.com/nathanfredericks/transactions/internal/signin"
	"github.com/nathanfredericks/transactions/internal/state"
)

func run(ctx context.Context, cfg aws.Config) error {
	app, secrets, err := open(ctx, cfg)
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
	if !ok || job.Lease.Generation != generation || job.Status != engine.StatusBrowser || !job.BrowserDeadline.After(time.Now().Add(15*time.Second)) {
		return bank.Fail(bank.Invalid, "browser-job")
	}
	lease := job.Lease
	if err = app.Store.Assert(ctx, lease); err != nil {
		return err
	}
	result := engine.BrowserResult{Generation: generation}
	// Reserve time for browser cleanup, fenced persistence and the callback,
	// including when authentication exhausts its own deadline.
	authDeadline := time.Now().Add(240 * time.Second)
	if job.BrowserDeadline.Before(authDeadline) {
		authDeadline = job.BrowserDeadline
	}
	authCtx, cancel := context.WithDeadline(ctx, authDeadline)
	defer cancel()
	session, authErr := authenticate(authCtx, app, secrets, registration, lease)
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
	_, err = app.SFN.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{TaskToken: &token, Output: new(string(payload))})
	return err
}

func authenticate(ctx context.Context, app *engine.Engine, secrets *config.Secrets, r bank.Registration, l state.Lease) (session bank.Session, err error) {
	settings := app.Settings.Banks[r.ID]
	stored, err := secrets.Bank(r.ID)
	if err != nil {
		return session, bank.Fail(bank.Invalid, "credentials-config")
	}
	credentials := signin.Credentials{Username: stored.Username, Password: stored.Password, MailToken: stored.MailToken}
	var previous *bank.Session
	var pointer engine.SessionPointer
	if found, e := app.Store.Get(ctx, "BANK#"+r.ID, "SESSION", &pointer); e != nil {
		return session, e
	} else if found && pointer.Key != "" {
		var saved bank.Session
		if e = app.Store.Download(ctx, pointer.Key, &saved); e != nil {
			if _, ok := errors.AsType[*s3types.NoSuchKey](e); !ok {
				return session, e
			}
		} else {
			if saved.Bank != r.ID {
				return session, bank.Fail(bank.Invalid, "saved-bank")
			}
			previous = &saved
		}
	}
	binary := os.Getenv("CHROMIUM_PATH")
	options := chromium.Options{Timeout: 30 * time.Second}
	if r.Browser == "cloak" {
		binary = os.Getenv("CLOAK_PATH")
		options.Cloak, options.Platform, options.Timezone, options.Locale = true, "windows", "America/Halifax", "en-CA"
	}
	if binary == "" {
		return session, bank.Fail(bank.Invalid, "browser-binary")
	}
	options.Executable = binary
	options.Env = browserEnv(r.Browser == "cloak")

	// The bank lease remains authoritative for bank data; this reservation only
	// serializes CloakBrowser capacity across independent applications.
	if r.Browser == "cloak" {
		client, err := browserbroker.NewClient(os.Getenv("BROWSER_BROKER_URL"), os.Getenv("BROWSER_BROKER_TOKEN"))
		if err != nil {
			return session, bank.Fail(bank.Capacity, "browser-broker-unavailable")
		}
		reservation, err := client.Acquire(ctx, browserbroker.Request{Bank: r.ID, JobID: l.Owner, Generation: l.Generation})
		if err != nil {
			return session, bank.Fail(bank.Capacity, "browser-broker-busy")
		}
		// Registered before the browser closes, so it runs after: capacity is
		// released only once the browser is gone.
		defer func() {
			clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Second)
			defer cancel()
			if client.Release(clean, reservation.ID) != nil {
				slog.Warn("browser reservation release unconfirmed", "bank", r.ID)
			}
		}()
	}
	version := ""
	if r.Browser == "cloak" {
		version = os.Getenv("CLOAK_VERSION")
		if version == "" {
			return session, bank.Fail(bank.Invalid, "browser-version")
		}
		// A remembered device belongs to the build and seed it was validated
		// with. A different build establishes a new device instead of pairing
		// old cookies with a different fingerprint.
		if previous != nil && (previous.BrowserVersion != version || previous.BrowserPlatform != options.Platform) {
			previous = nil
		}
		options.Seed = 10000 + rand.IntN(90000)
		if previous != nil && previous.BrowserSeed >= 10000 && previous.BrowserSeed <= 99999 {
			options.Seed = previous.BrowserSeed
		}
		slog.Info("CloakBrowser build", "bank", r.ID, "version", version)
	}
	instance, err := chromium.Launch(ctx, options)
	if err != nil {
		return session, launchFailure(r.ID, err)
	}
	defer instance.Close()
	b := &signin.Browser{Context: instance.Context, Page: instance.Page}
	if r.Browser == "cloak" {
		if b.Human, err = human.New(instance.Context, instance.Page); err != nil {
			return session, bank.Fail(bank.Temporary, "humanizer")
		}
	}
	session, err = signin.Authenticate(ctx, r.ID, signin.Dependencies{Previous: previous, Credentials: credentials, EmailSender: settings.EmailSender, EmailSubject: settings.EmailSubject, EmailCodeLength: settings.EmailCodeLength}, b)
	if err != nil {
		// CloakBrowser can refuse its licence after starting, on first use.
		if code, ok := instance.LicenceCode(); ok {
			return session, licenceFailure(code)
		}
		return session, err
	}
	if r.Browser == "cloak" {
		session.BrowserSeed, session.BrowserVersion, session.BrowserPlatform = options.Seed, version, options.Platform
	}
	snapshot, updated, err := r.API.Fetch(ctx, session, bank.FetchRequest{AccountsOnly: true})
	session = updated
	if err != nil {
		return session, err
	}
	if err = snapshot.Validate(settings.ExpectedAccounts); err != nil {
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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	// An ECS stop sends SIGTERM: end the work so the browser closes and the
	// broker reservation is released at once, not when it expires.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 270*time.Second)
	defer cancel()
	cfg, err := config.AWS(ctx)
	if err != nil {
		slog.Error("aws configuration unavailable", "error", err)
		os.Exit(1)
	}
	operation := func(ctx context.Context) error { return run(ctx, cfg) }
	if len(os.Args) > 1 {
		if len(os.Args) != 3 {
			slog.Error("expected verification mode and bank ID")
			os.Exit(2)
		}
		switch os.Args[1] {
		case "--verify-bank":
			operation = func(ctx context.Context) error { return verifyBank(ctx, cfg, os.Args[2]) }
		case "--verify-api", "--verify-renew":
			operation = func(ctx context.Context) error {
				app, _, err := open(ctx, cfg)
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
		fields := []any{"category", bank.Classify(err).Kind, "operation", bank.Classify(err).Operation, "errorKind", chromium.ErrorKind(err)}
		if apiError, ok := errors.AsType[smithy.APIError](err); ok {
			fields = append(fields, "awsCode", apiError.ErrorCode())
		}
		if operationError, ok := errors.AsType[*smithy.OperationError](err); ok {
			fields = append(fields, "awsService", operationError.Service(), "awsOperation", operationError.Operation())
		}
		slog.Error("browser failed", fields...)
		os.Exit(1)
	}
	fmt.Println("browser result published")
}

// verifyBank is an operator-only real-browser check using the same worker and lease.
// It validates account access and saves authentication, with no import or notification path.
func verifyBank(ctx context.Context, cfg aws.Config, bankID string) error {
	app, secrets, err := open(ctx, cfg)
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
	s, err := authenticate(ctx, app, secrets, registration, l)
	if err != nil {
		return err
	}
	key, err := app.Store.Upload(ctx, "sessions/"+bankID, s)
	if err != nil {
		return err
	}
	return app.Store.Put(ctx, l, "SESSION", engine.SessionPointer{Key: key, RenewAt: s.RenewAt})
}

// open loads the settings and the secret and builds the engine. The AWS
// configuration is loaded once per process; the rest on every call, so a
// settings change applies to the next invocation.
func open(ctx context.Context, cfg aws.Config) (*engine.Engine, *config.Secrets, error) {
	settings, err := config.Load(ctx, ssm.NewFromConfig(cfg), os.Getenv("SETTINGS_PARAMETER"))
	if err != nil {
		return nil, nil, err
	}
	secrets, err := config.LoadSecrets(ctx, secretsmanager.NewFromConfig(cfg), os.Getenv("AWS_SECRET_ARN"))
	if err != nil {
		return nil, nil, err
	}
	env := engine.Env{Table: os.Getenv("STATE_TABLE"), Bucket: os.Getenv("STATE_BUCKET"), Workflow: os.Getenv("WORKFLOW_ARN"), Commit: os.Getenv("DEPLOYMENT_COMMIT")}
	return engine.New(cfg, env, settings, secrets), secrets, nil
}

// browserEnv is the browser's environment: this process's, without the task
// token, the broker token or AWS credentials. Only CloakBrowser gets its
// licence key.
func browserEnv(cloak bool) map[string]string {
	env := map[string]string{"GOOGLE_API_KEY": "no"}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "TASK_TOKEN", k == "BROWSER_BROKER_TOKEN", strings.HasPrefix(k, "AWS_"), strings.HasPrefix(k, "ECS_"):
			continue
		case k == "CLOAKBROWSER_LICENSE_KEY" && !cloak:
			continue
		}
		env[k] = v
	}
	return env
}

// launchFailure maps a browser that did not start to a failure, using the
// licence code CloakBrowser wrote when it refused its licence.
func launchFailure(bankID string, err error) error {
	if launch, ok := errors.AsType[*chromium.LaunchError](err); ok && launch.Licence != 0 {
		slog.Warn("browser startup failed", "bank", bankID, "licenceCode", launch.Licence)
		return licenceFailure(launch.Licence)
	}
	slog.Warn("browser startup failed", "bank", bankID, "errorKind", chromium.ErrorKind(err))
	return bank.Fail(bank.Temporary, "browser-start")
}

// licenceFailure maps the vendor's documented licence exit codes.
func licenceFailure(code int) error {
	switch code {
	case 76:
		return bank.Fail(bank.Temporary, "browser-license-session-limit")
	case 77:
		return bank.Fail(bank.Invalid, "browser-license-invalid")
	case 78:
		return bank.Fail(bank.Temporary, "browser-license-unavailable")
	case 79:
		return bank.Fail(bank.Invalid, "browser-license-config")
	}
	return bank.Fail(bank.Temporary, "browser-start")
}
