package main

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecrassets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3assets"
	"github.com/aws/jsii-runtime-go"
)

type M = map[string]any

const (
	account = "187489282488"
	region  = "ca-central-1"
)

// schedules starts a scheduled retrieval for each bank, in America/Halifax time.
// A new bank needs its registry entry and a line here.
var schedules = map[string]string{
	"eq-bank":     "cron(0 0/4 * * ? *)",
	"rogers-bank": "cron(0 0/4 * * ? *)",
	"nbdb":        "cron(0 17 ? * MON-FRI *)",
}

func main() {
	defer jsii.Close()
	app := awscdk.NewApp(nil)
	stack := awscdk.NewStack(app, jsii.String("TransactionsEngine"), &awscdk.StackProps{Env: &awscdk.Environment{Account: jsii.String(account), Region: jsii.String(region)}})
	root, err := filepath.Abs(".")
	if err != nil {
		panic(err)
	}
	if filepath.Base(root) == "cdk" {
		root = filepath.Dir(root)
	}
	dist := filepath.Join(root, "dist", "lambda")
	if err := os.MkdirAll(dist, 0o750); err != nil {
		panic(err)
	}
	build := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dist, "bootstrap"), "./cmd/lambda") //nolint:gosec // fixed arguments; dist is under the repository
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	asset := awss3assets.NewAsset(stack, jsii.String("GoCode"), &awss3assets.AssetProps{Path: &dist})
	licenseFile := os.Getenv("CLOAKBROWSER_LICENSE_FILE")
	if licenseFile == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		licenseFile = filepath.Join(homeDir, ".cloakbrowser", "license.key")
	}
	image := awsecrassets.NewDockerImageAsset(stack, jsii.String("BrowserImage"), &awsecrassets.DockerImageAssetProps{Directory: &root, Platform: awsecrassets.Platform_LINUX_ARM64(), BuildSecrets: &map[string]*string{"cloak_license": awscdk.DockerBuildSecret_FromSrc(&licenseFile)}})
	res := func(id, kind string, p M) awscdk.CfnResource {
		return awscdk.NewCfnResource(stack, jsii.String(id), &awscdk.CfnResourceProps{Type: &kind, Properties: &p})
	}
	att := func(r awscdk.CfnResource, key string) *string {
		return r.GetAtt(&key, awscdk.ResolutionTypeHint_STRING).ToString()
	}
	sub := func(s string) any { return M{"Fn::Sub": s} }
	retain := func(r awscdk.CfnResource) { r.ApplyRemovalPolicy(awscdk.RemovalPolicy_RETAIN, nil) }
	table := res("State", "AWS::DynamoDB::Table", M{"BillingMode": "PAY_PER_REQUEST", "AttributeDefinitions": []M{{"AttributeName": "pk", "AttributeType": "S"}, {"AttributeName": "sk", "AttributeType": "S"}}, "KeySchema": []M{{"AttributeName": "pk", "KeyType": "HASH"}, {"AttributeName": "sk", "KeyType": "RANGE"}}, "PointInTimeRecoverySpecification": M{"PointInTimeRecoveryEnabled": true}, "SSESpecification": M{"SSEEnabled": true}})
	retain(table)
	bucketName := "transactions-engine-private-" + account + "-" + region
	bucket := res("PrivateState", "AWS::S3::Bucket", M{"BucketName": bucketName, "BucketEncryption": M{"ServerSideEncryptionConfiguration": []M{{"ServerSideEncryptionByDefault": M{"SSEAlgorithm": "AES256"}}}}, "PublicAccessBlockConfiguration": M{"BlockPublicAcls": true, "BlockPublicPolicy": true, "IgnorePublicAcls": true, "RestrictPublicBuckets": true}, "VersioningConfiguration": M{"Status": "Enabled"}, "LifecycleConfiguration": M{"Rules": []M{{"Id": "sessions", "Prefix": "sessions/", "Status": "Enabled", "ExpirationInDays": 7}, {"Id": "incoming", "Prefix": "incoming/", "Status": "Enabled", "ExpirationInDays": 30}, {"Id": "snapshots", "Prefix": "snapshots/", "Status": "Enabled", "ExpirationInDays": 90}, {"Id": "oldVersions", "Status": "Enabled", "NoncurrentVersionExpiration": M{"NoncurrentDays": 7}}}}})
	retain(bucket)
	// The single Transactions secret (application keys, browser license and
	// broker token, and every bank login) is created privately outside this
	// stack so deployments can never replace or empty it.
	secretArn, ok := app.Node().TryGetContext(jsii.String("secretArn")).(string)
	if !ok || secretArn == "" {
		panic("secretArn context is required")
	}
	settings := res("Settings", "AWS::SSM::Parameter", M{"Name": "/transactions-engine/settings", "Type": "String", "Value": "{\"version\":1,\"importsEnabled\":false}", "Description": "Configured by the operator before invocation"})
	role := func(id, principal string, statements []M) awscdk.CfnResource {
		return res(id, "AWS::IAM::Role", M{"AssumeRolePolicyDocument": M{"Version": "2012-10-17", "Statement": []M{{"Effect": "Allow", "Principal": M{"Service": principal}, "Action": "sts:AssumeRole"}}}, "Policies": []M{{"PolicyName": "runtime", "PolicyDocument": M{"Version": "2012-10-17", "Statement": statements}}}})
	}
	allow := func(actions []string, resources ...any) M {
		return M{"Effect": "Allow", "Action": actions, "Resource": resources}
	}
	// This account-wide logging role also serves unrelated REST APIs. Own it
	// here so retiring TransactionsStack cannot strand their logging access.
	apiLoggingRole := role("APIGatewayLoggingRole", "apigateway.amazonaws.com", []M{allow([]string{"logs:CreateLogGroup", "logs:CreateLogStream", "logs:DescribeLogGroups", "logs:DescribeLogStreams", "logs:PutLogEvents", "logs:GetLogEvents", "logs:FilterLogEvents"}, "*")})
	apiLoggingAccount := res("APIGatewayLoggingAccount", "AWS::ApiGateway::Account", M{"CloudWatchRoleArn": att(apiLoggingRole, "Arn")})
	retain(apiLoggingRole)
	retain(apiLoggingAccount)
	workflowARN := "arn:aws:states:" + region + ":" + account + ":stateMachine:transactions-engine"
	common := []M{allow([]string{"dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:Query", "dynamodb:TransactWriteItems", "dynamodb:ConditionCheckItem"}, att(table, "Arn")), allow([]string{"ssm:GetParameter"}, sub("arn:${AWS::Partition}:ssm:${AWS::Region}:${AWS::AccountId}:parameter/transactions-engine/settings")), allow([]string{"logs:CreateLogStream", "logs:PutLogEvents"}, sub("arn:${AWS::Partition}:logs:${AWS::Region}:${AWS::AccountId}:log-group:/transactions-engine/*:*"))}
	objectArn := "arn:aws:s3:::" + bucketName + "/*"
	processorRole := role("ProcessorRole", "lambda.amazonaws.com", append(append([]M{}, common...), allow([]string{"s3:GetObject", "s3:PutObject"}, objectArn), allow([]string{"secretsmanager:GetSecretValue"}, secretArn), allow([]string{"states:StartExecution"}, workflowARN)))
	gatewayRole := role("GatewayRole", "lambda.amazonaws.com", append(append([]M{}, common...), allow([]string{"s3:GetObject"}, "arn:aws:s3:::"+bucketName+"/incoming/*"), allow([]string{"secretsmanager:GetSecretValue"}, secretArn), allow([]string{"states:StartExecution"}, workflowARN)))
	taskRole := role("BrowserRole", "ecs-tasks.amazonaws.com", append(append([]M{}, common...), allow([]string{"s3:GetObject", "s3:PutObject"}, "arn:aws:s3:::"+bucketName+"/sessions/*"), allow([]string{"secretsmanager:GetSecretValue"}, secretArn), allow([]string{"states:SendTaskSuccess", "states:SendTaskFailure"}, "*")))
	executionRole := role("TaskExecutionRole", "ecs-tasks.amazonaws.com", []M{allow([]string{"ecr:GetAuthorizationToken"}, "*"), allow([]string{"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"}, sub("arn:${AWS::Partition}:ecr:${AWS::Region}:${AWS::AccountId}:repository/*")), allow([]string{"logs:CreateLogStream", "logs:PutLogEvents"}, sub("arn:${AWS::Partition}:logs:${AWS::Region}:${AWS::AccountId}:log-group:/transactions-engine/*:*")), allow([]string{"secretsmanager:GetSecretValue"}, secretArn)})
	env := M{"STATE_TABLE": table.Ref(), "STATE_BUCKET": bucketName, "SETTINGS_PARAMETER": settings.Ref(), "AWS_SECRET_ARN": secretArn, "WORKFLOW_ARN": workflowARN, "DEPLOYMENT_COMMIT": os.Getenv("TRANSACTIONS_REVISION")}
	lambdaFn := func(name string, r awscdk.CfnResource) awscdk.CfnResource {
		logs := res(name+"Logs", "AWS::Logs::LogGroup", M{"LogGroupName": "/transactions-engine/" + name, "RetentionInDays": 14})
		retain(logs)
		vars := maps.Clone(env)
		vars["ROLE"] = name
		f := res(name, "AWS::Lambda::Function", M{"FunctionName": "transactions-engine-" + name, "Runtime": "provided.al2023", "Architectures": []string{"arm64"}, "Handler": "bootstrap", "MemorySize": 512, "Timeout": 180, "Role": att(r, "Arn"), "Code": M{"S3Bucket": asset.S3BucketName(), "S3Key": asset.S3ObjectKey()}, "Environment": M{"Variables": vars}, "LoggingConfig": M{"LogFormat": "JSON", "LogGroup": logs.Ref()}})
		return f
	}
	gateway := lambdaFn("gateway", gatewayRole)
	processor := lambdaFn("processor", processorRole)
	permission := res("MailInvokePermission", "AWS::Lambda::Permission", M{"Action": "lambda:InvokeFunction", "FunctionName": gateway.Ref(), "Principal": "s3.amazonaws.com", "SourceAccount": account, "SourceArn": "arn:aws:s3:::" + bucketName})
	bucket.AddPropertyOverride(jsii.String("NotificationConfiguration"), M{"LambdaConfigurations": []M{{"Event": "s3:ObjectCreated:*", "Function": att(gateway, "Arn"), "Filter": M{"S3Key": M{"Rules": []M{{"Name": "prefix", "Value": "incoming/"}}}}}}})
	bucket.AddDependency(permission)
	bucketPolicy := res("BucketPolicy", "AWS::S3::BucketPolicy", M{"Bucket": bucket.Ref(), "PolicyDocument": M{"Version": "2012-10-17", "Statement": []M{{"Effect": "Deny", "Principal": "*", "Action": "s3:*", "Resource": []any{att(bucket, "Arn"), objectArn}, "Condition": M{"Bool": M{"aws:SecureTransport": "false"}}}, {"Effect": "Allow", "Principal": M{"Service": "ses.amazonaws.com"}, "Action": "s3:PutObject", "Resource": "arn:aws:s3:::" + bucketName + "/incoming/*", "Condition": M{"StringEquals": M{"AWS:SourceAccount": account}}}}}})
	active := app.Node().TryGetContext(jsii.String("activate")) == "true"
	receiptSet := res("ReceiptRules", "AWS::SES::ReceiptRuleSet", M{"RuleSetName": "transactions-engine"})
	receiptRule := res("ReceiptRule", "AWS::SES::ReceiptRule", M{"RuleSetName": receiptSet.Ref(), "Rule": M{"Name": "transactions-engine", "Enabled": active, "ScanEnabled": true, "Recipients": []string{"transactions@fredericks.app"}, "Actions": []M{{"S3Action": M{"BucketName": bucket.Ref(), "ObjectKeyPrefix": "incoming/"}}}}})
	receiptRule.AddDependency(bucketPolicy)
	vpc := res("BrowserVPC", "AWS::EC2::VPC", M{"CidrBlock": "10.43.0.0/24", "EnableDnsSupport": true, "EnableDnsHostnames": true})
	igw := res("InternetGateway", "AWS::EC2::InternetGateway", M{})
	attachment := res("GatewayAttachment", "AWS::EC2::VPCGatewayAttachment", M{"VpcId": vpc.Ref(), "InternetGatewayId": igw.Ref()})
	subnet := res("BrowserSubnet", "AWS::EC2::Subnet", M{"VpcId": vpc.Ref(), "CidrBlock": "10.43.0.0/25", "AvailabilityZone": region + "a", "MapPublicIpOnLaunch": true})
	routes := res("Routes", "AWS::EC2::RouteTable", M{"VpcId": vpc.Ref()})
	route := res("InternetRoute", "AWS::EC2::Route", M{"RouteTableId": routes.Ref(), "DestinationCidrBlock": "0.0.0.0/0", "GatewayId": igw.Ref()})
	route.AddDependency(attachment)
	res("SubnetRoutes", "AWS::EC2::SubnetRouteTableAssociation", M{"SubnetId": subnet.Ref(), "RouteTableId": routes.Ref()})
	sg := res("BrowserSecurityGroup", "AWS::EC2::SecurityGroup", M{"VpcId": vpc.Ref(), "GroupDescription": "Outbound browser access; no inbound listeners", "SecurityGroupEgress": []M{{"IpProtocol": "-1", "CidrIp": "0.0.0.0/0"}}})
	cluster := res("BrowserCluster", "AWS::ECS::Cluster", M{})
	browserLogs := res("BrowserLogs", "AWS::Logs::LogGroup", M{"LogGroupName": "/transactions-engine/browser", "RetentionInDays": 14})
	retain(browserLogs)
	task := res("BrowserTask", "AWS::ECS::TaskDefinition", M{"Cpu": "1024", "Memory": "2048", "NetworkMode": "awsvpc", "RequiresCompatibilities": []string{"FARGATE"}, "RuntimePlatform": M{"CpuArchitecture": "ARM64", "OperatingSystemFamily": "LINUX"}, "TaskRoleArn": att(taskRole, "Arn"), "ExecutionRoleArn": att(executionRole, "Arn"), "ContainerDefinitions": []M{{"Name": "browser", "Image": image.ImageUri(), "Essential": true, "StopTimeout": 30, "LinuxParameters": M{"InitProcessEnabled": true}, "Environment": []M{{"Name": "STATE_TABLE", "Value": table.Ref()}, {"Name": "STATE_BUCKET", "Value": bucketName}, {"Name": "SETTINGS_PARAMETER", "Value": settings.Ref()}, {"Name": "AWS_REGION", "Value": region}, {"Name": "AWS_SECRET_ARN", "Value": secretArn}, {"Name": "DEPLOYMENT_COMMIT", "Value": os.Getenv("TRANSACTIONS_REVISION")}, {"Name": "BROWSER_BROKER_URL", "Value": "https://browser-broker.fredericks.app"}}, "Secrets": []M{{"Name": "CLOAKBROWSER_LICENSE_KEY", "ValueFrom": secretArn + ":CLOAKBROWSER_LICENSE_KEY::"}, {"Name": "BROWSER_BROKER_TOKEN", "ValueFrom": secretArn + ":BROWSER_BROKER_TOKEN::"}}, "LogConfiguration": M{"LogDriver": "awslogs", "Options": M{"awslogs-group": browserLogs.Ref(), "awslogs-region": region, "awslogs-stream-prefix": "browser"}}}}})
	wfRole := role("WorkflowRole", "states.amazonaws.com", []M{allow([]string{"lambda:InvokeFunction"}, att(processor, "Arn")), allow([]string{"ecs:RunTask"}, task.Ref()), allow([]string{"ecs:StopTask", "ecs:DescribeTasks"}, "*"), allow([]string{"iam:PassRole"}, att(taskRole, "Arn"), att(executionRole, "Arn"))})
	// Run decides; Browser signs in and returns to Run, also when the task
	// fails, so Run ends the job; Wait re-runs a deferred job.
	run := M{"Type": "Task", "Resource": att(processor, "Arn"), "Parameters": M{"action": "run", "jobId.$": "$.jobId", "bank.$": "$.bank"}, "ResultPath": "$.result", "Next": "Outcome", "Catch": []M{{"ErrorEquals": []string{"States.ALL"}, "ResultPath": "$.failure", "Next": "Failed"}}}
	states := M{"Run": run, "Outcome": M{"Type": "Choice", "Choices": []M{{"Variable": "$.result.outcome", "StringEquals": "authentication-required", "Next": "Browser"}, {"Variable": "$.result.outcome", "StringEquals": "deferred", "Next": "Wait"}, {"Variable": "$.result.outcome", "StringEquals": "failed", "Next": "Failed"}}, "Default": "Done"}, "Wait": M{"Type": "Wait", "SecondsPath": "$.result.waitSeconds", "Next": "Run"}, "Browser": M{"Type": "Task", "Resource": "arn:aws:states:::ecs:runTask.waitForTaskToken", "TimeoutSeconds": 420, "Parameters": M{"Cluster": cluster.Ref(), "TaskDefinition": task.Ref(), "LaunchType": "FARGATE", "NetworkConfiguration": M{"AwsvpcConfiguration": M{"Subnets": []any{subnet.Ref()}, "SecurityGroups": []any{att(sg, "GroupId")}, "AssignPublicIp": "ENABLED"}}, "Overrides": M{"ContainerOverrides": []M{{"Name": "browser", "Environment": []M{{"Name": "BANK", "Value.$": "$.bank"}, {"Name": "JOB_ID", "Value.$": "$.jobId"}, {"Name": "GENERATION", "Value.$": "$.result.lease.generation"}, {"Name": "TASK_TOKEN", "Value.$": "$$.Task.Token"}}}}}}, "ResultPath": "$.browser", "Next": "Run", "Catch": []M{{"ErrorEquals": []string{"States.ALL"}, "ResultPath": "$.failure", "Next": "Run"}}}, "Done": M{"Type": "Succeed"}, "Failed": M{"Type": "Fail", "Error": "EngineFailed"}}
	definition := M{"StartAt": "Run", "TimeoutSeconds": 86400, "States": states}
	res("Workflow", "AWS::StepFunctions::StateMachine", M{"StateMachineName": "transactions-engine", "StateMachineType": "STANDARD", "RoleArn": att(wfRole, "Arn"), "Definition": definition})
	schedulerRole := role("SchedulerRole", "scheduler.amazonaws.com", []M{allow([]string{"lambda:InvokeFunction"}, att(gateway, "Arn"))})
	scheduleState := "DISABLED"
	if active {
		scheduleState = "ENABLED"
	}
	for id, expression := range schedules {
		input, err := json.Marshal(M{"action": "submit", "job": M{"version": 1, "bank": id, "source": "scheduled", "purpose": "retrieve", "dryRun": false}})
		if err != nil {
			panic(err)
		}
		res("Schedule-"+id, "AWS::Scheduler::Schedule", M{"State": scheduleState, "ScheduleExpression": expression, "ScheduleExpressionTimezone": "America/Halifax", "FlexibleTimeWindow": M{"Mode": "OFF"}, "Target": M{"Arn": att(gateway, "Arn"), "RoleArn": att(schedulerRole, "Arn"), "Input": string(input), "RetryPolicy": M{"MaximumRetryAttempts": 2, "MaximumEventAgeInSeconds": 3600}}})
	}
	outputs := M{"Gateway": gateway.Ref(), "Processor": processor.Ref(), "Workflow": workflowARN, "StateTable": table.Ref(), "StateBucket": bucket.Ref(), "Settings": settings.Ref(), "Secret": secretArn, "BrowserTask": task.Ref()}
	for name, value := range outputs {
		awscdk.NewCfnOutput(stack, jsii.String("Output"+name), &awscdk.CfnOutputProps{Value: toString(value)})
	}
	app.Synth(nil)
}

func toString(v any) *string {
	if s, ok := v.(*string); ok {
		return s
	}
	if s, ok := v.(string); ok {
		return &s
	}
	return awscdk.Token_AsString(v, nil)
}
