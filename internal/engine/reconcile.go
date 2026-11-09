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
	if a.BankID != nil && b.BankID != nil && *a.BankID != "" && *b.BankID != "" {
		return *a.BankID == *b.BankID
	}
	aHasReference, bHasReference := false, false
	for _, x := range a.References {
		aHasReference = aHasReference || x != ""
		for _, y := range b.References {
			bHasReference = bHasReference || y != ""
			if x != "" && x == y {
				return true
			}
		}
	}
	// EQ can reuse a provisional hash for equal purchases on different value
	// dates, and occurrence numbers can move when the history order changes.
	// Distinct native references must never be merged by that fallback identity.
	if aHasReference && bHasReference {
		return false
	}
	return a.ProvisionalID != "" && a.ProvisionalID == b.ProvisionalID &&
		a.Date == b.Date && a.Amount == b.Amount && merchant(a.Description) == merchant(b.Description)
}
func (s *Engine) reconcile(ctx context.Context, snapshot bank.Snapshot, baseline Baseline) (bool, error) {
	job := s.Job
	result := struct{ Records []Record }{snapshot.Records}
	// EQ retrievals import cleared rows only. Purchase emails are written as
	// amount-only entries that YNAB matches to the cleared import.
	if job.Bank == "eq-bank" {
		result.Records = make([]Record, 0, len(snapshot.Records))
		for _, record := range snapshot.Records {
			if record.Status == "posted" {
				result.Records = append(result.Records, record)
			}
		}
		slog.Info("EQ import eligibility", "source", job.Source, "posted", len(result.Records), "pendingSkipped", len(snapshot.Records)-len(result.Records))
	}
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
		snapshots[id], err = service.GetAccountTransactions(ctx, mapping[id])
		if err != nil {
			return false, err
		}
	}
	entries, err := s.ledger(ctx)
	if err != nil {
		return false, err
	}
	// A pending authorization still present in the full bank snapshot belongs to
	// its own purchase. Reserve it before matching posted rows, including when
	// scheduled imports omit pending rows and regardless of history order.
	pendingPresent := map[string]bool{}
	for _, entry := range entries {
		if entry.Record.Status != "pending" {
			continue
		}
		for _, record := range snapshot.Records {
			if record.Status == "pending" && exactMatch(entry.Record, record) {
				pendingPresent[entry.Key] = true
				break
			}
		}
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
		id := bank.Hash(r.AccountID + "#" + identity(r))
		if err := s.put(ctx, "review#"+id, map[string]any{"record": r, "candidates": keys, "reason": reason, "jobId": job.ID}, job.ID); err != nil {
			return err
		}
		return nil
	}
	overrides, err := override.GetTransactionOverrides(ctx)
	if err != nil {
		return false, err
	}
	var cache *learning
	for _, r := range result.Records {
		recordKey := r.AccountID + "#" + identity(r)
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
			if exactMatch(entry.Record, r) {
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
			if _, err := s.get(ctx, "RESOLUTION#"+bank.Hash(r.AccountID+"#"+identity(r)), &resolution); err != nil {
				return false, err
			}
			if resolution.EntryKey != "" {
				// A resolution approves the reviewed purchase, not every future
				// record that happens to reuse its provisional hash.
				var review struct {
					Record Record `json:"record"`
				}
				found, err := s.get(ctx, "review#"+bank.Hash(r.AccountID+"#"+identity(r)), &review)
				if err != nil {
					return false, err
				}
				if !found || !exactMatch(review.Record, r) {
					resolution.EntryKey = ""
				}
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
				if e.Record.Status == "pending" && !pendingPresent[e.Key] && samePurchase(e.Record, r) {
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
			stable := "tx#" + r.AccountID + "#" + bank.Hash(identity(r))
			entry = &Entry{Key: stable, Record: r, ImportID: s.importPrefix() + ":" + bank.Hash(stable)[:32]}
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
					if t.Deleted && t.ImportID != nil && *t.ImportID == entry.ImportID {
						return false, bank.Fail(bank.Invalid, "previous-import-missing")
					}
					if !t.Deleted && t.ImportID != nil && *t.ImportID == entry.ImportID {
						entry.YNABID = t.ID
					}
				}
			}
			if entry.YNABID == "" {
				var previous Write
				if _, err := s.get(ctx, "WRITE#"+entry.Key, &previous); err != nil {
					return false, err
				}
				// A pending create has no YNAB import ID. Its confirmed response ID
				// still recovers a crash between completing the write and ledger save.
				if previous.Status == "complete" && previous.TransactionID != "" {
					for _, tx := range snapshots[r.AccountID] {
						if !tx.Deleted && tx.ID == previous.TransactionID {
							entry.YNABID = tx.ID
						}
					}
				}
				if previous.Status != "" && entry.YNABID == "" {
					return false, bank.Fail(bank.Uncertain, "transaction-write")
				}
			}
			if entry.YNABID == "" {
				params, e := config.GetParameters(ctx)
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
				rule, e := override.FindOverride(ctx, overrides, math.Abs(float64(r.Amount))/1000, r.Description, day)
				if e != nil {
					return false, e
				}
				var decision Decision
				if rule != nil {
					decision = s.ruled(r.Description, r.Amount, r.Date, mapping[r.AccountID], rule)
				} else {
					decision = s.enrich(ctx, &cache, r.Description, r.Amount, r.Date, mapping[r.AccountID])
				}
				slog.Info("EQ transaction preview", "account", mapping[r.AccountID], "date", r.Date, "amount", r.Amount, "payeeSource", decision.Source(), "categorySource", decision.Suggestion.CategorySource, "existingPayee", decision.Enrichment.Payee.ID != "", "status", r.Status, "importId", entry.ImportID, "dryRun", job.DryRun)
				if !job.DryRun {
					intent := Write{Status: "started", AccountID: mapping[r.AccountID], Amount: r.Amount, Date: r.Date, ImportID: entry.ImportID, Decision: decision.Source(), Suggestion: &decision.Suggestion}
					if err := s.put(ctx, "WRITE#"+entry.Key, intent, job.ID); err != nil {
						return false, err
					}
					tx, e := service.CreateEQTransaction(ctx, mapping[r.AccountID], r.Date, r.Amount, decision.Enrichment, entry.ImportID, r.Status, rule)
					if e != nil {
						return false, writeFailure(e, "transaction-write")
					}
					s.recordSuggestion(ctx, decision, tx, entry.ImportID)
					entry.YNABID = tx.ID
					intent.Status, intent.TransactionID = "complete", tx.ID
					if err := s.put(ctx, "WRITE#"+entry.Key, intent, job.ID); err != nil {
						return false, err
					}
					snapshots[r.AccountID] = append(snapshots[r.AccountID], tx)
				}
				created++
			}
			if !job.DryRun && entry.YNABID != "" {
				var previous Write
				if found, err := s.get(ctx, "WRITE#"+entry.Key, &previous); err != nil {
					return false, err
				} else if found && previous.Status == "started" {
					previous.Status, previous.TransactionID = "complete", entry.YNABID
					if err := s.put(ctx, "WRITE#"+entry.Key, previous, job.ID); err != nil {
						return false, err
					}
				}
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
			seenReferences := map[string]bool{}
			references := []string{}
			for _, ref := range append(r.References, entry.Record.References...) {
				if ref != "" && !seenReferences[ref] {
					seenReferences[ref] = true
					references = append(references, ref)
				}
			}
			r.References = references
			entry.Record = r
		}
		claimed[entry.Key] = true
		for _, tx := range snapshots[r.AccountID] {
			if tx.ID == entry.YNABID && tx.PayeeName != nil {
				entry.Payee = *tx.PayeeName
			}
		}
		resolved[recordKey] = entry
		if !job.DryRun {
			if err = s.put(ctx, entry.Key, entry, job.ID); err != nil {
				return false, err
			}
		}
	}
	missing := false
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
		if _, err := service.UpdateEQTransaction(ctx, tx, r.Date, r.Amount, r.Status); err != nil {
			return bank.Fail(bank.Uncertain, "transaction-update")
		}
	}
	w.Status = "complete"
	return s.put(ctx, operation, w, s.Job.ID)
}
