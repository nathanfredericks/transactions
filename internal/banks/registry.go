// Package banks is the only bank-specific wiring in the application.
package banks

import (
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks/eq"
	"github.com/nathanfredericks/transactions/internal/banks/nbdb"
	"github.com/nathanfredericks/transactions/internal/banks/rogers"
)

var All = []bank.Registration{
	{ID: "eq-bank", ImportPrefix: "EQ", PurchaseAccount: "EQ Bank Card", Name: "EQ Bank", Strategy: "posted", Browser: "chromium", API: eq.API{}},
	{ID: "rogers-bank", Name: "Rogers Bank", Strategy: "posted", Browser: "cloak", API: rogers.API{}},
	{ID: "nbdb", Name: "NBDB", Strategy: "balances", Browser: "cloak", API: nbdb.API{}},
}

func Find(id string) (bank.Registration, bool) {
	for _, r := range All {
		if r.ID == id {
			return r, true
		}
	}
	return bank.Registration{}, false
}
