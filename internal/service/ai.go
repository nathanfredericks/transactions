package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/types"
)

var merchantAmountSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"amount":   map[string]any{"type": "number"},
		"merchant": map[string]any{"type": "string"},
	},
	"required":             []string{"amount", "merchant"},
	"additionalProperties": false,
}

var payeeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"payee": map[string]any{"type": "string"},
	},
	"required":             []string{"payee"},
	"additionalProperties": false,
}

var merchantPrefixRe = regexp.MustCompile(`.+\* `)

func callResponsesAPI(ctx context.Context, instructions string, userMessage string, schemaName string, schema map[string]any) (string, error) {
	env := config.GetEnv()
	secrets, err := config.GetSecrets(ctx)
	if err != nil {
		return "", fmt.Errorf("getting secrets: %w", err)
	}

	client := openai.NewClient(
		option.WithBaseURL(env.OpenAIEndpoint),
		option.WithAPIKey(secrets.OpenAIAPIKey),
	)

	resp, err := client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        env.OpenAIModel,
		Instructions: openai.String(instructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(userMessage),
		},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigParamOfJSONSchema(
				schemaName,
				schema,
			),
		},
	})
	if err != nil {
		return "", fmt.Errorf("calling Responses API: %w", err)
	}

	text := resp.OutputText()
	if text == "" {
		return "", fmt.Errorf("empty response from API")
	}

	return text, nil
}

func ExtractTransactionDetails(ctx context.Context, text string) (*types.MerchantAmount, error) {
	params, err := config.GetParameters(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting parameters: %w", err)
	}

	systemPrompt := fmt.Sprintf(
		"Extract the amount and merchant from the credit card alert. %s are payment processors, not merchants. Do not include them in the merchant.",
		strings.Join(params.PaymentProcessors, ", "),
	)

	rawText, err := callResponsesAPI(ctx, systemPrompt, text, "merchant_amount", merchantAmountSchema)
	if err != nil {
		return nil, fmt.Errorf("calling AI for transaction details: %w", err)
	}

	var result types.MerchantAmount
	if err := json.Unmarshal([]byte(rawText), &result); err != nil {
		return nil, fmt.Errorf("parsing merchant amount: %w", err)
	}
	return &result, nil
}

func MatchPayee(ctx context.Context, merchant string, payees []string) (string, error) {
	var payeeLines []string
	for _, p := range payees {
		payeeLines = append(payeeLines, "* "+p)
	}

	systemPrompt := fmt.Sprintf(
		"Payees:\n%s Select the most similar payee to the merchant from the list. If no match is found, create a new payee in Title Case using the merchant.",
		strings.Join(payeeLines, "\n"),
	)

	cleanedMerchant := merchantPrefixRe.ReplaceAllString(merchant, "")

	rawText, err := callResponsesAPI(ctx, systemPrompt, cleanedMerchant, "payee", payeeSchema)
	if err != nil {
		return "", fmt.Errorf("calling AI for payee match: %w", err)
	}

	var result types.PayeeMatch
	if err := json.Unmarshal([]byte(rawText), &result); err != nil {
		return "", fmt.Errorf("parsing payee match: %w", err)
	}
	return result.Payee, nil
}
