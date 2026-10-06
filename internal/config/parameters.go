package config

import (
	"context"
	"github.com/nathanfredericks/transactions/internal/types"
)

func GetParameters(ctx context.Context) (*types.Parameters, error) {
	settings, err := Load(ctx)
	if err != nil {
		return nil, err
	}
	return &types.Parameters{Timezone: settings.Timezone, OpenAIEndpoint: settings.OpenAIEndpoint, OpenAIModel: settings.OpenAIModel, PaymentProcessors: settings.PaymentProcessors}, nil
}
