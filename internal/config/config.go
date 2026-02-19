package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/appconfigdata"
	"github.com/nathanfredericks/transactions/internal/types"
)

var cachedConfig *types.Config

func GetConfig(ctx context.Context) (*types.Config, error) {
	if cachedConfig != nil {
		return cachedConfig, nil
	}
	env := GetEnv()
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
	var c types.Config
	if err := json.Unmarshal(latest.Configuration, &c); err != nil {
		return nil, fmt.Errorf("parsing config JSON: %w", err)
	}
	cachedConfig = &c
	return cachedConfig, nil
}
