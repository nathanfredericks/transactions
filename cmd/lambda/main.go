package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/smithy-go"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/engine"
	"log/slog"
	"os"
	"time"
)

func handle(ctx context.Context, raw json.RawMessage) (any, error) {
	started := time.Now()
	settings, err := config.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("configuration unavailable")
	}
	ctx = config.WithInvocationCache(config.WithSettings(ctx, settings))
	app, err := engine.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend unavailable")
	}
	defer func() {
		slog.Info("invocation", "role", os.Getenv("ROLE"), "durationMs", time.Since(started).Milliseconds())
	}()
	if os.Getenv("ROLE") == "gateway" {
		var envelope struct {
			Version        string          `json:"version"`
			RequestContext json.RawMessage `json:"requestContext"`
			Records        []struct {
				Source string `json:"eventSource"`
			} `json:"Records"`
		}
		_ = json.Unmarshal(raw, &envelope)
		if len(envelope.RequestContext) > 0 {
			var event events.APIGatewayV2HTTPRequest
			if json.Unmarshal(raw, &event) != nil {
				return nil, fmt.Errorf("invalid HTTP event")
			}
			return app.Webhook(ctx, event)
		}
		if len(envelope.Records) > 0 {
			var event events.S3Event
			if json.Unmarshal(raw, &event) != nil {
				return nil, fmt.Errorf("invalid S3 event")
			}
			return app.Email(ctx, event)
		}
	}
	var request engine.Request
	if json.Unmarshal(raw, &request) != nil {
		return nil, fmt.Errorf("invalid request")
	}
	if os.Getenv("ROLE") == "processor" {
		switch request.Action {
		case "verify-session":
			var options struct {
				Renew bool `json:"renew"`
			}
			if len(request.Payload) > 0 && json.Unmarshal(request.Payload, &options) != nil {
				return nil, fmt.Errorf("invalid verification options")
			}
			return app.VerifySession(ctx, request.Bank, options.Renew)
		case "run":
			return app.Run(ctx, request)
		case "recover-browser":
			return app.RecoverBrowser(ctx, request)
		case "notify":
			return app.Deliver(ctx, request)
		default:
			return nil, fmt.Errorf("invalid processor action")
		}
	}
	switch request.Action {
	case "maintain":
		return app.Maintain(ctx)
	case "submit":
		if request.Job == nil {
			return nil, fmt.Errorf("job required")
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
			var api smithy.APIError
			code := "backend-operation"
			if errors.As(err, &api) {
				code = api.ErrorCode()
			}
			slog.Error("request failed", "category", code)
			return nil, fmt.Errorf("operation failed; inspect job status")
		}
		return result, nil
	})
}
