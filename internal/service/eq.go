package service

import (
	"context"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/types"
	"net/http"
)

type YNABAccount struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Note    *string `json:"note"`
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
	err = client.do(ctx, http.MethodGet, fmt.Sprintf("/budgets/%s/accounts", config.GetEnv().YNABBudgetID), nil, &response)
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
	err = client.do(ctx, http.MethodGet, fmt.Sprintf("/budgets/%s/accounts/%s/transactions?since_date=2026-10-02", config.GetEnv().YNABBudgetID, account), nil, &response)
	return response.Data.Transactions, err
}
func CreateEQTransaction(ctx context.Context, account, date string, amount int64, payee, importID, status string, rule *types.TransactionOverride) (*types.YNABTransaction, error) {
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
	tx, err := createTransaction(ctx, config.GetEnv().YNABBudgetID, payload)
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
	err = client.do(ctx, http.MethodPut, fmt.Sprintf("/budgets/%s/transactions/%s", config.GetEnv().YNABBudgetID, tx.ID), request, &response)
	return response.Data.Transaction, err
}
