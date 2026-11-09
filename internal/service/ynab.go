package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/types"
)

const (
	ynabAPIEndpoint             = "https://api.youneedabudget.com/v1"
	ynabClearingStatusUncleared = "uncleared"
)

type ynabClient struct {
	accessToken string
	budgetID    string
	httpClient  *http.Client
}

// ErrDuplicateImport means YNAB already has a transaction with the write's
// import ID, deleted ones included, so the write is already done.
var ErrDuplicateImport = errors.New("ynab already has this import id")

// StatusError is a YNAB response with an HTTP error status. A 4xx other than
// 429 means YNAB did not apply the request.
type StatusError struct {
	Status           int
	ID, Name, Detail string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ynab api: status=%d id=%s name=%s detail=%s", e.Status, e.ID, e.Name, e.Detail)
}

type ynabPayee struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Deleted           bool    `json:"deleted"`
	TransferAccountID *string `json:"transfer_account_id"`
}

type ynabPayloadTransaction struct {
	ImportID   *string `json:"import_id,omitempty"`
	AccountID  string  `json:"account_id"`
	Date       string  `json:"date"`
	Amount     int64   `json:"amount"`
	Cleared    string  `json:"cleared"`
	Approved   bool    `json:"approved"`
	PayeeID    *string `json:"payee_id,omitempty"`
	PayeeName  *string `json:"payee_name,omitempty"`
	CategoryID *string `json:"category_id,omitempty"`
	Memo       *string `json:"memo,omitempty"`
}

func newYNABClient(ctx context.Context) (*ynabClient, error) {
	secrets, err := config.GetSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting secrets: %w", err)
	}

	settings, err := config.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &ynabClient{
		budgetID:    settings.BudgetID,
		accessToken: secrets.YNABAccessToken,
		// Reuse the process transport so tests can substitute it.
		httpClient: &http.Client{Transport: http.DefaultClient.Transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *ynabClient) do(ctx context.Context, method string, path string, requestBody any, responseBody any) error {
	var bodyReader io.Reader
	if requestBody != nil {
		buf, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, ynabAPIEndpoint+path, bodyReader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 20*1024*1024))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if res.StatusCode >= 400 {
		apiErr := &StatusError{Status: res.StatusCode}
		var parsed struct {
			Error *struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Detail string `json:"detail"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &parsed) == nil && parsed.Error != nil {
			apiErr.ID, apiErr.Name, apiErr.Detail = parsed.Error.ID, parsed.Error.Name, parsed.Error.Detail
		}
		return apiErr
	}

	if responseBody == nil || len(body) == 0 {
		return nil
	}

	if err := json.Unmarshal(body, responseBody); err != nil {
		return fmt.Errorf("decoding response body: %w", err)
	}
	return nil
}

func createTransaction(ctx context.Context, budgetID string, payload ynabPayloadTransaction) (*types.YNABTransaction, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}

	request := struct {
		Transactions []ynabPayloadTransaction `json:"transactions"`
	}{
		Transactions: []ynabPayloadTransaction{payload},
	}

	response := struct {
		Data struct {
			DuplicateImportIDs []string                 `json:"duplicate_import_ids"`
			Transaction        *types.YNABTransaction   `json:"transaction"`
			Transactions       []*types.YNABTransaction `json:"transactions"`
		} `json:"data"`
	}{}

	path := fmt.Sprintf("/budgets/%s/transactions", budgetID)
	if err := client.do(ctx, http.MethodPost, path, &request, &response); err != nil {
		return nil, err
	}

	if response.Data.Transaction != nil {
		return response.Data.Transaction, nil
	}
	if len(response.Data.Transactions) > 0 {
		return response.Data.Transactions[0], nil
	}
	for _, id := range response.Data.DuplicateImportIDs {
		if payload.ImportID != nil && id == *payload.ImportID {
			return nil, ErrDuplicateImport
		}
	}
	return nil, errors.New("ynab api returned no created transaction")
}
