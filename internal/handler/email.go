package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/email"
	"github.com/nathanfredericks/transactions/internal/notify"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
)

var whitespaceRe = regexp.MustCompile(`\s+`)
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

func handleIncomingEmail(ctx context.Context, event events.SNSEvent) (any, error) {
	slog.Debug("Received SNS event", "event", event)

	env := config.GetEnv()
	cfg, err := config.GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	var notification types.SESNotification
	if err := json.Unmarshal([]byte(event.Records[0].SNS.Message), &notification); err != nil {
		return nil, fmt.Errorf("parsing SES notification: %w", err)
	}
	slog.Debug("Parsed notification", "notification", notification)

	if notification.Mail.MessageID == "" {
		return nil, fmt.Errorf("message ID not found in notification")
	}

	slog.Debug("Fetching S3 object", "key", notification.Mail.MessageID)
	awsCfg, err := config.GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)
	s3Out, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &env.AWSS3BucketName,
		Key:    &notification.Mail.MessageID,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching email from S3: %w", err)
	}
	defer s3Out.Body.Close()

	rawEmail, err := io.ReadAll(s3Out.Body)
	if err != nil {
		return nil, fmt.Errorf("reading S3 object body: %w", err)
	}

	slog.Debug("Parsing email message")
	parsed, err := email.Parse(rawEmail)
	if err != nil {
		return nil, fmt.Errorf("parsing email: %w", err)
	}

	if parsed.HTML == "" {
		return nil, fmt.Errorf("email message has no HTML content")
	}

	slog.Debug("Converting HTML to plain text")
	rawText := email.HTMLToText(parsed.HTML)

	text := whitespaceRe.ReplaceAllString(rawText, " ")
	text = multiNewlineRe.ReplaceAllString(text, "\n\n")
	text = strings.ReplaceAll(text, "\t", " ")
	text = strings.TrimSpace(text)

	var matchedNotification *types.EmailConfig
	for i, n := range cfg.Email {
		if strings.EqualFold(parsed.From, n.EmailAddress) && parsed.Subject == n.EmailSubject {
			for _, last4 := range n.Last4 {
				if strings.Contains(text, last4) {
					matchedNotification = &cfg.Email[i]
					break
				}
			}
			if matchedNotification != nil {
				break
			}
		}
	}

	if matchedNotification == nil {
		return nil, fmt.Errorf("no matching notification found for email")
	}

	slog.Info("Matched notification", "ynabAccountId", matchedNotification.YNABAccountID)

	details, err := service.ExtractTransactionDetails(ctx, text)
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
	matchedOverride, err := override.FindOverride(ctx, overrides, details.Amount, details.Merchant, parsed.Date)
	if err != nil {
		return nil, fmt.Errorf("finding override: %w", err)
	}

	var tx *types.YNABTransaction

	if matchedOverride != nil {
		slog.Info("Override matched", "override", matchedOverride)
		tx, err = service.CreateTransactionWithOverride(
			ctx, matchedNotification.YNABAccountID, details.Amount, parsed.Date, matchedOverride,
		)
		if err != nil {
			return nil, fmt.Errorf("creating transaction with override: %w", err)
		}
	} else {
		slog.Info("No override applicable")

		hasRecent, err := service.CheckForRecentTransaction(ctx, matchedNotification.YNABAccountID, details.Amount, 10)
		if err != nil {
			return nil, fmt.Errorf("checking recent transactions: %w", err)
		}
		if hasRecent {
			slog.Info("Recent matching transaction found in the account")
			return nil, nil
		}

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
			ctx, matchedNotification.YNABAccountID, details.Amount, payee, parsed.Date, "", "",
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

	return tx, nil
}
