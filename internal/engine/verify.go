package engine

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
)

// VerifySession exercises real API access in a separate process, using the normal
// lease, renewal policy and encrypted session store. It never invokes an importer.
func (e *Engine) VerifySession(ctx context.Context, bankID string, renew bool) (any, error) {
	started := time.Now()
	r, ok := banks.Find(bankID)
	if !ok {
		return nil, bank.Fail(bank.Invalid, "bank")
	}
	settings, ok := e.Settings.Banks[bankID]
	if !ok || !settings.Enabled {
		return nil, bank.Fail(bank.Invalid, "bank-disabled")
	}
	e.Job = Job{ID: "verify-" + uuid.NewString(), Bank: bankID, Purpose: "maintain-session", DryRun: true}
	l, acquired, err := e.Store.Acquire(ctx, bankID, e.Job.ID)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, bank.Fail(bank.Temporary, "bank-busy")
	}
	e.Lease = l
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.Store.Release(cleanup, l)
	}()
	s, err := e.session(ctx)
	if err != nil {
		return nil, err
	}
	if s.RenewalUncertain {
		return nil, bank.Fail(bank.Invalid, "renewal-outcome-unknown")
	}
	adapter := r.New(bank.Dependencies{})
	if renew {
		updated, renewalErr := e.renew(ctx, adapter, s)
		if err = e.saveSession(ctx, updated); err != nil {
			return nil, err
		}
		if renewalErr != nil {
			return nil, renewalErr
		}
		s = updated
	}
	snapshot, updated, fetchErr := adapter.Fetch(ctx, s, bank.FetchRequest{StartDate: settings.StartDate})
	if err = e.saveSession(ctx, updated); err != nil {
		return nil, err
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	if err = ValidateSnapshot(snapshot, settings.ExpectedAccounts); err != nil {
		return nil, err
	}
	key, err := e.Store.Upload(ctx, "snapshots/"+bankID, snapshot)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(snapshot.Accounts))
	for _, a := range snapshot.Accounts {
		ids = append(ids, a.ID)
	}
	return map[string]any{"bank": bankID, "accounts": ids, "records": len(snapshot.Records), "snapshotKey": key,
		"expiresAt": updated.ExpiresAt, "renewAt": updated.RenewAt, "maximumExpiresAt": updated.MaximumExpiresAt,
		"durationMs": time.Since(started).Milliseconds(), "renewed": renew}, nil
}
