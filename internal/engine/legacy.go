package engine

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
)

// legacyLedger covers EQ records imported before EQ moved to the posted path:
// posted rows of the old tx# ledger, and the records the cutover baseline linked
// to existing YNAB entries. Some carry no import ID in YNAB, so the import-ID
// check alone would write them again. Delete this once EQ's fetch window starts
// after the last ledger row.
type legacyLedger struct {
	records []Record
	linked  map[string]bool
}

func (e *Engine) legacyLedger(ctx context.Context) (legacyLedger, error) {
	l := legacyLedger{linked: map[string]bool{}}
	rows, err := e.Store.List(ctx, "BANK#"+e.Job.Bank, "tx#")
	if err != nil {
		return l, err
	}
	for _, raw := range rows {
		var entry struct {
			Record Record `json:"record"`
		}
		if json.Unmarshal(raw, &entry) != nil {
			return l, bankStateError()
		}
		// A pending row was written without an import ID; YNAB matches its
		// posted record when that is imported.
		if entry.Record.Status == "posted" {
			l.records = append(l.records, entry.Record)
		}
	}
	var baseline struct {
		Seen map[string]bool `json:"seen"`
	}
	if _, err = e.get(ctx, "BASELINE", &baseline); err != nil {
		return l, err
	}
	maps.Copy(l.linked, baseline.Seen)
	return l, nil
}

func (l legacyLedger) covers(r Record) bool {
	if l.linked[r.AccountID+"#"+identity(r)] {
		return true
	}
	for _, x := range l.records {
		if exactMatch(x, r) {
			return true
		}
	}
	return false
}

func identity(r Record) string {
	if r.BankID != nil {
		return *r.BankID
	}
	return r.ProvisionalID
}

func merchant(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }

// exactMatch is the old ledger's identity test: the native bank ID, else a
// shared native reference, else the provisional hash with the same date,
// amount and merchant.
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
	if aHasReference && bHasReference {
		return false
	}
	return a.ProvisionalID != "" && a.ProvisionalID == b.ProvisionalID &&
		a.Date == b.Date && a.Amount == b.Amount && merchant(a.Description) == merchant(b.Description)
}

type Record = bank.Record

func purchaseAccount(id string) string { r, _ := banks.Find(id); return r.PurchaseAccount }
