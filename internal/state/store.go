// Package state owns all persistence and the single fencing implementation.
package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	d "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
)

type Store struct {
	DB            *dynamodb.Client
	S3            *s3.Client
	Table, Bucket string
}
type Lease struct {
	Bank       string `json:"bank"`
	Owner      string `json:"owner"`
	Generation string `json:"generation"`
	Expires    int64  `json:"expires"`
}

func key(pk, sk string) map[string]d.AttributeValue {
	return map[string]d.AttributeValue{"pk": &d.AttributeValueMemberS{Value: pk}, "sk": &d.AttributeValueMemberS{Value: sk}}
}
func (s *Store) Get(ctx context.Context, pk, sk string, out any) (bool, error) {
	r, e := s.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.Table, Key: key(pk, sk), ConsistentRead: aws.Bool(true)})
	if e != nil {
		return false, e
	}
	if len(r.Item) == 0 {
		return false, nil
	}
	v, ok := r.Item["data"].(*d.AttributeValueMemberS)
	if !ok {
		return false, fmt.Errorf("invalid state")
	}
	return true, json.Unmarshal([]byte(v.Value), out)
}
func (s *Store) Create(ctx context.Context, pk, sk string, value any) (bool, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return false, e
	}
	item := key(pk, sk)
	item["data"] = &d.AttributeValueMemberS{Value: string(b)}
	_, e = s.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.Table, Item: item, ConditionExpression: aws.String("attribute_not_exists(pk)")})
	var exists *d.ConditionalCheckFailedException
	if errors.As(e, &exists) {
		return false, nil
	}
	return e == nil, e
}
func (s *Store) Acquire(ctx context.Context, bank, owner string) (Lease, bool, error) {
	l := Lease{Bank: bank, Owner: owner, Generation: uuid.NewString(), Expires: time.Now().Add(15 * time.Minute).Unix()}
	b, _ := json.Marshal(l)
	_, e := s.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.Table, Item: map[string]d.AttributeValue{"pk": &d.AttributeValueMemberS{Value: "BANK#" + bank}, "sk": &d.AttributeValueMemberS{Value: "LEASE"}, "data": &d.AttributeValueMemberS{Value: string(b)}, "generation": &d.AttributeValueMemberS{Value: l.Generation}, "expires": &d.AttributeValueMemberN{Value: fmt.Sprint(l.Expires)}}, ConditionExpression: aws.String("attribute_not_exists(pk) OR expires < :now"), ExpressionAttributeValues: map[string]d.AttributeValue{":now": &d.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Unix())}}})
	var busy *d.ConditionalCheckFailedException
	if errors.As(e, &busy) {
		var current Lease
		found, readErr := s.Get(ctx, "BANK#"+bank, "LEASE", &current)
		if readErr != nil {
			return l, false, readErr
		}
		return l, found && current.Generation == l.Generation && current.Owner == owner, nil
	}
	return l, e == nil, e
}
func (s *Store) Assert(ctx context.Context, l Lease) error {
	var current Lease
	ok, e := s.Get(ctx, "BANK#"+l.Bank, "LEASE", &current)
	if e != nil {
		return e
	}
	if !ok || current.Generation != l.Generation || current.Owner != l.Owner || current.Expires <= time.Now().Unix() {
		return fmt.Errorf("lease lost")
	}
	return nil
}
func (s *Store) Put(ctx context.Context, l Lease, sk string, value any) error {
	return s.PutMany(ctx, l, map[string]any{sk: value})
}

// Commit related state and notification intents under the same fencing condition.
func (s *Store) PutMany(ctx context.Context, l Lease, values map[string]any) error {
	items := []d.TransactWriteItem{{ConditionCheck: &d.ConditionCheck{TableName: &s.Table, Key: key("BANK#"+l.Bank, "LEASE"), ConditionExpression: aws.String("generation = :g AND expires > :now"), ExpressionAttributeValues: map[string]d.AttributeValue{":g": &d.AttributeValueMemberS{Value: l.Generation}, ":now": &d.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Unix())}}}}}
	for sk, value := range values {
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		item := key("BANK#"+l.Bank, sk)
		item["data"] = &d.AttributeValueMemberS{Value: string(b)}
		items = append(items, d.TransactWriteItem{Put: &d.Put{TableName: &s.Table, Item: item}})
	}
	_, err := s.DB.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{ClientRequestToken: aws.String(uuid.NewString()), TransactItems: items})
	return err
}
func (s *Store) Release(ctx context.Context, l Lease) error {
	_, e := s.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &s.Table, Key: key("BANK#"+l.Bank, "LEASE"), ConditionExpression: aws.String("generation = :g"), ExpressionAttributeValues: map[string]d.AttributeValue{":g": &d.AttributeValueMemberS{Value: l.Generation}}})
	var missing *d.ConditionalCheckFailedException
	if errors.As(e, &missing) {
		return nil
	}
	return e
}
func (s *Store) List(ctx context.Context, pk, prefix string) ([]json.RawMessage, error) {
	var result []json.RawMessage
	var cursor map[string]d.AttributeValue
	for {
		r, e := s.DB.Query(ctx, &dynamodb.QueryInput{TableName: &s.Table, KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"), ExpressionAttributeValues: map[string]d.AttributeValue{":pk": &d.AttributeValueMemberS{Value: pk}, ":prefix": &d.AttributeValueMemberS{Value: prefix}}, ConsistentRead: aws.Bool(true), ExclusiveStartKey: cursor})
		if e != nil {
			return nil, e
		}
		for _, v := range r.Items {
			if b, ok := v["data"].(*d.AttributeValueMemberS); ok {
				result = append(result, json.RawMessage(b.Value))
			}
		}
		cursor = r.LastEvaluatedKey
		if len(cursor) == 0 {
			break
		}
	}
	return result, nil
}
func (s *Store) Upload(ctx context.Context, prefix string, value any) (string, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	k := prefix + "/" + uuid.NewString() + ".json"
	_, e = s.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.Bucket, Key: &k, Body: bytes.NewReader(b), ContentType: aws.String("application/json"), ServerSideEncryption: s3types.ServerSideEncryptionAes256})
	return k, e
}
func (s *Store) Download(ctx context.Context, k string, out any) error {
	r, e := s.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.Bucket, Key: &k})
	if e != nil {
		return e
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 20*1024*1024)).Decode(out)
}

// Set uses optimistic revisions for admin edits; no read/overwrite race.
func (s *Store) Set(ctx context.Context, pk, sk string, value any, revision string) (string, error) {
	next := uuid.NewString()
	b, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	var object map[string]any
	if json.Unmarshal(b, &object) == nil && object != nil {
		object["revision"] = next
		b, _ = json.Marshal(object)
	}
	item := key(pk, sk)
	item["data"] = &d.AttributeValueMemberS{Value: string(b)}
	item["revision"] = &d.AttributeValueMemberS{Value: next}
	condition := "attribute_not_exists(pk)"
	var values map[string]d.AttributeValue
	if revision != "" {
		condition = "revision = :revision"
		values = map[string]d.AttributeValue{":revision": &d.AttributeValueMemberS{Value: revision}}
	}
	_, e = s.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.Table, Item: item, ConditionExpression: &condition, ExpressionAttributeValues: values})
	return next, e
}
