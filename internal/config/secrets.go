package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Secrets is the JSON stored in the single Transactions secret. The Lambdas
// use the first four keys; the browser task uses the browser and bank keys.
type Secrets struct {
	YNABAccessToken        string                     `json:"YNAB_ACCESS_TOKEN"`
	OpenAIAPIKey           string                     `json:"OPENAI_API_KEY"`
	PushoverToken          string                     `json:"PUSHOVER_TOKEN"`
	PushoverUser           string                     `json:"PUSHOVER_USER"`
	CloakBrowserLicenseKey string                     `json:"CLOAKBROWSER_LICENSE_KEY"`
	BrowserBrokerToken     string                     `json:"BROWSER_BROKER_TOKEN"`
	Banks                  map[string]BankCredentials `json:"BANKS"`
}

// BankCredentials is one bank's login under the secret's BANKS map.
type BankCredentials struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	MailToken string `json:"mailToken"`
}

// LoadSecrets reads the Transactions secret.
func LoadSecrets(ctx context.Context, client *secretsmanager.Client, arn string) (*Secrets, error) {
	if arn == "" {
		return nil, errors.New("secret ARN is required")
	}
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &arn})
	if err != nil {
		return nil, fmt.Errorf("getting secret value: %w", err)
	}
	payload := out.SecretBinary
	if out.SecretString != nil && *out.SecretString != "" {
		payload = []byte(*out.SecretString)
	}
	if len(payload) == 0 {
		return nil, errors.New("secret has no usable payload")
	}
	var s Secrets
	// The parse error could quote the secret, so it is not passed on.
	if json.Unmarshal(payload, &s) != nil {
		return nil, errors.New("secret is not valid JSON")
	}
	return &s, nil
}

// Bank returns one bank's login, which must be complete.
func (s *Secrets) Bank(id string) (BankCredentials, error) {
	c, ok := s.Banks[id]
	if !ok || c.Username == "" || c.Password == "" || c.MailToken == "" {
		return BankCredentials{}, fmt.Errorf("bank %q credentials incomplete", id)
	}
	return c, nil
}
