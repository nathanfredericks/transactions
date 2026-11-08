package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/nathanfredericks/transactions/internal/config"
)

var getParameters = config.GetParameters
var getSecrets = config.GetSecrets
var loadSettings = config.Load

func callResponsesAPI(ctx context.Context, instructions string, userMessage string, schemaName string, schema map[string]any) (string, error) {
	params, err := getParameters(ctx)
	if err != nil {
		return "", fmt.Errorf("getting parameters: %w", err)
	}
	secrets, err := getSecrets(ctx)
	if err != nil {
		return "", fmt.Errorf("getting secrets: %w", err)
	}

	// Keep the configured transport while giving AI its own request deadline.
	httpClient := *http.DefaultClient
	httpClient.Timeout = 30 * time.Second
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := openai.NewClient(
		option.WithBaseURL(params.OpenAIEndpoint),
		option.WithAPIKey(secrets.OpenAIAPIKey),
		// Pushover requires a bounded process-wide default client. AI has its
		// own deadline and leaves operation retries to the shared workflow.
		option.WithHTTPClient(&httpClient),
		option.WithMaxRetries(0),
	)

	resp, err := client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        params.OpenAIModel,
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
