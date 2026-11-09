package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
)

type Baseline struct {
	Approved    bool              `json:"approved"`
	At          time.Time         `json:"at"`
	Seen        map[string]bool   `json:"seen"`
	Links       map[string]string `json:"links"`
	Settlements map[string]Write  `json:"settlements,omitempty"`
	NewRecords  map[string]bool   `json:"newRecords"`
	SnapshotKey string            `json:"snapshotKey"`
}
type Write struct {
	Status        string `json:"status"`
	TransactionID string `json:"transactionId"`
	AccountID     string `json:"accountId"`
	Amount        int64  `json:"amount"`
	Date          string `json:"date"`
	ImportID      string `json:"importId,omitempty"`
	// Alert writes keep what the alert stated and what was written, so a later
	// bank credit can recognize an echoed refund and prove it was not edited.
	Description string `json:"description,omitempty"`
	AlertDate   string `json:"alertDate,omitempty"`
	Stale       bool   `json:"stale,omitempty"`
	PayeeID     string `json:"payeeId,omitempty"`
	PayeeName   string `json:"payeeName,omitempty"`
	CategoryID  string `json:"categoryId,omitempty"`
	Memo        string `json:"memo,omitempty"`
	Decision    string `json:"decision,omitempty"`
	// Suggestion records what the importer proposed and why.
	Suggestion *learn.Suggestion `json:"suggestion,omitempty"`
}

func writeFailure(cause error, operation string) error {
	if bank.Classify(cause).Kind == bank.Invalid {
		return cause
	}
	return bank.Fail(bank.Uncertain, operation)
}

func bankStateError() error { return bank.Fail(bank.Invalid, "state") }
func (e *Engine) mapping(ctx context.Context, s bank.Snapshot) (map[string]service.YNABAccount, error) {
	all, err := service.GetAccounts(ctx)
	if err != nil {
		return nil, err
	}
	mapped := map[string]service.YNABAccount{}
	used := map[string]bool{}
	excluded := map[string]bool{}
	for _, id := range e.Settings.Banks[e.Job.Bank].ExcludedAccounts {
		excluded[id] = true
	}
	for _, a := range s.Accounts {
		if excluded[a.ID] {
			continue
		}
		for _, target := range all {
			if !target.Closed && !target.Deleted && target.Note != nil && strings.Contains(*target.Note, a.ID) {
				if mapped[a.ID].ID != "" || used[target.ID] {
					return nil, bank.Fail(bank.Invalid, "account-mapping")
				}
				mapped[a.ID] = target
				used[target.ID] = true
			}
		}
		if mapped[a.ID].ID == "" {
			return nil, bank.Fail(bank.Invalid, "account-mapping")
		}
	}
	return mapped, nil
}

func (e *Engine) posted(ctx context.Context, s bank.Snapshot, b Baseline) error {
	mapping, err := e.mapping(ctx, s)
	if err != nil {
		return err
	}
	var cache *learning
	for _, r := range s.Records {
		target := mapping[r.AccountID]
		if target.ID == "" {
			continue
		}
		if b.Seen[r.AccountID+"#"+r.ProvisionalID] {
			continue
		}
		id := r.ProvisionalID
		operation := "WRITE#" + r.AccountID + "#" + bank.Hash(id)
		var previous Write
		_, err = e.get(ctx, operation, &previous)
		if err != nil {
			return err
		}
		if previous.Status == "complete" {
			continue
		}
		if previous.Status == "started" {
			rows, err := service.GetAccountTransactions(ctx, target.ID)
			if err != nil {
				return err
			}
			for _, tx := range rows {
				if !tx.Deleted && tx.ImportID != nil && *tx.ImportID == id {
					previous.Status, previous.TransactionID = "complete", tx.ID
					break
				}
			}
			if previous.Status != "complete" {
				return bank.Fail(bank.Uncertain, "transaction-write")
			}
			if !e.Job.DryRun {
				if err := e.put(ctx, operation, previous, e.Job.ID); err != nil {
					return err
				}
			}
			continue
		}
		decision := e.enrich(ctx, &cache, r.Description, r.Amount, r.Date, target.ID)
		slog.Info("posted transaction preview", "bank", e.Job.Bank, "date", r.Date, "amount", r.Amount, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "dryRun", e.Job.DryRun)
		if e.Job.DryRun {
			continue
		}
		if err = e.put(ctx, operation, Write{Status: "started", AccountID: target.ID, Amount: r.Amount, Date: r.Date, ImportID: id, Description: r.Description, Decision: decision.Source(), Suggestion: &decision.Suggestion}, e.Job.ID); err != nil {
			return err
		}
		tx, err := service.CreateEQTransaction(ctx, target.ID, r.Date, r.Amount, decision.Enrichment, id, "posted")
		if err != nil {
			return writeFailure(err, "transaction-write")
		}
		if err = e.put(ctx, operation, Write{Status: "complete", TransactionID: tx.ID, ImportID: id, Description: r.Description, Decision: decision.Source(), Suggestion: &decision.Suggestion}, e.Job.ID); err != nil {
			return err
		}
		e.recordSuggestion(ctx, decision, tx, id)
		if r.Amount > 0 {
			if err = e.clearEchoes(ctx, target.ID, r, tx.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) balances(ctx context.Context, s bank.Snapshot) error {
	mapping, err := e.mapping(ctx, s)
	if err != nil {
		return err
	}
	if err = service.ValidateAdjustmentPayee(ctx, e.Settings.AdjustmentPayeeID); err != nil {
		return err
	}
	zone, _ := time.LoadLocation(e.Settings.Timezone)
	day := time.Now().In(zone).Format("2006-01-02")
	for _, a := range s.Accounts {
		target := mapping[a.ID]
		if target.ID == "" {
			continue
		}
		var uncertain Write
		found, err := e.get(ctx, "BALANCE#"+a.ID, &uncertain)
		if err != nil {
			return err
		}
		if found && uncertain.Status == "started" {
			return bank.Fail(bank.Uncertain, "balance-adjustment")
		}
		amount := a.Balance - target.Balance
		if amount == 0 || e.Job.DryRun {
			continue
		}
		w := Write{Status: "started", AccountID: target.ID, Amount: amount, Date: day}
		if err = e.put(ctx, "BALANCE#"+a.ID, w, e.Job.ID); err != nil {
			return err
		}
		registration, _ := banks.Find(e.Job.Bank)
		tx, err := service.CreateBalanceAdjustment(ctx, target.ID, day, amount, e.Settings.AdjustmentPayeeID, registration.Name)
		if err != nil {
			return bank.Fail(bank.Uncertain, "balance-adjustment")
		}
		w.Status = "complete"
		w.TransactionID = tx.ID
		if err = e.put(ctx, "BALANCE#"+a.ID, w, e.Job.ID); err != nil {
			return err
		}
	}
	return nil
}

// echoWaits are the delays, in seconds, a late alert waits for its bank credit
// to reach YNAB before it is treated as a genuine purchase.
var echoWaits = []int{60, 300, 900}

// notificationImport writes the purchase a card alert describes. It returns a
// positive wait when the alert must be re-checked later.
func (e *Engine) notificationImport(ctx context.Context) (int, error) {
	var previous Write
	key := "WRITE#notification#" + e.Job.ID
	if _, err := e.get(ctx, key, &previous); err != nil {
		return 0, err
	}
	switch previous.Status {
	case "complete", "echo":
		return 0, nil
	case "started":
		// Recover the recorded write before invoking AI again. A new interpretation
		// must never change an operation whose financial outcome is uncertain.
		if e.Job.DryRun {
			return 0, nil
		}
		transactions, err := service.GetAccountTransactions(ctx, previous.AccountID)
		if err != nil {
			return 0, err
		}
		for _, tx := range transactions {
			if !tx.Deleted && tx.ImportID != nil && *tx.ImportID == previous.ImportID {
				return 0, e.completeNotification(ctx, key, previous, tx)
			}
		}
		return 0, bank.Fail(bank.Uncertain, "notification-import")
	}
	alert, err := service.ExtractAlert(ctx, e.Job.Text)
	if err != nil {
		slog.Warn("notification extraction failed", "job", e.Job.ID, "error", err)
		return 0, bank.Fail(bank.Temporary, "notification-extract")
	}
	zone, _ := time.LoadLocation(e.Settings.Timezone)
	received := e.Job.OccurredAt.In(zone).Format("2006-01-02")
	date := received
	if alert.Date != "" {
		date = alert.Date
	}
	// A bank can resend an alert when a refund or credit posts. Those echoes
	// arrive after the date they state; a genuine purchase alert is immediate.
	stale := alert.Date != "" && alert.Date < received
	if stale {
		credit, err := e.findEchoCredit(ctx, alert, received)
		if err != nil {
			return 0, err
		}
		if credit != nil {
			slog.Info("alert matched a posted credit", "job", e.Job.ID, "credit", credit.ID, "source", alert.Source)
			if e.Job.DryRun {
				return 0, nil
			}
			return 0, e.Store.PutMany(ctx, e.Lease, map[string]any{
				"ECHO#" + credit.ID: map[string]any{"jobId": e.Job.ID, "action": "skipped-alert"},
				key:                 Write{Status: "echo", TransactionID: credit.ID, AccountID: e.Job.AccountID, Amount: alert.Amount, Date: date, Description: alert.Descriptor, AlertDate: alert.Date, Stale: true},
			})
		}
		if e.Job.MissingAttempt < len(echoWaits) {
			if e.Job.MissingAttempt == 0 && !e.Job.DryRun {
				if err := e.requestRetrieval(ctx); err != nil {
					return 0, err
				}
			}
			wait := echoWaits[e.Job.MissingAttempt]
			e.Job.MissingAttempt++
			return wait, nil
		}
		slog.Info("late alert has no matching credit; importing it as a purchase", "job", e.Job.ID)
	}
	amount := -alert.Amount
	if _, err := time.ParseInLocation("2006-01-02", date, zone); err != nil {
		return 0, err
	}
	var cache *learning
	decision := e.enrich(ctx, &cache, alert.Descriptor, amount, date, e.Job.AccountID)
	slog.Info("notification preview", "job", e.Job.ID, "date", date, "amount", amount, "parser", alert.Source, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "stale", stale, "dryRun", e.Job.DryRun)
	importID := "ALERT:" + bank.Hash(e.Job.ID)[:30]
	if e.Job.DryRun {
		return 0, nil
	}
	intent := Write{Status: "started", AccountID: e.Job.AccountID, Amount: amount, Date: date, ImportID: importID, Description: alert.Descriptor, AlertDate: alert.Date, Stale: stale, Decision: decision.Source(), Suggestion: &decision.Suggestion}
	if err = e.put(ctx, key, intent, e.Job.ID); err != nil {
		return 0, err
	}
	tx, err := service.CreateEQTransaction(ctx, e.Job.AccountID, date, amount, decision.Enrichment, importID, "pending")
	if err != nil {
		return 0, writeFailure(err, "notification-import")
	}
	e.recordSuggestion(ctx, decision, tx, "")
	return 0, e.completeNotification(ctx, key, intent, tx)
}

// completeNotification records the created transaction exactly as YNAB returned
// it, so a later check can tell whether the user has edited it.
func (e *Engine) completeNotification(ctx context.Context, key string, intent Write, tx *types.YNABTransaction) error {
	done := intent
	done.Status, done.TransactionID = "complete", tx.ID
	done.PayeeID, done.PayeeName, done.CategoryID, done.Memo = deref(tx.PayeeID), deref(tx.PayeeName), deref(tx.CategoryID), deref(tx.Memo)
	payee := done.PayeeName
	if payee == "" {
		payee = intent.Description
	}
	notice := Notification{ID: "purchase#" + e.Job.ID, JobID: e.Job.ID, Title: "Transaction Approved", Message: fmt.Sprintf("A transaction of $%.2f at %s was approved on your %s.", -float64(tx.Amount)/1000, payee, tx.AccountName), Status: "pending", At: time.Now()}
	return e.Store.PutMany(ctx, e.Lease, map[string]any{key: done, "NOTICE#" + notice.ID: notice})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// findEchoCredit looks in YNAB for the bank credit a late alert repeats: an
// inflow of the same amount, from the same descriptor, posted between the date
// the alert states and the day it arrived, not already claimed by another alert.
func (e *Engine) findEchoCredit(ctx context.Context, alert service.Alert, received string) (*types.YNABTransaction, error) {
	rows, err := service.GetAccountTransactions(ctx, e.Job.AccountID)
	if err != nil {
		return nil, err
	}
	var best *types.YNABTransaction
	for _, tx := range rows {
		if tx.Deleted || tx.Amount != alert.Amount || tx.Date < alert.Date || tx.Date > addDays(received, 1) {
			continue
		}
		descriptor := ""
		for _, s := range []*string{tx.ImportPayeeNameOriginal, tx.ImportPayeeName} {
			if s != nil && *s != "" {
				descriptor = *s
				break
			}
		}
		if !learn.Similar(descriptor, alert.Descriptor) {
			continue
		}
		var claim map[string]any
		claimed, err := e.get(ctx, "ECHO#"+tx.ID, &claim)
		if err != nil {
			return nil, err
		}
		if claimed {
			continue
		}
		if best == nil || tx.Date < best.Date {
			best = tx
		}
	}
	return best, nil
}

// requestRetrieval starts an immediate import from the alert's bank, so a
// credit that has posted but not yet reached YNAB can settle a late alert.
func (e *Engine) requestRetrieval(ctx context.Context) error {
	registration, ok := banks.Find(e.Job.Bank)
	if !ok || !e.Settings.Banks[e.Job.Bank].Enabled {
		return nil
	}
	_, err := e.Submit(ctx, Job{Version: 1, ID: "echo-" + bank.Hash(e.Job.ID)[:32], Bank: registration.ID, Source: "scheduled", Purpose: "retrieve"})
	return err
}

// clearEchoes runs when a bank credit is imported. An alert this importer wrote
// that the credit explains is removed if it is the only match and the user has
// not touched it; otherwise the matches are left as they are.
func (e *Engine) clearEchoes(ctx context.Context, account string, credit Record, creditID string) error {
	var claim map[string]any
	if claimed, err := e.get(ctx, "ECHO#"+creditID, &claim); err != nil || claimed {
		return err
	}
	raw, err := e.Store.List(ctx, "BANK#"+e.Job.Bank, "WRITE#notification#")
	if err != nil {
		return err
	}
	type match struct {
		w Write
	}
	var matches []match
	for _, item := range raw {
		var w Write
		if json.Unmarshal(item, &w) != nil || w.Status != "complete" || !w.Stale || w.AccountID != account || w.Amount != -credit.Amount {
			continue
		}
		if w.AlertDate > credit.Date || w.AlertDate < addDays(credit.Date, -5) || !learn.Similar(w.Description, credit.Description) {
			continue
		}
		matches = append(matches, match{w: w})
	}
	if len(matches) == 0 {
		return nil
	}
	rows, err := service.GetAccountTransactions(ctx, account)
	if err != nil {
		return err
	}
	byID := map[string]*types.YNABTransaction{}
	for _, tx := range rows {
		byID[tx.ID] = tx
	}
	if len(matches) == 1 {
		w := matches[0].w
		if tx := byID[w.TransactionID]; tx != nil && !tx.Deleted && untouched(tx, w) {
			if err := service.DeleteTransaction(ctx, tx.ID); err != nil {
				return err
			}
			slog.Info("removed alert repeated by a bank credit", "credit", creditID, "transaction", tx.ID)
			return e.put(ctx, "ECHO#"+creditID, map[string]any{"jobId": e.Job.ID, "action": "removed", "transactionId": tx.ID}, e.Job.ID)
		}
	}
	var left []string
	for _, m := range matches {
		left = append(left, m.w.TransactionID)
	}
	slog.Info("left alerts a bank credit may repeat", "credit", creditID, "count", len(left))
	return e.put(ctx, "ECHO#"+creditID, map[string]any{"jobId": e.Job.ID, "action": "left", "transactionIds": left}, e.Job.ID)
}

// untouched reports whether a transaction is still exactly as the importer wrote it.
func untouched(tx *types.YNABTransaction, w Write) bool {
	return !tx.Approved && tx.Cleared == "uncleared" && tx.Amount == w.Amount && tx.Date == w.Date &&
		deref(tx.PayeeID) == w.PayeeID && deref(tx.CategoryID) == w.CategoryID && deref(tx.Memo) == w.Memo && deref(tx.FlagColor) == ""
}

func addDays(date string, days int) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, days).Format("2006-01-02")
}

// alertImport writes an amount-only purchase for an alert that names no merchant.
// It never signs in to the bank or calls a model; a later cleared import supplies
// the details. The write has no YNAB import identity, so an interrupted create is
// held for review and never re-sent or inferred from amount and date.
func (e *Engine) alertImport(ctx context.Context) error {
	account := e.Settings.Banks[e.Job.Bank].AlertAccountID
	if account == "" {
		return bank.Fail(bank.Invalid, "alert-account")
	}
	if e.Job.AlertAmount <= 0 || e.Job.AlertDate == "" {
		return bank.Fail(bank.Invalid, "alert-amount")
	}
	// An alert the earlier bank-retrieval path already linked must not be written again.
	var legacy map[string]any
	if linked, err := e.get(ctx, "alert#"+bank.Hash(e.Job.MessageID), &legacy); err != nil || linked {
		return err
	}
	key := "WRITE#alert#" + e.Job.ID
	var previous Write
	if _, err := e.get(ctx, key, &previous); err != nil {
		return err
	}
	switch previous.Status {
	case "complete":
		return nil
	case "started":
		return bank.Fail(bank.Uncertain, "alert-import")
	}
	slog.Info("amount-only alert preview", "job", e.Job.ID, "date", e.Job.AlertDate, "amount", -e.Job.AlertAmount, "dryRun", e.Job.DryRun)
	if e.Job.DryRun {
		return nil
	}
	amount := -e.Job.AlertAmount
	if err := e.put(ctx, key, Write{Status: "started", AccountID: account, Amount: amount, Date: e.Job.AlertDate}, e.Job.ID); err != nil {
		return err
	}
	tx, err := service.CreateAmountOnlyTransaction(ctx, account, e.Job.AlertDate, amount)
	if err != nil {
		return writeFailure(err, "alert-import")
	}
	name := tx.AccountName
	if name == "" {
		name = purchaseAccount(e.Job.Bank)
	}
	notice := Notification{ID: "purchase#" + e.Job.ID, JobID: e.Job.ID, Title: "Transaction Approved", Message: fmt.Sprintf("A transaction of $%.2f was approved on your %s.", float64(e.Job.AlertAmount)/1000, name), Status: "pending", At: time.Now()}
	return e.Store.PutMany(ctx, e.Lease, map[string]any{key: Write{Status: "complete", TransactionID: tx.ID, AccountID: account, Amount: amount, Date: e.Job.AlertDate}, "NOTICE#" + notice.ID: notice})
}

func (e *Engine) ledger(ctx context.Context) ([]*Entry, error) {
	rows, err := e.Store.List(ctx, "BANK#"+e.Job.Bank, "tx#")
	if err != nil {
		return nil, err
	}
	var out []*Entry
	for _, raw := range rows {
		var entry Entry
		if json.Unmarshal(raw, &entry) != nil {
			return nil, bankStateError()
		}
		out = append(out, &entry)
	}
	return out, nil
}
