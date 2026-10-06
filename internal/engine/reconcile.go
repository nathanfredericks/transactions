package engine

import (
	"context"

	"fmt"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
	"log/slog"
	"math"
	"strings"
	"time"
)

type Record = bank.Record
type Entry struct {
	Key                 string `json:"key"`
	Record              Record `json:"record"`
	ImportID            string `json:"importId"`
	YNABID              string `json:"ynabId"`
	Adopted             bool   `json:"adopted"`
	Payee               string `json:"payee"`
	AuthorizationAmount *int64 `json:"authorizationAmount,omitempty"`
	AuthorizationDate   string `json:"authorizationDate,omitempty"`
	AlertMessageID      string `json:"alertMessageId,omitempty"`
}

var getAccountTransactions = service.GetAccountTransactions
var getOverrides = override.GetTransactionOverrides
var getParameters = config.GetParameters
var findOverride = override.FindOverride
var getPayees = service.GetPayees
var matchPayee = service.MatchPayee
var createEQTransaction = service.CreateEQTransaction
var updateEQTransaction = service.UpdateEQTransaction
var hash = bank.Hash

func (s *Engine) importPrefix() string { r, _ := banks.Find(s.Job.Bank); return r.ImportPrefix }
func identity(r Record) string {
	if r.BankID != nil {
		return *r.BankID
	}
	return r.ProvisionalID
}
func merchant(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }
func near(a, b string, days int) bool {
	aa, e := time.Parse("2006-01-02", a)
	if e != nil {
		return false
	}
	bb, e := time.Parse("2006-01-02", b)
	return e == nil && math.Abs(aa.Sub(bb).Hours()) <= float64(days*24)
}
func samePurchase(a, b Record) bool {
	if a.AccountID != b.AccountID || a.Amount == 0 || b.Amount == 0 || (a.Amount < 0) != (b.Amount < 0) || !near(a.Date, b.Date, 14) {
		return false
	}
	sameMerchant := merchant(a.Description) == merchant(b.Description)
	if a.OriginalAmount != nil && b.OriginalAmount != nil && a.OriginalCurrency != nil && b.OriginalCurrency != nil {
		// EQ reverses the original-currency sign and can change its trailing
		// city/country descriptor on posting. The purchase direction above,
		// original currency/value and merchant (including order number) remain.
		sameMerchant = sameMerchant || merchant(strings.SplitN(a.Description, ",", 2)[0]) == merchant(strings.SplitN(b.Description, ",", 2)[0])
		return sameMerchant && (*a.OriginalAmount == *b.OriginalAmount || *a.OriginalAmount == -*b.OriginalAmount) && *a.OriginalCurrency == *b.OriginalCurrency
	}
	return sameMerchant && a.Amount == b.Amount
}
func exactMatch(a, b Record) bool {
	if a.AccountID != b.AccountID {
		return false
	}
	if a.ProvisionalID == b.ProvisionalID || (a.BankID != nil && b.BankID != nil && *a.BankID == *b.BankID) {
		return true
	}
	for _, x := range a.References {
		for _, y := range b.References {
			if x != "" && x == y {
				return true
			}
		}
	}
	return false
}
func (s *Engine) reconcile(ctx context.Context, snapshot bank.Snapshot, baseline Baseline) (bool, error) {
	job := s.Job
	result := struct{ Records []Record }{snapshot.Records}
	mapping := map[string]string{}
	mapped, err := s.mapping(ctx, snapshot)
	if err != nil {
		return false, err
	}
	var accountIDs []string
	for id, target := range mapped {
		mapping[id] = target.ID
		accountIDs = append(accountIDs, id)
	}
	snapshots := map[string][]*types.YNABTransaction{}
	for _, id := range accountIDs {
		snapshots[id], err = getAccountTransactions(ctx, mapping[id])
		if err != nil {
			return false, err
		}
	}
	entries, err := s.ledger(ctx)
	if err != nil {
		return false, err
	}
	// A native row may only claim one pending row, even when several identical purchases post together.
	claimed := map[string]bool{}
	resolved := map[string]*Entry{}
	review := 0
	created := 0
	adopted := 0
	updated := 0
	hold := func(r Record, candidates []*Entry, reason string) error {
		review++
		if job.DryRun {
			return nil
		}
		var keys []string
		for _, e := range candidates {
			keys = append(keys, e.Key)
		}
		id := hash(r.AccountID + "#" + identity(r))
		if err := s.put(ctx, "review#"+id, map[string]any{"record": r, "candidates": keys, "reason": reason, "jobId": job.ID}, job.ID); err != nil {
			return err
		}
		return nil
	}
	overrides, err := getOverrides(ctx)
	if err != nil {
		return false, err
	}
	var payees []string
	for _, r := range result.Records {
		if mapping[r.AccountID] == "" {
			return false, bank.Fail(bank.Invalid, "record-account")
		}
		if baseline.Seen[r.AccountID+"#"+identity(r)] {
			continue
		}

		if _, e := time.Parse("2006-01-02", r.Date); e != nil {
			return false, e
		}
		var exact []*Entry
		for _, entry := range entries {
			if entry.Record.AccountID != r.AccountID {
				continue
			}
			match := entry.Record.ProvisionalID == r.ProvisionalID || (r.BankID != nil && entry.Record.BankID != nil && *r.BankID == *entry.Record.BankID)
			for _, a := range entry.Record.References {
				for _, b := range r.References {
					if a != "" && a == b {
						match = true
					}
				}
			}
			if match {
				exact = append(exact, entry)
			}
		}
		if len(exact) > 1 {
			return false, bank.Fail(bank.Invalid, "conflicting-ledger-references")
		}
		var entry *Entry
		if len(exact) == 1 {
			entry = exact[0]
		}
		if entry == nil {
			var resolution struct {
				EntryKey string `json:"entryKey"`
			}
			if _, err := s.get(ctx, "RESOLUTION#"+hash(r.AccountID+"#"+identity(r)), &resolution); err != nil {
				return false, err
			}
			if resolution.EntryKey != "" {
				for _, candidate := range entries {
					if candidate.Key == resolution.EntryKey && candidate.Record.AccountID == r.AccountID {
						entry = candidate
						break
					}
				}
				if entry == nil {
					return false, bank.Fail(bank.Invalid, "review-link-missing")
				}
			}
		}
		if entry == nil && r.Status == "posted" && r.Amount != 0 {
			var candidates []*Entry
			for _, e := range entries {
				if e.Record.Status == "pending" && samePurchase(e.Record, r) {
					candidates = append(candidates, e)
				}
			}
			// Also look forward: two posted rows matching the same pending row are ambiguous.
			competing := 0
			for _, other := range result.Records {
				if other.Status == "posted" && samePurchase(other, r) {
					if baseline.Seen[other.AccountID+"#"+identity(other)] {
						continue
					}
					known := false
					for _, e := range entries {
						if e.Record.Status == "posted" && exactMatch(e.Record, other) {
							known = true
							break
						}
					}
					if !known {
						competing++
					}
				}
			}
			if len(candidates) > 1 || (len(candidates) == 1 && competing > 1) {
				if err = hold(r, candidates, "Multiple pending or newly posted records match"); err != nil {
					return false, err
				}
				continue
			}
			if len(candidates) == 1 {
				if claimed[candidates[0].Key] {
					if err = hold(r, candidates, "Pending record is still present in this history"); err != nil {
						return false, err
					}
					continue
				}
				entry = candidates[0]
			}
		}
		if entry == nil {
			stable := "tx#" + r.AccountID + "#" + hash(identity(r))
			entry = &Entry{Key: stable, Record: r, ImportID: s.importPrefix() + ":" + hash(stable)[:32]}
			if r.Status == "pending" {
				amount := r.Amount
				entry.AuthorizationAmount = &amount
				entry.AuthorizationDate = r.Date
			}
			if linked := baseline.Links[r.AccountID+"#"+identity(r)]; linked != "" {
				entry.YNABID = linked
				entry.Adopted = true
				adopted++
			}
			if settlement, ok := baseline.Settlements[r.AccountID+"#"+identity(r)]; ok {
				var existing *types.YNABTransaction
				for _, tx := range snapshots[r.AccountID] {
					if tx.ID == settlement.TransactionID && !tx.Deleted {
						existing = tx
					}
				}
				if existing == nil || existing.AccountID != settlement.AccountID || ((existing.Amount != settlement.Amount || existing.Date != settlement.Date) && (existing.Amount != r.Amount || existing.Date != r.Date)) {
					return false, bank.Fail(bank.Invalid, "baseline-settlement-changed")
				}
				entry.YNABID, entry.Adopted = existing.ID, true
				amount := settlement.Amount
				entry.AuthorizationAmount, entry.AuthorizationDate = &amount, settlement.Date
				if !job.DryRun {
					if err := s.updateTransaction(ctx, entry.Key, existing, r); err != nil {
						return false, err
					}
				}
				adopted++
			}

			if entry.YNABID == "" {
				// Recover from a write which succeeded before the ledger was committed.
				for _, t := range snapshots[r.AccountID] {
					if !t.Deleted && t.ImportID != nil && *t.ImportID == entry.ImportID {
						entry.YNABID = t.ID
					}
				}
			}
			if entry.YNABID == "" {
				params, e := getParameters(ctx)
				if e != nil {
					return false, e
				}
				loc, e := time.LoadLocation(params.Timezone)
				if e != nil {
					return false, e
				}
				day, e := time.ParseInLocation("2006-01-02", r.Date, loc)
				if e != nil {
					return false, e
				}
				rule, e := findOverride(ctx, overrides, math.Abs(float64(r.Amount))/1000, r.Description, day)
				if e != nil {
					return false, e
				}
				payee := r.Description
				if rule == nil {
					if payees == nil {
						payees, e = getPayees(ctx)
						if e != nil {
							return false, e
						}
					}
					payee, e = matchPayee(ctx, r.Description, payees)
					if e != nil {
						return false, e
					}
				}
				slog.Info("EQ transaction preview", "account", mapping[r.AccountID], "date", r.Date, "amount", r.Amount, "payee", payee, "status", r.Status, "importId", entry.ImportID, "dryRun", job.DryRun)
				if !job.DryRun {
					tx, e := createEQTransaction(ctx, mapping[r.AccountID], r.Date, r.Amount, payee, entry.ImportID, r.Status, rule)
					if e != nil {
						return false, bank.Fail(bank.Uncertain, "transaction-write")
					}
					entry.YNABID = tx.ID
					snapshots[r.AccountID] = append(snapshots[r.AccountID], tx)
				}
				created++
			}
			entries = append(entries, entry)
		} else {
			if claimed[entry.Key] {
				return false, bank.Fail(bank.Invalid, "multiple-ledger-claims")
			}
			if entry.Record.Status == "posted" && r.Status == "pending" {
				return false, bank.Fail(bank.Invalid, "posted-regressed")
			}
			if entry.Record.Status != r.Status || entry.Record.Amount != r.Amount || entry.Record.Date != r.Date {
				var tx *types.YNABTransaction
				for _, t := range snapshots[r.AccountID] {
					if t.ID == entry.YNABID && !t.Deleted {
						tx = t
					}
				}
				if tx == nil {
					return false, bank.Fail(bank.Invalid, "ledger-transaction-removed")
				}
				if !job.DryRun {
					if err = s.updateTransaction(ctx, entry.Key, tx, r); err != nil {
						return false, err
					}
				}
				updated++
			}
			// Keep the provisional identity when the native posted identity arrives.
			r.ProvisionalID = entry.Record.ProvisionalID
			r.References = append(r.References, entry.Record.References...)
			entry.Record = r
		}
		claimed[entry.Key] = true
		for _, tx := range snapshots[r.AccountID] {
			if tx.ID == entry.YNABID && tx.PayeeName != nil {
				entry.Payee = *tx.PayeeName
			}
		}
		resolved[identity(r)] = entry
		if !job.DryRun {
			if err = s.put(ctx, entry.Key, entry, job.ID); err != nil {
				return false, err
			}
		}
	}
	missing := false
	if job.Source == "alert" {
		var candidates []*Entry
		for _, r := range result.Records {
			e := resolved[identity(r)]
			if e == nil {
				continue
			}
			// An earlier alert already identifies this purchase. Keep equal amounts
			// on distinct purchases eligible for their own original message IDs.
			if e.AlertMessageID != "" && e.AlertMessageID != job.MessageID {
				continue
			}
			amount, date := r.Amount, r.Date
			if e.AuthorizationAmount != nil {
				amount = *e.AuthorizationAmount
				date = e.AuthorizationDate
			}
			if amount == -job.AlertAmount && near(date, job.AlertDate, 1) {
				candidates = append(candidates, e)
			}
		}
		if len(candidates) != 1 {
			missing = true
		} else if !job.DryRun {
			entry := candidates[0]
			payee := entry.Payee
			if payee == "" {
				payee = entry.Record.Description
			}
			if err = s.notifyOnce(ctx, "purchase#"+entry.Key, fmt.Sprintf("A transaction of $%.2f at %s was approved on your %s.", float64(job.AlertAmount)/1000, payee, purchaseAccount(job.Bank)), "Transaction Approved"); err != nil {
				return false, err
			}
			entry.AlertMessageID = job.MessageID
			if err = s.put(ctx, entry.Key, entry, job.ID); err != nil {
				return false, err
			}
			if err = s.put(ctx, "alert#"+hash(job.MessageID), map[string]any{"ynabId": entry.YNABID, "jobId": job.ID}, job.ID); err != nil {
				return false, err
			}
		}
	}
	if review > 0 {
		return false, bank.Fail(bank.Invalid, "ambiguous-posting")
	}
	_ = created
	_ = adopted
	_ = updated
	return missing, nil
}

func purchaseAccount(id string) string { r, _ := banks.Find(id); return r.PurchaseAccount }

// An update with a lost response is recovered by reading the identified transaction.
// If its bank fields do not prove completion, subsequent jobs retain the hold.
func (s *Engine) updateTransaction(ctx context.Context, key string, tx *types.YNABTransaction, r Record) error {
	operation := "UPDATE#" + key + "#" + bank.Hash(r.Date+fmt.Sprint(r.Amount)+r.Status)
	var previous Write
	if _, err := s.get(ctx, operation, &previous); err != nil {
		return err
	}
	matches := tx.Amount == r.Amount && tx.Date == r.Date && (tx.Cleared == "reconciled" || (r.Status == "posted" && tx.Cleared == "cleared") || (r.Status == "pending" && tx.Cleared == "uncleared"))
	if previous.Status == "started" && !matches {
		return bank.Fail(bank.Uncertain, "transaction-update")
	}
	w := Write{Status: "started", TransactionID: tx.ID, AccountID: tx.AccountID, Amount: r.Amount, Date: r.Date}
	if !matches {
		if err := s.put(ctx, operation, w, s.Job.ID); err != nil {
			return err
		}
		if _, err := updateEQTransaction(ctx, tx, r.Date, r.Amount, r.Status); err != nil {
			return bank.Fail(bank.Uncertain, "transaction-update")
		}
	}
	w.Status = "complete"
	return s.put(ctx, operation, w, s.Job.ID)
}
