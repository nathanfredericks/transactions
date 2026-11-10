package engine

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/email"
)

// eqPurchase is the one line of an EQ card alert that states the amount.
var eqPurchase = regexp.MustCompile(`A \$([0-9]+\.[0-9]{2}) purchase has been made on your EQ Bank Card\.`)

// Email turns stored alert mail into jobs. Mail whose sender SES did not
// verify, and mail that is not a recognized purchase alert, is ignored.
func (e *Engine) Email(ctx context.Context, event events.S3Event) (any, error) {
	var ids []string
	for _, record := range event.Records {
		key, err := url.QueryUnescape(record.S3.Object.Key)
		if err != nil || record.S3.Bucket.Name != e.Store.Bucket || !strings.HasPrefix(key, "incoming/") {
			return nil, bank.Fail(bank.Invalid, "mail-object")
		}
		parsed, err := e.readMail(ctx, key)
		if err != nil {
			return nil, err
		}
		// Anyone can send to the intake address; act only on mail whose sender
		// SES verified, so a forged alert cannot create a transaction.
		if !parsed.SenderVerified() {
			slog.Warn("mail ignored: sender not verified", "key", key)
			continue
		}
		j, ok, err := e.mailJob(parsed)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
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

// readMail reads and parses one stored message, at most 5 MB.
func (e *Engine) readMail(ctx context.Context, key string) (*email.ParsedEmail, error) {
	obj, err := e.Store.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: &e.Store.Bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(obj.Body, 5*1024*1024+1))
	obj.Body.Close()
	if err != nil || len(raw) > 5*1024*1024 {
		return nil, bank.Fail(bank.Invalid, "mail-size")
	}
	return email.Parse(raw)
}

// mailJob is the job a verified message asks for: an amount-only EQ purchase,
// or a card alert for a configured sender whose text names a known card. It
// reports false for any other mail.
func (e *Engine) mailJob(parsed *email.ParsedEmail) (Job, bool, error) {
	j := Job{Version: 1, ID: "mail-" + bank.Hash(parsed.MessageID), Source: "notification", Purpose: "retrieve", ReceivedAt: time.Now(), OccurredAt: parsed.Date, Text: parsed.Text, MessageID: parsed.MessageID}
	if strings.EqualFold(parsed.From, "alert@eqbank.ca") {
		if parsed.Subject != "Purchase made on your EQ Bank Card" {
			return j, false, nil
		}
		matches := eqPurchase.FindAllStringSubmatch(parsed.Text, -1)
		if len(matches) != 1 {
			return j, false, bank.Fail(bank.Invalid, "purchase-amount")
		}
		amount, err := bank.Milliunits(matches[0][1])
		if err != nil {
			return j, false, err
		}
		j.Bank, j.Source, j.AlertAmount = "eq-bank", "alert", amount
		j.AlertDate = parsed.Date.In(e.Settings.Location()).Format("2006-01-02")
		return j, true, nil
	}
	for _, entry := range e.Settings.Email {
		if !strings.EqualFold(entry.EmailAddress, parsed.From) || entry.EmailSubject != parsed.Subject {
			continue
		}
		for _, last4 := range entry.Last4 {
			if strings.Contains(parsed.Text, last4) {
				j.AccountID = entry.YNABAccountID
				j.Bank = cmp.Or(entry.Bank, "notification-"+entry.YNABAccountID)
				return j, true, nil
			}
		}
	}
	return j, false, nil
}
