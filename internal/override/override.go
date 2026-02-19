package override

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	env := config.GetEnv()
	cfg, err := config.GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	client := dynamodb.NewFromConfig(cfg)
	out, err := client.Scan(ctx, &dynamodb.ScanInput{
		TableName: &env.AWSTransactionOverridesDynamoDBTable,
	})
	if err != nil {
		return nil, fmt.Errorf("scanning overrides table: %w", err)
	}

	var overrides []types.TransactionOverride
	for _, item := range out.Items {
		o := types.TransactionOverride{}
		if v, ok := item["payee"]; ok {
			o.Payee = attrToString(v)
		}
		if v, ok := item["category"]; ok {
			o.Category = attrToString(v)
		}
		if v, ok := item["memo"]; ok {
			o.Memo = attrToString(v)
		}
		if v, ok := item["query"]; ok {
			o.Query = attrToString(v)
		}
		if v, ok := item["updatedAt"]; ok {
			o.UpdatedAt = attrToString(v)
		}
		overrides = append(overrides, o)
	}

	sort.Slice(overrides, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, overrides[i].UpdatedAt)
		tj, _ := time.Parse(time.RFC3339, overrides[j].UpdatedAt)
		return ti.After(tj)
	})

	return overrides, nil
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

	now := date.In(loc)

	for i, o := range overrides {
		if o.Query == "" {
			continue
		}

		data := map[string]any{
			"amount":   amount,
			"merchant": strings.ToUpper(merchant),
			"day":      now.Day(),
			"month":    int(now.Month()),
		}

		queryReader := strings.NewReader(o.Query)
		dataBytes, err := json.Marshal(data)
		if err != nil {
			slog.Warn("failed to marshal override data", "error", err)
			continue
		}
		dataReader := strings.NewReader(string(dataBytes))

		var result strings.Builder
		if err := jsonlogic.Apply(queryReader, dataReader, &result); err != nil {
			slog.Warn("failed to evaluate JSON Logic", "error", err)
			continue
		}

		resultStr := strings.TrimSpace(result.String())
		if resultStr == "true" {
			return &overrides[i], nil
		}
	}

	return nil, nil
}

func RenderMemoTemplate(memoTemplate string, date time.Time) (string, error) {
	funcMap := template.FuncMap{
		"formatDate": func(layout string) string {
			return date.Format(layout)
		},
		"subtractMonthFromDate": func(months int) time.Time {
			return date.AddDate(0, -months, 0)
		},
	}

	tmpl, err := template.New("memo").Funcs(funcMap).Parse(memoTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing memo template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return "", fmt.Errorf("executing memo template: %w", err)
	}

	return buf.String(), nil
}
