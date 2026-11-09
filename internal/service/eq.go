package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nathanfredericks/transactions/internal/types"
)

type YNABAccount struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Note    *string `json:"note"`
	Balance int64   `json:"balance"`
	Closed  bool    `json:"closed"`
	Deleted bool    `json:"deleted"`
}

func GetAccounts(ctx context.Context) ([]YNABAccount, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Accounts []YNABAccount `json:"accounts"`
		} `json:"data"`
	}
	err = client.do(ctx, http.MethodGet, fmt.Sprintf("/budgets/%s/accounts", client.budgetID), nil, &response)
	return response.Data.Accounts, err
}

func GetAccountTransactions(ctx context.Context, account string) ([]*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Transactions []*types.YNABTransaction `json:"transactions"`
		} `json:"data"`
	}
	err = client.do(ctx, http.MethodGet, fmt.Sprintf("/budgets/%s/accounts/%s/transactions?last_knowledge_of_server=0", client.budgetID, account), nil, &response)
	return response.Data.Transactions, err
}

func CreateEQTransaction(ctx context.Context, account, date string, amount int64, enrichment types.Enrichment, importID, status string) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	return createTransaction(ctx, client.budgetID, eqTransactionPayload(account, date, amount, enrichment, importID, status))
}

func eqTransactionPayload(account, date string, amount int64, enrichment types.Enrichment, importID, status string) ynabPayloadTransaction {
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

// CreateAmountOnlyTransaction records a purchase alert that states only an amount.
// It has no import ID, so YNAB can match it to the later cleared import.
func CreateAmountOnlyTransaction(ctx context.Context, account, date string, amount int64) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	return createTransaction(ctx, client.budgetID, amountOnlyPayload(account, date, amount))
}

// DeleteTransaction removes a transaction this importer created.
func DeleteTransaction(ctx context.Context, id string) error {
	client, err := newYNABClient(ctx)
	if err != nil {
		return err
	}
	return client.do(ctx, http.MethodDelete, fmt.Sprintf("/budgets/%s/transactions/%s", client.budgetID, id), nil, nil)
}

func ValidateAdjustmentPayee(ctx context.Context, id string) error {
	client, err := newYNABClient(ctx)
	if err != nil {
		return err
	}
	var response struct {
		Data struct {
			Payees []ynabPayee `json:"payees"`
		} `json:"data"`
	}
	if err = client.do(ctx, "GET", fmt.Sprintf("/budgets/%s/payees", client.budgetID), nil, &response); err != nil {
		return err
	}
	for _, p := range response.Data.Payees {
		if p.ID == id && !p.Deleted && p.TransferAccountID == nil {
			return nil
		}
	}
	return errors.New("invalid adjustment payee")
}

func CreateBalanceAdjustment(ctx context.Context, account, date string, amount int64, payee, bankName string) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	memo := "Entered automatically from " + bankName
	return createTransaction(ctx, client.budgetID, ynabPayloadTransaction{AccountID: account, Date: date, Amount: amount, PayeeID: &payee, Memo: &memo, Cleared: "reconciled", Approved: true})
}
