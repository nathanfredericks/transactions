package engine

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	d "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/state"
	"github.com/nathanfredericks/transactions/internal/types"
)

// testEngine returns an engine on a fresh DynamoDB Local table, holding the
// lease for bankName. Tests skip when DYNAMODB_LOCAL is not set.
func testEngine(t *testing.T, bankName string, job Job) *Engine {
	t.Helper()
	endpoint := os.Getenv("DYNAMODB_LOCAL")
	if endpoint == "" {
		t.Skip("DYNAMODB_LOCAL not set")
	}
	ctx := context.Background()
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), Credentials: credentials.NewStaticCredentialsProvider("local", "local", "")})
	table := "test-" + uuid.NewString()
	if _, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            &table,
		BillingMode:          d.BillingModePayPerRequest,
		AttributeDefinitions: []d.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: d.ScalarAttributeTypeS}, {AttributeName: aws.String("sk"), AttributeType: d.ScalarAttributeTypeS}},
		KeySchema:            []d.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: d.KeyTypeHash}, {AttributeName: aws.String("sk"), KeyType: d.KeyTypeRange}},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: &table}) })
	store := &state.Store{DB: db, Table: table}
	lease, ok, err := store.Acquire(ctx, bankName, "test")
	if err != nil || !ok {
		t.Fatalf("acquire lease: %v %v", ok, err)
	}
	job.Bank = bankName
	return &Engine{
		Store: store,
		Settings: &config.Settings{Timezone: "America/Halifax", ImportsEnabled: true, Banks: map[string]config.BankSettings{
			"rogers-bank": {Enabled: true},
			"eq-bank":     {Enabled: true, AlertAccountID: "acct-card"},
		}},
		Lease: lease,
		Job:   job,
	}
}

// fakeYNAB stands in for every YNAB and model call the import paths make.
type fakeYNAB struct {
	rows     []*types.YNABTransaction
	created  []types.PayeeChoice
	enriched []types.Enrichment
	dates    []string
	deleted  []string
	flagged  []string
	submits  []Job
	model    func(learn.ModelInput) (learn.ModelOutput, error)
	mirror   *learn.Mirror
}

// testMirror holds one approved purchase from payee-1, so lookalike descriptors
// offer it to the model as a candidate.
func testMirror() *learn.Mirror {
	m := learn.NewMirror()
	m.ApplyPayees([]*learn.Payee{{ID: "payee-1", Name: "Chosen"}})
	m.ApplyCategories([]*learn.Category{{ID: "cat-1", Name: "Eating Out", GroupName: "Frequent"}}, "2000-01-01")
	desc, payee, cat := "SH VENDING MACHINES 22", "payee-1", "cat-1"
	m.ApplyTransactions([]*types.YNABTransaction{{ID: "old-1", Date: "2026-01-01", Amount: -2000, Approved: true, PayeeID: &payee, CategoryID: &cat, ImportPayeeNameOriginal: &desc}})
	return m
}

func stubYNAB(t *testing.T) *fakeYNAB {
	t.Helper()
	f := &fakeYNAB{mirror: testMirror(), model: func(in learn.ModelInput) (learn.ModelOutput, error) {
		for _, c := range in.Candidates {
			if c.ID == "payee-1" {
				return learn.ModelOutput{PayeeID: "payee-1", CategoryID: "cat-1"}, nil
			}
		}
		return learn.ModelOutput{PayeeID: "none", NewPayeeName: "New Place", CategoryID: "cat-1"}, nil
	}}
	old := []any{extractAlert, loadLearning, chooseWithModel, createTransaction, accountTransaction, deleteTransaction, flagTransaction, findRules, submitJob}
	extractAlert = service.ExtractAlert
	loadLearning = func(context.Context, *Engine) (*learning, error) { return buildLearning(f.mirror, nil), nil }
	chooseWithModel = func(_ context.Context, in learn.ModelInput) (learn.ModelOutput, string, error) {
		out, err := f.model(in)
		return out, "test-model", err
	}
	createTransaction = func(_ context.Context, account, date string, amount int64, enrichment types.Enrichment, _, status string, _ *types.TransactionOverride) (*types.YNABTransaction, error) {
		payee := enrichment.Payee
		f.created = append(f.created, payee)
		f.enriched = append(f.enriched, enrichment)
		f.dates = append(f.dates, date)
		tx := &types.YNABTransaction{ID: uuid.NewString(), AccountID: account, Date: date, Amount: amount, Cleared: "uncleared", AccountName: "Card"}
		if payee.ID != "" {
			tx.PayeeID = &payee.ID
		}
		tx.PayeeName = &payee.Name
		f.rows = append(f.rows, tx)
		return tx, nil
	}
	accountTransaction = func(context.Context, string) ([]*types.YNABTransaction, error) { return f.rows, nil }
	deleteTransaction = func(_ context.Context, id string) error { f.deleted = append(f.deleted, id); return nil }
	flagTransaction = func(_ context.Context, id, _ string) error { f.flagged = append(f.flagged, id); return nil }
	findRules = func(context.Context, float64, string, time.Time) (*types.TransactionOverride, error) { return nil, nil }
	submitJob = func(_ context.Context, _ *Engine, j Job) error { f.submits = append(f.submits, j); return nil }
	t.Cleanup(func() {
		extractAlert = old[0].(func(context.Context, string) (service.Alert, error))
		loadLearning = old[1].(func(context.Context, *Engine) (*learning, error))
		chooseWithModel = old[2].(func(context.Context, learn.ModelInput) (learn.ModelOutput, string, error))
		createTransaction = old[3].(func(context.Context, string, string, int64, types.Enrichment, string, string, *types.TransactionOverride) (*types.YNABTransaction, error))
		accountTransaction = old[4].(func(context.Context, string) ([]*types.YNABTransaction, error))
		deleteTransaction = old[5].(func(context.Context, string) error)
		flagTransaction = old[6].(func(context.Context, string, string) error)
		findRules = old[7].(func(context.Context, float64, string, time.Time) (*types.TransactionOverride, error))
		submitJob = old[8].(func(context.Context, *Engine, Job) error)
	})
	return f
}

const footer = " https://rogersbank.com/en/help_centre_answers/managing_your_account#q174 Thank you for choosing Rogers Bank."

func rogersJob(id, text, occurred string) Job {
	at, _ := time.Parse(time.RFC3339, occurred)
	return Job{ID: id, Source: "notification", Purpose: "retrieve", AccountID: "acct-rogers", Text: text, OccurredAt: at}
}

func credit(id, date string, amount int64, descriptor string) *types.YNABTransaction {
	return &types.YNABTransaction{ID: id, Date: date, Amount: amount, Cleared: "cleared", Approved: true, ImportPayeeName: &descriptor}
}

func TestNotificationWritesChosenPayeeOnStatedDate(t *testing.T) {
	f := stubYNAB(t)
	// Sent just after midnight: the stated purchase date is the day before.
	e := testEngine(t, "rogers-bank", rogersJob("job-1", "$4.15 on Oct 25, 2026 on your credit card ending in 0000. Details: SH Vending."+footer, "2026-10-26T03:03:12Z"))
	f.submits = nil
	wait, err := e.notificationImport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A late alert with no matching credit waits for the bank before importing.
	if wait == 0 || len(f.created) != 0 || len(f.submits) != 1 || f.submits[0].Bank != "rogers-bank" {
		t.Fatalf("wait=%d created=%v submits=%v", wait, f.created, f.submits)
	}
	for range echoWaits {
		if wait, err = e.notificationImport(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if wait != 0 || len(f.created) != 1 || f.created[0].ID != "payee-1" || f.dates[0] != "2026-10-25" || len(f.submits) != 1 {
		t.Fatalf("wait=%d created=%v dates=%v submits=%d", wait, f.created, f.dates, len(f.submits))
	}
	var w Write
	if _, err := e.get(context.Background(), "WRITE#notification#job-1", &w); err != nil || w.Status != "complete" || !w.Stale || w.PayeeID != "payee-1" || w.Description != "SH Vending" {
		t.Fatalf("write = %+v %v", w, err)
	}
	// A payee with history takes its category from history, not the model.
	if f.enriched[0].CategoryID != "cat-1" || w.Suggestion == nil || w.Suggestion.PayeeSource != "model" || w.Suggestion.CategorySource != "history" || w.Suggestion.Model != "test-model" {
		t.Fatalf("enrichment %+v suggestion %+v", f.enriched[0], w.Suggestion)
	}
	var rec learn.Suggestion
	if found, err := e.Store.Get(context.Background(), learningPK, "TX#"+f.rows[0].ID, &rec); err != nil || !found || rec.Descriptor != "SH Vending" || rec.TransactionID != f.rows[0].ID {
		t.Fatalf("suggestion record %+v %v %v", rec, found, err)
	}
	var n Notification
	if found, _ := e.get(context.Background(), "NOTICE#purchase#job-1", &n); !found {
		t.Fatal("no purchase notice")
	}
}

func TestImmediateAlertWritesWithoutWaiting(t *testing.T) {
	f := stubYNAB(t)
	// A credit for the same amount exists, but an immediate alert is a purchase.
	f.rows = []*types.YNABTransaction{credit("c1", "2026-11-04", 3790, "STORE A")}
	e := testEngine(t, "rogers-bank", rogersJob("job-2", "$3.79 on Nov 04, 2026 on your credit card ending in 0000. Details: STORE A."+footer, "2026-11-04T15:00:00Z"))
	wait, err := e.notificationImport(context.Background())
	if err != nil || wait != 0 || len(f.created) != 1 || len(f.submits) != 0 {
		t.Fatalf("wait=%d err=%v created=%v submits=%v", wait, err, f.created, f.submits)
	}
}

func TestLateAlertRepeatingAPostedCreditIsSkipped(t *testing.T) {
	f := stubYNAB(t)
	f.rows = []*types.YNABTransaction{credit("c1", "2026-10-22", 18100, "CashBack / Remises"), credit("c2", "2026-10-22", 24720, "CashBack / Remises")}
	ctx := context.Background()
	e := testEngine(t, "rogers-bank", rogersJob("job-3", "$18.10 on Oct 19, 2026 on your credit card ending in 0000. Details: CashBack / Remises."+footer, "2026-10-23T11:02:06Z"))
	wait, err := e.notificationImport(ctx)
	if err != nil || wait != 0 || len(f.created) != 0 {
		t.Fatalf("wait=%d err=%v created=%v", wait, err, f.created)
	}
	var w Write
	if _, err := e.get(ctx, "WRITE#notification#job-3", &w); err != nil || w.Status != "echo" || w.TransactionID != "c1" {
		t.Fatalf("write = %+v %v", w, err)
	}
	if found, _ := e.get(ctx, "NOTICE#purchase#job-3", &Notification{}); found {
		t.Fatal("echo sent a purchase notice")
	}
	// The claimed credit cannot explain a second alert of the same amount.
	e.Job = rogersJob("job-4", "$18.10 on Oct 19, 2026 on your credit card ending in 0000. Details: CashBack / Remises."+footer, "2026-10-23T11:05:00Z")
	e.Job.Bank = "rogers-bank"
	if wait, err = e.notificationImport(ctx); err != nil || wait == 0 {
		t.Fatalf("second alert: wait=%d err=%v", wait, err)
	}
	// A credit from a different descriptor does not explain the alert.
	e.Job = rogersJob("job-5", "$24.72 on Oct 19, 2026 on your credit card ending in 0000. Details: OTHER SHOP."+footer, "2026-10-23T11:00:00Z")
	e.Job.Bank = "rogers-bank"
	if wait, err = e.notificationImport(ctx); err != nil || wait == 0 {
		t.Fatalf("different descriptor: wait=%d err=%v", wait, err)
	}
}

func TestNotificationKeepsDescriptorWhenPayeeChoiceFails(t *testing.T) {
	f := stubYNAB(t)
	f.model = func(learn.ModelInput) (learn.ModelOutput, error) {
		return learn.ModelOutput{}, errors.New("model unavailable")
	}
	e := testEngine(t, "rogers-bank", rogersJob("job-6", "$9.99 on Nov 04, 2026 on your credit card ending in 0000. Details: NEW PLACE 12."+footer, "2026-11-04T15:00:00Z"))
	if _, err := e.notificationImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0] != (types.PayeeChoice{Name: "NEW PLACE 12"}) {
		t.Fatalf("created %v", f.created)
	}
}

func TestNewMerchantTakesTheModelsNameAndCategory(t *testing.T) {
	f := stubYNAB(t)
	e := testEngine(t, "rogers-bank", rogersJob("job-8", "$12.00 on Nov 04, 2026 on your credit card ending in 0000. Details: QQQ ZZZ KIOSK."+footer, "2026-11-04T15:00:00Z"))
	if _, err := e.notificationImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.enriched) != 1 || f.enriched[0] != (types.Enrichment{Payee: types.PayeeChoice{Name: "New Place"}, CategoryID: "cat-1"}) {
		t.Fatalf("enriched %+v", f.enriched)
	}
}

func TestRememberedDescriptorSkipsTheModel(t *testing.T) {
	f := stubYNAB(t)
	f.model = func(learn.ModelInput) (learn.ModelOutput, error) {
		t.Fatal("the model must not be asked when history decides")
		return learn.ModelOutput{}, nil
	}
	e := testEngine(t, "rogers-bank", rogersJob("job-9", "$2.00 on Nov 04, 2026 on your credit card ending in 0000. Details: SH VENDING MACHINES 31."+footer, "2026-11-04T15:00:00Z"))
	if _, err := e.notificationImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.enriched) != 1 || f.enriched[0].Payee.ID != "payee-1" || f.enriched[0].CategoryID != "cat-1" {
		t.Fatalf("enriched %+v", f.enriched)
	}
}

func TestNotificationExtractionFailureRetries(t *testing.T) {
	stubYNAB(t)
	extractAlert = func(context.Context, string) (service.Alert, error) {
		return service.Alert{}, errors.New("provider 503")
	}
	e := testEngine(t, "rogers-bank", rogersJob("job-7", "unrecognized", "2026-11-04T15:00:00Z"))
	if _, err := e.notificationImport(context.Background()); bank.Classify(err).Kind != bank.Temporary {
		t.Fatalf("want temporary, got %v", err)
	}
}

func seedAlertWrite(t *testing.T, e *Engine, job string, w Write) {
	t.Helper()
	if err := e.put(context.Background(), "WRITE#notification#"+job, w, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostedCreditRemovesOnlyAnUntouchedEcho(t *testing.T) {
	ctx := context.Background()
	r := Record{Amount: 5690, Date: "2026-10-29", Description: "Amazon.ca Prime Member"}
	echo := func(id string) Write {
		return Write{Status: "complete", TransactionID: id, AccountID: "acct-rogers", Amount: -5690, Date: "2026-10-28", AlertDate: "2026-10-28", Stale: true, Description: "Amazon.ca Prime Member", PayeeName: "Amazon"}
	}
	pending := func(id string) *types.YNABTransaction {
		return &types.YNABTransaction{ID: id, Date: "2026-10-28", Amount: -5690, Cleared: "uncleared", PayeeName: strPtr("Amazon")}
	}

	f := stubYNAB(t)
	e := testEngine(t, "rogers-bank", Job{ID: "posted-1"})
	f.rows = []*types.YNABTransaction{pending("t1")}
	seedAlertWrite(t, e, "a1", echo("t1"))
	if err := e.clearEchoes(ctx, "acct-rogers", r, "credit-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "t1" || len(f.flagged) != 0 {
		t.Fatalf("deleted %v flagged %v", f.deleted, f.flagged)
	}
	// The same credit is handled once.
	if err := e.clearEchoes(ctx, "acct-rogers", r, "credit-1"); err != nil || len(f.deleted) != 1 {
		t.Fatalf("second pass: %v %v", f.deleted, err)
	}

	// An entry the user approved is flagged, not removed.
	f = stubYNAB(t)
	e = testEngine(t, "rogers-bank", Job{ID: "posted-2"})
	edited := pending("t2")
	edited.Approved = true
	f.rows = []*types.YNABTransaction{edited}
	seedAlertWrite(t, e, "a2", echo("t2"))
	if err := e.clearEchoes(ctx, "acct-rogers", r, "credit-2"); err != nil || len(f.deleted) != 0 || len(f.flagged) != 1 {
		t.Fatalf("edited: deleted %v flagged %v err %v", f.deleted, f.flagged, err)
	}

	// Two possible echoes are both flagged and neither is removed.
	f = stubYNAB(t)
	e = testEngine(t, "rogers-bank", Job{ID: "posted-3"})
	f.rows = []*types.YNABTransaction{pending("t3"), pending("t4")}
	seedAlertWrite(t, e, "a3", echo("t3"))
	seedAlertWrite(t, e, "a4", echo("t4"))
	if err := e.clearEchoes(ctx, "acct-rogers", r, "credit-3"); err != nil || len(f.deleted) != 0 || len(f.flagged) != 2 {
		t.Fatalf("ambiguous: deleted %v flagged %v err %v", f.deleted, f.flagged, err)
	}

	// An immediate (non-late) alert is a genuine purchase and is never touched.
	f = stubYNAB(t)
	e = testEngine(t, "rogers-bank", Job{ID: "posted-4"})
	f.rows = []*types.YNABTransaction{pending("t5")}
	genuine := echo("t5")
	genuine.Stale = false
	seedAlertWrite(t, e, "a5", genuine)
	if err := e.clearEchoes(ctx, "acct-rogers", r, "credit-4"); err != nil || len(f.deleted)+len(f.flagged) != 0 {
		t.Fatalf("genuine: deleted %v flagged %v err %v", f.deleted, f.flagged, err)
	}
}

func strPtr(s string) *string { return &s }

type amountOnlyCall struct {
	account, date string
	amount        int64
}

func stubAmountOnly(t *testing.T, err error) *[]amountOnlyCall {
	t.Helper()
	calls := &[]amountOnlyCall{}
	original := createAmountOnly
	createAmountOnly = func(_ context.Context, account, date string, amount int64) (*types.YNABTransaction, error) {
		*calls = append(*calls, amountOnlyCall{account, date, amount})
		if err != nil {
			return nil, err
		}
		return &types.YNABTransaction{ID: "ynab-1", AccountName: "EQ Card"}, nil
	}
	t.Cleanup(func() { createAmountOnly = original })
	return calls
}

func eqAlert() Job {
	return Job{ID: "job-eq", Source: "alert", Purpose: "retrieve", AlertAmount: 12340, AlertDate: "2026-11-06", MessageID: "msg-1"}
}

func TestAlertImportWritesAmountOnlyOnce(t *testing.T) {
	e := testEngine(t, "eq-bank", eqAlert())
	calls := stubAmountOnly(t, nil)
	ctx := context.Background()
	for range 2 {
		if err := e.alertImport(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(*calls) != 1 || (*calls)[0] != (amountOnlyCall{"acct-card", "2026-11-06", -12340}) {
		t.Fatalf("creates: %+v", *calls)
	}
	var w Write
	if _, err := e.get(ctx, "WRITE#alert#job-eq", &w); err != nil || w.Status != "complete" || w.TransactionID != "ynab-1" {
		t.Fatalf("write = %+v %v", w, err)
	}
	if found, _ := e.get(ctx, "NOTICE#purchase#job-eq", &Notification{}); !found {
		t.Fatal("no purchase notice")
	}
}

func TestAlertImportHoldsInterruptedWrite(t *testing.T) {
	e := testEngine(t, "eq-bank", eqAlert())
	ctx := context.Background()
	stubAmountOnly(t, errors.New("connection reset"))
	if err := e.alertImport(ctx); err == nil {
		t.Fatal("want error")
	}
	calls := stubAmountOnly(t, nil)
	if err := e.alertImport(ctx); bank.Classify(err).Kind != bank.Uncertain || len(*calls) != 0 {
		t.Fatalf("retry: %v, creates %d", err, len(*calls))
	}
}

func TestAlertImportSkipsAlertsAlreadyLinked(t *testing.T) {
	e := testEngine(t, "eq-bank", eqAlert())
	calls := stubAmountOnly(t, nil)
	if err := e.put(context.Background(), "alert#"+bank.Hash("msg-1"), map[string]any{"ynabId": "old"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.alertImport(context.Background()); err != nil || len(*calls) != 0 {
		t.Fatalf("%v creates %d", err, len(*calls))
	}
}

func TestAlertImportRequiresAccountAndAmount(t *testing.T) {
	calls := stubAmountOnly(t, nil)
	e := testEngine(t, "eq-bank", eqAlert())
	e.Settings.Banks["eq-bank"] = config.BankSettings{Enabled: true}
	if err := e.alertImport(context.Background()); bank.Classify(err).Kind != bank.Invalid {
		t.Fatalf("missing account: %v", err)
	}
	e = testEngine(t, "eq-bank", eqAlert())
	e.Job.AlertAmount = 0
	if err := e.alertImport(context.Background()); bank.Classify(err).Kind != bank.Invalid {
		t.Fatalf("missing amount: %v", err)
	}
	e = testEngine(t, "eq-bank", eqAlert())
	e.Job.DryRun = true
	if err := e.alertImport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("invalid or dry-run alerts reached YNAB %d times", len(*calls))
	}
}
