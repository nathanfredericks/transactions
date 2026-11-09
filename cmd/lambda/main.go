package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/smithy-go"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/engine"
)

func handle(ctx context.Context, raw json.RawMessage) (any, error) {
	started := time.Now()
	// Stop work 20 s before the Lambda deadline, leaving time to save the job
	// and release its lease; the workflow then re-runs it.
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-20*time.Second))
		defer cancel()
	}
	settings, err := config.Load(ctx)
	if err != nil {
		return nil, errors.New("configuration unavailable")
	}
	ctx = config.WithInvocationCache(config.WithSettings(ctx, settings))
	app, err := engine.New(ctx)
	if err != nil {
		return nil, errors.New("backend unavailable")
	}
	defer func() {
		slog.Info("invocation", "role", os.Getenv("ROLE"), "durationMs", time.Since(started).Milliseconds())
	}()
	if os.Getenv("ROLE") == "gateway" {
		var envelope struct {
			Records []struct {
				Source string `json:"eventSource"`
			} `json:"Records"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if len(envelope.Records) > 0 {
			var event events.S3Event
			if json.Unmarshal(raw, &event) != nil {
				return nil, errors.New("invalid S3 event")
			}
			return app.Email(ctx, event)
		}
	}
	var request engine.Request
	if json.Unmarshal(raw, &request) != nil {
		return nil, errors.New("invalid request")
	}
	if os.Getenv("ROLE") == "processor" {
		switch request.Action {
		case "verify-session":
			var options struct {
				Renew bool `json:"renew"`
			}
			if len(request.Payload) > 0 && json.Unmarshal(request.Payload, &options) != nil {
				return nil, errors.New("invalid verification options")
			}
			return app.VerifySession(ctx, request.Bank, options.Renew)
		case "run":
			return app.Run(ctx, request)
		default:
			return nil, errors.New("invalid processor action")
		}
	}
	switch request.Action {
	case "submit":
		if request.Job == nil {
			return nil, errors.New("job required")
		}
		if request.Job.ID == "" {
			request.Job.ID = fmt.Sprintf("scheduled-%s-%d", request.Job.Bank, time.Now().Unix()/60)
		}
		return app.Submit(ctx, *request.Job)
	default:
		return app.Admin(ctx, request)
	}
}

func main() {
	lambda.Start(func(ctx context.Context, raw json.RawMessage) (any, error) {
		result, err := handle(ctx, raw)
		if err != nil {
			code := "backend-operation"
			if api, ok := errors.AsType[smithy.APIError](err); ok {
				code = api.ErrorCode()
			}
			slog.Error("request failed", "category", code)
			return nil, errors.New("operation failed; inspect job status")
		}
		return result, nil
	})
}
