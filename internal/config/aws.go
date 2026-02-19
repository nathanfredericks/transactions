package config

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

var (
	awsCfg  aws.Config
	awsOnce sync.Once
	awsErr  error
)

func GetAWSConfig(ctx context.Context) (aws.Config, error) {
	awsOnce.Do(func() {
		awsCfg, awsErr = awsconfig.LoadDefaultConfig(ctx)
	})
	return awsCfg, awsErr
}
