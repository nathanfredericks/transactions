package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/types"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type redirectTransport struct{ base *url.URL }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = r.base.Scheme
	req.URL.Host = r.base.Host
	return http.DefaultTransport.RoundTrip(req)
}
func setupService(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	u, _ := url.Parse(server.URL)
	oldClient := http.DefaultClient
	oldS, oldP := getSecrets, getParameters
	http.DefaultClient = &http.Client{Transport: redirectTransport{u}, Timeout: time.Second * 5}
	getSecrets = func(context.Context) (*types.Secrets, error) {
		return &types.Secrets{YNABAccessToken: "test", OpenAIAPIKey: "test"}, nil
	}
	getParameters = func(context.Context) (*types.Parameters, error) {
		return &types.Parameters{Timezone: "America/Halifax", OpenAIEndpoint: server.URL + "/", OpenAIModel: "test"}, nil
	}
	t.Setenv("YNAB_BUDGET_ID", "test")
	t.Cleanup(func() { http.DefaultClient = oldClient; getSecrets = oldS; getParameters = oldP; server.Close() })
}
func TestOverridePayloadAndTimezone(t *testing.T) {
	var got ynabPayloadTransaction
	setupService(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Transactions []ynabPayloadTransaction }
		json.NewDecoder(r.Body).Decode(&req)
		got = req.Transactions[0]
		fmt.Fprint(w, `{"data":{"transactions":[{"id":"created"}]}}`)
	})
	date := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	rule := &types.TransactionOverride{Payee: "apple", Category: "monthly", Memo: "subscription {{.Date}}"}
	tx, e := CreateTransactionWithOverride(context.Background(), "account", 5.69, date, rule)
	if e != nil || tx.ID != "created" {
		t.Fatal(tx, e)
	}
	if got.Amount != -5690 || got.Date != "2026-10-27" || *got.PayeeID != "apple" || *got.CategoryID != "monthly" || *got.Memo != "subscription 2026-10-27" || got.PayeeName != nil {
		t.Fatalf("%+v", got)
	}
	if _, e = CreateEQTransaction(context.Background(), "account", "2026-10-27", -5690, "ignored", "stable", "posted", rule); e != nil {
		t.Fatal(e)
	}
	if got.Cleared != "cleared" || *got.ImportID != "stable" || *got.Memo != "subscription 2026-10-27" {
		t.Fatal(got)
	}
}
func TestPayeesRecentAndCreate(t *testing.T) {
	setupService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/payees"):
			fmt.Fprint(w, `{"data":{"payees":[{"name":"Apple"},{"name":"Deleted","deleted":true},{"name":"Transfer","transfer_account_id":"other"}]}}`)
		case r.Method == "GET":
			fmt.Fprint(w, `{"data":{"transactions":[{"amount":-5690,"deleted":true},{"amount":-4550}]}}`)
		default:
			fmt.Fprint(w, `{"data":{"transaction":{"id":"created"}}}`)
		}
	})
	p, e := GetPayees(context.Background())
	if e != nil || len(p) != 1 || p[0] != "Apple" {
		t.Fatal(p, e)
	}
	found, e := CheckForRecentTransaction(context.Background(), "account", 5.69, 10)
	if e != nil || found {
		t.Fatal(found, e)
	}
	found, e = CheckForRecentTransaction(context.Background(), "account", 4.55, 10)
	if e != nil || !found {
		t.Fatal(found, e)
	}
	if _, e = CreateTransaction(context.Background(), "account", 5.69, "Apple", time.Now(), "", ""); e != nil {
		t.Fatal(e)
	}
}
func TestYNABErrorAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"error":{"id":"400","name":"bad","detail":"invalid"}}`, 400}, {"not-json", 500}, {"not-json", 200}, {`{"data":{}}`, 200}} {
		t.Run(tc.body, func(t *testing.T) {
			setupService(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			if _, e := createTransaction(context.Background(), "test", ynabPayloadTransaction{}); e == nil {
				t.Fatal("expected error")
			}
		})
	}
}
func TestAIKeepsBillingDescriptor(t *testing.T) {
	var output string = `{"amount":5.69,"merchant":"Apple"}`
	setupService(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": "resp_test", "object": "response", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": output, "annotations": []any{}}}}}})
	})
	d, e := ExtractTransactionDetails(context.Background(), "Purchase $5.69 at APPLE.COM/BILL TORONTO ON")
	if e != nil || d.Merchant != "APPLE.COM/BILL" {
		t.Fatal(d, e)
	}
	output = `{"payee":"Apple"}`
	p, e := MatchPayee(context.Background(), "PAY* Apple", []string{"Apple"})
	if e != nil || p != "Apple" {
		t.Fatal(p, e)
	}
	output = `{"amount":0,"merchant":"Apple"}`
	if _, e = ExtractTransactionDetails(context.Background(), "bad"); e == nil {
		t.Fatal("invalid amount accepted")
	}
	output = "bad"
	if _, e = ExtractTransactionDetails(context.Background(), "bad"); e == nil {
		t.Fatal("invalid JSON accepted")
	}
}
func TestConfigurationErrors(t *testing.T) {
	setupService(t, func(w http.ResponseWriter, r *http.Request) {})
	getSecrets = func(context.Context) (*types.Secrets, error) { return nil, errors.New("unavailable") }
	if _, e := newYNABClient(context.Background()); e == nil {
		t.Fatal("missing secrets")
	}
	getParameters = func(context.Context) (*types.Parameters, error) { return nil, errors.New("unavailable") }
	if _, e := formatDate(context.Background(), time.Now()); e == nil {
		t.Fatal("missing params")
	}
}
func TestEQUpdatePreservesUserFields(t *testing.T) {
	setupService(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		tx := body["transaction"]
		if tx["memo"] != "user memo" || tx["category_id"] != "user category" || tx["cleared"] != "reconciled" || tx["approved"] != true {
			t.Error(tx)
		}
		fmt.Fprint(w, `{"data":{"transaction":{"id":"same"}}}`)
	})
	memo, category := "user memo", "user category"
	tx := &types.YNABTransaction{ID: "same", AccountID: "account", Memo: &memo, CategoryID: &category, Cleared: "reconciled", Approved: true}
	if _, e := UpdateEQTransaction(context.Background(), tx, "2026-10-27", -5700, "posted"); e != nil {
		t.Fatal(e)
	}
}
func TestAdminIntegration(t *testing.T) {
	path := os.Getenv("CONTRACT_CASES")
	if path == "" {
		t.Skip("run scripts/integration.sh with DynamoDB Local")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Merchant                              string
			Amount                                float64
			Date, Expected, Memo, Category, Payee string
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	var payload ynabPayloadTransaction
	setupService(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Transactions []ynabPayloadTransaction }
		json.NewDecoder(r.Body).Decode(&body)
		payload = body.Transactions[0]
		fmt.Fprint(w, `{"data":{"transactions":[{"id":"created"}]}}`)
	})
	rules, e := override.GetTransactionOverrides(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	zone, _ := time.LoadLocation("America/Halifax")
	for _, tc := range fixture.Cases {
		date, _ := time.ParseInLocation("2006-01-02", tc.Date, zone)
		r := override.Match(rules, tc.Amount, tc.Merchant, date)
		if tc.Expected == "" {
			if r != nil {
				t.Errorf("unexpected %s for %+v", r.Name, tc)
			}
			continue
		}
		if r == nil || r.Name != tc.Expected {
			t.Fatalf("expected %s got %+v", tc.Expected, r)
		}
		if _, e = CreateTransactionWithOverride(context.Background(), "account", tc.Amount, date, r); e != nil {
			t.Fatal(e)
		}
		if (tc.Memo != "" && (payload.Memo == nil || *payload.Memo != tc.Memo)) || (tc.Memo == "" && payload.Memo != nil) || payload.PayeeID == nil || *payload.PayeeID != tc.Payee || payload.Date != tc.Date || payload.Amount != -int64(math.Round(tc.Amount*1000)) || payload.AccountID != "account" || payload.Cleared != "uncleared" || payload.Approved {
			t.Fatalf("metadata mismatch %+v", payload)
		}
		if tc.Category != "" && (payload.CategoryID == nil || *payload.CategoryID != tc.Category) {
			t.Fatal("category missing")
		}
	}
}
