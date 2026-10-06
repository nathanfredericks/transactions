package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecrassets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3assets"
	"github.com/aws/jsii-runtime-go"
	"github.com/nathanfredericks/transactions/internal/banks"
)

type M = map[string]any

func main() {
	defer jsii.Close()
	app := awscdk.NewApp(nil)
	stack := awscdk.NewStack(app, jsii.String("TransactionsEngine"), &awscdk.StackProps{Env: &awscdk.Environment{Account: jsii.String("187489282488"), Region: jsii.String("ca-central-1")}})
	root, _ := filepath.Abs(".")
	if filepath.Base(root) == "cdk" {
		root = filepath.Dir(root)
	}
	dist := filepath.Join(root, "dist", "lambda")
	if err := os.MkdirAll(dist, 0755); err != nil {
		panic(err)
	}
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dist, "bootstrap"), "./cmd/lambda")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	asset := awss3assets.NewAsset(stack, jsii.String("GoCode"), &awss3assets.AssetProps{Path: &dist})
	image := awsecrassets.NewDockerImageAsset(stack, jsii.String("BrowserImage"), &awsecrassets.DockerImageAssetProps{Directory: &root, Platform: awsecrassets.Platform_LINUX_ARM64(), Exclude: &[]*string{jsii.String(".git"), jsii.String("admin"), jsii.String("dist"), jsii.String("cdk.out"), jsii.String("cdk"), jsii.String("**/*_test.go")}})
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
	bucketName := "transactions-engine-private-187489282488-ca-central-1"
	bucket := res("PrivateState", "AWS::S3::Bucket", M{"BucketName": bucketName, "BucketEncryption": M{"ServerSideEncryptionConfiguration": []M{{"ServerSideEncryptionByDefault": M{"SSEAlgorithm": "AES256"}}}}, "PublicAccessBlockConfiguration": M{"BlockPublicAcls": true, "BlockPublicPolicy": true, "IgnorePublicAcls": true, "RestrictPublicBuckets": true}, "VersioningConfiguration": M{"Status": "Enabled"}, "LifecycleConfiguration": M{"Rules": []M{{"Id": "sessions", "Prefix": "sessions/", "Status": "Enabled", "ExpirationInDays": 7}, {"Id": "incoming", "Prefix": "incoming/", "Status": "Enabled", "ExpirationInDays": 30}, {"Id": "diagnostics", "Prefix": "diagnostics/", "Status": "Enabled", "ExpirationInDays": 7}, {"Id": "snapshots", "Prefix": "snapshots/", "Status": "Enabled", "ExpirationInDays": 90}, {"Id": "oldVersions", "Status": "Enabled", "NoncurrentVersionExpiration": M{"NoncurrentDays": 7}}}}})
	retain(bucket)
	failures := res("DeliveryFailures", "AWS::SQS::Queue", M{"MessageRetentionPeriod": 1209600, "SqsManagedSseEnabled": true})
	retain(failures)
	appSecret := res("ApplicationSecret", "AWS::SecretsManager::Secret", M{"Name": "transactions-engine/application", "Description": "YNAB, AI, Pushover and webhook credentials"})
	retain(appSecret)
	connectionSecret := res("RepositoryConnection", "AWS::SecretsManager::Secret", M{"Name": "transactions-engine/github", "Description": "Repository connection for the CDK-defined admin"})
	retain(connectionSecret)
	bankSecrets := []any{}
	for _, registration := range banks.All {
		secret := res("Credentials-"+registration.ID, "AWS::SecretsManager::Secret", M{"Name": "transactions-engine/" + registration.ID, "Description": "Bank credentials and Fastmail MFA only; never YNAB"})
		retain(secret)
		bankSecrets = append(bankSecrets, secret.Ref())
	}
	settings := res("Settings", "AWS::SSM::Parameter", M{"Name": "/transactions-engine/settings", "Type": "String", "Value": "{\"version\":1,\"importsEnabled\":false}", "Description": "Configured by the operator before invocation"})
	role := func(id, principal string, statements []M) awscdk.CfnResource {
		return res(id, "AWS::IAM::Role", M{"AssumeRolePolicyDocument": M{"Version": "2012-10-17", "Statement": []M{{"Effect": "Allow", "Principal": M{"Service": principal}, "Action": "sts:AssumeRole"}}}, "Policies": []M{{"PolicyName": "runtime", "PolicyDocument": M{"Version": "2012-10-17", "Statement": statements}}}})
	}
	allow := func(actions []string, resources ...any) M {
		return M{"Effect": "Allow", "Action": actions, "Resource": resources}
	}
	workflowARN := "arn:aws:states:ca-central-1:187489282488:stateMachine:transactions-engine"
	common := []M{allow([]string{"dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:Query", "dynamodb:TransactWriteItems", "dynamodb:ConditionCheckItem"}, att(table, "Arn")), allow([]string{"ssm:GetParameter"}, sub("arn:${AWS::Partition}:ssm:${AWS::Region}:${AWS::AccountId}:parameter/transactions-engine/settings")), allow([]string{"logs:CreateLogStream", "logs:PutLogEvents"}, sub("arn:${AWS::Partition}:logs:${AWS::Region}:${AWS::AccountId}:log-group:/transactions-engine/*:*"))}
	objectArn := "arn:aws:s3:::" + bucketName + "/*"
	processorRole := role("ProcessorRole", "lambda.amazonaws.com", append(append([]M{}, common...), allow([]string{"s3:GetObject", "s3:PutObject"}, objectArn), allow([]string{"secretsmanager:GetSecretValue"}, appSecret.Ref())))
	gatewayRole := role("GatewayRole", "lambda.amazonaws.com", append(append([]M{}, common...), allow([]string{"s3:GetObject"}, "arn:aws:s3:::"+bucketName+"/incoming/*", "arn:aws:s3:::"+bucketName+"/snapshots/*"), allow([]string{"secretsmanager:GetSecretValue"}, appSecret.Ref()), allow([]string{"states:StartExecution"}, workflowARN), allow([]string{"states:DescribeExecution"}, sub("arn:${AWS::Partition}:states:${AWS::Region}:${AWS::AccountId}:execution:transactions-engine:*")), allow([]string{"sqs:SendMessage"}, att(failures, "Arn"))))
	taskRole := role("BrowserRole", "ecs-tasks.amazonaws.com", append(append([]M{}, common...), allow([]string{"s3:GetObject", "s3:PutObject"}, "arn:aws:s3:::"+bucketName+"/sessions/*"), allow([]string{"secretsmanager:GetSecretValue"}, bankSecrets...), allow([]string{"states:SendTaskSuccess", "states:SendTaskFailure"}, "*")))
	executionRole := role("TaskExecutionRole", "ecs-tasks.amazonaws.com", []M{allow([]string{"ecr:GetAuthorizationToken"}, "*"), allow([]string{"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer", "ecr:BatchCheckLayerAvailability"}, sub("arn:${AWS::Partition}:ecr:${AWS::Region}:${AWS::AccountId}:repository/*")), allow([]string{"logs:CreateLogStream", "logs:PutLogEvents"}, sub("arn:${AWS::Partition}:logs:${AWS::Region}:${AWS::AccountId}:log-group:/transactions-engine/*:*"))})
	env := M{"STATE_TABLE": table.Ref(), "STATE_BUCKET": bucketName, "SETTINGS_PARAMETER": settings.Ref(), "AWS_SECRET_ARN": appSecret.Ref(), "WORKFLOW_ARN": workflowARN, "DEPLOYMENT_COMMIT": os.Getenv("TRANSACTIONS_REVISION")}
	lambdaFn := func(name string, r awscdk.CfnResource) awscdk.CfnResource {
		logs := res(name+"Logs", "AWS::Logs::LogGroup", M{"LogGroupName": "/transactions-engine/" + name, "RetentionInDays": 14})
		retain(logs)
		vars := M{}
		for k, v := range env {
			vars[k] = v
		}
		vars["ROLE"] = name
		f := res(name, "AWS::Lambda::Function", M{"FunctionName": "transactions-engine-" + name, "Runtime": "provided.al2023", "Architectures": []string{"arm64"}, "Handler": "bootstrap", "MemorySize": 512, "Timeout": 180, "Role": att(r, "Arn"), "Code": M{"S3Bucket": asset.S3BucketName(), "S3Key": asset.S3ObjectKey()}, "Environment": M{"Variables": vars}, "LoggingConfig": M{"LogFormat": "JSON", "LogGroup": logs.Ref()}})
		return f
	}
	gateway := lambdaFn("gateway", gatewayRole)
	processor := lambdaFn("processor", processorRole)
	res("GatewayFailureDestination", "AWS::Lambda::EventInvokeConfig", M{"FunctionName": gateway.Ref(), "Qualifier": "$LATEST", "MaximumRetryAttempts": 2, "MaximumEventAgeInSeconds": 3600, "DestinationConfig": M{"OnFailure": M{"Destination": att(failures, "Arn")}}})
	permission := res("MailInvokePermission", "AWS::Lambda::Permission", M{"Action": "lambda:InvokeFunction", "FunctionName": gateway.Ref(), "Principal": "s3.amazonaws.com", "SourceAccount": "187489282488", "SourceArn": "arn:aws:s3:::" + bucketName})
	bucket.AddPropertyOverride(jsii.String("NotificationConfiguration"), M{"LambdaConfigurations": []M{{"Event": "s3:ObjectCreated:*", "Function": att(gateway, "Arn"), "Filter": M{"S3Key": M{"Rules": []M{{"Name": "prefix", "Value": "incoming/"}}}}}}})
	bucket.AddDependency(permission)
	bucketPolicy := res("BucketPolicy", "AWS::S3::BucketPolicy", M{"Bucket": bucket.Ref(), "PolicyDocument": M{"Version": "2012-10-17", "Statement": []M{{"Effect": "Deny", "Principal": "*", "Action": "s3:*", "Resource": []any{att(bucket, "Arn"), objectArn}, "Condition": M{"Bool": M{"aws:SecureTransport": "false"}}}, {"Effect": "Allow", "Principal": M{"Service": "ses.amazonaws.com"}, "Action": "s3:PutObject", "Resource": "arn:aws:s3:::" + bucketName + "/incoming/*", "Condition": M{"StringEquals": M{"AWS:SourceAccount": "187489282488"}}}}}})
	active := app.Node().TryGetContext(jsii.String("activate")) == "true"
	receiptSet := res("ReceiptRules", "AWS::SES::ReceiptRuleSet", M{"RuleSetName": "transactions-engine"})
	receiptRule := res("ReceiptRule", "AWS::SES::ReceiptRule", M{"RuleSetName": receiptSet.Ref(), "Rule": M{"Name": "transactions-engine", "Enabled": active, "ScanEnabled": true, "Recipients": []string{"transactions@fredericks.app"}, "Actions": []M{{"S3Action": M{"BucketName": bucket.Ref(), "ObjectKeyPrefix": "incoming/"}}}}})
	receiptRule.AddDependency(bucketPolicy)
	vpc := res("BrowserVPC", "AWS::EC2::VPC", M{"CidrBlock": "10.43.0.0/24", "EnableDnsSupport": true, "EnableDnsHostnames": true})
	igw := res("InternetGateway", "AWS::EC2::InternetGateway", M{})
	attachment := res("GatewayAttachment", "AWS::EC2::VPCGatewayAttachment", M{"VpcId": vpc.Ref(), "InternetGatewayId": igw.Ref()})
	subnet := res("BrowserSubnet", "AWS::EC2::Subnet", M{"VpcId": vpc.Ref(), "CidrBlock": "10.43.0.0/25", "AvailabilityZone": "ca-central-1a", "MapPublicIpOnLaunch": true})
	routes := res("Routes", "AWS::EC2::RouteTable", M{"VpcId": vpc.Ref()})
	route := res("InternetRoute", "AWS::EC2::Route", M{"RouteTableId": routes.Ref(), "DestinationCidrBlock": "0.0.0.0/0", "GatewayId": igw.Ref()})
	route.AddDependency(attachment)
	res("SubnetRoutes", "AWS::EC2::SubnetRouteTableAssociation", M{"SubnetId": subnet.Ref(), "RouteTableId": routes.Ref()})
	sg := res("BrowserSecurityGroup", "AWS::EC2::SecurityGroup", M{"VpcId": vpc.Ref(), "GroupDescription": "Outbound browser access; no inbound listeners", "SecurityGroupEgress": []M{{"IpProtocol": "-1", "CidrIp": "0.0.0.0/0"}}})
	cluster := res("BrowserCluster", "AWS::ECS::Cluster", M{})
	browserLogs := res("BrowserLogs", "AWS::Logs::LogGroup", M{"LogGroupName": "/transactions-engine/browser", "RetentionInDays": 14})
	retain(browserLogs)
	task := res("BrowserTask", "AWS::ECS::TaskDefinition", M{"Cpu": "1024", "Memory": "2048", "NetworkMode": "awsvpc", "RequiresCompatibilities": []string{"FARGATE"}, "RuntimePlatform": M{"CpuArchitecture": "ARM64", "OperatingSystemFamily": "LINUX"}, "TaskRoleArn": att(taskRole, "Arn"), "ExecutionRoleArn": att(executionRole, "Arn"), "ContainerDefinitions": []M{{"Name": "browser", "Image": image.ImageUri(), "Essential": true, "StopTimeout": 30, "LinuxParameters": M{"InitProcessEnabled": true}, "Environment": []M{{"Name": "STATE_TABLE", "Value": table.Ref()}, {"Name": "STATE_BUCKET", "Value": bucketName}, {"Name": "SETTINGS_PARAMETER", "Value": settings.Ref()}, {"Name": "AWS_REGION", "Value": "ca-central-1"}}, "LogConfiguration": M{"LogDriver": "awslogs", "Options": M{"awslogs-group": browserLogs.Ref(), "awslogs-region": "ca-central-1", "awslogs-stream-prefix": "browser"}}}}})
	wfRole := role("WorkflowRole", "states.amazonaws.com", []M{allow([]string{"lambda:InvokeFunction"}, att(processor, "Arn")), allow([]string{"ecs:RunTask"}, task.Ref()), allow([]string{"ecs:StopTask", "ecs:DescribeTasks"}, "*"), allow([]string{"iam:PassRole"}, att(taskRole, "Arn"), att(executionRole, "Arn"))})
	call := func(action, next string) M {
		return M{"Type": "Task", "Resource": att(processor, "Arn"), "Parameters": M{"action": action, "jobId.$": "$.jobId", "bank.$": "$.bank", "execution.$": "$$.Execution.Id"}, "ResultPath": "$.result", "Next": next, "Catch": []M{{"ErrorEquals": []string{"States.ALL"}, "ResultPath": "$.failure", "Next": "Failed"}}}
	}
	states := M{"Dispatch": M{"Type": "Choice", "Choices": []M{{"Variable": "$.action", "StringEquals": "notify", "Next": "NotificationOnly"}}, "Default": "Run"}, "NotificationOnly": M{"Type": "Pass", "Result": M{"outcome": "complete"}, "ResultPath": "$.financial", "Next": "Notify"}, "Run": call("run", "Outcome"), "Outcome": M{"Type": "Choice", "Choices": []M{{"Variable": "$.result.outcome", "StringEquals": "authentication-required", "Next": "Browser"}, {"Variable": "$.result.outcome", "StringEquals": "deferred", "Next": "Wait"}}, "Default": "PreserveOutcome"}, "Wait": M{"Type": "Wait", "SecondsPath": "$.result.waitSeconds", "Next": "Run"}, "Browser": M{"Type": "Task", "Resource": "arn:aws:states:::ecs:runTask.waitForTaskToken", "TimeoutSeconds": 420, "Parameters": M{"Cluster": cluster.Ref(), "TaskDefinition": task.Ref(), "LaunchType": "FARGATE", "NetworkConfiguration": M{"AwsvpcConfiguration": M{"Subnets": []any{subnet.Ref()}, "SecurityGroups": []any{att(sg, "GroupId")}, "AssignPublicIp": "ENABLED"}}, "Overrides": M{"ContainerOverrides": []M{{"Name": "browser", "Environment": []M{{"Name": "BANK", "Value.$": "$.bank"}, {"Name": "JOB_ID", "Value.$": "$.jobId"}, {"Name": "GENERATION", "Value.$": "$.result.lease.generation"}, {"Name": "TASK_TOKEN", "Value.$": "$$.Task.Token"}}}}}}, "ResultPath": "$.browser", "Next": "Run", "Catch": []M{{"ErrorEquals": []string{"States.ALL"}, "ResultPath": "$.failure", "Next": "RecoverBrowser"}}}, "RecoverBrowser": call("recover-browser", "Outcome"), "PreserveOutcome": M{"Type": "Pass", "Parameters": M{"outcome.$": "$.result.outcome"}, "ResultPath": "$.financial", "Next": "Notify"}, "Notify": call("notify", "NotificationOutcome"), "NotificationOutcome": M{"Type": "Choice", "Choices": []M{{"Variable": "$.result.outcome", "StringEquals": "deferred", "Next": "WaitNotification"}}, "Default": "FinalOutcome"}, "FinalOutcome": M{"Type": "Choice", "Choices": []M{{"Variable": "$.financial.outcome", "StringEquals": "failed", "Next": "Failed"}}, "Default": "Done"}, "WaitNotification": M{"Type": "Wait", "SecondsPath": "$.result.waitSeconds", "Next": "Notify"}, "Done": M{"Type": "Succeed"}, "Failed": M{"Type": "Fail", "Error": "EngineFailed"}}
	definition := M{"StartAt": "Dispatch", "TimeoutSeconds": 86400, "States": states}
	res("Workflow", "AWS::StepFunctions::StateMachine", M{"StateMachineName": "transactions-engine", "StateMachineType": "STANDARD", "RoleArn": att(wfRole, "Arn"), "Definition": definition})
	schedulerRole := role("SchedulerRole", "scheduler.amazonaws.com", []M{allow([]string{"lambda:InvokeFunction"}, att(gateway, "Arn")), allow([]string{"sqs:SendMessage"}, att(failures, "Arn"))})
	scheduleState := "DISABLED"
	if active {
		scheduleState = "ENABLED"
	}
	schedule := func(id, expression string, input any) {
		b, _ := json.Marshal(input)
		res(id, "AWS::Scheduler::Schedule", M{"State": scheduleState, "ScheduleExpression": expression, "ScheduleExpressionTimezone": "America/Halifax", "FlexibleTimeWindow": M{"Mode": "OFF"}, "Target": M{"Arn": att(gateway, "Arn"), "RoleArn": att(schedulerRole, "Arn"), "Input": string(b), "RetryPolicy": M{"MaximumRetryAttempts": 2, "MaximumEventAgeInSeconds": 3600}, "DeadLetterConfig": M{"Arn": att(failures, "Arn")}}})
	}
	for _, b := range banks.All {
		schedule("Schedule-"+b.ID, b.Schedule, M{"action": "submit", "job": M{"version": 1, "bank": b.ID, "source": "scheduled", "purpose": "retrieve", "dryRun": false}})
	}
	schedule("EQUpkeep", "rate(1 minute)", M{"action": "maintain"})
	api := res("WebhookAPI", "AWS::ApiGatewayV2::Api", M{"Name": "transactions-engine", "ProtocolType": "HTTP"})
	integration := res("WebhookIntegration", "AWS::ApiGatewayV2::Integration", M{"ApiId": api.Ref(), "IntegrationType": "AWS_PROXY", "IntegrationUri": att(gateway, "Arn"), "PayloadFormatVersion": "2.0"})
	res("WebhookRoute", "AWS::ApiGatewayV2::Route", M{"ApiId": api.Ref(), "RouteKey": "POST /webhook", "Target": sub("integrations/${WebhookIntegration}")})
	_ = integration
	res("WebhookStage", "AWS::ApiGatewayV2::Stage", M{"ApiId": api.Ref(), "StageName": "$default", "AutoDeploy": true})
	res("WebhookPermission", "AWS::Lambda::Permission", M{"Action": "lambda:InvokeFunction", "FunctionName": gateway.Ref(), "Principal": "apigateway.amazonaws.com", "SourceArn": sub("arn:${AWS::Partition}:execute-api:${AWS::Region}:${AWS::AccountId}:${WebhookAPI}/*/POST/webhook")})
	alarmTopic := res("OperatorAlerts", "AWS::SNS::Topic", M{})
	email := app.Node().TryGetContext(jsii.String("alarmEmail"))
	if email == nil {
		email = "nathanfredericks@icloud.com"
	}
	if email != nil {
		res("OperatorEmail", "AWS::SNS::Subscription", M{"TopicArn": alarmTopic.Ref(), "Protocol": "email", "Endpoint": email})
	}
	alarm := func(id, namespace, metric, dimension string, value any) {
		res(id, "AWS::CloudWatch::Alarm", M{"Namespace": namespace, "MetricName": metric, "Dimensions": []M{{"Name": dimension, "Value": value}}, "Statistic": "Sum", "Period": 300, "EvaluationPeriods": 1, "Threshold": 0, "ComparisonOperator": "GreaterThanThreshold", "TreatMissingData": "notBreaching", "AlarmActions": []any{alarmTopic.Ref()}})
	}
	alarm("ProcessorErrors", "AWS/Lambda", "Errors", "FunctionName", processor.Ref())
	alarm("GatewayErrors", "AWS/Lambda", "Errors", "FunctionName", gateway.Ref())
	alarm("WorkflowTimeouts", "AWS/States", "ExecutionsTimedOut", "StateMachineArn", workflowARN)
	alarm("WorkflowFailures", "AWS/States", "ExecutionsFailed", "StateMachineArn", workflowARN)
	alarm("UndeliveredEvents", "AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", att(failures, "QueueName"))
	adminRole := role("AdminComputeRole", "amplify.amazonaws.com", []M{allow([]string{"lambda:InvokeFunction"}, att(gateway, "Arn"))})
	_ = adminRole
	// Hosting is defined here; repository connection is supplied securely at deployment.
	repo := app.Node().TryGetContext(jsii.String("repository"))
	token := app.Node().TryGetContext(jsii.String("githubTokenSecretArn"))
	if repo != nil && token != nil {
		adminPassword := res("AdminPassword", "AWS::SecretsManager::Secret", M{"Name": "transactions-engine/admin-password", "GenerateSecretString": M{"PasswordLength": 32, "ExcludePunctuation": true}})
		retain(adminPassword)
		adminPassword.CfnOptions().SetDeletionPolicy(awscdk.CfnDeletionPolicy_RETAIN_EXCEPT_ON_CREATE)
		admin := res("Admin", "AWS::Amplify::App", M{"Name": "Transactions Engine Admin", "BasicAuthConfig": M{"EnableBasicAuth": true, "Username": "operator", "Password": sub("{{resolve:secretsmanager:${AdminPassword}:SecretString}}")}, "Platform": "WEB_COMPUTE", "Repository": repo, "AccessToken": fmt.Sprintf("{{resolve:secretsmanager:%v:SecretString:token}}", token), "ComputeRoleArn": att(adminRole, "Arn"), "EnvironmentVariables": []M{{"Name": "BACKEND_FUNCTION", "Value": gateway.Ref()}, {"Name": "AMPLIFY_MONOREPO_APP_ROOT", "Value": "admin"}}, "BuildSpec": "version: 1\napplications:\n  - appRoot: admin\n    frontend:\n      phases:\n        preBuild:\n          commands:\n            - npm ci\n        build:\n          commands:\n            - echo \"BACKEND_FUNCTION=$BACKEND_FUNCTION\" >> .env.production\n            - npm run build\n      artifacts:\n        baseDirectory: .next\n        files:\n          - '**/*'\n      cache:\n        paths:\n          - node_modules/**/*\n"})
		branch := app.Node().TryGetContext(jsii.String("adminBranch"))
		if branch == nil {
			branch = "main"
		}
		res("AdminBranch", "AWS::Amplify::Branch", M{"AppId": att(admin, "AppId"), "BranchName": branch, "EnableAutoBuild": true, "Framework": "Next.js - SSR"})
		awscdk.NewCfnOutput(stack, jsii.String("OutputAdminApp"), &awscdk.CfnOutputProps{Value: toString(att(admin, "AppId"))})
	}
	outputs := M{"Gateway": gateway.Ref(), "Processor": processor.Ref(), "Workflow": workflowARN, "StateTable": table.Ref(), "StateBucket": bucket.Ref(), "Settings": settings.Ref(), "ApplicationSecret": appSecret.Ref(), "Webhook": sub("https://${WebhookAPI}.execute-api.${AWS::Region}.amazonaws.com/webhook"), "BrowserTask": task.Ref(), "AlarmTopic": alarmTopic.Ref()}
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
