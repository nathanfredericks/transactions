package service

import (
	"context"
	"fmt"
	"time"

	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/llm"
)

// modelConfig is the configured endpoint, model and reasoning effort.
func modelConfig(ctx context.Context) (llm.Config, error) {
	settings, err := config.Load(ctx)
	if err != nil {
		return llm.Config{}, fmt.Errorf("getting settings: %w", err)
	}
	secrets, err := config.GetSecrets(ctx)
	if err != nil {
		return llm.Config{}, fmt.Errorf("getting secrets: %w", err)
	}
	return llm.Config{Endpoint: settings.OpenAIEndpoint, Key: secrets.OpenAIAPIKey, Model: settings.OpenAIModel, Effort: settings.OpenAIEffort, Timeout: 45 * time.Second}, nil
}

func callResponsesAPI(ctx context.Context, instructions string, userMessage string, schemaName string, schema map[string]any) (string, error) {
	cfg, err := modelConfig(ctx)
	if err != nil {
		return "", err
	}
	res, err := llm.Call(ctx, cfg, instructions, userMessage, schemaName, schema)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// ChooseWithModel asks the configured model to choose a payee (or name a new
// one) and a category, using the same prompt the replay harness evaluated. It
// returns the answer and the model that gave it.
func ChooseWithModel(ctx context.Context, in learn.ModelInput) (learn.ModelOutput, string, error) {
	cfg, err := modelConfig(ctx)
	if err != nil {
		return learn.ModelOutput{}, "", err
	}
	instructions, message, schema := learn.Prompt(in)
	raw, err := callResponsesAPI(ctx, instructions, message, "transaction_choice", schema)
	if err != nil {
		return learn.ModelOutput{}, cfg.Model, fmt.Errorf("calling model: %w", err)
	}
	out, err := learn.ParseOutput(raw, in)
	return out, cfg.Model, err
}
