package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/aws/aws-lambda-go/events"
	"github.com/nathanfredericks/transactions/internal/config"
)

func Handle(ctx context.Context, raw json.RawMessage) (any, error) {
	ctx = config.WithInvocationCache(ctx)

	var apiCheck struct {
		HTTPMethod string `json:"httpMethod"`
	}
	if err := json.Unmarshal(raw, &apiCheck); err == nil && apiCheck.HTTPMethod != "" {
		slog.Debug("Detected API Gateway event")
		var event events.APIGatewayProxyRequest
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("parsing API Gateway event: %w", err)
		}
		return handleIncomingWebhook(ctx, event)
	}

	var snsCheck struct {
		Records []struct {
			SNS json.RawMessage `json:"Sns"`
		} `json:"Records"`
	}
	if err := json.Unmarshal(raw, &snsCheck); err == nil && len(snsCheck.Records) > 0 && snsCheck.Records[0].SNS != nil {
		slog.Debug("Detected SNS event")
		var event events.SNSEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("parsing SNS event: %w", err)
		}
		return handleIncomingEmail(ctx, event)
	}

	return nil, fmt.Errorf("unsupported event type")
}
