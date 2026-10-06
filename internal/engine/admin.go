package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	d "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/google/uuid"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
	"os"
	"strings"
	"time"
)

func (e *Engine) Admin(ctx context.Context, r Request) (any, error) {
	switch r.Action {
	case "rules.list":
		return override.GetTransactionOverrides(ctx)
	case "rules.get":
		var input struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(r.Payload, &input) != nil {
			return nil, bankStateError()
		}
		var rule types.TransactionOverride
		found, err := e.Store.Get(ctx, "RULES", "RULE#"+input.ID, &rule)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, nil
		}
		return rule, nil
	case "rules.save":
		var rule types.TransactionOverride
		if json.Unmarshal(r.Payload, &rule) != nil {
			return nil, bankStateError()
		}
		if err := validateRule(rule); err != nil {
			return nil, err
		}
		if err := service.ValidateRuleReferences(ctx, rule.Payee, rule.Category); err != nil {
			return nil, err
		}
		if rule.ID == "" {
			rule.ID = uuid.NewString()
		}
		rule.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		revision, err := e.Store.Set(ctx, "RULES", "RULE#"+rule.ID, rule, rule.Revision)
		return map[string]string{"id": rule.ID, "revision": revision}, err
	case "rules.delete":
		var input struct{ ID, Revision string }
		if json.Unmarshal(r.Payload, &input) != nil || input.ID == "" || input.Revision == "" {
			return nil, bankStateError()
		}
		_, err := e.Store.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &e.Store.Table, Key: map[string]d.AttributeValue{"pk": &d.AttributeValueMemberS{Value: "RULES"}, "sk": &d.AttributeValueMemberS{Value: "RULE#" + input.ID}}, ConditionExpression: aws.String("revision = :r"), ExpressionAttributeValues: map[string]d.AttributeValue{":r": &d.AttributeValueMemberS{Value: input.Revision}}})
		return map[string]bool{"deleted": err == nil}, err
	case "lookups":
		return service.Lookups(ctx)
	case "jobs.list":
		var jobs []json.RawMessage
		ids := map[string]bool{}
		for _, registration := range banks.All {
			ids[registration.ID] = true
		}
		for _, entry := range e.Settings.Email {
			id := entry.Bank
			if id == "" {
				id = "notification-" + entry.YNABAccountID
			}
			ids[id] = true
		}
		for _, entry := range e.Settings.Webhook {
			ids[entry.Bank] = true
		}
		for id := range ids {
			rows, err := e.Store.List(ctx, "BANK#"+id, "JOB#")
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, rows...)
		}
		return jobs, nil
	case "job.get":
		if err := e.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		notices, err := e.Store.List(ctx, "BANK#"+r.Bank, "NOTICE#")
		if err != nil {
			return nil, err
		}
		reviews, err := e.Store.List(ctx, "BANK#"+r.Bank, "review#")
		if err != nil {
			return nil, err
		}
		balances, err := e.Store.List(ctx, "BANK#"+r.Bank, "BALANCE#")
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": e.Job, "notifications": notices, "reviews": reviews, "balanceWrites": balances}, nil
	case "notifications.retry":
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "notification-retry")
		if err != nil || !ok {
			return nil, fmt.Errorf("bank busy")
		}
		defer e.Store.Release(ctx, l)
		rows, err := e.Store.List(ctx, "BANK#"+r.Bank, "NOTICE#")
		if err != nil {
			return nil, err
		}
		for _, raw := range rows {
			var n Notification
			_ = json.Unmarshal(raw, &n)
			if n.JobID == r.JobID && n.Status != "sent" && n.Status != "superseded" {
				n.Attempts = 0
				n.Status = "pending"
				if err = e.Store.Put(ctx, l, "NOTICE#"+n.ID, n); err != nil {
					return nil, err
				}
			}
		}
		if err = e.Store.Release(ctx, l); err != nil {
			return nil, err
		}
		return e.startOperation(ctx, r, "notify")
	case "job.retry":
		if err := e.loadJob(ctx, r.JobID, r.Bank); err != nil {
			return nil, err
		}
		if e.Job.Status != "failed" && e.Job.Status != "review-required" && e.Job.Status != "paused" {
			return nil, fmt.Errorf("job is not retryable")
		}
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "operator-retry")
		if err != nil || !ok {
			return nil, fmt.Errorf("bank busy")
		}
		defer e.Store.Release(ctx, l)
		e.Lease = l
		e.Job.Status = "accepted"
		e.Job.Attempt = 0
		e.Job.BrowserAttempted = false
		e.Job.Error = ""
		if err = e.saveJob(ctx); err != nil {
			return nil, err
		}
		return e.startOperation(ctx, r, "run")
	case "banks.list":
		result := []map[string]any{}
		for _, registration := range banks.All {
			var h Health
			var baseline Baseline
			if _, err := e.Store.Get(ctx, "BANK#"+registration.ID, "HEALTH", &h); err != nil {
				return nil, err
			}
			if _, err := e.Store.Get(ctx, "BANK#"+registration.ID, "BASELINE", &baseline); err != nil {
				return nil, err
			}
			result = append(result, map[string]any{"bank": registration.ID, "name": registration.Name, "health": h, "baselineApproved": baseline.Approved, "enabled": e.Settings.Banks[registration.ID].Enabled})
		}
		return result, nil
	case "bank.resume":
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "operator-resume")
		if err != nil || !ok {
			return nil, fmt.Errorf("bank busy")
		}
		defer e.Store.Release(ctx, l)
		var health Health
		if _, err = e.Store.Get(ctx, "BANK#"+r.Bank, "HEALTH", &health); err != nil {
			return nil, err
		}
		health.Blocked = false
		health.RetryAt = time.Time{}
		return map[string]bool{"resumed": true}, e.Store.PutMany(ctx, l, map[string]any{"HEALTH": health, "SESSION": SessionPointer{}})
	case "baseline.preview", "baseline.approve":
		return e.baseline(ctx, r)
	case "review.match":
		var input struct{ ReviewID, EntryKey string }
		if json.Unmarshal(r.Payload, &input) != nil || input.ReviewID == "" || input.EntryKey == "" {
			return nil, bankStateError()
		}
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "match-review")
		if err != nil || !ok {
			return nil, fmt.Errorf("bank busy")
		}
		defer e.Store.Release(ctx, l)
		var review struct {
			Record     bank.Record `json:"record"`
			Candidates []string    `json:"candidates"`
		}
		found, err := e.Store.Get(ctx, "BANK#"+r.Bank, "review#"+input.ReviewID, &review)
		if err != nil || !found {
			return nil, bankStateError()
		}
		allowed := false
		for _, candidate := range review.Candidates {
			allowed = allowed || candidate == input.EntryKey
		}
		if !allowed {
			return nil, fmt.Errorf("entry is not a review candidate")
		}
		return map[string]bool{"resolved": true}, e.Store.Put(ctx, l, "RESOLUTION#"+input.ReviewID, map[string]string{"entryKey": input.EntryKey})
	case "review.update":
		var input struct{ Key, Decision string }
		if json.Unmarshal(r.Payload, &input) != nil || !strings.HasPrefix(input.Key, "UPDATE#") || input.Decision != "not-written" {
			return nil, bankStateError()
		}
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "update-review")
		if err != nil || !ok {
			return nil, fmt.Errorf("bank busy")
		}
		defer e.Store.Release(ctx, l)
		var w Write
		found, err := e.Store.Get(ctx, "BANK#"+r.Bank, input.Key, &w)
		if err != nil || !found || w.Status != "started" {
			return nil, bankStateError()
		}
		w.Status = "reviewed-not-written"
		return w, e.Store.Put(ctx, l, input.Key, w)
	case "review.balance":
		var input struct{ AccountID, TransactionID, Decision string }
		if json.Unmarshal(r.Payload, &input) != nil || input.AccountID == "" || (input.Decision != "confirmed" && input.Decision != "not-written") {
			return nil, bankStateError()
		}
		l, ok, err := e.Store.Acquire(ctx, r.Bank, "balance-review")
		if err != nil || !ok {
			return nil, fmt.Errorf("bank busy")
		}
		defer e.Store.Release(ctx, l)
		var w Write
		found, err := e.Store.Get(ctx, "BANK#"+r.Bank, "BALANCE#"+input.AccountID, &w)
		if err != nil || !found {
			return nil, bankStateError()
		}
		if input.Decision == "confirmed" {
			rows, err := service.GetAccountTransactions(ctx, w.AccountID)
			if err != nil {
				return nil, err
			}
			matched := false
			for _, tx := range rows {
				if tx.ID == input.TransactionID && !tx.Deleted && tx.Amount == w.Amount && tx.Date == w.Date {
					matched = true
				}
			}
			if !matched {
				return nil, fmt.Errorf("transaction does not match uncertain adjustment")
			}
			w.Status = "complete"
			w.TransactionID = input.TransactionID
		} else {
			w.Status = "reviewed-not-written"
		}
		return w, e.Store.Put(ctx, l, "BALANCE#"+input.AccountID, w)
	default:
		return nil, fmt.Errorf("unsupported operation")
	}
}
func validateRule(r types.TransactionOverride) error {
	if strings.TrimSpace(r.Name) == "" || r.Payee == "" || len(r.Memo) > 500 || len(r.Query) > 20000 {
		return fmt.Errorf("invalid rule")
	}
	if _, err := override.RenderMemo(r.Memo, "2026-10-06"); err != nil {
		return err
	}
	var query any
	if json.Unmarshal([]byte(r.Query), &query) != nil {
		return fmt.Errorf("invalid query")
	}
	var walk func(any, int) error
	walk = func(v any, depth int) error {
		if depth > 20 {
			return fmt.Errorf("query too deep")
		}
		switch x := v.(type) {
		case map[string]any:
			if len(x) != 1 {
				return fmt.Errorf("invalid expression")
			}
			for op, value := range x {
				switch op {
				case "and", "or", "!", "==", "!=", "===", "!==", "<", ">", "<=", ">=", "in":
					return walk(value, depth+1)
				case "var":
					field, ok := value.(string)
					if !ok || (field != "amount" && field != "merchant" && field != "day" && field != "month") {
						return fmt.Errorf("invalid field")
					}
				default:
					return fmt.Errorf("unsupported operator")
				}
			}
		case []any:
			if len(x) == 0 {
				return fmt.Errorf("empty expression")
			}
			for _, child := range x {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case string, float64, bool:
		default:
			return fmt.Errorf("invalid operand")
		}
		return nil
	}
	return walk(query, 0)
}
func (e *Engine) baseline(ctx context.Context, r Request) (any, error) {
	if err := e.loadJob(ctx, r.JobID, r.Bank); err != nil {
		return nil, err
	}
	if !e.Job.DryRun || e.Job.SnapshotKey == "" {
		return nil, fmt.Errorf("baseline requires a completed dry-run snapshot")
	}
	var snapshot bank.Snapshot
	if err := e.Store.Download(ctx, e.Job.SnapshotKey, &snapshot); err != nil {
		return nil, err
	}
	mapped, err := e.mapping(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	b := Baseline{At: snapshot.At, Seen: map[string]bool{}, Links: map[string]string{}, SnapshotKey: e.Job.SnapshotKey}
	var unresolved []bank.Record
	var input struct {
		Links    map[string]string `json:"links"`
		Approved bool              `json:"approved"`
	}
	_ = json.Unmarshal(r.Payload, &input)
	for _, record := range snapshot.Records {
		key := record.AccountID + "#" + identity(record)
		if record.Status == "posted" {
			b.Seen[key] = true
			continue
		}
		rows, err := service.GetAccountTransactions(ctx, mapped[record.AccountID].ID)
		if err != nil {
			return nil, err
		}
		registration, _ := banks.Find(r.Bank)
		importID := registration.ImportPrefix + ":" + bank.Hash("tx#" + record.AccountID + "#" + bank.Hash(identity(record)))[:32]
		for _, tx := range rows {
			if tx.Deleted {
				continue
			}
			exact := tx.ImportID != nil && *tx.ImportID == importID
			chosen := input.Links[key] != "" && input.Links[key] == tx.ID
			if (exact || chosen) && tx.Amount == record.Amount && near(tx.Date, record.Date, 14) {
				b.Links[key] = tx.ID
			}
		}
		if b.Links[key] == "" {
			unresolved = append(unresolved, record)
		}
	}
	if r.Action == "baseline.preview" {
		return map[string]any{"baseline": b, "unresolved": unresolved, "snapshot": snapshot, "ynabAccounts": mapped}, nil
	}
	if e.Settings.ImportsEnabled {
		return nil, fmt.Errorf("pause imports before approving a baseline")
	}
	if !input.Approved || len(unresolved) > 0 {
		return nil, fmt.Errorf("baseline has unresolved pending records or no approval")
	}
	l, ok, err := e.Store.Acquire(ctx, r.Bank, "baseline-approval")
	if err != nil || !ok {
		return nil, fmt.Errorf("bank busy")
	}
	defer e.Store.Release(ctx, l)
	b.Approved = true
	return b, e.Store.Put(ctx, l, "BASELINE", b)
}

func (e *Engine) startOperation(ctx context.Context, r Request, action string) (any, error) {
	input, _ := json.Marshal(Request{Action: action, Bank: r.Bank, JobID: r.JobID})
	_, err := e.SFN.StartExecution(ctx, &sfn.StartExecutionInput{StateMachineArn: aws.String(os.Getenv("WORKFLOW_ARN")), Name: aws.String(action + "-" + uuid.NewString()), Input: aws.String(string(input))})
	return map[string]bool{"accepted": err == nil}, err
}
