package engine

import (
	"context"
	"errors"
	"log/slog"
	"time"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nathanfredericks/transactions/internal/bank"
)

type SessionPointer struct {
	Invalidated bool      `json:"invalidated,omitempty"`
	Key         string    `json:"key"`
	RenewAt     time.Time `json:"renewAt"`
}

func (t *task) session(ctx context.Context) (bank.Session, error) {
	var pointer SessionPointer
	found, err := t.get(ctx, "SESSION", &pointer)
	if err != nil {
		return bank.Session{}, err
	}
	if !found || pointer.Key == "" || pointer.Invalidated {
		return bank.Session{}, bank.Fail(bank.Authentication, "session-missing")
	}
	var s bank.Session
	if err = t.Store.Download(ctx, pointer.Key, &s); err != nil {
		if _, ok := errors.AsType[*s3types.NoSuchKey](err); ok {
			return s, bank.Fail(bank.Authentication, "session-missing")
		}
		return s, err
	}
	if s.Version != 1 || s.Bank != t.Job.Bank {
		return s, bank.Fail(bank.Invalid, "session-identity")
	}
	return s, nil
}

func (t *task) saveSession(ctx context.Context, s bank.Session) error {
	if s.Version != 1 || s.Bank != t.Job.Bank {
		return bank.Fail(bank.Invalid, "session-identity")
	}
	if s.RenewAt.IsZero() {
		bank.TokenTiming(&s)
	}
	key, err := t.Store.Upload(ctx, "sessions/"+t.Job.Bank, s)
	if err != nil {
		return err
	}
	return t.Store.Put(ctx, t.Lease, "SESSION", SessionPointer{Key: key, RenewAt: s.RenewAt})
}

// renew exchanges the session's tokens. A renewal whose outcome is unknown
// counts as expired authentication, so the job signs in through the browser.
func (t *task) renew(ctx context.Context, adapter bank.API, session bank.Session) (bank.Session, error) {
	oldAccess, oldRefresh, oldExpiry := session.Headers["authorization"], session.Headers["refreshtoken"], session.ExpiresAt
	renewed, err := adapter.Renew(ctx, session)
	slog.Info("session-renewal", "bank", t.Job.Bank, "success", err == nil,
		"accessRotated", oldAccess != renewed.Headers["authorization"], "refreshRotated", oldRefresh != renewed.Headers["refreshtoken"],
		"expiryExtended", renewed.ExpiresAt.After(oldExpiry))
	if err != nil {
		failure := bank.Classify(err)
		// Never log exchange bodies or authentication material.
		slog.Warn("session-renewal-failed", "bank", t.Job.Bank, "kind", failure.Kind,
			"operation", failure.Operation, "exchangeUncertain", failure.ExchangeUncertain)
		if failure.ExchangeUncertain || failure.Kind == bank.Invalid {
			return renewed, bank.Fail(bank.Authentication, "renewal-outcome-unknown")
		}
	}
	return renewed, err
}
