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

	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

type Kind string

const (
	Authentication Kind = "authentication-required"
	Credentials    Kind = "credentials-rejected"
	Challenge      Kind = "challenge-required"
	Maintenance    Kind = "bank-maintenance"
	Throttled      Kind = "throttled"
	Temporary      Kind = "temporary"
	Invalid        Kind = "invalid-response"
	Uncertain      Kind = "uncertain-write"
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
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	return &Failure{Kind: Temporary, Operation: "operation"}
}

type Session struct {
	RenewalUncertain bool              `json:"renewalUncertain,omitempty"`
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
type FetchRequest struct {
	AccountsOnly bool
	StartDate    string
}
type Adapter interface {
	Authenticate(context.Context, *rod.Browser) (Session, error)
	Renew(context.Context, Session) (Session, error)
	Fetch(context.Context, Session, FetchRequest) (Snapshot, Session, error)
}
type CredentialsData struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	MailToken string `json:"mailToken"`
}
type Dependencies struct {
	Previous                  *Session
	Credentials               CredentialsData
	EmailSender, EmailSubject string
	EmailCodeLength           int
}
type Registration struct {
	ID, Name, Strategy, Browser, Schedule, ImportPrefix, PurchaseAccount string
	KeepWarm                                                             bool
	New                                                                  func(Dependencies) Adapter
}

const Namespace = "f47ac10b-58cc-4372-a567-0e02b2c3d479"

func AccountID(number string) string {
	return uuid.NewSHA1(uuid.MustParse(Namespace), []byte(number)).String()
}
func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
