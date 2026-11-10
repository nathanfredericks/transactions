// Package signin signs in to banks in a browser. Only the browser task links
// it; the Lambdas use each bank's direct API.
package signin

import (
	"context"

	"github.com/nathanfredericks/transactions/internal/bank"
)

// Credentials are a bank login from the Transactions secret.
type Credentials struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	MailToken string `json:"mailToken"`
}

// Dependencies are what a sign-in needs besides the browser.
type Dependencies struct {
	Previous                  *bank.Session
	Credentials               Credentials
	EmailSender, EmailSubject string
	EmailCodeLength           int
}

// Authenticate signs in to the bank in the browser and returns its session.
func Authenticate(ctx context.Context, bankID string, d Dependencies, b *Browser) (bank.Session, error) {
	switch bankID {
	case "eq-bank":
		return signInEQ(ctx, d, b)
	case "rogers-bank":
		return signInRogers(ctx, d, b)
	case "nbdb":
		return signInNBDB(ctx, d, b)
	}
	return bank.Session{}, bank.Fail(bank.Invalid, "unknown-bank")
}
