package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"

	"strings"
	"time"
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
		if e.Job.DryRun {
			continue
		}
		if err = e.put(ctx, operation, Write{Status: "started", AccountID: target.ID, Amount: r.Amount, Date: r.Date, ImportID: id}, e.Job.ID); err != nil {
			return err
		}
		tx, err := service.CreateEQTransaction(ctx, target.ID, r.Date, r.Amount, r.Description, id, "posted", nil)
		if err != nil {
			return writeFailure(err, "transaction-write")
		}
		if err = e.put(ctx, operation, Write{Status: "complete", TransactionID: tx.ID, ImportID: id}, e.Job.ID); err != nil {
			return err
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
func (e *Engine) notificationImport(ctx context.Context) error {
	var previous Write
	key := "WRITE#notification#" + e.Job.ID
	if _, err := e.get(ctx, key, &previous); err != nil {
		return err
	}
	if previous.Status == "complete" {
		return nil
	}
	if previous.Status == "started" {
		// Recover the recorded write before invoking AI again. A new interpretation
		// must never change an operation whose financial outcome is uncertain.
		if e.Job.DryRun {
			return nil
		}
		transactions, err := service.GetAccountTransactions(ctx, previous.AccountID)
		if err != nil {
			return err
		}
		for _, tx := range transactions {
			if !tx.Deleted && tx.ImportID != nil && *tx.ImportID == previous.ImportID {
				return e.completeNotification(ctx, key, previous.ImportID, tx, "the recorded payee")
			}
		}
		return bank.Fail(bank.Uncertain, "notification-import")
	}
	details, err := service.ExtractTransactionDetails(ctx, e.Job.Text)
	if err != nil {
		return err
	}
	rules, err := override.GetTransactionOverrides(ctx)
	if err != nil {
		return err
	}
	rule, err := override.FindOverride(ctx, rules, details.Amount, details.Merchant, e.Job.OccurredAt)
	if err != nil {
		return err
	}
	payee := details.Merchant
	if rule == nil {
		payees, err := service.GetPayees(ctx)
		if err != nil {
			return err
		}
		payee, err = service.MatchPayee(ctx, payee, payees)
		if err != nil {
			return err
		}
	}
	zone, _ := time.LoadLocation(e.Settings.Timezone)
	date := e.Job.OccurredAt.In(zone).Format("2006-01-02")
	amount, err := bank.Milliunits(fmt.Sprintf("%.3f", -details.Amount))
	if err != nil {
		return err
	}
	importID := "ALERT:" + bank.Hash(e.Job.ID)[:30]
	if e.Job.DryRun {
		return nil
	}
	if err = e.put(ctx, key, Write{Status: "started", AccountID: e.Job.AccountID, Amount: amount, Date: date, ImportID: importID}, e.Job.ID); err != nil {
		return err
	}
	tx, err := service.CreateEQTransaction(ctx, e.Job.AccountID, date, amount, payee, importID, "pending", rule)
	if err != nil {
		return writeFailure(err, "notification-import")
	}
	return e.completeNotification(ctx, key, importID, tx, payee)
}
func (e *Engine) completeNotification(ctx context.Context, key, importID string, tx *types.YNABTransaction, payee string) error {
	if tx.PayeeName != nil {
		payee = *tx.PayeeName
	}
	notice := Notification{ID: "purchase#" + e.Job.ID, JobID: e.Job.ID, Title: "Transaction Approved", Message: fmt.Sprintf("A transaction of $%.2f at %s was approved on your %s.", -float64(tx.Amount)/1000, payee, tx.AccountName), Status: "pending", At: time.Now()}
	return e.Store.PutMany(ctx, e.Lease, map[string]any{key: Write{Status: "complete", TransactionID: tx.ID, ImportID: importID}, "NOTICE#" + notice.ID: notice})
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
