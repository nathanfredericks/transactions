package eq

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/nathanfredericks/transactions/internal/notify"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupProcess(t *testing.T, result Result, entries []*Entry) (*Store, *int) {
	t.Helper()
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("X-Amz-Target"), "Scan") {
			items := []any{}
			for _, e := range entries {
				raw, _ := json.Marshal(e)
				items = append(items, map[string]any{"data": map[string]string{"S": string(raw)}})
			}
			json.NewEncoder(w).Encode(map[string]any{"Items": items})
			return
		}
		if strings.Contains(r.Header.Get("X-Amz-Target"), "TransactWriteItems") {
			writes++
			fmt.Fprint(w, `{}`)
			return
		}
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(result)
			return
		}
		if r.Method == "PUT" {
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(server.Close)
	cfg := aws.Config{Region: "ca-central-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client()}
	store := &Store{db: dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(server.URL) }), s3: s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true }), table: "ledger", bucket: "private"}
	oldAccounts, oldTx, oldOverrides, oldParams, oldFind, oldCreate, oldUpdate := getAccounts, getAccountTransactions, getOverrides, getParameters, findOverride, createEQTransaction, updateEQTransaction
	t.Cleanup(func() {
		getAccounts = oldAccounts
		getAccountTransactions = oldTx
		getOverrides = oldOverrides
		getParameters = oldParams
		findOverride = oldFind
		createEQTransaction = oldCreate
		updateEQTransaction = oldUpdate
	})
	getAccounts = func(context.Context) ([]service.YNABAccount, error) {
		return []service.YNABAccount{{ID: "personal", Note: ptr(personal)}, {ID: "card", Note: ptr(card)}}, nil
	}
	getAccountTransactions = func(context.Context, string) ([]*types.YNABTransaction, error) {
		return []*types.YNABTransaction{{ID: "existing", AccountID: "card", Memo: ptr("custom"), CategoryID: ptr("category"), PayeeName: ptr("Apple"), Cleared: "reconciled", Approved: true}}, nil
	}
	getOverrides = func(context.Context) ([]types.TransactionOverride, error) {
		return []types.TransactionOverride{{Name: "Watch", Payee: "apple", Category: "category", Memo: "Watch {{.Date}}", Query: `{"and":[{"==":[{"var":"day"},27]},{"==":[{"var":"amount"},5.69]}]}`}}, nil
	}
	getParameters = func(context.Context) (*types.Parameters, error) {
		return &types.Parameters{Timezone: "America/Halifax"}, nil
	}
	findOverride = func(_ context.Context, rules []types.TransactionOverride, amount float64, m string, date time.Time) (*types.TransactionOverride, error) {
		return override.Match(rules, amount, m, date), nil
	}
	return store, &writes
}
func bankRecord() Record {
	return Record{AccountID: card, AccountNumber: "302911525", ProvisionalID: "pending", Amount: -5690, Date: "2026-10-27", Description: "APPLE.COM/BILL", Status: "pending"}
}
func envelope(records ...Record) Result {
	return Result{Version: 1, JobID: "job", RequestID: "request", Complete: true, Records: records}
}
func TestProcessCreationRetryAndDateOnly(t *testing.T) {
	r := bankRecord()
	s, writes := setupProcess(t, envelope(r), nil)
	calls := 0
	createEQTransaction = func(_ context.Context, a, d string, n int64, p, i, status string, rule *types.TransactionOverride) (*types.YNABTransaction, error) {
		calls++
		if d != "2026-10-27" || n != -5690 || a != "card" || rule == nil || rule.Name != "Watch" || !strings.HasPrefix(i, "EQ:") {
			t.Fatal("calendar date rule lost", d, rule)
		}
		return &types.YNABTransaction{ID: "created", PayeeName: ptr("Apple")}, nil
	}
	out, e := s.process(context.Background(), Job{JobID: "job", RequestID: "request", Source: "manual"})
	if e != nil {
		t.Fatal(e)
	}
	if out.(map[string]any)["created"] != 1 || calls != 1 || *writes != 1 {
		t.Fatal(out, calls, *writes)
	}
	stable := "tx#" + r.AccountID + "#" + hash(identity(r))
	id := "EQ:" + hash(stable)[:32]
	getAccountTransactions = func(context.Context, string) ([]*types.YNABTransaction, error) {
		return []*types.YNABTransaction{{ID: "recovered", ImportID: &id}}, nil
	}
	if _, e = s.process(context.Background(), Job{JobID: "job", RequestID: "request", Source: "manual"}); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("retry created duplicate")
	}
}
func TestProcessPostingAndAmbiguity(t *testing.T) {
	pending := bankRecord()
	posted := pending
	posted.Status = "posted"
	posted.ProvisionalID = "posted"
	posted.Date = "2026-10-28"
	entry := &Entry{Key: "entry", Record: pending, YNABID: "existing"}
	s, _ := setupProcess(t, envelope(posted), []*Entry{entry})
	calls := 0
	updateEQTransaction = func(_ context.Context, tx *types.YNABTransaction, d string, n int64, status string) (*types.YNABTransaction, error) {
		calls++
		if *tx.Memo != "custom" || *tx.CategoryID != "category" || tx.Cleared != "reconciled" || !tx.Approved {
			t.Fatal("metadata lost")
		}
		return tx, nil
	}
	if _, e := s.process(context.Background(), Job{JobID: "job", RequestID: "request"}); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("posting not reconciled")
	}
	other := *entry
	other.Key = "other"
	other.Record.ProvisionalID = "other"
	s, _ = setupProcess(t, envelope(posted), []*Entry{entry, &other})
	out, e := s.process(context.Background(), Job{JobID: "job", RequestID: "request", DryRun: true})
	if e != nil || out.(map[string]any)["review"] != 1 {
		t.Fatal(out, e)
	}
}
func TestProcessDryRunAndInvalidResults(t *testing.T) {
	for _, result := range []Result{{Version: 2, JobID: "job", RequestID: "request"}, {Version: 1, JobID: "other", RequestID: "request"}, {Version: 1, JobID: "job", RequestID: "wrong"}, envelope(Record{Date: "bad"})} {
		s, _ := setupProcess(t, result, nil)
		if _, e := s.process(context.Background(), Job{JobID: "job", RequestID: "request", DryRun: true}); e == nil {
			t.Fatal("invalid result accepted", result)
		}
	}
	s, writes := setupProcess(t, envelope(bankRecord()), nil)
	createEQTransaction = func(context.Context, string, string, int64, string, string, string, *types.TransactionOverride) (*types.YNABTransaction, error) {
		t.Fatal("dry run wrote YNAB")
		return nil, nil
	}
	if _, e := s.process(context.Background(), Job{JobID: "job", RequestID: "request", DryRun: true}); e != nil {
		t.Fatal(e)
	}
	if *writes != 0 {
		t.Fatal("dry run wrote ledger")
	}
	s, _ = setupProcess(t, envelope(), nil)
	out, e := s.process(context.Background(), Job{JobID: "job", RequestID: "request", Purpose: "maintain-session"})
	if e != nil || out.(map[string]any)["outcome"] != "session-ready" {
		t.Fatal(out, e)
	}
}
func TestMaintenanceRetryIsIdempotent(t *testing.T) {
	state := maintenanceState{}
	notifications := 0
	keys := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(r.Header.Get("X-Amz-Target"), "GetItem"):
			raw, _ := json.Marshal(state)
			json.NewEncoder(w).Encode(map[string]any{"Item": map[string]any{"data": map[string]string{"S": string(raw)}}})
		case strings.Contains(r.Header.Get("X-Amz-Target"), "TransactWriteItems"):
			v := body["TransactItems"].([]any)[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)["data"].(map[string]any)["S"].(string)
			json.Unmarshal([]byte(v), &state)
			fmt.Fprint(w, `{}`)
		case strings.Contains(r.Header.Get("X-Amz-Target"), "PutItem"):
			k := body["Item"].(map[string]any)["key"].(map[string]any)["S"].(string)
			if keys[k] {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"__type":"ConditionalCheckFailedException"}`)
			} else {
				keys[k] = true
				fmt.Fprint(w, `{}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	cfg := aws.Config{Region: "test", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client()}
	s := &Store{db: dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(server.URL) }), s3: s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true }), table: "ledger", bucket: "private"}
	old := sendNotification
	t.Cleanup(func() { sendNotification = old })
	sendNotification = func(context.Context, string, notify.NotificationOptions) error { notifications++; return nil }
	for _, id := range []string{"first", "first", "second", "second", "third"} {
		if _, e := s.maintenance(context.Background(), Job{JobID: id, Source: "scheduled"}); e != nil {
			t.Fatal(e)
		}
	}
	if state.Count != 3 || notifications != 1 {
		t.Fatal(state, notifications)
	}
	if e := s.resetMaintenance(context.Background(), Job{JobID: "recovered", Source: "scheduled"}, true); e != nil {
		t.Fatal(e)
	}
	if state.Count != 0 || state.Episode != "" {
		t.Fatal(state)
	}
}
func TestCoordinatorLifecycleAndWorkerEnvelope(t *testing.T) {
	s, _ := setupProcess(t, envelope(), nil)
	old := makeStore
	t.Cleanup(func() { makeStore = old })
	makeStore = func(context.Context) (*Store, error) { return s, nil }
	call := func(request Request) (any, error) {
		raw, _ := json.Marshal(request)
		return Handle(context.Background(), raw)
	}
	request := Request{Action: "prepare", Execution: "job", Job: Job{Version: 1, Source: "manual", DryRun: true}}
	out, e := call(request)
	if e != nil {
		t.Fatal(e)
	}
	prepared := out.(map[string]any)
	job := prepared["job"].(Job)
	if prepared["acquired"] != true || job.RequestID == "" || job.Purpose != "retrieve" {
		t.Fatal(prepared)
	}
	request = Request{Action: "process", Job: Job{Version: 1, JobID: "job", RequestID: "request", Source: "manual", DryRun: true, Purpose: "maintain-session"}}
	if _, e = call(request); e == nil {
		t.Fatal("unconfirmed worker accepted")
	}
	request.Worker.Transport = "api"
	request.Worker.JobID = "job"
	request.Worker.RequestID = "wrong"
	if _, e = call(request); e == nil {
		t.Fatal("wrong request accepted")
	}
	request.Worker.RequestID = "request"
	if _, e = call(request); e != nil {
		t.Fatal(e)
	}
	request.Action = "finish"
	out, e = call(request)
	if e != nil || out.(map[string]any)["released"] != true {
		t.Fatal(out, e)
	}
	request.Action = "fail"
	if _, e = call(request); e != nil {
		t.Fatal(e)
	}
	request.Action = "unknown"
	if _, e = call(request); e == nil {
		t.Fatal("unknown action accepted")
	}
}
func TestNotificationFailureRemainsRetryable(t *testing.T) {
	puts, deletes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("X-Amz-Target"), "PutItem") {
			puts++
		}
		if strings.Contains(r.Header.Get("X-Amz-Target"), "DeleteItem") {
			deletes++
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	cfg := aws.Config{Region: "test", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client()}
	s := &Store{db: dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(server.URL) }), table: "ledger"}
	old := sendNotification
	t.Cleanup(func() { sendNotification = old })
	sendNotification = func(_ context.Context, _ string, opts notify.NotificationOptions) error {
		if opts.Sound != "cashregister" || opts.Priority != 1 {
			t.Fatal(opts)
		}
		return fmt.Errorf("offline")
	}
	if e := s.notifyOnce(context.Background(), "purchase", "hello", "Transaction Approved"); e == nil {
		t.Fatal("failure ignored")
	}
	if puts != 1 || deletes != 1 {
		t.Fatal(puts, deletes)
	}
	sendNotification = func(context.Context, string, notify.NotificationOptions) error { return nil }
	if e := s.notifyOnce(context.Background(), "purchase", "hello", "Transaction Approved"); e != nil {
		t.Fatal(e)
	}
}
