package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
)

// VerifySession exercises real API access in a separate process, using the normal
// lease, renewal policy and encrypted session store. It never invokes an importer.
func (t *task) verifySession(ctx context.Context, bankID string, renew bool) (any, error) {
	started := time.Now()
	r, ok := banks.Find(bankID)
	if !ok {
		return nil, bank.Fail(bank.Invalid, "bank")
	}
	settings, ok := t.Settings.Banks[bankID]
	if !ok || !settings.Enabled {
		return nil, bank.Fail(bank.Invalid, "bank-disabled")
	}
	t.Job = Job{ID: "verify-" + uuid.NewString(), Bank: bankID, DryRun: true}
	l, acquired, err := t.Store.Acquire(ctx, bankID, t.Job.ID)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, bank.Fail(bank.Temporary, "bank-busy")
	}
	t.Lease = l
	defer func() {
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		if err := t.Store.Release(cleanup, l); err != nil {
			slog.Warn("lease not released", "bank", bankID, "error", err)
		}
	}()
	s, err := t.session(ctx)
	if err != nil {
		return nil, err
	}
	adapter := r.API
	if renew {
		updated, renewalErr := t.renew(ctx, adapter, s)
		if err = t.saveSession(ctx, updated); err != nil {
			return nil, err
		}
		if renewalErr != nil {
			return nil, renewalErr
		}
		s = updated
	}
	snapshot, updated, fetchErr := adapter.Fetch(ctx, s, bank.FetchRequest{StartDate: t.historyStart(settings.StartDate)})
	if err = t.saveSession(ctx, updated); err != nil {
		return nil, err
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	if err = snapshot.Validate(settings.ExpectedAccounts); err != nil {
		return nil, err
	}
	key, err := t.Store.Upload(ctx, "snapshots/"+bankID, snapshot)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(snapshot.Accounts))
	for _, a := range snapshot.Accounts {
		ids = append(ids, a.ID)
	}
	return map[string]any{
		"bank": bankID, "accounts": ids, "records": len(snapshot.Records), "snapshotKey": key,
		"expiresAt": updated.ExpiresAt, "renewAt": updated.RenewAt, "maximumExpiresAt": updated.MaximumExpiresAt,
		"durationMs": time.Since(started).Milliseconds(), "renewed": renew,
	}, nil
}
