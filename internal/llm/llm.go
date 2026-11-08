// Package llm sends one structured-output request through an OpenAI-compatible
// Responses endpoint (OpenRouter). Production and the replay harness share it,
// so what is evaluated is exactly what ships.
package llm

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Config selects the endpoint, key, model and reasoning effort.
type Config struct {
	Endpoint string
	Key      string
	Model    string
	// Effort is the reasoning effort ("low", "medium", "high"), or "" for the
	// model's default.
	Effort  string
	Timeout time.Duration
}

// Result is the model's text with its token usage and latency.
type Result struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	Latency      time.Duration
}

// Call sends instructions and input and returns JSON matching schema. Requests
// are only routed to providers that do not collect or train on the data.
func Call(ctx context.Context, cfg Config, instructions, input, schemaName string, schema map[string]any) (Result, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	httpClient := *http.DefaultClient
	httpClient.Timeout = timeout
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := openai.NewClient(
		option.WithBaseURL(cfg.Endpoint),
		option.WithAPIKey(cfg.Key),
		option.WithHTTPClient(&httpClient),
		// Operation retries belong to the caller's workflow.
		option.WithMaxRetries(0),
	)
	params := responses.ResponseNewParams{
		Model:        cfg.Model,
		Instructions: openai.String(instructions),
		Input:        responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
		Text:         responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigParamOfJSONSchema(schemaName, schema)},
	}
	if cfg.Effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(cfg.Effort)}
	}
	started := time.Now()
	resp, err := client.Responses.New(ctx, params, option.WithJSONSet("provider", map[string]any{"data_collection": "deny"}))
	if err != nil {
		return Result{Latency: time.Since(started)}, fmt.Errorf("calling Responses API: %w", err)
	}
	out := Result{Text: resp.OutputText(), InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, Latency: time.Since(started)}
	if out.Text == "" {
		return out, fmt.Errorf("empty response from API")
	}
	return out, nil
}
