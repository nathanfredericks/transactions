package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/nathanfredericks/transactions/internal/types"
)

var loadSecretPayload = func(ctx context.Context, secretARN string) ([]byte, error) {
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
	env := GetEnv()
	payload, err := loadSecretPayload(ctx, env.AWSSecretARN)
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
	return nil, fmt.Errorf("secret has no usable payload")
}
