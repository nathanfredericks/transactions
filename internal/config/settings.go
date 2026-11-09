package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nathanfredericks/transactions/internal/types"
)

type BankSettings struct {
	// SecretARN is ignored: bank logins now live in the Transactions secret.
	SecretARN        string   `json:"secretArn,omitempty"`
	ExpectedAccounts []string `json:"expectedAccounts"`
	ExcludedAccounts []string `json:"excludedAccounts"`
	StartDate        string   `json:"startDate"`
	Enabled          bool     `json:"enabled"`
	EmailSender      string   `json:"emailSender"`
	EmailSubject     string   `json:"emailSubject"`
	EmailCodeLength  int      `json:"emailCodeLength"`
	// AlertAccountID is the YNAB account that receives amount-only purchase
	// alerts, for banks whose alerts do not identify the account themselves.
	AlertAccountID string `json:"alertAccountId,omitempty"`
}
type Settings struct {
	Version           int    `json:"version"`
	BudgetID          string `json:"budgetId"`
	AdjustmentPayeeID string `json:"adjustmentPayeeId"`
	Timezone          string `json:"timezone"`
	OpenAIEndpoint    string `json:"openaiEndpoint"`
	OpenAIModel       string `json:"openaiModel"`
	// OpenAIEffort is the model's reasoning effort ("" for its default).
	OpenAIEffort      string                  `json:"openaiEffort,omitempty"`
	PaymentProcessors []string                `json:"paymentProcessors"`
	ImportsEnabled    bool                    `json:"importsEnabled"`
	Banks             map[string]BankSettings `json:"banks"`
	Email             []types.EmailConfig     `json:"email"`
	Webhook           []types.WebhookConfig   `json:"webhook"`
}
type settingsKey struct{}

func WithSettings(ctx context.Context, s *Settings) context.Context {
	return context.WithValue(ctx, settingsKey{}, s)
}
func Load(ctx context.Context) (*Settings, error) {
	if s, ok := ctx.Value(settingsKey{}).(*Settings); ok {
		return s, nil
	}
	cfg, e := GetAWSConfig(ctx)
	if e != nil {
		return nil, e
	}
	name := os.Getenv("SETTINGS_PARAMETER")
	if name == "" {
		return nil, fmt.Errorf("SETTINGS_PARAMETER is required")
	}
	r, e := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{Name: &name})
	if e != nil {
		return nil, e
	}
	var s Settings
	if json.Unmarshal([]byte(*r.Parameter.Value), &s) != nil {
		return nil, fmt.Errorf("invalid settings JSON")
	}
	if s.Version != 1 || s.BudgetID == "" || s.Timezone == "" || s.OpenAIEndpoint == "" || s.OpenAIModel == "" {
		return nil, fmt.Errorf("incomplete settings")
	}
	if _, e = time.LoadLocation(s.Timezone); e != nil {
		return nil, e
	}
	for _, b := range s.Banks {
		if !b.Enabled {
			continue
		}
		if len(b.ExpectedAccounts) == 0 {
			return nil, fmt.Errorf("bank configuration incomplete")
		}
		seen := map[string]bool{}
		for _, id := range b.ExpectedAccounts {
			if id == "" || seen[id] {
				return nil, fmt.Errorf("invalid expected accounts")
			}
			seen[id] = true
		}
		for _, id := range b.ExcludedAccounts {
			if !seen[id] {
				return nil, fmt.Errorf("excluded account not expected")
			}
		}
	}
	return &s, nil
}
