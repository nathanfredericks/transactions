package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"text/template"
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
	httpClient  *http.Client
}

type ynabErrorResponse struct {
	APIError *struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Detail string `json:"detail"`
	} `json:"error"`
}

func (e *ynabErrorResponse) Error() string {
	if e == nil || e.APIError == nil {
		return "ynab api: unknown error"
	}
	return fmt.Sprintf("api: error id=%s name=%s detail=%s", e.APIError.ID, e.APIError.Name, e.APIError.Detail)
}

type ynabPayee struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Deleted           bool    `json:"deleted"`
	TransferAccountID *string `json:"transfer_account_id"`
}

type ynabPayloadTransaction struct {
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

	return &ynabClient{
		accessToken: secrets.YNABAccessToken,
		httpClient:  http.DefaultClient,
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
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.accessToken))
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if res.StatusCode >= 400 {
		apiErr := &ynabErrorResponse{}
		if err := json.Unmarshal(body, apiErr); err == nil && apiErr.APIError != nil {
			return apiErr
		}
		return fmt.Errorf("ynab api: status=%d body=%s", res.StatusCode, strings.TrimSpace(string(body)))
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
			Transaction  *types.YNABTransaction   `json:"transaction"`
			Transactions []*types.YNABTransaction `json:"transactions"`
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
	return nil, fmt.Errorf("ynab api returned no created transaction")
}

func GetPayees(ctx context.Context) ([]string, error) {
	env := config.GetEnv()
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}

	response := struct {
		Data struct {
			Payees []*ynabPayee `json:"payees"`
		} `json:"data"`
	}{}

	path := fmt.Sprintf("/budgets/%s/payees", env.YNABBudgetID)
	if err := client.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, fmt.Errorf("fetching payees: %w", err)
	}

	var names []string
	for _, p := range response.Data.Payees {
		if p.TransferAccountID == nil && !p.Deleted {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

func CheckForRecentTransaction(ctx context.Context, accountID string, amount float64, daysBack int) (bool, error) {
	env := config.GetEnv()
	client, err := newYNABClient(ctx)
	if err != nil {
		return false, err
	}

	sinceDate := time.Now().AddDate(0, 0, -daysBack).Format("2006-01-02")
	values := url.Values{}
	values.Set("since_date", sinceDate)

	response := struct {
		Data struct {
			Transactions []*types.YNABTransaction `json:"transactions"`
		} `json:"data"`
	}{}

	path := fmt.Sprintf("/budgets/%s/accounts/%s/transactions?%s", env.YNABBudgetID, accountID, values.Encode())
	if err := client.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return false, fmt.Errorf("fetching transactions: %w", err)
	}

	targetAmount := int64(math.Round(amount * 1000))
	for _, t := range response.Data.Transactions {
		if t.Amount == targetAmount {
			return true, nil
		}
	}
	return false, nil
}

func formatDate(ctx context.Context, t time.Time) (string, error) {
	params, err := config.GetParameters(ctx)
	if err != nil {
		return "", fmt.Errorf("getting parameters: %w", err)
	}
	loc, err := time.LoadLocation(params.Timezone)
	if err != nil {
		return "", fmt.Errorf("loading timezone %s: %w", params.Timezone, err)
	}
	return t.In(loc).Format("2006-01-02"), nil
}

func CreateTransaction(ctx context.Context, accountID string, amount float64, payee string, date time.Time, category string, memo string) (*types.YNABTransaction, error) {
	env := config.GetEnv()

	dateStr, err := formatDate(ctx, date)
	if err != nil {
		return nil, err
	}

	payload := ynabPayloadTransaction{
		AccountID: accountID,
		Date:      dateStr,
		Amount:    int64(math.Round(amount * -1000)),
		PayeeName: &payee,
		Cleared:   ynabClearingStatusUncleared,
	}
	if category != "" {
		payload.CategoryID = &category
	}
	if memo != "" {
		payload.Memo = &memo
	}

	tx, err := createTransaction(ctx, env.YNABBudgetID, payload)
	if err != nil {
		return nil, fmt.Errorf("creating transaction: %w", err)
	}
	return tx, nil
}

func CreateTransactionWithOverride(ctx context.Context, accountID string, amount float64, date time.Time, override *types.TransactionOverride) (*types.YNABTransaction, error) {
	env := config.GetEnv()

	dateStr, err := formatDate(ctx, date)
	if err != nil {
		return nil, err
	}

	payload := ynabPayloadTransaction{
		AccountID: accountID,
		Date:      dateStr,
		Amount:    int64(math.Round(amount * -1000)),
		PayeeID:   &override.Payee,
		Cleared:   ynabClearingStatusUncleared,
	}
	if override.Category != "" {
		payload.CategoryID = &override.Category
	}
	if override.Memo != "" {
		funcMap := template.FuncMap{
			"formatDate": func(dateVal string, layout string) string {
				t, err := time.Parse("2006-01-02", dateVal)
				if err != nil {
					return dateVal
				}
				return t.Format(layout)
			},
			"subtractMonthFromDate": func(dateVal string) string {
				t, err := time.Parse("2006-01-02", dateVal)
				if err != nil {
					return dateVal
				}
				return t.AddDate(0, -1, 0).Format("2006-01-02")
			},
		}
		tmpl, err := template.New("memo").Funcs(funcMap).Parse(override.Memo)
		if err != nil {
			return nil, fmt.Errorf("parsing memo template: %w", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, map[string]string{"Date": dateStr}); err != nil {
			return nil, fmt.Errorf("executing memo template: %w", err)
		}
		memoStr := buf.String()
		payload.Memo = &memoStr
	}

	tx, err := createTransaction(ctx, env.YNABBudgetID, payload)
	if err != nil {
		return nil, fmt.Errorf("creating transaction with override: %w", err)
	}
	return tx, nil
}
