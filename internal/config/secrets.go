package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/nathanfredericks/transactions/internal/types"
)

func loadSecretPayload(ctx context.Context, secretARN string) ([]byte, error) {
	cfg, err := GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := secretsmanager.NewFromConfig(cfg)
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &secretARN,
	})
	if err != nil {
		return nil, fmt.Errorf("getting secret value: %w", err)
	}
	payload, err := parseSecretPayload(out.SecretString, out.SecretBinary)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func GetSecrets(ctx context.Context) (*types.Secrets, error) {
	if cache := getInvocationCache(ctx); cache != nil && cache.secrets != nil {
		return cache.secrets, nil
	}
	payload, err := loadSecretPayload(ctx, os.Getenv("AWS_SECRET_ARN"))
	if err != nil {
		return nil, err
	}

	var s types.Secrets
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("parsing secret JSON: %w", err)
	}
	if cache := getInvocationCache(ctx); cache != nil {
		cache.secrets = &s
	}
	return &s, nil
}

func parseSecretPayload(secretString *string, secretBinary []byte) ([]byte, error) {
	if secretString != nil && *secretString != "" {
		return []byte(*secretString), nil
	}
	if len(secretBinary) > 0 {
		return secretBinary, nil
	}
	return nil, errors.New("secret has no usable payload")
}

// BankCredentials returns one bank's login from the Transactions secret.
// A missing bank or any empty field is an error, so callers can keep
// treating it as a configuration failure.
func BankCredentials(ctx context.Context, bankID string) (types.BankCredentials, error) {
	s, err := GetSecrets(ctx)
	if err != nil {
		return types.BankCredentials{}, err
	}
	c, ok := s.Banks[bankID]
	if !ok || c.Username == "" || c.Password == "" || c.MailToken == "" {
		return types.BankCredentials{}, fmt.Errorf("bank %q credentials incomplete", bankID)
	}
	return c, nil
}
