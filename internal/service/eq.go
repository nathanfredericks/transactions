package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/bank"

	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/types"
	"net/http"
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
func CreateEQTransaction(ctx context.Context, account, date string, amount int64, payee, importID, status string, rule *types.TransactionOverride) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	cleared := "uncleared"
	if status == "posted" {
		cleared = "cleared"
	}
	payload := ynabPayloadTransaction{AccountID: account, Date: date, Amount: amount, PayeeName: &payee, ImportID: &importID, Cleared: cleared}
	if rule != nil {
		payload.PayeeName = nil
		payload.PayeeID = &rule.Payee
		if rule.Category != "" {
			payload.CategoryID = &rule.Category
		}
		if rule.Memo != "" {
			memo, e := override.RenderMemo(rule.Memo, date)
			if e != nil {
				return nil, e
			}
			payload.Memo = &memo
		}
	}
	tx, err := createTransaction(ctx, client.budgetID, payload)
	if err == nil {
		return tx, nil
	}
	// An interrupted POST can have succeeded. Recover by stable import ID before retrying.
	existing, lookupErr := GetAccountTransactions(ctx, account)
	if lookupErr == nil {
		for _, t := range existing {
			if !t.Deleted && t.ImportID != nil && *t.ImportID == importID {
				return t, nil
			}
		}
	}
	if lookupErr == nil && errors.Is(err, errDuplicateImport) {
		return nil, bank.Fail(bank.Invalid, "previous-import-missing")
	}
	return nil, err
}
func UpdateEQTransaction(ctx context.Context, tx *types.YNABTransaction, date string, amount int64, status string) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	cleared := tx.Cleared
	if cleared != "reconciled" {
		cleared = "uncleared"
		if status == "posted" {
			cleared = "cleared"
		}
	}
	// Explicitly retain user fields; only bank date, amount and clearing state change.
	request := map[string]any{"transaction": map[string]any{"account_id": tx.AccountID, "date": date, "amount": amount, "cleared": cleared, "approved": tx.Approved, "payee_id": tx.PayeeID, "category_id": tx.CategoryID, "memo": tx.Memo}}
	var response struct {
		Data struct {
			Transaction *types.YNABTransaction `json:"transaction"`
		} `json:"data"`
	}
	err = client.do(ctx, http.MethodPut, fmt.Sprintf("/budgets/%s/transactions/%s", client.budgetID, tx.ID), request, &response)
	return response.Data.Transaction, err
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
	return fmt.Errorf("invalid adjustment payee")
}
func CreateBalanceAdjustment(ctx context.Context, account, date string, amount int64, payee, bankName string) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	memo := "Entered automatically from " + bankName
	return createTransaction(ctx, client.budgetID, ynabPayloadTransaction{AccountID: account, Date: date, Amount: amount, PayeeID: &payee, Memo: &memo, Cleared: "reconciled", Approved: true})
}

type LookupPayee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type LookupCategory struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}
type LookupGroup struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Deleted    bool             `json:"deleted"`
	Categories []LookupCategory `json:"categories"`
}
type LookupResult struct {
	Payees     []LookupPayee `json:"payees"`
	Categories []LookupGroup `json:"categories"`
}

func Lookups(ctx context.Context) (LookupResult, error) {
	out := LookupResult{Payees: []LookupPayee{}, Categories: []LookupGroup{}}
	c, e := newYNABClient(ctx)
	if e != nil {
		return out, e
	}
	base := fmt.Sprintf("/budgets/%s", c.budgetID)
	var payees struct {
		Data struct {
			Payees []ynabPayee `json:"payees"`
		} `json:"data"`
	}
	if e = c.do(ctx, "GET", base+"/payees", nil, &payees); e != nil {
		return out, e
	}
	for _, p := range payees.Data.Payees {
		if !p.Deleted && p.TransferAccountID == nil {
			out.Payees = append(out.Payees, LookupPayee{p.ID, p.Name})
		}
	}
	var categories struct {
		Data struct {
			Groups []LookupGroup `json:"category_groups"`
		} `json:"data"`
	}
	if e = c.do(ctx, "GET", base+"/categories", nil, &categories); e != nil {
		return out, e
	}
	for _, g := range categories.Data.Groups {
		if g.Deleted || g.Name == "Internal Master Category" || g.Name == "Credit Card Payments" || g.Name == "Hidden Categories" {
			continue
		}
		kept := []LookupCategory{}
		for _, v := range g.Categories {
			if !v.Deleted {
				kept = append(kept, v)
			}
		}
		g.Categories = kept
		out.Categories = append(out.Categories, g)
	}
	return out, nil
}
func ValidateRuleReferences(ctx context.Context, payee, category string) error {
	v, e := Lookups(ctx)
	if e != nil {
		return e
	}
	found := false
	for _, p := range v.Payees {
		found = found || p.ID == payee
	}
	if !found {
		return fmt.Errorf("invalid payee")
	}
	if category != "" {
		found = false
		for _, g := range v.Categories {
			for _, c := range g.Categories {
				found = found || c.ID == category
			}
		}
		if !found {
			return fmt.Errorf("invalid category")
		}
	}
	return nil
}
