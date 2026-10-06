package main

import (
	"fmt"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigateway"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambdaeventsources"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssecretsmanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsses"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssesactions"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssns"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsssm"
	"github.com/aws/aws-cdk-go/awscdk/v2/customresources"
	golambda "github.com/aws/aws-cdk-go/awscdklambdagoalpha/v2"
	"github.com/aws/jsii-runtime-go"
)

func main() {
	defer jsii.Close()

	const (
		account              = "187489282488"
		region               = "ca-central-1"
		bucketName           = "nathanfredericks-transactions"
		tableName            = "TransactionOverrides"
		secretArn            = "arn:aws:secretsmanager:ca-central-1:187489282488:secret:transactions-ZuMkXL"
		ynabBudgetID         = "e0e7f122-6f2f-41f3-9b84-6d8f49fd5eab"
		appconfigApp         = "fvwbvsa"
		appconfigEnvironment = "yu292q7"
		appconfigConfig      = "59dpaji"
	)

	appconfigBase := fmt.Sprintf("arn:aws:appconfig:%s:%s:application/%s", region, account, appconfigApp)
	ssmBase := fmt.Sprintf("arn:aws:ssm:%s:%s:parameter/transactions", region, account)

	app := awscdk.NewApp(nil)

	stack := awscdk.NewStack(app, jsii.String("TransactionsStack"), &awscdk.StackProps{
		Env: &awscdk.Environment{
			Account: jsii.String(account),
			Region:  jsii.String(region),
		},
	})

	bucket := awss3.Bucket_FromBucketName(stack, jsii.String("TransactionsBucket"), jsii.String(bucketName))

	table := awsdynamodb.Table_FromTableName(stack, jsii.String("OverridesTable"), jsii.String(tableName))

	secret := awssecretsmanager.Secret_FromSecretCompleteArn(stack, jsii.String("TransactionsSecret"), jsii.String(secretArn))

	eqBucketName := "transactions-eq-state-" + account + "-" + region
	eqTable := awsdynamodb.NewTable(stack, jsii.String("EQState"), &awsdynamodb.TableProps{
		TableName: jsii.String("TransactionsEQState"), PartitionKey: &awsdynamodb.Attribute{Name: jsii.String("key"), Type: awsdynamodb.AttributeType_STRING},
		BillingMode: awsdynamodb.BillingMode_PAY_PER_REQUEST, Encryption: awsdynamodb.TableEncryption_AWS_MANAGED,
		PointInTimeRecoverySpecification: &awsdynamodb.PointInTimeRecoverySpecification{PointInTimeRecoveryEnabled: jsii.Bool(true)},
		RemovalPolicy:                    awscdk.RemovalPolicy_RETAIN,
	})
	eqBucket := awss3.NewBucket(stack, jsii.String("EQPrivateState"), &awss3.BucketProps{
		BucketName: jsii.String(eqBucketName), Encryption: awss3.BucketEncryption_S3_MANAGED,
		BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(), EnforceSSL: jsii.Bool(true), Versioned: jsii.Bool(true), RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
		LifecycleRules: &[]*awss3.LifecycleRule{
			{Prefix: jsii.String("jobs/"), Expiration: awscdk.Duration_Days(jsii.Number(30)), NoncurrentVersionExpiration: awscdk.Duration_Days(jsii.Number(7))},
			{Prefix: jsii.String("session/"), NoncurrentVersionExpiration: awscdk.Duration_Days(jsii.Number(1))},
		},
	})
	awsssm.NewStringParameter(stack, jsii.String("EQIntakeEnabled"), &awsssm.StringParameterProps{ParameterName: jsii.String("/transactions/eq-enabled"), StringValue: jsii.String("false")})

	topic := awssns.NewTopic(stack, jsii.String("TransactionsTopic"), nil)

	topic.AddToResourcePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Principals: &[]awsiam.IPrincipal{
			awsiam.NewServicePrincipal(jsii.String("ses.amazonaws.com"), nil),
		},
		Actions: &[]*string{
			jsii.String("sns:Publish"),
		},
		Resources: &[]*string{
			topic.TopicArn(),
		},
		Conditions: &map[string]interface{}{
			"StringEquals": map[string]string{
				"AWS:SourceAccount": account,
			},
		},
	}))

	ruleSet := awsses.NewReceiptRuleSet(stack, jsii.String("DefaultRuleSet"), &awsses.ReceiptRuleSetProps{
		ReceiptRuleSetName: jsii.String("Default"),
	})

	transactionsRule := ruleSet.AddRule(jsii.String("TransactionsRule"), &awsses.ReceiptRuleOptions{
		Recipients: &[]*string{
			jsii.String("transactions@fredericks.app"),
		},
		ScanEnabled: jsii.Bool(true),
		Actions: &[]awsses.IReceiptRuleAction{
			awssesactions.NewS3(&awssesactions.S3Props{
				Bucket: bucket,
				Topic:  topic,
			}),
		},
	})

	setActiveRuleSet := customresources.NewAwsCustomResource(stack, jsii.String("ActivateDefaultRuleSet"), &customresources.AwsCustomResourceProps{
		InstallLatestAwsSdk: jsii.Bool(false),
		OnCreate: &customresources.AwsSdkCall{
			Service: jsii.String("SES"),
			Action:  jsii.String("setActiveReceiptRuleSet"),
			Parameters: map[string]interface{}{
				"RuleSetName": "Default",
			},
			PhysicalResourceId: customresources.PhysicalResourceId_Of(jsii.String("DefaultReceiptRuleSetActive")),
		},
		OnUpdate: &customresources.AwsSdkCall{
			Service: jsii.String("SES"),
			Action:  jsii.String("setActiveReceiptRuleSet"),
			Parameters: map[string]interface{}{
				"RuleSetName": "Default",
			},
			PhysicalResourceId: customresources.PhysicalResourceId_Of(jsii.String("DefaultReceiptRuleSetActive")),
		},
		OnDelete: &customresources.AwsSdkCall{
			Service:            jsii.String("SES"),
			Action:             jsii.String("setActiveReceiptRuleSet"),
			Parameters:         map[string]interface{}{},
			PhysicalResourceId: customresources.PhysicalResourceId_Of(jsii.String("DefaultReceiptRuleSetInactive")),
		},
		Policy: customresources.AwsCustomResourcePolicy_FromSdkCalls(&customresources.SdkCallsPolicyOptions{
			Resources: customresources.AwsCustomResourcePolicy_ANY_RESOURCE(),
		}),
	})
	setActiveRuleSet.Node().AddDependency(transactionsRule)

	fn := golambda.NewGoFunction(stack, jsii.String("TransactionsFunction"), &golambda.GoFunctionProps{
		Entry:        jsii.String("cmd/lambda"),
		Architecture: awslambda.Architecture_ARM_64(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(30)),
		Environment: &map[string]*string{
			"AWS_S3_BUCKET_NAME":                            jsii.String(bucketName),
			"YNAB_BUDGET_ID":                                jsii.String(ynabBudgetID),
			"AWS_SECRET_ARN":                                jsii.String(secretArn),
			"APPCONFIG_APPLICATION":                         jsii.String(appconfigApp),
			"APPCONFIG_ENVIRONMENT":                         jsii.String(appconfigEnvironment),
			"AWS_TRANSACTION_OVERRIDES_DYNAMODB_TABLE_NAME": jsii.String(tableName),
			"APPCONFIG_CONFIGURATION":                       jsii.String(appconfigConfig),
		},
	})

	workflowArn := fmt.Sprintf("arn:aws:states:%s:%s:stateMachine:bank-import-eq", region, account)
	fn.AddEnvironment(jsii.String("EQ_WORKFLOW_ARN"), jsii.String(workflowArn), nil)
	fn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{Actions: &[]*string{jsii.String("states:StartExecution")}, Resources: &[]*string{jsii.String(workflowArn)}}))
	eqFn := golambda.NewGoFunction(stack, jsii.String("EQCoordinator"), &golambda.GoFunctionProps{
		FunctionName: jsii.String("transactions-eq-coordinator"), Entry: jsii.String("cmd/lambda"), Architecture: awslambda.Architecture_ARM_64(), Timeout: awscdk.Duration_Seconds(jsii.Number(180)),
		Environment: &map[string]*string{
			"EQ_INTERNAL": jsii.String("true"), "EQ_STATE_TABLE": eqTable.TableName(), "EQ_STATE_BUCKET": eqBucket.BucketName(),
			"YNAB_BUDGET_ID": jsii.String(ynabBudgetID), "AWS_SECRET_ARN": jsii.String(secretArn),
			"AWS_TRANSACTION_OVERRIDES_DYNAMODB_TABLE_NAME": jsii.String(tableName),
		},
	})
	eqTable.GrantReadWriteData(eqFn)
	eqBucket.GrantReadWrite(eqFn, nil)
	table.GrantReadData(eqFn)
	secret.GrantRead(eqFn, nil)
	eqFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{Actions: &[]*string{jsii.String("ssm:GetParameter"), jsii.String("ssm:GetParameters")}, Resources: &[]*string{jsii.String(ssmBase + "/payment-processors"), jsii.String(ssmBase + "/timezone"), jsii.String(ssmBase + "/openai-endpoint"), jsii.String(ssmBase + "/openai-model")}}))
	awscdk.NewCfnOutput(stack, jsii.String("EQStateBucket"), &awscdk.CfnOutputProps{Value: eqBucket.BucketName()})
	awscdk.NewCfnOutput(stack, jsii.String("EQStateTable"), &awscdk.CfnOutputProps{Value: eqTable.TableName()})
	awscdk.NewCfnOutput(stack, jsii.String("EQCoordinatorArn"), &awscdk.CfnOutputProps{Value: eqFn.FunctionArn()})

	bucket.GrantRead(fn, nil)
	table.GrantReadData(fn)
	secret.GrantRead(fn, nil)

	fn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("ssm:GetParameter"),
			jsii.String("ssm:GetParameters"),
		},
		Resources: &[]*string{
			jsii.String(ssmBase + "/payment-processors"),
			jsii.String(ssmBase + "/timezone"),
			jsii.String(ssmBase + "/openai-endpoint"),
			jsii.String(ssmBase + "/openai-model"),
			jsii.String(ssmBase + "/eq-enabled"),
		},
	}))

	fn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("appconfig:GetLatestConfiguration"),
			jsii.String("appconfig:StartConfigurationSession"),
		},
		Resources: &[]*string{
			jsii.String(appconfigBase),
			jsii.String(appconfigBase + "/environment/" + appconfigEnvironment),
			jsii.String(appconfigBase + "/configurationprofile/" + appconfigConfig),
			jsii.String(appconfigBase + "/environment/" + appconfigEnvironment + "/configuration/*"),
		},
	}))

	fn.AddEventSource(awslambdaeventsources.NewSnsEventSource(topic, nil))

	api := awsapigateway.NewRestApi(stack, jsii.String("TransactionsApi"), &awsapigateway.RestApiProps{
		RestApiName: jsii.String("TransactionsApi"),
	})

	integration := awsapigateway.NewLambdaIntegration(fn, nil)
	api.Root().AddMethod(jsii.String("POST"), integration, nil)

	app.Synth(nil)
}
