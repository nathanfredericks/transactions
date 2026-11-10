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
func (t *task) mapping(ctx context.Context, s bank.Snapshot) (map[string]ynab.Account, error) {
	all, err := t.YNAB.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	mapped := map[string]ynab.Account{}
	used := map[string]bool{}
	excluded := map[string]bool{}
	for _, id := range t.Settings.Banks[t.Job.Bank].ExcludedAccounts {
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
func (t *task) posted(ctx context.Context, s bank.Snapshot) error {
	mapping, err := t.mapping(ctx, s)
	if err != nil {
		return err
	}
	records, legacy, err := t.importable(ctx, s)
	if err != nil {
		return err
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
		if imported[target.ID] == nil {
			if imported[target.ID], err = t.importIDs(ctx, target.ID, since); err != nil {
				return err
			}
		}
		if err = t.importRecord(ctx, &cache, target.ID, r, imported[target.ID]); err != nil {
			return err
		}
	}
	return nil
}

// importable is the snapshot's records to import. EQ imports cleared rows
// only, and skips the records its earlier ledger already covered.
func (t *task) importable(ctx context.Context, s bank.Snapshot) ([]Record, legacyLedger, error) {
	if t.Job.Bank != "eq-bank" {
		return s.Records, legacyLedger{}, nil
	}
	// Purchase emails are written as amount-only entries that YNAB matches to
	// the cleared import.
	records := make([]Record, 0, len(s.Records))
	for _, r := range s.Records {
		if r.Status == "posted" {
			records = append(records, r)
		}
	}
	slog.Info("EQ import eligibility", "source", t.Job.Source, "posted", len(records), "pendingSkipped", len(s.Records)-len(records))
	legacy, err := t.legacyLedger(ctx)
	return records, legacy, err
}

// importIDs is the set of import IDs YNAB holds for the account since a date.
func (t *task) importIDs(ctx context.Context, account, since string) (map[string]bool, error) {
	rows, err := t.YNAB.AccountTransactions(ctx, account, since)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, tx := range rows {
		if tx.ImportID != nil {
			ids[*tx.ImportID] = true
		}
	}
	return ids, nil
}

// importRecord writes one posted record unless its import ID is among ids,
// and adds it there once written.
func (t *task) importRecord(ctx context.Context, cache **learning, account string, r Record, ids map[string]bool) error {
	id := t.importID(r)
	if ids[id] {
		return nil
	}
	decision := t.enrich(ctx, cache, r.Description, r.Amount, r.Date, account)
	slog.Info("posted transaction preview", "bank", t.Job.Bank, "date", r.Date, "amount", r.Amount, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "dryRun", t.Job.DryRun)
	if t.Job.DryRun {
		return nil
	}
	tx, err := t.YNAB.CreateTransaction(ctx, account, r.Date, r.Amount, decision.Enrichment, id, "posted")
	if errors.Is(err, ynab.ErrDuplicateImport) {
		ids[id] = true
		return nil
	}
	if err != nil {
		return writeFailure(err, "transaction-write")
	}
	ids[id] = true
	t.recordSuggestion(ctx, decision, tx, id)
	if r.Amount > 0 {
		// The credit is already written; a failed echo check leaves at most a
		// visible duplicate alert, so it never fails the job.
		if err := t.clearEchoes(ctx, account, r, tx.ID); err != nil {
			slog.Warn("echo check failed", "job", t.Job.ID, "credit", tx.ID, "error", err)
		}
	}
	return nil
}

// importID is the YNAB import ID of a posted record. EQ keeps the formula of
// its earlier ledger, so its past imports still dedupe.
func (t *task) importID(r Record) string {
	registration, _ := banks.Find(t.Job.Bank)
	if registration.ImportPrefix == "" {
		return r.ProvisionalID
	}
	return registration.ImportPrefix + ":" + bank.Hash("tx#" + r.AccountID + "#" + bank.Hash(identity(r)))[:32]
}

func (t *task) balances(ctx context.Context, s bank.Snapshot) error {
	mapping, err := t.mapping(ctx, s)
	if err != nil {
		return err
	}
	if err = t.YNAB.CheckAdjustmentPayee(ctx, t.Settings.AdjustmentPayeeID); err != nil {
		return err
	}
	day := t.today()
	for _, a := range s.Accounts {
		target := mapping[a.ID]
		if target.ID == "" {
			continue
		}
		amount := a.Balance - target.Balance
		if amount == 0 || t.Job.DryRun {
			continue
		}
		// Each run writes the remaining difference, so a lost response is
		// corrected by the next run rather than held for review.
		registration, _ := banks.Find(t.Job.Bank)
		if _, err := t.YNAB.CreateBalanceAdjustment(ctx, target.ID, day, amount, t.Settings.AdjustmentPayeeID, registration.Name); err != nil {
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
func (t *task) notificationImport(ctx context.Context) (int, error) {
	var previous Write
	key := "WRITE#notification#" + t.Job.ID
	if _, err := t.get(ctx, key, &previous); err != nil {
		return 0, err
	}
	switch previous.Status {
	case "complete", "echo":
		return 0, nil
	}
	stated, err := alert.Extract(ctx, t.Job.Text, t.Model)
	if err != nil {
		slog.Warn("notification extraction failed", "job", t.Job.ID, "error", err)
		return 0, bank.Fail(bank.Temporary, "notification-extract")
	}
	zone := t.Settings.Location()
	received := t.Job.OccurredAt.In(zone).Format("2006-01-02")
	date := received
	if stated.Date != "" {
		date = stated.Date
	}
	// A bank can resend an alert when a refund or credit posts. Those echoes
	// arrive after the date they state; a genuine purchase alert is immediate.
	stale := stated.Date != "" && stated.Date < received
	if stale {
		if wait, done, err := t.lateAlert(ctx, stated, received, date, key); done {
			return wait, err
		}
	}
	amount := -stated.Amount
	if _, err := time.ParseInLocation("2006-01-02", date, zone); err != nil {
		return 0, err
	}
	var cache *learning
	decision := t.enrich(ctx, &cache, stated.Descriptor, amount, date, t.Job.AccountID)
	slog.Info("notification preview", "job", t.Job.ID, "date", date, "amount", amount, "parser", stated.Source, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "stale", stale, "dryRun", t.Job.DryRun)
	importID := "ALERT:" + bank.Hash(t.Job.ID)[:30]
	if t.Job.DryRun {
		return 0, nil
	}
	intent := Write{AccountID: t.Job.AccountID, Amount: amount, Date: date, ImportID: importID, Description: stated.Descriptor, AlertDate: stated.Date, Stale: stale, Decision: decision.Source(), Suggestion: &decision.Suggestion}
	tx, err := t.YNAB.CreateTransaction(ctx, t.Job.AccountID, date, amount, decision.Enrichment, importID, "pending")
	if err != nil {
		return 0, writeFailure(err, "notification-import")
	}
	t.recordSuggestion(ctx, decision, tx, "")
	return 0, t.completeNotification(ctx, key, intent, tx)
}

// lateAlert handles an alert that arrived after the date it states, which a
// bank sends again when a refund or credit posts. It records the alert as an
// echo of a matching credit, or waits for the credit to reach YNAB. It reports
// false once the waits are spent, and the alert is imported as a purchase.
func (t *task) lateAlert(ctx context.Context, stated alert.Alert, received, date, key string) (int, bool, error) {
	credit, err := t.findEchoCredit(ctx, stated, received)
	if err != nil {
		return 0, true, err
	}
	if credit != nil {
		slog.Info("alert matched a posted credit", "job", t.Job.ID, "credit", credit.ID, "source", stated.Source)
		if t.Job.DryRun {
			return 0, true, nil
		}
		return 0, true, t.Store.PutMany(ctx, t.Lease, map[string]any{
			"ECHO#" + credit.ID: map[string]any{"jobId": t.Job.ID, "action": "skipped-alert"},
			key:                 Write{Status: "echo", TransactionID: credit.ID, AccountID: t.Job.AccountID, Amount: stated.Amount, Date: date, Description: stated.Descriptor, AlertDate: stated.Date, Stale: true},
		})
	}
	if t.Job.MissingAttempt < len(echoWaits) {
		if t.Job.MissingAttempt == 0 && !t.Job.DryRun {
			if err := t.requestRetrieval(ctx); err != nil {
				return 0, true, err
			}
		}
		wait := echoWaits[t.Job.MissingAttempt]
		t.Job.MissingAttempt++
		return wait, true, nil
	}
	slog.Info("late alert has no matching credit; importing it as a purchase", "job", t.Job.ID)
	return 0, false, nil
}

// completeNotification records the created transaction exactly as YNAB returned
// it, so a later check can tell whether the user has edited it.
func (t *task) completeNotification(ctx context.Context, key string, intent Write, tx *ynab.Transaction) error {
	done := intent
	done.Status, done.TransactionID = "complete", tx.ID
	done.PayeeID, done.PayeeName, done.CategoryID, done.Memo = deref(tx.PayeeID), deref(tx.PayeeName), deref(tx.CategoryID), deref(tx.Memo)
	payee := done.PayeeName
	if payee == "" {
		payee = intent.Description
	}
	if err := t.put(ctx, key, done, t.Job.ID); err != nil {
		return err
	}
	t.send(ctx, "Transaction Approved", fmt.Sprintf("A transaction of $%.2f at %s was approved on your %s.", -float64(tx.Amount)/1000, payee, tx.AccountName), true)
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
func (t *task) findEchoCredit(ctx context.Context, a alert.Alert, received string) (*ynab.Transaction, error) {
	rows, err := t.YNAB.AccountTransactions(ctx, t.Job.AccountID, a.Date)
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
		claimed, err := t.get(ctx, "ECHO#"+tx.ID, &claim)
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
func (t *task) requestRetrieval(ctx context.Context) error {
	registration, ok := banks.Find(t.Job.Bank)
	if !ok || !t.Settings.Banks[t.Job.Bank].Enabled {
		return nil
	}
	_, err := t.Submit(ctx, Job{Version: 1, ID: "echo-" + bank.Hash(t.Job.ID)[:32], Bank: registration.ID, Source: "scheduled", Purpose: "retrieve"})
	return err
}

// clearEchoes runs when a bank credit is imported. An alert this importer wrote
// that the credit explains is removed if it is the only match and the user has
// not touched it; otherwise the matches are left as they are.
func (t *task) clearEchoes(ctx context.Context, account string, credit Record, creditID string) error {
	var claim map[string]any
	if claimed, err := t.get(ctx, "ECHO#"+creditID, &claim); err != nil || claimed {
		return err
	}
	raw, err := t.Store.List(ctx, "BANK#"+t.Job.Bank, "WRITE#notification#")
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
	rows, err := t.YNAB.AccountTransactions(ctx, account, addDays(credit.Date, -5))
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
			if err := t.YNAB.Delete(ctx, tx.ID); err != nil {
				return err
			}
			slog.Info("removed alert repeated by a bank credit", "credit", creditID, "transaction", tx.ID)
			return t.put(ctx, "ECHO#"+creditID, map[string]any{"jobId": t.Job.ID, "action": "removed", "transactionId": tx.ID}, t.Job.ID)
		}
	}
	var left []string
	for _, m := range matches {
		left = append(left, m.w.TransactionID)
	}
	slog.Info("left alerts a bank credit may repeat", "credit", creditID, "count", len(left))
	return t.put(ctx, "ECHO#"+creditID, map[string]any{"jobId": t.Job.ID, "action": "left", "transactionIds": left}, t.Job.ID)
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
func (t *task) alertImport(ctx context.Context) error {
	account := t.Settings.Banks[t.Job.Bank].AlertAccountID
	if account == "" {
		return bank.Fail(bank.Invalid, "alert-account")
	}
	if t.Job.AlertAmount <= 0 || t.Job.AlertDate == "" {
		return bank.Fail(bank.Invalid, "alert-amount")
	}
	slog.Info("amount-only alert preview", "job", t.Job.ID, "date", t.Job.AlertDate, "amount", -t.Job.AlertAmount, "dryRun", t.Job.DryRun)
	if t.Job.DryRun {
		return nil
	}
	amount := -t.Job.AlertAmount
	tx, err := t.YNAB.CreateAmountOnly(ctx, account, t.Job.AlertDate, amount)
	if err != nil {
		return writeFailure(err, "alert-import")
	}
	name := tx.AccountName
	if name == "" {
		name = purchaseAccount(t.Job.Bank)
	}
	t.send(ctx, "Transaction Approved", fmt.Sprintf("A transaction of $%.2f was approved on your %s.", float64(t.Job.AlertAmount)/1000, name), true)
	return nil
}
