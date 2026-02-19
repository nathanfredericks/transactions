package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/notify"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
)

func handleIncomingWebhook(ctx context.Context, event events.APIGatewayProxyRequest) (any, error) {
	slog.Debug("Received webhook event", "event", event)

	cfg, err := config.GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	if event.Body == "" {
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       `{"error":"No event body provided"}`,
		}, nil
	}

	var payload types.WebhookPayload
	if err := json.Unmarshal([]byte(event.Body), &payload); err != nil {
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       `{"error":"Invalid webhook payload"}`,
		}, nil
	}
	slog.Debug("Parsed webhook payload", "payload", payload)

	var matchedNotification *types.WebhookConfig
	for i, n := range cfg.Webhook {
		if n.Bank == payload.Bank {
			for _, last4 := range n.Last4 {
				if strings.Contains(payload.Notification, last4) {
					matchedNotification = &cfg.Webhook[i]
					break
				}
			}
			if matchedNotification != nil {
				break
			}
		}
	}

	if matchedNotification == nil {
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       `{"error":"No matching notification found for bank"}`,
		}, nil
	}

	slog.Info("Matched notification", "ynabAccountId", matchedNotification.YNABAccountID)

	details, err := service.ExtractTransactionDetails(ctx, payload.Notification)
	if err != nil {
		return nil, fmt.Errorf("extracting transaction details: %w", err)
	}
	slog.Info("Extracted transaction details", "amount", details.Amount, "merchant", details.Merchant)

	slog.Debug("Scanning DynamoDB for override configurations")
	overrides, err := override.GetTransactionOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting overrides: %w", err)
	}

	slog.Debug("Searching for applicable override")
	matchedOverride, err := override.FindOverride(ctx, overrides, details.Amount, details.Merchant, time.Now())
	if err != nil {
		return nil, fmt.Errorf("finding override: %w", err)
	}

	var tx *types.YNABTransaction

	if matchedOverride != nil {
		slog.Info("Override matched", "override", matchedOverride)
		tx, err = service.CreateTransactionWithOverride(
			ctx, matchedNotification.YNABAccountID, details.Amount, time.Now(), matchedOverride,
		)
		if err != nil {
			return nil, fmt.Errorf("creating transaction with override: %w", err)
		}
	} else {
		slog.Info("No override applicable")

		payees, err := service.GetPayees(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting payees: %w", err)
		}
		payee, err := service.MatchPayee(ctx, details.Merchant, payees)
		if err != nil {
			return nil, fmt.Errorf("matching payee: %w", err)
		}
		slog.Debug("Creating transaction with matched payee", "payee", payee)

		tx, err = service.CreateTransaction(
			ctx, matchedNotification.YNABAccountID, details.Amount, payee, time.Now(), "", "",
		)
		if err != nil {
			return nil, fmt.Errorf("creating transaction: %w", err)
		}
	}

	slog.Info("Transaction created successfully", "transaction", tx)

	formattedAmount := fmt.Sprintf("$%.2f", details.Amount)

	payeeName := ""
	if tx.PayeeName != nil {
		payeeName = *tx.PayeeName
	}

	err = notify.SendNotification(ctx,
		fmt.Sprintf("A transaction of %s at %s was approved on your %s.",
			formattedAmount, payeeName, tx.AccountName),
		notify.NotificationOptions{
			Priority: 1,
			Title:    "Transaction Approved",
			Sound:    "cashregister",
			URL:      "ynab://",
			URLTitle: "Open YNAB",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("sending notification: %w", err)
	}

	txJSON, _ := json.Marshal(tx)
	return events.APIGatewayProxyResponse{
		StatusCode: 201,
		Body:       string(txJSON),
	}, nil
}
