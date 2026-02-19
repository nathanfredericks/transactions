package config

import (
	"os"

	"github.com/nathanfredericks/transactions/internal/types"
)

var cachedEnv *types.Env

func GetEnv() *types.Env {
	if cachedEnv != nil {
		return cachedEnv
	}
	budgetID := os.Getenv("YNAB_BUDGET_ID")
	if budgetID == "" {
		budgetID = "last-used"
	}
	cachedEnv = &types.Env{
		AWSSecretARN:                         os.Getenv("AWS_SECRET_ARN"),
		AppConfigApplication:                 os.Getenv("APPCONFIG_APPLICATION"),
		AppConfigEnvironment:                 os.Getenv("APPCONFIG_ENVIRONMENT"),
		AppConfigConfiguration:               os.Getenv("APPCONFIG_CONFIGURATION"),
		AWSS3BucketName:                      os.Getenv("AWS_S3_BUCKET_NAME"),
		YNABBudgetID:                         budgetID,
		AWSTransactionOverridesDynamoDBTable: os.Getenv("AWS_TRANSACTION_OVERRIDES_DYNAMODB_TABLE_NAME"),
	}
	return cachedEnv
}
