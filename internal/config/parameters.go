package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nathanfredericks/transactions/internal/types"
)

const (
	paymentProcessorsParameterName = "/transactions/payment-processors"
	timezoneParameterName          = "/transactions/timezone"
	openAIEndpointParameterName    = "/transactions/openai-endpoint"
	openAIModelParameterName       = "/transactions/openai-model"
	defaultOpenAIEndpoint          = "https://openrouter.ai/api/v1"
	defaultOpenAIModel             = "openai/gpt-5-mini"
)

var loadParameters = func(ctx context.Context) (map[string]string, error) {
	cfg, err := GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ssm.NewFromConfig(cfg)
	names := []string{
		paymentProcessorsParameterName,
		timezoneParameterName,
		openAIEndpointParameterName,
		openAIModelParameterName,
	}
	out, err := client.GetParameters(ctx, &ssm.GetParametersInput{
		Names: names,
	})
	if err != nil {
		return nil, fmt.Errorf("getting parameters: %w", err)
	}
	if len(out.InvalidParameters) > 0 {
		return nil, fmt.Errorf("missing required parameters: %s", strings.Join(out.InvalidParameters, ", "))
	}

	values := make(map[string]string, len(out.Parameters))
	for _, p := range out.Parameters {
		if p.Name != nil && p.Value != nil {
			values[*p.Name] = *p.Value
		}
	}
	return values, nil
}

func GetParameters(ctx context.Context) (*types.Parameters, error) {
	if cache := getInvocationCache(ctx); cache != nil && cache.parameters != nil {
		return cache.parameters, nil
	}

	values, err := loadParameters(ctx)
	if err != nil {
		return nil, err
	}

	params := parametersFromValues(values)
	if cache := getInvocationCache(ctx); cache != nil {
		cache.parameters = params
	}
	return params, nil
}

func parametersFromValues(values map[string]string) *types.Parameters {
	tz := "UTC"
	if val := strings.TrimSpace(values[timezoneParameterName]); val != "" {
		tz = val
	}

	endpoint := defaultOpenAIEndpoint
	if val := strings.TrimSpace(values[openAIEndpointParameterName]); val != "" {
		endpoint = val
	}

	model := defaultOpenAIModel
	if val := strings.TrimSpace(values[openAIModelParameterName]); val != "" {
		model = val
	}

	return &types.Parameters{
		PaymentProcessors: splitCommaValues(values[paymentProcessorsParameterName]),
		Timezone:          tz,
		OpenAIEndpoint:    endpoint,
		OpenAIModel:       model,
	}
}

func splitCommaValues(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
}
