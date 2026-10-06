package engine

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfnt "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/email"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

func (e *Engine) Submit(ctx context.Context, j Job) (string, error) {
	if j.Version != 1 || j.ID == "" || j.Bank == "" || len(j.ID) > 80 || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(j.ID) {
		return "", bank.Fail(bank.Invalid, "job")
	}
	if j.Purpose != "retrieve" && j.Purpose != "maintain-session" {
		return "", bank.Fail(bank.Invalid, "job-purpose")
	}
	if j.Purpose == "maintain-session" && j.Source != "session" && j.Source != "manual" {
		return "", bank.Fail(bank.Invalid, "maintenance-source")
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
		if existing.Status == "complete" || existing.Status == "review-required" || existing.Status == "failed" {
			return j.ID, nil
		}
	}
	b, _ := json.Marshal(Request{Action: "run", JobID: j.ID, Bank: j.Bank})
	_, err = e.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(os.Getenv("WORKFLOW_ARN")), Name: &j.ID, Input: aws.String(string(b))})
	var exists *sfnt.ExecutionAlreadyExists
	if errors.As(err, &exists) {
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
			zone, _ := time.LoadLocation(e.Settings.Timezone)
			j.AlertDate = parsed.Date.In(zone).Format("2006-01-02")
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
func (e *Engine) Webhook(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	response := func(status int, v any) events.APIGatewayV2HTTPResponse {
		b, _ := json.Marshal(v)
		return events.APIGatewayV2HTTPResponse{StatusCode: status, Headers: map[string]string{"content-type": "application/json"}, Body: string(b)}
	}
	if event.RequestContext.HTTP.Method != "POST" || event.RawPath != "/webhook" {
		return response(404, map[string]string{"error": "not found"}), nil
	}
	secrets, err := config.GetSecrets(ctx)
	if err != nil {
		return response(503, map[string]string{"error": "temporarily unavailable"}), nil
	}
	provided := strings.TrimPrefix(event.Headers["authorization"], "Bearer ")
	if !strings.HasPrefix(event.Headers["authorization"], "Bearer ") || secrets.WebhookToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(secrets.WebhookToken)) != 1 {
		return response(401, map[string]string{"error": "unauthorized"}), nil
	}
	var p struct {
		Bank, Notification, EventID string
		OccurredAt                  time.Time
	}
	if event.IsBase64Encoded || json.Unmarshal([]byte(event.Body), &p) != nil || p.EventID == "" || p.OccurredAt.IsZero() || p.Notification == "" || len(p.Notification) > 20000 {
		return response(400, map[string]string{"error": "bank, notification, eventId and occurredAt are required"}), nil
	}
	j := Job{Version: 1, ID: "webhook-" + bank.Hash(p.Bank+"#"+p.EventID), Bank: p.Bank, Source: "notification", Purpose: "retrieve", Text: p.Notification, OccurredAt: p.OccurredAt, ReceivedAt: time.Now()}
	for _, entry := range e.Settings.Webhook {
		if entry.Bank != p.Bank {
			continue
		}
		for _, last4 := range entry.Last4 {
			if strings.Contains(p.Notification, last4) {
				j.AccountID = entry.YNABAccountID
				break
			}
		}
	}
	if j.AccountID == "" {
		return response(400, map[string]string{"error": "unrecognized bank account"}), nil
	}
	id, err := e.Submit(ctx, j)
	if err != nil {
		return response(503, map[string]string{"error": "could not accept job"}), nil
	}
	return response(202, map[string]string{"jobId": id}), nil
}
func (e *Engine) Maintain(ctx context.Context) (any, error) {
	var started []string
	for _, registration := range banks.All {
		if !registration.KeepWarm || !e.Settings.Banks[registration.ID].Enabled {
			continue
		}
		e.Job = Job{Bank: registration.ID}
		var lease struct {
			Expires int64 `json:"expires"`
		}
		ok, err := e.get(ctx, "LEASE", &lease)
		if err != nil {
			return nil, err
		}
		if ok && lease.Expires > time.Now().Unix() {
			continue
		}
		var health Health
		if _, err = e.get(ctx, "HEALTH", &health); err != nil {
			return nil, err
		}
		if health.Blocked || health.RetryAt.After(time.Now()) {
			continue
		}
		var pointer SessionPointer
		_, err = e.get(ctx, "SESSION", &pointer)
		if err != nil {
			return nil, err
		}
		if pointer.RenewAt.After(time.Now()) {
			continue
		}
		if err != nil && bank.Classify(err).Kind != bank.Authentication {
			return nil, err
		}
		id := fmt.Sprintf("upkeep-%s-%d", registration.ID, time.Now().Unix()/60)
		if _, err = e.Submit(ctx, Job{Version: 1, ID: id, Bank: registration.ID, Source: "session", Purpose: "maintain-session", DryRun: false}); err != nil {
			return nil, err
		}
		started = append(started, id)
	}
	return map[string]any{"started": started}, nil
}
