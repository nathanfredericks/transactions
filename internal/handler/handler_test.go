package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/aws/aws-lambda-go/events"
	"github.com/nathanfredericks/transactions/internal/notify"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/types"
	"strings"
	"testing"
	"time"
)

func setupHandler(t *testing.T) {
	oldC, oldE, oldG, oldF, oldO, oldR, oldP, oldM, oldT, oldN := getConfig, extractDetails, getOverrides, findOverride, createWithOverride, recentTransaction, getPayees, matchPayee, createTransaction, sendNotification
	t.Cleanup(func() {
		getConfig = oldC
		extractDetails = oldE
		getOverrides = oldG
		findOverride = oldF
		createWithOverride = oldO
		recentTransaction = oldR
		getPayees = oldP
		matchPayee = oldM
		createTransaction = oldT
		sendNotification = oldN
	})
	getConfig = func(context.Context) (*types.Config, error) {
		return &types.Config{Webhook: []types.WebhookConfig{{Bank: "bmo", Last4: []string{"1234"}, YNABAccountID: "account"}}}, nil
	}
	extractDetails = func(context.Context, string) (*types.MerchantAmount, error) {
		return &types.MerchantAmount{Amount: 5.69, Merchant: "APPLE.COM/BILL"}, nil
	}
	getOverrides = func(context.Context) ([]types.TransactionOverride, error) {
		return []types.TransactionOverride{{Payee: "apple", Category: "monthly", Memo: "AppleCare+", Query: `{"in":["APPLE.COM/BILL",{"var":"merchant"}]}`}}, nil
	}
	findOverride = func(_ context.Context, r []types.TransactionOverride, a float64, m string, d time.Time) (*types.TransactionOverride, error) {
		return override.Match(r, a, m, d), nil
	}
	sendNotification = func(context.Context, string, notify.NotificationOptions) error { return nil }
}
func TestWebhookOverrideAndFallback(t *testing.T) {
	setupHandler(t)
	created := false
	createWithOverride = func(_ context.Context, account string, amount float64, date time.Time, r *types.TransactionOverride) (*types.YNABTransaction, error) {
		created = true
		if account != "account" || amount != 5.69 || r.Memo != "AppleCare+" {
			t.Error(r)
		}
		name := "Apple"
		return &types.YNABTransaction{PayeeName: &name}, nil
	}
	body := `{"bank":"bmo","notification":"card 1234 at APPLE.COM/BILL"}`
	for _, encoded := range []bool{false, true} {
		b := body
		if encoded {
			b = base64.StdEncoding.EncodeToString([]byte(body))
		}
		out, e := Handle(context.Background(), json.RawMessage(mustJSON(events.APIGatewayProxyRequest{HTTPMethod: "POST", Body: b, IsBase64Encoded: encoded})))
		if e != nil || out.(events.APIGatewayProxyResponse).StatusCode != 201 || !created {
			t.Fatal(out, e)
		}
	}
	getOverrides = func(context.Context) ([]types.TransactionOverride, error) { return nil, nil }
	getPayees = func(context.Context) ([]string, error) { return []string{"Apple"}, nil }
	matchPayee = func(context.Context, string, []string) (string, error) { return "Apple", nil }
	createTransaction = func(context.Context, string, float64, string, time.Time, string, string) (*types.YNABTransaction, error) {
		return &types.YNABTransaction{}, nil
	}
	if _, e := handleIncomingWebhook(context.Background(), events.APIGatewayProxyRequest{Body: body}); e != nil {
		t.Fatal(e)
	}
	extractDetails = func(context.Context, string) (*types.MerchantAmount, error) { return nil, errors.New("offline") }
	if _, e := handleIncomingWebhook(context.Background(), events.APIGatewayProxyRequest{Body: body}); e == nil {
		t.Fatal("extraction failure ignored")
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func TestInvalidEvents(t *testing.T) {
	setupHandler(t)
	for _, b := range []json.RawMessage{[]byte(`{}`), []byte(`bad`), []byte(`{"Records":[]}`)} {
		if _, e := Handle(context.Background(), b); e == nil {
			t.Fatal("unsupported event accepted")
		}
	}
	for _, req := range []events.APIGatewayProxyRequest{{}, {Body: "bad"}, {Body: "%%%", IsBase64Encoded: true}, {Body: `{"bank":"wrong","notification":"1234"}`}} {
		out, e := handleIncomingWebhook(context.Background(), req)
		if e != nil || out.(events.APIGatewayProxyResponse).StatusCode != 400 {
			t.Fatal(out, e)
		}
	}
}
func TestEmailMissingRecordData(t *testing.T) {
	if _, e := handleIncomingEmail(context.Background(), events.SNSEvent{Records: []events.SNSEventRecord{{SNS: events.SNSEntity{Message: `{"mail":{}}`}}}}); e == nil {
		t.Fatal("missing message accepted")
	}
}
func TestEmailPipelineAndDuplicate(t *testing.T) {
	setupHandler(t)
	old := fetchEmail
	oldEQ := startEQAlert
	t.Cleanup(func() { fetchEmail = old; startEQAlert = oldEQ })
	raw := []byte("From: Bank <bank@example.com>\r\nSubject: Approved\r\nDate: Tue, 27 Oct 2026 23:59:00 -0300\r\nMessage-ID: <original@example.com>\r\nContent-Type: text/html\r\n\r\n<p>Card 1234 purchase at APPLE.COM/BILL for $5.69</p>")
	fetchEmail = func(context.Context, string) ([]byte, error) { return raw, nil }
	getConfig = func(context.Context) (*types.Config, error) {
		return &types.Config{Email: []types.EmailConfig{{EmailAddress: "bank@example.com", EmailSubject: "Approved", Last4: []string{"1234"}, YNABAccountID: "account"}}}, nil
	}
	created := 0
	createWithOverride = func(_ context.Context, a string, n float64, d time.Time, r *types.TransactionOverride) (*types.YNABTransaction, error) {
		created++
		if d.Format("2006-01-02") != "2026-10-27" || a != "account" {
			t.Fatal(d, a)
		}
		return &types.YNABTransaction{}, nil
	}
	event := events.SNSEvent{Records: []events.SNSEventRecord{{SNS: events.SNSEntity{Message: `{"mail":{"messageId":"message"}}`}}}}
	if _, e := handleIncomingEmail(context.Background(), event); e != nil {
		t.Fatal(e)
	}
	if created != 1 {
		t.Fatal("override not created")
	}
	getOverrides = func(context.Context) ([]types.TransactionOverride, error) { return nil, nil }
	recentTransaction = func(context.Context, string, float64, int) (bool, error) { return true, nil }
	if _, e := handleIncomingEmail(context.Background(), event); e != nil {
		t.Fatal(e)
	}
	if created != 1 {
		t.Fatal("duplicate created")
	}
	raw = []byte(strings.Replace(string(raw), "bank@example.com", "wrong@example.com", 1))
	if _, e := handleIncomingEmail(context.Background(), event); e == nil {
		t.Fatal("sender not checked")
	}
	raw = []byte("From: alert@eqbank.ca\r\nSubject: Sign in\r\nDate: Tue, 27 Oct 2026 23:59:00 -0300\r\nContent-Type: text/html\r\n\r\n<p>Code</p>")
	if out, e := handleIncomingEmail(context.Background(), event); e != nil || out.(map[string]any)["ignored"] != true {
		t.Fatal(out, e)
	}
	fetchEmail = func(context.Context, string) ([]byte, error) { return nil, errors.New("S3 offline") }
	if _, e := handleIncomingEmail(context.Background(), event); e == nil {
		t.Fatal("storage failure ignored")
	}
}
