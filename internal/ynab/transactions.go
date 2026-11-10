package ynab

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Account is a YNAB account; its note carries the bank account IDs it maps to.
type Account struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Note    *string `json:"note"`
	Balance int64   `json:"balance"`
	Closed  bool    `json:"closed"`
	Deleted bool    `json:"deleted"`
}

// Accounts reads the budget's accounts.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var response struct {
		Data struct {
			Accounts []Account `json:"accounts"`
		} `json:"data"`
	}
	err := c.Get(ctx, "/accounts", &response)
	return response.Data.Accounts, err
}

// AccountTransactions reads an account's transactions dated on or after
// since (YYYY-MM-DD), deleted ones included.
func (c *Client) AccountTransactions(ctx context.Context, account, since string) ([]*Transaction, error) {
	var response struct {
		Data struct {
			Transactions []*Transaction `json:"transactions"`
		} `json:"data"`
	}
	err := c.Get(ctx, "/accounts/"+account+"/transactions?last_knowledge_of_server=0&since_date="+url.QueryEscape(since), &response)
	return response.Data.Transactions, err
}

// CreateTransaction writes a bank or alert transaction. A posted one is
// cleared and carries its import ID; a pending one carries none, so YNAB can
// match it to the cleared import later.
func (c *Client) CreateTransaction(ctx context.Context, account, date string, amount int64, fields Fields, importID, status string) (*Transaction, error) {
	return c.create(ctx, transactionPayload(account, date, amount, fields, importID, status))
}

func transactionPayload(account, date string, amount int64, enrichment Fields, importID, status string) ynabPayloadTransaction {
	cleared := "uncleared"
	if status == "posted" {
		cleared = "cleared"
	}
	payload := ynabPayloadTransaction{AccountID: account, Date: date, Amount: amount, Cleared: cleared}
	payee := enrichment.Payee
	// An existing payee is written by ID so YNAB cannot re-resolve it by name.
	if payee.ID != "" {
		payload.PayeeID = &payee.ID
	} else if payee.Name != "" {
		name := payee.Name
		payload.PayeeName = &name
	}
	if enrichment.CategoryID != "" {
		category := enrichment.CategoryID
		payload.CategoryID = &category
	}
	if enrichment.Memo != "" {
		memo := enrichment.Memo
		payload.Memo = &memo
	}
	// Pending authorizations must remain user-entered in YNAB so a later
	// cleared import can match them. Keep operation identities in our ledger.
	if status == "posted" && importID != "" {
		payload.ImportID = &importID
	}
	return payload
}

// amountOnlyPayload is a user-entered, uncleared, unapproved entry with no payee,
// category, memo or import ID, so a later cleared import can match it.
func amountOnlyPayload(account, date string, amount int64) ynabPayloadTransaction {
	return ynabPayloadTransaction{AccountID: account, Date: date, Amount: amount, Cleared: ynabClearingStatusUncleared}
}

// CreateAmountOnly records a purchase alert that states only an amount.
// It has no import ID, so YNAB can match it to the later cleared import.
func (c *Client) CreateAmountOnly(ctx context.Context, account, date string, amount int64) (*Transaction, error) {
	return c.create(ctx, amountOnlyPayload(account, date, amount))
}

// Delete removes a transaction this importer created.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/budgets/"+c.budget+"/transactions/"+id, nil, nil)
}

// CheckAdjustmentPayee confirms the balance-adjustment payee exists and is not a transfer.
func (c *Client) CheckAdjustmentPayee(ctx context.Context, id string) error {
	var response struct {
		Data struct {
			Payees []ynabPayee `json:"payees"`
		} `json:"data"`
	}
	if err := c.Get(ctx, "/payees", &response); err != nil {
		return err
	}
	for _, p := range response.Data.Payees {
		if p.ID == id && !p.Deleted && p.TransferAccountID == nil {
			return nil
		}
	}
	return errors.New("invalid adjustment payee")
}

// CreateBalanceAdjustment writes a reconciled, approved adjustment from the bank.
func (c *Client) CreateBalanceAdjustment(ctx context.Context, account, date string, amount int64, payee, bankName string) (*Transaction, error) {
	memo := "Entered automatically from " + bankName
	return c.create(ctx, ynabPayloadTransaction{AccountID: account, Date: date, Amount: amount, PayeeID: &payee, Memo: &memo, Cleared: "reconciled", Approved: true})
}
