package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfnt "github.com/aws/aws-sdk-go-v2/service/sfn/types"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/email"
)

func (e *Engine) Submit(ctx context.Context, j Job) (string, error) {
	if j.Version != 1 || j.ID == "" || j.Bank == "" || len(j.ID) > 80 || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(j.ID) {
		return "", bank.Fail(bank.Invalid, "job")
	}
	if j.Purpose != "retrieve" {
		return "", bank.Fail(bank.Invalid, "job-purpose")
	}
	if j.ReceivedAt.IsZero() {
		j.ReceivedAt = time.Now()
	}
	j.Status = "accepted"
	created, err := e.Store.Create(ctx, "BANK#"+j.Bank, "JOB#"+j.ID, j)
	if err != nil {
		return "", err
	}
	if !created {
		var existing Job
		_, err = e.Store.Get(ctx, "BANK#"+j.Bank, "JOB#"+j.ID, &existing)
		if err != nil {
			return "", err
		}
		if existing.Bank != j.Bank || existing.Source != j.Source || existing.Purpose != j.Purpose || existing.DryRun != j.DryRun || existing.MessageID != j.MessageID || existing.AlertAmount != j.AlertAmount || existing.AlertDate != j.AlertDate || existing.Text != j.Text || existing.AccountID != j.AccountID || !existing.OccurredAt.Equal(j.OccurredAt) {
			return "", bank.Fail(bank.Invalid, "event-id-reused")
		}
		if existing.finished() {
			return j.ID, nil
		}
	}
	b, _ := json.Marshal(Request{Action: "run", JobID: j.ID, Bank: j.Bank})
	_, err = e.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(os.Getenv("WORKFLOW_ARN")), Name: &j.ID, Input: aws.String(string(b))})
	if _, ok := errors.AsType[*sfnt.ExecutionAlreadyExists](err); ok {
		err = nil
	}
	return j.ID, err
}

func (e *Engine) Email(ctx context.Context, event events.S3Event) (any, error) {
	var ids []string
	for _, record := range event.Records {
		key, err := url.QueryUnescape(record.S3.Object.Key)
		if err != nil || record.S3.Bucket.Name != e.Store.Bucket || !strings.HasPrefix(key, "incoming/") {
			return nil, bank.Fail(bank.Invalid, "mail-object")
		}
		obj, err := e.Store.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: &e.Store.Bucket, Key: &key})
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(obj.Body, 5*1024*1024+1))
		obj.Body.Close()
		if err != nil || len(raw) > 5*1024*1024 {
			return nil, bank.Fail(bank.Invalid, "mail-size")
		}
		parsed, err := email.Parse(raw)
		if err != nil {
			return nil, err
		}
		// Anyone can send to the intake address; act only on mail whose sender
		// SES verified, so a forged alert cannot create a transaction.
		if !parsed.SenderVerified() {
			slog.Warn("mail ignored: sender not verified", "key", key)
			continue
		}
		j := Job{Version: 1, ID: "mail-" + bank.Hash(parsed.MessageID), Source: "notification", Purpose: "retrieve", ReceivedAt: time.Now(), OccurredAt: parsed.Date, Text: parsed.Text, MessageID: parsed.MessageID}
		if strings.EqualFold(parsed.From, "alert@eqbank.ca") {
			if parsed.Subject != "Purchase made on your EQ Bank Card" {
				continue
			}
			matches := regexp.MustCompile(`A \$([0-9]+\.[0-9]{2}) purchase has been made on your EQ Bank Card\.`).FindAllStringSubmatch(parsed.Text, -1)
			if len(matches) != 1 {
				return nil, bank.Fail(bank.Invalid, "purchase-amount")
			}
			amount, err := bank.Milliunits(matches[0][1])
			if err != nil {
				return nil, err
			}
			j.Bank = "eq-bank"
			j.Source = "alert"
			j.AlertAmount = amount
			j.AlertDate = parsed.Date.In(e.Settings.Location()).Format("2006-01-02")
		} else {
			for _, entry := range e.Settings.Email {
				if !strings.EqualFold(entry.EmailAddress, parsed.From) || entry.EmailSubject != parsed.Subject {
					continue
				}
				for _, last4 := range entry.Last4 {
					if strings.Contains(parsed.Text, last4) {
						j.AccountID = entry.YNABAccountID
						j.Bank = entry.Bank
						if j.Bank == "" {
							j.Bank = "notification-" + entry.YNABAccountID
						}
						break
					}
				}
				if j.AccountID != "" {
					break
				}
			}
			if j.AccountID == "" {
				continue
			}
		}
		// Infrastructure notices and unrelated mail are ignored above. Only
		// recognized purchase alerts require a durable source identity.
		if parsed.MessageID == "" {
			return nil, bank.Fail(bank.Invalid, "mail-message-id")
		}
		id, err := e.Submit(ctx, j)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return map[string]any{"jobs": ids}, nil
}
