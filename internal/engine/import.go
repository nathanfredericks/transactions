package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/alert"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/ynab"
)

// Write is the record of a notification import, kept so a later bank credit can
// recognize an echoed refund and prove the user has not edited it.
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

// writeFailure classifies a failed YNAB write. A 4xx other than 429 means YNAB
// did not apply it. Anything else is retried by the job, then the next
// schedule: posted writes carry an import ID YNAB dedupes, and alert writes
// can at worst leave one visible, unapproved duplicate.
func writeFailure(err error, operation string) error {
	if f, ok := errors.AsType[*bank.Failure](err); ok {
		return f
	}
	if status, ok := errors.AsType[*ynab.StatusError](err); ok {
		if status.Status == http.StatusTooManyRequests {
			return bank.Fail(bank.Throttled, operation)
		}
		if status.Status < 500 {
			return bank.Fail(bank.Invalid, operation)
		}
	}
	return bank.Fail(bank.Temporary, operation)
}

func bankStateError() error { return bank.Fail(bank.Invalid, "state") }
func (e *Engine) mapping(ctx context.Context, s bank.Snapshot) (map[string]ynab.Account, error) {
	all, err := e.YNAB.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	mapped := map[string]ynab.Account{}
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

// posted imports each posted record that YNAB does not already hold under its
// import ID, deleted transactions included, so the user's deletions stand.
func (e *Engine) posted(ctx context.Context, s bank.Snapshot) error {
	mapping, err := e.mapping(ctx, s)
	if err != nil {
		return err
	}
	records := s.Records
	var legacy legacyLedger
	if e.Job.Bank == "eq-bank" {
		// EQ retrievals import cleared rows only. Purchase emails are written as
		// amount-only entries that YNAB matches to the cleared import.
		records = make([]Record, 0, len(s.Records))
		for _, r := range s.Records {
			if r.Status == "posted" {
				records = append(records, r)
			}
		}
		slog.Info("EQ import eligibility", "source", e.Job.Source, "posted", len(records), "pendingSkipped", len(s.Records)-len(records))
		if legacy, err = e.legacyLedger(ctx); err != nil {
			return err
		}
	}
	// Read YNAB from a month before the oldest record, so a date the user
	// moved back in YNAB still finds the transaction's import ID.
	since := ""
	for _, r := range records {
		if since == "" || r.Date < since {
			since = r.Date
		}
	}
	since = addDays(since, -30)
	imported := map[string]map[string]bool{}
	var cache *learning
	for _, r := range records {
		target := mapping[r.AccountID]
		if target.ID == "" || legacy.covers(r) {
			continue
		}
		ids, ok := imported[target.ID]
		if !ok {
			rows, err := e.YNAB.AccountTransactions(ctx, target.ID, since)
			if err != nil {
				return err
			}
			ids = map[string]bool{}
			for _, tx := range rows {
				if tx.ImportID != nil {
					ids[*tx.ImportID] = true
				}
			}
			imported[target.ID] = ids
		}
		id := e.importID(r)
		if ids[id] {
			continue
		}
		decision := e.enrich(ctx, &cache, r.Description, r.Amount, r.Date, target.ID)
		slog.Info("posted transaction preview", "bank", e.Job.Bank, "date", r.Date, "amount", r.Amount, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "dryRun", e.Job.DryRun)
		if e.Job.DryRun {
			continue
		}
		tx, err := e.YNAB.CreateTransaction(ctx, target.ID, r.Date, r.Amount, decision.Enrichment, id, "posted")
		if errors.Is(err, ynab.ErrDuplicateImport) {
			ids[id] = true
			continue
		}
		if err != nil {
			return writeFailure(err, "transaction-write")
		}
		ids[id] = true
		e.recordSuggestion(ctx, decision, tx, id)
		if r.Amount > 0 {
			// The credit is already written; a failed echo check leaves at
			// most a visible duplicate alert, so it never fails the job.
			if err := e.clearEchoes(ctx, target.ID, r, tx.ID); err != nil {
				slog.Warn("echo check failed", "job", e.Job.ID, "credit", tx.ID, "error", err)
			}
		}
	}
	return nil
}

// importID is the YNAB import ID of a posted record. EQ keeps the formula of
// its earlier ledger, so its past imports still dedupe.
func (e *Engine) importID(r Record) string {
	registration, _ := banks.Find(e.Job.Bank)
	if registration.ImportPrefix == "" {
		return r.ProvisionalID
	}
	return registration.ImportPrefix + ":" + bank.Hash("tx#" + r.AccountID + "#" + bank.Hash(identity(r)))[:32]
}

func (e *Engine) balances(ctx context.Context, s bank.Snapshot) error {
	mapping, err := e.mapping(ctx, s)
	if err != nil {
		return err
	}
	if err = e.YNAB.CheckAdjustmentPayee(ctx, e.Settings.AdjustmentPayeeID); err != nil {
		return err
	}
	day := e.today()
	for _, a := range s.Accounts {
		target := mapping[a.ID]
		if target.ID == "" {
			continue
		}
		amount := a.Balance - target.Balance
		if amount == 0 || e.Job.DryRun {
			continue
		}
		// Each run writes the remaining difference, so a lost response is
		// corrected by the next run rather than held for review.
		registration, _ := banks.Find(e.Job.Bank)
		if _, err := e.YNAB.CreateBalanceAdjustment(ctx, target.ID, day, amount, e.Settings.AdjustmentPayeeID, registration.Name); err != nil {
			return writeFailure(err, "balance-adjustment")
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
	}
	stated, err := alert.Extract(ctx, e.Job.Text, e.Model)
	if err != nil {
		slog.Warn("notification extraction failed", "job", e.Job.ID, "error", err)
		return 0, bank.Fail(bank.Temporary, "notification-extract")
	}
	zone := e.Settings.Location()
	received := e.Job.OccurredAt.In(zone).Format("2006-01-02")
	date := received
	if stated.Date != "" {
		date = stated.Date
	}
	// A bank can resend an alert when a refund or credit posts. Those echoes
	// arrive after the date they state; a genuine purchase alert is immediate.
	stale := stated.Date != "" && stated.Date < received
	if stale {
		credit, err := e.findEchoCredit(ctx, stated, received)
		if err != nil {
			return 0, err
		}
		if credit != nil {
			slog.Info("alert matched a posted credit", "job", e.Job.ID, "credit", credit.ID, "source", stated.Source)
			if e.Job.DryRun {
				return 0, nil
			}
			return 0, e.Store.PutMany(ctx, e.Lease, map[string]any{
				"ECHO#" + credit.ID: map[string]any{"jobId": e.Job.ID, "action": "skipped-alert"},
				key:                 Write{Status: "echo", TransactionID: credit.ID, AccountID: e.Job.AccountID, Amount: stated.Amount, Date: date, Description: stated.Descriptor, AlertDate: stated.Date, Stale: true},
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
	amount := -stated.Amount
	if _, err := time.ParseInLocation("2006-01-02", date, zone); err != nil {
		return 0, err
	}
	var cache *learning
	decision := e.enrich(ctx, &cache, stated.Descriptor, amount, date, e.Job.AccountID)
	slog.Info("notification preview", "job", e.Job.ID, "date", date, "amount", amount, "parser", stated.Source, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "stale", stale, "dryRun", e.Job.DryRun)
	importID := "ALERT:" + bank.Hash(e.Job.ID)[:30]
	if e.Job.DryRun {
		return 0, nil
	}
	intent := Write{AccountID: e.Job.AccountID, Amount: amount, Date: date, ImportID: importID, Description: stated.Descriptor, AlertDate: stated.Date, Stale: stale, Decision: decision.Source(), Suggestion: &decision.Suggestion}
	tx, err := e.YNAB.CreateTransaction(ctx, e.Job.AccountID, date, amount, decision.Enrichment, importID, "pending")
	if err != nil {
		return 0, writeFailure(err, "notification-import")
	}
	e.recordSuggestion(ctx, decision, tx, "")
	return 0, e.completeNotification(ctx, key, intent, tx)
}

// completeNotification records the created transaction exactly as YNAB returned
// it, so a later check can tell whether the user has edited it.
func (e *Engine) completeNotification(ctx context.Context, key string, intent Write, tx *ynab.Transaction) error {
	done := intent
	done.Status, done.TransactionID = "complete", tx.ID
	done.PayeeID, done.PayeeName, done.CategoryID, done.Memo = deref(tx.PayeeID), deref(tx.PayeeName), deref(tx.CategoryID), deref(tx.Memo)
	payee := done.PayeeName
	if payee == "" {
		payee = intent.Description
	}
	if err := e.put(ctx, key, done, e.Job.ID); err != nil {
		return err
	}
	e.send(ctx, "Transaction Approved", fmt.Sprintf("A transaction of $%.2f at %s was approved on your %s.", -float64(tx.Amount)/1000, payee, tx.AccountName), true)
	return nil
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
func (e *Engine) findEchoCredit(ctx context.Context, a alert.Alert, received string) (*ynab.Transaction, error) {
	rows, err := e.YNAB.AccountTransactions(ctx, e.Job.AccountID, a.Date)
	if err != nil {
		return nil, err
	}
	var best *ynab.Transaction
	for _, tx := range rows {
		if tx.Deleted || tx.Amount != a.Amount || tx.Date < a.Date || tx.Date > addDays(received, 1) {
			continue
		}
		descriptor := ""
		for _, s := range []*string{tx.ImportPayeeNameOriginal, tx.ImportPayeeName} {
			if s != nil && *s != "" {
				descriptor = *s
				break
			}
		}
		if !learn.Similar(descriptor, a.Descriptor) {
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
	rows, err := e.YNAB.AccountTransactions(ctx, account, addDays(credit.Date, -5))
	if err != nil {
		return err
	}
	byID := map[string]*ynab.Transaction{}
	for _, tx := range rows {
		byID[tx.ID] = tx
	}
	if len(matches) == 1 {
		w := matches[0].w
		if tx := byID[w.TransactionID]; tx != nil && !tx.Deleted && untouched(tx, w) {
			if err := e.YNAB.Delete(ctx, tx.ID); err != nil {
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
func untouched(tx *ynab.Transaction, w Write) bool {
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
// the details. It carries no import ID, so YNAB can match it to that import.
func (e *Engine) alertImport(ctx context.Context) error {
	account := e.Settings.Banks[e.Job.Bank].AlertAccountID
	if account == "" {
		return bank.Fail(bank.Invalid, "alert-account")
	}
	if e.Job.AlertAmount <= 0 || e.Job.AlertDate == "" {
		return bank.Fail(bank.Invalid, "alert-amount")
	}
	slog.Info("amount-only alert preview", "job", e.Job.ID, "date", e.Job.AlertDate, "amount", -e.Job.AlertAmount, "dryRun", e.Job.DryRun)
	if e.Job.DryRun {
		return nil
	}
	amount := -e.Job.AlertAmount
	tx, err := e.YNAB.CreateAmountOnly(ctx, account, e.Job.AlertDate, amount)
	if err != nil {
		return writeFailure(err, "alert-import")
	}
	name := tx.AccountName
	if name == "" {
		name = purchaseAccount(e.Job.Bank)
	}
	e.send(ctx, "Transaction Approved", fmt.Sprintf("A transaction of $%.2f was approved on your %s.", float64(e.Job.AlertAmount)/1000, name), true)
	return nil
}
