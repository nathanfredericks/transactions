// Package banks is the only bank-specific wiring in the application.
package banks

import (
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks/eq"
	"github.com/nathanfredericks/transactions/internal/banks/nbdb"
	"github.com/nathanfredericks/transactions/internal/banks/rogers"
)

var All = []bank.Registration{
	{ID: "eq-bank", ImportPrefix: "EQ", PurchaseAccount: "EQ Bank Card", Name: "EQ Bank", Strategy: "reconcile", Browser: "chromium", Schedule: "cron(0 0/4 * * ? *)", New: func(d bank.Dependencies) bank.Adapter { return &eq.Adapter{Dependencies: d} }},
	{ID: "rogers-bank", Name: "Rogers Bank", Strategy: "posted", Browser: "cloak", Schedule: "cron(0 0/4 * * ? *)", New: func(d bank.Dependencies) bank.Adapter { return &rogers.Adapter{Dependencies: d} }},
	{ID: "nbdb", Name: "NBDB", Strategy: "balances", Browser: "cloak", Schedule: "cron(0 17 ? * MON-FRI *)", New: func(d bank.Dependencies) bank.Adapter { return &nbdb.Adapter{Dependencies: d} }},
}

func Find(id string) (bank.Registration, bool) {
	for _, r := range All {
		if r.ID == id {
			return r, true
		}
	}
	return bank.Registration{}, false
}
