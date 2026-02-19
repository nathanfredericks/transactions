package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/appconfigdata"
	"github.com/nathanfredericks/transactions/internal/types"
)

var loadLatestConfiguration = func(ctx context.Context, env *types.Env) ([]byte, error) {
	cfg, err := GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := appconfigdata.NewFromConfig(cfg)
	session, err := client.StartConfigurationSession(ctx, &appconfigdata.StartConfigurationSessionInput{
		ApplicationIdentifier:          &env.AppConfigApplication,
		EnvironmentIdentifier:          &env.AppConfigEnvironment,
		ConfigurationProfileIdentifier: &env.AppConfigConfiguration,
	})
	if err != nil {
		return nil, fmt.Errorf("starting AppConfig session: %w", err)
	}
	latest, err := client.GetLatestConfiguration(ctx, &appconfigdata.GetLatestConfigurationInput{
		ConfigurationToken: session.InitialConfigurationToken,
	})
	if err != nil {
		return nil, fmt.Errorf("getting latest configuration: %w", err)
	}
	return latest.Configuration, nil
}

func GetConfig(ctx context.Context) (*types.Config, error) {
	if cache := getInvocationCache(ctx); cache != nil && cache.config != nil {
		return cache.config, nil
	}
	env := GetEnv()
	content, err := loadLatestConfiguration(ctx, env)
	if err != nil {
		return nil, err
	}

	var c types.Config
	if err := json.Unmarshal(content, &c); err != nil {
		return nil, fmt.Errorf("parsing config JSON: %w", err)
	}
	if cache := getInvocationCache(ctx); cache != nil {
		cache.config = &c
	}
	return &c, nil
}
