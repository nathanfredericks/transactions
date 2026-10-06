package eq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
	"log/slog"
	"math"
	"strings"
	"time"
)

var getAccounts = service.GetAccounts
var getAccountTransactions = service.GetAccountTransactions
var getOverrides = override.GetTransactionOverrides
var getParameters = config.GetParameters
var findOverride = override.FindOverride
var getPayees = service.GetPayees
var matchPayee = service.MatchPayee
var createEQTransaction = service.CreateEQTransaction
var updateEQTransaction = service.UpdateEQTransaction

const personal = "50ef2940-404a-5073-8472-5e0f6bd3c398"
const card = "13a60c53-989f-5c2e-88bd-6eddacd972be"

type Record struct {
	AccountID        string   `json:"accountId"`
	AccountNumber    string   `json:"accountNumber"`
	BankID           *string  `json:"bankId"`
	ProvisionalID    string   `json:"provisionalId"`
	References       []string `json:"references"`
	Amount           int64    `json:"amount"`
	Date             string   `json:"date"`
	Description      string   `json:"description"`
	Status           string   `json:"status"`
	OriginalAmount   *int64   `json:"originalAmount"`
	OriginalCurrency *string  `json:"originalCurrency"`
}
type Entry struct {
	Key                 string `json:"key"`
	Record              Record `json:"record"`
	ImportID            string `json:"importId"`
	YNABID              string `json:"ynabId"`
	Adopted             bool   `json:"adopted"`
	Payee               string `json:"payee"`
	AuthorizationAmount *int64 `json:"authorizationAmount,omitempty"`
	AuthorizationDate   string `json:"authorizationDate,omitempty"`
	AlertMessageID      string `json:"alertMessageId,omitempty"`
}
type Result struct {
	Version   int      `json:"version"`
	JobID     string   `json:"jobId"`
	RequestID string   `json:"requestId"`
	Complete  bool     `json:"complete"`
	Error     string   `json:"error"`
	Records   []Record `json:"records"`
}

func (s *Store) ledger(ctx context.Context) ([]*Entry, error) {
	var entries []*Entry
	var cursor map[string]dt.AttributeValue
	for {
		result, err := s.db.Scan(ctx, &dynamodb.ScanInput{TableName: &s.table, ConsistentRead: aws.Bool(true), ExclusiveStartKey: cursor, FilterExpression: aws.String("begins_with(#key, :prefix)"), ExpressionAttributeNames: map[string]string{"#key": "key"}, ExpressionAttributeValues: map[string]dt.AttributeValue{":prefix": &dt.AttributeValueMemberS{Value: "tx#"}}})
		if err != nil {
			return nil, err
		}
		for _, item := range result.Items {
			data, ok := item["data"].(*dt.AttributeValueMemberS)
			if !ok {
				return nil, errors.New("Invalid EQ ledger")
			}
			var e Entry
			if err = json.Unmarshal([]byte(data.Value), &e); err != nil {
				return nil, err
			}
			entries = append(entries, &e)
		}
		cursor = result.LastEvaluatedKey
		if len(cursor) == 0 {
			break
		}
	}
	return entries, nil
}
func identity(r Record) string {
	if r.BankID != nil {
		return *r.BankID
	}
	return r.ProvisionalID
}
func merchant(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }
func near(a, b string, days int) bool {
	aa, e := time.Parse("2006-01-02", a)
	if e != nil {
		return false
	}
	bb, e := time.Parse("2006-01-02", b)
	return e == nil && math.Abs(aa.Sub(bb).Hours()) <= float64(days*24)
}
func samePurchase(a, b Record) bool {
	if a.AccountID != b.AccountID || a.Amount*b.Amount <= 0 || merchant(a.Description) != merchant(b.Description) || !near(a.Date, b.Date, 14) {
		return false
	}
	if a.OriginalAmount != nil && b.OriginalAmount != nil && a.OriginalCurrency != nil && b.OriginalCurrency != nil {
		return *a.OriginalAmount == *b.OriginalAmount && *a.OriginalCurrency == *b.OriginalCurrency
	}
	return a.Amount == b.Amount
}
func exactMatch(a, b Record) bool {
	if a.AccountID != b.AccountID {
		return false
	}
	if a.ProvisionalID == b.ProvisionalID || (a.BankID != nil && b.BankID != nil && *a.BankID == *b.BankID) {
		return true
	}
	for _, x := range a.References {
		for _, y := range b.References {
			if x != "" && x == y {
				return true
			}
		}
	}
	return false
}
func (s *Store) process(ctx context.Context, job Job) (any, error) {
	var result Result
	if err := s.download(ctx, "jobs/"+job.JobID+"/result.json", &result); err != nil {
		return nil, err
	}
	if result.Version != 1 || result.JobID != job.JobID || result.RequestID == "" || result.RequestID != job.RequestID {
		return nil, errors.New("Invalid EQ result envelope")
	}
	if !result.Complete && result.Error == "bank-maintenance" {
		return s.maintenance(ctx, job)
	}
	if err := s.resetMaintenance(ctx, job, result.Complete); err != nil {
		return nil, err
	}
	if !result.Complete {
		if result.Error == "credentials-rejected" || result.Error == "challenge-required" {
			if err := s.put(ctx, "credentials-blocked", map[string]any{"at": time.Now().UTC(), "reason": result.Error}, job.JobID); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("EQ bank retrieval failed: " + result.Error)
	}
	if job.Purpose == "maintain-session" {
		return map[string]any{"missing": false, "maintenance": false, "outcome": "session-ready"}, nil
	}
	accounts, err := getAccounts(ctx)
	if err != nil {
		return nil, err
	}
	mapping := map[string]string{}
	for _, id := range []string{personal, card} {
		for _, a := range accounts {
			if !a.Closed && !a.Deleted && a.Note != nil && strings.Contains(*a.Note, id) {
				if mapping[id] != "" {
					return nil, errors.New("Duplicate EQ YNAB account mapping")
				}
				mapping[id] = a.ID
			}
		}
		if mapping[id] == "" {
			return nil, errors.New("Missing EQ YNAB account mapping")
		}
	}
	snapshots := map[string][]*types.YNABTransaction{}
	for _, id := range []string{personal, card} {
		snapshots[id], err = getAccountTransactions(ctx, mapping[id])
		if err != nil {
			return nil, err
		}
	}
	entries, err := s.ledger(ctx)
	if err != nil {
		return nil, err
	}
	// A native row may only claim one pending row, even when several identical purchases post together.
	claimed := map[string]bool{}
	resolved := map[string]*Entry{}
	review := 0
	created := 0
	adopted := 0
	updated := 0
	hold := func(r Record, candidates []*Entry, reason string) error {
		review++
		if job.DryRun {
			return nil
		}
		var keys []string
		for _, e := range candidates {
			keys = append(keys, e.Key)
		}
		id := hash(r.AccountID + "#" + identity(r))
		if err := s.put(ctx, "review#"+id, map[string]any{"record": r, "candidates": keys, "reason": reason, "jobId": job.JobID}, job.JobID); err != nil {
			return err
		}
		return s.notifyOnce(ctx, "review-notification#"+id, "EQ posting matches are ambiguous. Existing pending transactions were retained for review.", "EQ Bank Review Required")
	}
	overrides, err := getOverrides(ctx)
	if err != nil {
		return nil, err
	}
	var payees []string
	for _, r := range result.Records {
		if mapping[r.AccountID] == "" || (r.AccountID == personal && r.AccountNumber != "130742998") || (r.AccountID == card && r.AccountNumber != "302911525") || r.Description == "" || r.ProvisionalID == "" || r.Date < "2026-10-02" || (r.Status != "pending" && r.Status != "posted") {
			return nil, errors.New("Invalid EQ bank record")
		}
		if _, e := time.Parse("2006-01-02", r.Date); e != nil {
			return nil, e
		}
		var exact []*Entry
		for _, entry := range entries {
			if entry.Record.AccountID != r.AccountID {
				continue
			}
			match := entry.Record.ProvisionalID == r.ProvisionalID || (r.BankID != nil && entry.Record.BankID != nil && *r.BankID == *entry.Record.BankID)
			for _, a := range entry.Record.References {
				for _, b := range r.References {
					if a != "" && a == b {
						match = true
					}
				}
			}
			if match {
				exact = append(exact, entry)
			}
		}
		if len(exact) > 1 {
			return nil, errors.New("Conflicting EQ bank references in ledger")
		}
		var entry *Entry
		if len(exact) == 1 {
			entry = exact[0]
		}
		if entry == nil && r.Status == "posted" && r.AccountID == card && r.Amount != 0 {
			var candidates []*Entry
			for _, e := range entries {
				if e.Record.Status == "pending" && samePurchase(e.Record, r) {
					candidates = append(candidates, e)
				}
			}
			// Also look forward: two posted rows matching the same pending row are ambiguous.
			competing := 0
			for _, other := range result.Records {
				if other.Status == "posted" && samePurchase(other, r) {
					known := false
					for _, e := range entries {
						if e.Record.Status == "posted" && exactMatch(e.Record, other) {
							known = true
							break
						}
					}
					if !known {
						competing++
					}
				}
			}
			if len(candidates) > 1 || (len(candidates) == 1 && competing > 1) {
				if err = hold(r, candidates, "Multiple pending or newly posted records match"); err != nil {
					return nil, err
				}
				continue
			}
			if len(candidates) == 1 {
				if claimed[candidates[0].Key] {
					if err = hold(r, candidates, "Pending record is still present in this history"); err != nil {
						return nil, err
					}
					continue
				}
				entry = candidates[0]
			}
		}
		if entry == nil {
			stable := "tx#" + r.AccountID + "#" + hash(identity(r))
			entry = &Entry{Key: stable, Record: r, ImportID: "EQ:" + hash(stable)[:32]}
			if r.Status == "pending" {
				amount := r.Amount
				entry.AuthorizationAmount = &amount
				entry.AuthorizationDate = r.Date
			}
			// Adopt only these inspected reconciled funding records; future loads are independent entries.
			bootstrap := map[string]string{"FT26275WHGBV": "a3ed8176-cd43-4840-a9a9-5bfe2c01fe9c", "FT26275MC20D": "5ebe1550-ec78-4f31-81b1-5e518cb1e4c6", "2a46bfb6-d74f-471e-913c-87e90c5e13e3": "be12012a-3fdb-4ac6-86a8-5c238179af48"}
			if r.BankID != nil && bootstrap[*r.BankID] != "" {
				for _, t := range snapshots[r.AccountID] {
					if t.ID == bootstrap[*r.BankID] && !t.Deleted && t.Date == r.Date && t.Amount == r.Amount && t.Cleared == "reconciled" && t.TransferAccountID != nil {
						entry.YNABID = t.ID
						entry.Adopted = true
						adopted++
					}
				}
				if !entry.Adopted {
					return nil, errors.New("EQ bootstrap transfer differs from the inspected reconciled transaction")
				}
			}
			if entry.YNABID == "" {
				// Recover from a write which succeeded before the ledger was committed.
				for _, t := range snapshots[r.AccountID] {
					if !t.Deleted && t.ImportID != nil && *t.ImportID == entry.ImportID {
						entry.YNABID = t.ID
					}
				}
			}
			if entry.YNABID == "" {
				params, e := getParameters(ctx)
				if e != nil {
					return nil, e
				}
				loc, e := time.LoadLocation(params.Timezone)
				if e != nil {
					return nil, e
				}
				day, e := time.ParseInLocation("2006-01-02", r.Date, loc)
				if e != nil {
					return nil, e
				}
				rule, e := findOverride(ctx, overrides, math.Abs(float64(r.Amount))/1000, r.Description, day)
				if e != nil {
					return nil, e
				}
				payee := r.Description
				if rule == nil {
					if payees == nil {
						payees, e = getPayees(ctx)
						if e != nil {
							return nil, e
						}
					}
					payee, e = matchPayee(ctx, r.Description, payees)
					if e != nil {
						return nil, e
					}
				}
				slog.Info("EQ transaction preview", "account", mapping[r.AccountID], "date", r.Date, "amount", r.Amount, "payee", payee, "status", r.Status, "importId", entry.ImportID, "dryRun", job.DryRun)
				if !job.DryRun {
					tx, e := createEQTransaction(ctx, mapping[r.AccountID], r.Date, r.Amount, payee, entry.ImportID, r.Status, rule)
					if e != nil {
						return nil, e
					}
					entry.YNABID = tx.ID
					snapshots[r.AccountID] = append(snapshots[r.AccountID], tx)
				}
				created++
			}
			entries = append(entries, entry)
		} else {
			if claimed[entry.Key] {
				return nil, errors.New("Multiple EQ records claimed the same ledger transaction")
			}
			if entry.Record.Status == "posted" && r.Status == "pending" {
				return nil, errors.New("EQ posted transaction regressed to pending")
			}
			if entry.Record.Status != r.Status || entry.Record.Amount != r.Amount || entry.Record.Date != r.Date {
				var tx *types.YNABTransaction
				for _, t := range snapshots[r.AccountID] {
					if t.ID == entry.YNABID && !t.Deleted {
						tx = t
					}
				}
				if tx == nil {
					return nil, errors.New("EQ ledger transaction was removed from YNAB; review required")
				}
				if !job.DryRun {
					if _, err = updateEQTransaction(ctx, tx, r.Date, r.Amount, r.Status); err != nil {
						return nil, err
					}
				}
				updated++
			}
			// Keep the provisional identity when the native posted identity arrives.
			r.ProvisionalID = entry.Record.ProvisionalID
			r.References = append(r.References, entry.Record.References...)
			entry.Record = r
		}
		claimed[entry.Key] = true
		for _, tx := range snapshots[r.AccountID] {
			if tx.ID == entry.YNABID && tx.PayeeName != nil {
				entry.Payee = *tx.PayeeName
			}
		}
		resolved[identity(r)] = entry
		if !job.DryRun {
			if err = s.put(ctx, entry.Key, entry, job.JobID); err != nil {
				return nil, err
			}
		}
	}
	missing := false
	if job.Source == "alert" {
		var candidates []*Entry
		for _, r := range result.Records {
			e := resolved[identity(r)]
			if e == nil || r.AccountID != card {
				continue
			}
			// An earlier alert already identifies this purchase. Keep equal amounts
			// on distinct purchases eligible for their own original message IDs.
			if e.AlertMessageID != "" && e.AlertMessageID != job.MessageID {
				continue
			}
			amount, date := r.Amount, r.Date
			if e.AuthorizationAmount != nil {
				amount = *e.AuthorizationAmount
				date = e.AuthorizationDate
			}
			if amount == -job.AlertAmount && near(date, job.AlertDate, 1) {
				candidates = append(candidates, e)
			}
		}
		if len(candidates) != 1 {
			missing = true
		} else if !job.DryRun {
			entry := candidates[0]
			payee := entry.Payee
			if payee == "" {
				payee = entry.Record.Description
			}
			if err = s.notifyOnce(ctx, "purchase#"+entry.Key, fmt.Sprintf("A transaction of $%.2f at %s was approved on your EQ Bank Card.", float64(job.AlertAmount)/1000, payee), "Transaction Approved"); err != nil {
				return nil, err
			}
			entry.AlertMessageID = job.MessageID
			if err = s.put(ctx, entry.Key, entry, job.JobID); err != nil {
				return nil, err
			}
			if err = s.put(ctx, "alert#"+hash(job.MessageID), map[string]any{"ynabId": entry.YNABID, "jobId": job.JobID}, job.JobID); err != nil {
				return nil, err
			}
		}
	}
	summary := map[string]any{"version": 1, "jobId": job.JobID, "dryRun": job.DryRun, "records": len(result.Records), "created": created, "adopted": adopted, "updated": updated, "review": review, "missing": missing, "maintenance": false}
	if received, parseErr := time.Parse(time.RFC3339Nano, job.ReceivedAt); parseErr == nil {
		summary["elapsedMs"] = time.Since(received).Milliseconds()
	}
	if err = s.upload(ctx, "jobs/"+job.JobID+"/preview.json", summary); err != nil {
		return nil, err
	}
	slog.Info("EQ reconciliation", "summary", summary)
	return summary, nil
}
