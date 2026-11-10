package config

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	// Embeds the zone database, so time zones load in any runtime image.
	_ "time/tzdata"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// EmailConfig routes a bank's alert mail to a YNAB account.
type EmailConfig struct {
	Bank          string   `json:"bank"`
	YNABAccountID string   `json:"ynabAccountId"`
	EmailAddress  string   `json:"emailAddress"`
	EmailSubject  string   `json:"emailSubject"`
	Last4         []string `json:"last4"`
}

type BankSettings struct {
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
	OpenAIEffort   string                  `json:"openaiEffort,omitempty"`
	ImportsEnabled bool                    `json:"importsEnabled"`
	Banks          map[string]BankSettings `json:"banks"`
	Email          []EmailConfig           `json:"email"`
	location       *time.Location
}

// Location is the budget's time zone, which dates transactions and alerts.
func (s *Settings) Location() *time.Location {
	if s.location == nil {
		return time.UTC
	}
	return s.location
}

// Today is the current date in the budget's time zone.
func (s *Settings) Today() time.Time {
	now := time.Now().In(s.Location())
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.Location())
}

// Load reads and validates the settings document from the named parameter.
func Load(ctx context.Context, params *ssm.Client, name string) (*Settings, error) {
	if name == "" {
		return nil, errors.New("settings parameter name is required")
	}
	r, e := params.GetParameter(ctx, &ssm.GetParameterInput{Name: &name})
	if e != nil {
		return nil, e
	}
	var s Settings
	if json.Unmarshal([]byte(*r.Parameter.Value), &s) != nil {
		return nil, errors.New("invalid settings JSON")
	}
	if s.Version != 1 || s.BudgetID == "" || s.Timezone == "" || s.OpenAIEndpoint == "" || s.OpenAIModel == "" {
		return nil, errors.New("incomplete settings")
	}
	if s.location, e = time.LoadLocation(s.Timezone); e != nil {
		return nil, e
	}
	for _, b := range s.Banks {
		if !b.Enabled {
			continue
		}
		if len(b.ExpectedAccounts) == 0 {
			return nil, errors.New("bank configuration incomplete")
		}
		seen := map[string]bool{}
		for _, id := range b.ExpectedAccounts {
			if id == "" || seen[id] {
				return nil, errors.New("invalid expected accounts")
			}
			seen[id] = true
		}
		for _, id := range b.ExcludedAccounts {
			if !seen[id] {
				return nil, errors.New("excluded account not expected")
			}
		}
	}
	return &s, nil
}
