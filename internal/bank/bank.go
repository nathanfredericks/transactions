// Package bank is the complete boundary between bank protocols and the engine.
package bank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	Capacity       Kind = "browser-capacity"
	Authentication Kind = "authentication-required"
	Credentials    Kind = "credentials-rejected"
	Challenge      Kind = "challenge-required"
	Maintenance    Kind = "bank-maintenance"
	Throttled      Kind = "throttled"
	Temporary      Kind = "temporary"
	Invalid        Kind = "invalid-response"
)

type Failure struct {
	Kind              Kind
	Operation         string
	RetryAt           time.Time
	Detail            string
	ExchangeUncertain bool // A mutating HTTP request may have completed without confirmation.
}

func (e *Failure) Error() string             { return string(e.Kind) + ": " + e.Operation }
func Fail(kind Kind, operation string) error { return &Failure{Kind: kind, Operation: operation} }
func Classify(err error) *Failure {
	if f, ok := errors.AsType[*Failure](err); ok {
		return f
	}
	return &Failure{Kind: Temporary, Operation: "operation"}
}

type Session struct {
	BrowserPlatform  string            `json:"browserPlatform,omitempty"`
	BrowserVersion   string            `json:"browserVersion,omitempty"`
	BrowserSeed      int               `json:"browserSeed,omitempty"`
	Version          int               `json:"version"`
	Bank             string            `json:"bank"`
	Headers          map[string]string `json:"headers"`
	Cookies          []Cookie          `json:"cookies,omitempty"`
	Auth             json.RawMessage   `json:"auth,omitempty"`
	ExpiresAt        time.Time         `json:"expiresAt"`
	RenewAt          time.Time         `json:"renewAt"`
	MaximumExpiresAt time.Time         `json:"maximumExpiresAt"`
}
type Cookie struct {
	URL   string      `json:"url"`
	Value http.Cookie `json:"value"`
}
type Account struct {
	ID      string `json:"id"`
	Number  string `json:"number"`
	Name    string `json:"name"`
	Balance int64  `json:"balance"`
}
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
type Snapshot struct {
	Accounts []Account `json:"accounts"`
	Records  []Record  `json:"records"`
	At       time.Time `json:"at"`
}

// Validate checks a fetched snapshot: the expected accounts, each once, and
// records with an identity, a description, a known status and a valid date.
func (s Snapshot) Validate(expected []string) error {
	if len(s.Accounts) == 0 {
		return Fail(Invalid, "empty-accounts")
	}
	seen := map[string]bool{}
	for _, a := range s.Accounts {
		if a.ID == "" || seen[a.ID] {
			return Fail(Invalid, "account-identity")
		}
		seen[a.ID] = true
	}
	if len(expected) != len(seen) {
		return Fail(Invalid, "account-count")
	}
	for _, id := range expected {
		if !seen[id] {
			return Fail(Invalid, "account-identity")
		}
	}
	for _, r := range s.Records {
		if !seen[r.AccountID] || r.ProvisionalID == "" || r.Description == "" || (r.Status != "posted" && r.Status != "pending") {
			return Fail(Invalid, "record")
		}
		if _, err := time.Parse("2006-01-02", r.Date); err != nil {
			return Fail(Invalid, "record-date")
		}
	}
	return nil
}

type FetchRequest struct {
	AccountsOnly bool
	StartDate    string
}

// API is a bank's direct protocol: renewing a saved session and fetching
// accounts and transactions with it. Signing in is browser-only, in signin.
type API interface {
	Renew(context.Context, Session) (Session, error)
	Fetch(context.Context, Session, FetchRequest) (Snapshot, Session, error)
}

type Registration struct {
	ID, Name, Strategy, Browser, ImportPrefix, PurchaseAccount string
	API                                                        API
}

const Namespace = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

func AccountID(number string) string {
	return uuid.NewSHA1(uuid.MustParse(Namespace), []byte(number)).String()
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
