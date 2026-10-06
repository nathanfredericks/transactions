package eq

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	st "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfnt "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/email"
	"github.com/nathanfredericks/transactions/internal/notify"
)

type Job struct {
	Purpose     string `json:"purpose,omitempty"`
	ReceivedAt  string `json:"receivedAt,omitempty"`
	Version     int    `json:"version"`
	JobID       string `json:"jobId"`
	RequestID   string `json:"requestId"`
	Source      string `json:"source"`
	DryRun      bool   `json:"dryRun"`
	MessageID   string `json:"messageId,omitempty"`
	AlertAmount int64  `json:"alertAmount,omitempty"`
	AlertDate   string `json:"alertDate,omitempty"`
}
type Request struct {
	Maintenance bool   `json:"maintenance"`
	Action      string `json:"action"`
	Execution   string `json:"execution"`
	Job         Job    `json:"job"`
	Worker      struct {
		Transport  string `json:"transport"`
		JobID      string `json:"jobId"`
		RequestID  string `json:"requestId"`
		Containers []struct {
			Name     string `json:"Name"`
			ExitCode *int   `json:"ExitCode"`
		} `json:"Containers"`
	} `json:"worker"`
}
type Store struct {
	db            *dynamodb.Client
	s3            *s3.Client
	table, bucket string
}

func newStore(ctx context.Context) (*Store, error) {
	cfg, err := config.GetAWSConfig(ctx)
	if err != nil {
		return nil, err
	}
	table, bucket := os.Getenv("EQ_STATE_TABLE"), os.Getenv("EQ_STATE_BUCKET")
	if table == "" || bucket == "" {
		return nil, errors.New("EQ private state is not configured")
	}
	return &Store{dynamodb.NewFromConfig(cfg), s3.NewFromConfig(cfg), table, bucket}, nil
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func key(s string) map[string]dt.AttributeValue {
	return map[string]dt.AttributeValue{"key": &dt.AttributeValueMemberS{Value: s}}
}
func (s *Store) get(ctx context.Context, k string, out any) (bool, error) {
	v, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: key(k), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return false, err
	}
	if len(v.Item) == 0 {
		return false, nil
	}
	data, ok := v.Item["data"].(*dt.AttributeValueMemberS)
	if !ok {
		return false, errors.New("Invalid EQ ledger item")
	}
	return true, json.Unmarshal([]byte(data.Value), out)
}
func (s *Store) put(ctx context.Context, k string, value any, owner string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	item := key(k)
	item["data"] = &dt.AttributeValueMemberS{Value: string(data)}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []dt.TransactWriteItem{
		{ConditionCheck: &dt.ConditionCheck{TableName: &s.table, Key: key("lease"), ConditionExpression: aws.String("#owner = :owner AND expires > :now"), ExpressionAttributeNames: map[string]string{"#owner": "owner"}, ExpressionAttributeValues: map[string]dt.AttributeValue{":owner": &dt.AttributeValueMemberS{Value: owner}, ":now": &dt.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Unix(), 10)}}}},
		{Put: &dt.Put{TableName: &s.table, Item: item}},
	}})
	return err
}
func (s *Store) upload(ctx context.Context, k string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &k, Body: strings.NewReader(string(data)), ContentType: aws.String("application/json"), ServerSideEncryption: st.ServerSideEncryptionAes256})
	return err
}
func (s *Store) download(ctx context.Context, k string, out any) error {
	result, err := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil {
		return err
	}
	defer result.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Body, 10*1024*1024))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

var purchase = regexp.MustCompile(`A \$([0-9]+)\.([0-9]{2}) purchase has been made on your EQ Bank Card\.`)

func StartAlert(ctx context.Context, parsed *email.ParsedEmail, text string) (any, error) {
	if !strings.EqualFold(parsed.From, "alert@eqbank.ca") || parsed.Subject != "Purchase made on your EQ Bank Card" {
		return nil, errors.New("EQ email is not on the purchase allowlist")
	}
	if parsed.MessageID == "" {
		return nil, errors.New("EQ purchase email has no original message ID")
	}
	matches := purchase.FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		return nil, errors.New("EQ purchase amount is missing or ambiguous")
	}
	whole, err := strconv.ParseInt(matches[0][1], 10, 64)
	if err != nil || whole > 100000000 {
		return nil, errors.New("Invalid EQ alert amount")
	}
	cents, _ := strconv.ParseInt(matches[0][2], 10, 64)
	cfg, err := config.GetAWSConfig(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String("/transactions/eq-enabled")})
	if err != nil {
		return nil, err
	}
	if aws.ToString(enabled.Parameter.Value) != "true" {
		return map[string]any{"eqIntakeEnabled": false}, nil
	}
	zone, _ := time.LoadLocation("America/Halifax")
	job := Job{Version: 1, Source: "alert", DryRun: false, MessageID: parsed.MessageID, AlertAmount: whole*1000 + cents*10, AlertDate: parsed.Date.In(zone).Format("2006-01-02"), Purpose: "retrieve", ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	name := "eq-alert-" + hash(parsed.MessageID)[:60]
	payload, _ := json.Marshal(job)
	result, err := sfn.NewFromConfig(cfg).StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(os.Getenv("EQ_WORKFLOW_ARN")), Name: &name, Input: aws.String(string(payload))})
	var exists *sfnt.ExecutionAlreadyExists
	if errors.As(err, &exists) {
		return map[string]any{"duplicate": true}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"executionArn": result.ExecutionArn}, nil
}
func validate(job Job) error {
	if job.Version != 1 || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`).MatchString(job.JobID) {
		return errors.New("Invalid EQ job envelope")
	}
	if job.Source != "alert" && job.Source != "scheduled" && job.Source != "manual" && job.Source != "session" {
		return errors.New("Invalid EQ job source")
	}
	if job.Purpose != "" && job.Purpose != "retrieve" && job.Purpose != "maintain-session" {
		return errors.New("Invalid EQ purpose")
	}
	if job.Source == "session" && job.Purpose != "maintain-session" {
		return errors.New("Invalid EQ maintenance purpose")
	}
	if job.Source == "alert" && job.Purpose == "maintain-session" {
		return errors.New("Alert cannot maintain a session")
	}
	if job.Source == "alert" && (job.MessageID == "" || job.AlertAmount <= 0 || job.AlertDate == "") {
		return errors.New("Invalid EQ alert envelope")
	}
	return nil
}
func Handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var request Request
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Action == "prepare" {
		request.Job.JobID = request.Execution
		if request.Job.Purpose == "" {
			request.Job.Purpose = "retrieve"
		}
	}
	if err := validate(request.Job); err != nil {
		return nil, err
	}
	s, err := newStore(ctx)
	if err != nil {
		return nil, err
	}
	switch request.Action {
	case "prepare":
		var blocked map[string]any
		found, err := s.get(ctx, "credentials-blocked", &blocked)
		if err != nil {
			return nil, err
		}
		if found {
			return map[string]any{"blocked": true, "acquired": false, "job": request.Job}, nil
		}
		if request.Job.Source == "alert" && !request.Job.DryRun {
			var done map[string]any
			found, err = s.get(ctx, "alert#"+hash(request.Job.MessageID), &done)
			if err != nil {
				return nil, err
			}
			if found {
				return map[string]any{"done": true, "acquired": false, "job": request.Job}, nil
			}
		}
		_, err = s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: &s.table, Key: key("lease"),
			UpdateExpression: aws.String("SET #owner = :owner, expires = :expires"), ConditionExpression: aws.String("attribute_not_exists(#owner) OR expires < :now OR #owner = :owner"), ExpressionAttributeNames: map[string]string{"#owner": "owner"},
			ExpressionAttributeValues: map[string]dt.AttributeValue{":owner": &dt.AttributeValueMemberS{Value: request.Job.JobID}, ":now": &dt.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Unix(), 10)}, ":expires": &dt.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10)}}})
		var busy *dt.ConditionalCheckFailedException
		if errors.As(err, &busy) {
			return map[string]any{"acquired": false, "job": request.Job}, nil
		}
		if err != nil {
			return nil, err
		}
		request.Job.RequestID = hash(request.Job.JobID + strconv.FormatInt(time.Now().UnixNano(), 10))
		if err = s.upload(ctx, "jobs/"+request.Job.JobID+"/job.json", request.Job); err != nil {
			return nil, err
		}
		return map[string]any{"acquired": true, "job": request.Job}, nil
	case "process":
		workerOK := (request.Worker.Transport == "api" || request.Worker.Transport == "browser-callback") && request.Worker.JobID == request.Job.JobID && request.Worker.RequestID != "" && request.Worker.RequestID == request.Job.RequestID
		for _, container := range request.Worker.Containers {
			if container.Name == "bank-import" && container.ExitCode != nil && *container.ExitCode == 0 {
				workerOK = true
			}
		}
		if !workerOK {
			return nil, errors.New("EQ retrieval completion did not match the active request")
		}
		result, err := s.process(ctx, request.Job)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "finish", "fail":
		if request.Action == "fail" && !request.Job.DryRun {
			if !request.Maintenance {
				if err = s.resetMaintenance(ctx, request.Job, false); err != nil {
					return nil, err
				}
			}
			message, title := "EQ Bank import failed; review the workflow and bank session.", "EQ Bank Import Failed"
			if request.Maintenance {
				message, title = "EQ Bank maintenance prevented purchase lookup after all retries. The alert remains unresolved.", "EQ Bank Maintenance"
			}
			if err = s.notifyOnce(ctx, "failure#"+request.Job.JobID, message, title); err != nil {
				return nil, err
			}
		}
		_, err = s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &s.table, Key: key("lease"), ConditionExpression: aws.String("#owner = :owner"), ExpressionAttributeNames: map[string]string{"#owner": "owner"}, ExpressionAttributeValues: map[string]dt.AttributeValue{":owner": &dt.AttributeValueMemberS{Value: request.Job.JobID}}})
		var missing *dt.ConditionalCheckFailedException
		if errors.As(err, &missing) {
			err = nil
		}
		return map[string]any{"released": err == nil}, err
	default:
		return nil, errors.New("Unsupported EQ internal action")
	}
}
func (s *Store) notifyOnce(ctx context.Context, k, message, title string) error {
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: key(k), ConditionExpression: aws.String("attribute_not_exists(#key)"), ExpressionAttributeNames: map[string]string{"#key": "key"}})
	var duplicate *dt.ConditionalCheckFailedException
	if errors.As(err, &duplicate) {
		return nil
	}
	if err != nil {
		return err
	}
	options := notify.NotificationOptions{Title: title, URL: "ynab://", URLTitle: "Open YNAB"}
	if title == "Transaction Approved" {
		options.Priority = 1
		options.Sound = "cashregister"
	}
	err = notify.SendNotification(ctx, message, options)
	if err != nil {
		// A rejected notification must remain eligible for delivery on a retry.
		_, cleanup := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &s.table, Key: key(k)})
		return errors.Join(err, cleanup)
	}
	return nil
}
