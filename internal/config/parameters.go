package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nathanfredericks/transactions/internal/types"
)

var cachedParameters *types.Parameters

func GetParameters(ctx context.Context) (*types.Parameters, error) {
	if cachedParameters != nil {
		return cachedParameters, nil
	}
	cfg, err := GetAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ssm.NewFromConfig(cfg)
	ppName := "/transactions/payment-processors"
	ppOut, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name: &ppName,
	})
	if err != nil {
		return nil, fmt.Errorf("getting payment-processors parameter: %w", err)
	}
	tzName := "/transactions/timezone"
	tzOut, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name: &tzName,
	})
	if err != nil {
		return nil, fmt.Errorf("getting timezone parameter: %w", err)
	}
	tz := "UTC"
	if tzOut.Parameter != nil && tzOut.Parameter.Value != nil {
		tz = *tzOut.Parameter.Value
	}
	var processors []string
	if ppOut.Parameter != nil && ppOut.Parameter.Value != nil {
		processors = strings.Split(*ppOut.Parameter.Value, ",")
	}
	cachedParameters = &types.Parameters{
		PaymentProcessors: processors,
		Timezone:          tz,
	}
	return cachedParameters, nil
}
