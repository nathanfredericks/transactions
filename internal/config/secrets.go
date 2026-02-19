package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/nathanfredericks/transactions/internal/types"
)

var cachedSecrets *types.Secrets

func GetSecrets(ctx context.Context) (*types.Secrets, error) {
	if cachedSecrets != nil {
		return cachedSecrets, nil
	}
	env := GetEnv()
	cfg, err := GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := secretsmanager.NewFromConfig(cfg)
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &env.AWSSecretARN,
	})
	if err != nil {
		return nil, fmt.Errorf("getting secret value: %w", err)
	}
	var s types.Secrets
	if err := json.Unmarshal([]byte(*out.SecretString), &s); err != nil {
		return nil, fmt.Errorf("parsing secret JSON: %w", err)
	}
	cachedSecrets = &s
	return cachedSecrets, nil
}
