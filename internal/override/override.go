package override

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/state"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/diegoholiveira/jsonlogic/v3"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/types"
)

func GetTransactionOverrides(ctx context.Context) ([]types.TransactionOverride, error) {
	cfg, err := config.GetAWSConfig(ctx)
	if err != nil {
		return nil, err
	}
	store := state.Store{DB: dynamodb.NewFromConfig(cfg), Table: os.Getenv("STATE_TABLE")}
	rows, err := store.List(ctx, "RULES", "RULE#")
	if err != nil {
		return nil, err
	}
	var rules []types.TransactionOverride
	for _, raw := range rows {
		var r types.TransactionOverride
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].UpdatedAt == rules[j].UpdatedAt {
			return rules[i].ID < rules[j].ID
		}
		return rules[i].UpdatedAt > rules[j].UpdatedAt
	})
	return rules, nil
}

// DecodeOverrides is shared by the DynamoDB reader and contract tests.
func DecodeOverrides(items []map[string]ddbtypes.AttributeValue) []types.TransactionOverride {
	overrides := make([]types.TransactionOverride, 0, len(items))
	for _, item := range items {
		overrides = append(overrides, types.TransactionOverride{
			ID: attrToString(item["id"]), Name: attrToString(item["name"]),
			Payee: attrToString(item["payee"]), Category: attrToString(item["category"]),
			Memo: attrToString(item["memo"]), Query: attrToString(item["query"]), UpdatedAt: attrToString(item["updatedAt"]),
		})
	}
	sort.SliceStable(overrides, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, overrides[i].UpdatedAt)
		tj, _ := time.Parse(time.RFC3339, overrides[j].UpdatedAt)
		if ti.Equal(tj) {
			return overrides[i].ID < overrides[j].ID
		}
		return ti.After(tj)
	})
	return overrides
}

func attrToString(v ddbtypes.AttributeValue) string {
	if sv, ok := v.(*ddbtypes.AttributeValueMemberS); ok {
		return sv.Value
	}
	return ""
}

func FindOverride(ctx context.Context, overrides []types.TransactionOverride, amount float64, merchant string, date time.Time) (*types.TransactionOverride, error) {
	params, err := config.GetParameters(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting parameters: %w", err)
	}

	loc, err := time.LoadLocation(params.Timezone)
	if err != nil {
		return nil, fmt.Errorf("loading timezone %s: %w", params.Timezone, err)
	}

	return Match(overrides, amount, merchant, date.In(loc)), nil
}

// Match receives an already localized transaction date. Date-only bank records
// must be parsed in the budget timezone, not shifted from midnight UTC.
func Match(overrides []types.TransactionOverride, amount float64, merchant string, date time.Time) *types.TransactionOverride {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return nil
	}
	for i, o := range overrides {
		if o.Query == "" || o.Payee == "" {
			continue
		}
		data, _ := json.Marshal(map[string]any{"amount": amount, "merchant": strings.Join(strings.Fields(strings.ToUpper(merchant)), " "), "day": date.Day(), "month": int(date.Month())})
		var result strings.Builder
		if err := safeApply(o.Query, data, &result); err != nil {
			slog.Warn("Invalid override query", "ruleId", o.ID, "ruleName", o.Name, "error", err)
			continue
		}
		if strings.TrimSpace(result.String()) == "true" {
			slog.Info("Override matched", "ruleId", o.ID, "ruleName", o.Name)
			return &overrides[i]
		}
	}
	return nil
}

func safeApply(query string, data []byte, result *strings.Builder) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("invalid JSON Logic: %v", recovered)
		}
	}()
	return jsonlogic.Apply(strings.NewReader(query), bytes.NewReader(data), result)
}

// RenderMemo renders the admin's Go template contract against a local ISO date.
func RenderMemo(memo, date string) (string, error) {
	funcs := template.FuncMap{
		"formatDate": func(value, layout string) (string, error) {
			d, err := time.Parse("2006-01-02", value)
			if err != nil {
				return "", err
			}
			return d.Format(layout), nil
		},
		"subtractMonthFromDate": func(value string) (string, error) {
			d, err := time.Parse("2006-01-02", value)
			if err != nil {
				return "", err
			}
			first := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
			last := first.AddDate(0, 1, -1).Day()
			day := d.Day()
			if day > last {
				day = last
			}
			return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), nil
		},
	}
	tmpl, err := template.New("memo").Option("missingkey=error").Funcs(funcs).Parse(memo)
	if err != nil {
		return "", fmt.Errorf("parsing memo template: %w", err)
	}
	var buf bytes.Buffer
	if err = tmpl.Execute(&buf, map[string]string{"Date": date}); err != nil {
		return "", fmt.Errorf("rendering memo template: %w", err)
	}
	return buf.String(), nil
}

func RenderMemoTemplate(memo string, date time.Time) (string, error) {
	return RenderMemo(memo, date.Format("2006-01-02"))
}
