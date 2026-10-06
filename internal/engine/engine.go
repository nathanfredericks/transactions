package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"log/slog"
	"math"
	"time"
)

func (e *Engine) Run(ctx context.Context, request Request) (out Outcome, err error) {
	started := time.Now()
	defer func() {
		slog.Info("bank-job", "bank", e.Job.Bank, "jobId", e.Job.ID, "purpose", e.Job.Purpose, "outcome", out.Outcome, "browserUsed", e.Job.BrowserAttempted, "durationMs", time.Since(started).Milliseconds())
	}()
	if err = e.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return out, err
	}
	if request.Execution == "" || (e.Job.Execution != "" && e.Job.Execution != request.Execution) {
		return out, bank.Fail(bank.Invalid, "stale-execution")
	}
	if e.Job.Status == "complete" || e.Job.Status == "review-required" || e.Job.Status == "failed" {
		return e.outcome(e.Job.Status, 0), nil
	}
	if e.Job.Status == "browser" {
		e.Lease = e.Job.Lease
		if err = e.Store.Assert(ctx, e.Lease); err != nil {
			return e.outcome("failed", 0), err
		}
	} else {
		var acquired bool
		e.Lease, acquired, err = e.Store.Acquire(ctx, e.Job.Bank, e.Job.ID)
		if err != nil {
			return out, err
		}
		if !acquired {
			return e.outcome("deferred", 5), nil
		}
	}
	hold := false
	defer func() {
		if !hold {
			release := e.Store.Release(ctx, e.Lease)
			err = errors.Join(err, release)
		}
	}()
	// Reload after acquiring the lease: another job may have completed while we waited.
	if err = e.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return out, err
	}
	if e.Job.Execution != "" && e.Job.Execution != request.Execution {
		return out, bank.Fail(bank.Invalid, "stale-execution")
	}
	if e.Job.Status == "complete" || e.Job.Status == "review-required" || e.Job.Status == "failed" {
		return e.outcome(e.Job.Status, 0), nil
	}
	e.Job.Execution = request.Execution
	e.Job.Lease = e.Lease
	if err = e.saveJob(ctx); err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			out, err = e.failure(ctx, err)
		}
	}()
	var health Health
	if _, err = e.get(ctx, e.healthKey(), &health); err != nil {
		return out, err
	}
	if health.Blocked {
		e.Job.Status = "review-required"
		e.Job.Error = health.Kind
		err = e.saveJob(ctx)
		return e.outcome("review-required", 0), err
	}
	if health.RetryAt.After(time.Now()) {
		if e.Job.Purpose == "maintain-session" {
			e.Job.Status = "complete"
			err = e.saveJob(ctx)
			return e.outcome("complete", 0), err
		}
		return e.outcome("deferred", int(math.Ceil(time.Until(health.RetryAt).Seconds()))), nil
	}
	if !e.Job.DryRun && e.Job.Purpose != "maintain-session" && !e.Settings.ImportsEnabled {
		e.Job.Status = "paused"
		err = e.saveJob(ctx)
		return e.outcome("paused", 0), err
	}
	if e.Job.Source == "notification" && e.Job.Purpose == "retrieve" {
		err = e.notificationImport(ctx)
		if err != nil {
			return out, err
		}
		return e.recovered(ctx, "Transaction imports", health)
	}
	registration, ok := banks.Find(e.Job.Bank)
	if !ok {
		return out, bank.Fail(bank.Invalid, "unknown-bank")
	}
	settings, ok := e.Settings.Banks[e.Job.Bank]
	if !ok || !settings.Enabled {
		return out, bank.Fail(bank.Invalid, "bank-disabled")
	}
	adapter := registration.New(bank.Dependencies{})
	if e.Job.Status == "browser" {
		var result BrowserResult
		found, re := e.get(ctx, "BROWSER#"+e.Lease.Generation, &result)
		if re != nil {
			return out, re
		}
		if !found {
			return out, bank.Fail(bank.Temporary, "browser-result-missing")
		}
		if result.Generation != e.Lease.Generation {
			return out, bank.Fail(bank.Invalid, "stale-browser")
		}
		if result.Error != nil {
			// This job already used its one browser attempt. Re-entering the
			// retrieval loop with no session would misclassify the same failure
			// as a rejected fresh login. End it; later jobs respect bank backoff.
			e.Job.Attempt = 2
			return out, result.Error
		}
		e.Job.Status = "running"
	}
	session, se := e.session(ctx)
	needsAuth := se != nil && bank.Classify(se).Kind == bank.Authentication
	if se != nil && !needsAuth {
		return out, se
	}
	if session.RenewalUncertain {
		return out, bank.Fail(bank.Invalid, "renewal-outcome-unknown")
	}
	if !needsAuth {
		if !session.MaximumExpiresAt.IsZero() && !session.MaximumExpiresAt.After(time.Now()) {
			needsAuth = true
		} else if !session.RenewAt.IsZero() && !session.RenewAt.After(time.Now()) {
			renewed, re := e.renew(ctx, adapter, session)
			persist := e.saveSession(ctx, renewed)
			if persist != nil {
				return out, persist
			}
			session = renewed
			if re != nil {
				if bank.Classify(re).Kind == bank.Authentication {
					needsAuth = true
				} else {
					return out, re
				}
			}
		}
	}
	if needsAuth {
		if e.Job.BrowserAttempted {
			return out, bank.Fail(bank.Challenge, "fresh-login-not-usable")
		}
		e.Job.BrowserAttempted = true
		e.Job.BrowserDeadline = time.Now().Add(330 * time.Second)
		e.Job.Status = "browser"
		if err = e.saveJob(ctx); err != nil {
			return out, err
		}
		hold = true
		return e.outcome("authentication-required", 0), nil
	}
	snapshot, updated, fetchErr := adapter.Fetch(ctx, session, bank.FetchRequest{AccountsOnly: e.Job.Purpose == "maintain-session", StartDate: settings.StartDate})
	if err = e.saveSession(ctx, updated); err != nil {
		return out, err
	}
	if fetchErr != nil {
		if bank.Classify(fetchErr).Kind == bank.Authentication && !e.Job.BrowserAttempted {
			renewed, re := e.renew(ctx, adapter, updated)
			if err = e.saveSession(ctx, renewed); err != nil {
				return out, err
			}
			if re == nil {
				snapshot, updated, fetchErr = adapter.Fetch(ctx, renewed, bank.FetchRequest{AccountsOnly: e.Job.Purpose == "maintain-session", StartDate: settings.StartDate})
				if err = e.saveSession(ctx, updated); err != nil {
					return out, err
				}
			} else {
				fetchErr = re
			}
			if fetchErr != nil && bank.Classify(fetchErr).Kind == bank.Authentication {
				e.Job.BrowserAttempted = true
				e.Job.BrowserDeadline = time.Now().Add(330 * time.Second)
				e.Job.Status = "browser"
				if err = e.saveJob(ctx); err != nil {
					return out, err
				}
				hold = true
				return e.outcome("authentication-required", 0), nil
			}
		}
		if fetchErr != nil {
			return out, fetchErr
		}
	}
	if err = ValidateSnapshot(snapshot, settings.ExpectedAccounts); err != nil {
		return out, err
	}
	e.Job.SnapshotKey, err = e.Store.Upload(ctx, "snapshots/"+e.Job.Bank, snapshot)
	if err != nil {
		return out, err
	}
	if err = e.saveJob(ctx); err != nil {
		return out, err
	}
	if e.Job.Purpose != "maintain-session" {
		var baseline Baseline
		found, be := e.get(ctx, "BASELINE", &baseline)
		if be != nil {
			return out, be
		}
		if !found || !baseline.Approved {
			if !e.Job.DryRun {
				return out, bank.Fail(bank.Invalid, "baseline-review-required")
			}
		}
		switch registration.Strategy {
		case "reconcile":
			var missing bool
			missing, err = e.reconcile(ctx, snapshot, baseline)
			if err == nil && missing {
				if e.Job.MissingAttempt < 3 {
					delays := []int{60, 300, 900}
					wait := delays[e.Job.MissingAttempt]
					e.Job.MissingAttempt++
					e.Job.Status = "waiting"
					err = e.saveJob(ctx)
					return e.outcome("deferred", wait), err
				}
				return out, bank.Fail(bank.Invalid, "purchase-not-found")
			}
		case "posted":
			err = e.posted(ctx, snapshot, baseline)
		case "balances":
			err = e.balances(ctx, snapshot)
		default:
			err = bank.Fail(bank.Invalid, "import-strategy")
		}
		if err != nil {
			return out, err
		}
	}
	return e.recovered(ctx, registration.Name, health)
}

func (e *Engine) recovered(ctx context.Context, name string, health Health) (Outcome, error) {
	// Successful authentication cannot resolve an outstanding financial review.
	if e.Job.Purpose == "maintain-session" && health.ImportReview {
		return e.complete(ctx)
	}
	var err error
	if health.Notified && !e.Job.DryRun {
		var incident Notification
		if _, err = e.get(ctx, "NOTICE#incident#"+health.Episode, &incident); err != nil {
			return Outcome{}, err
		}
		if incident.Status == "sent" {
			if err = e.notifyOnce(ctx, "recovery#"+health.Episode, name+" is working again.", name+" recovered"); err != nil {
				return Outcome{}, err
			}
		} else if incident.ID != "" {
			incident.Status = "superseded"
			if err = e.put(ctx, "NOTICE#"+incident.ID, incident, e.Job.ID); err != nil {
				return Outcome{}, err
			}
		}
	}
	healthy := Health{}
	if e.Job.DryRun && health.Notified {
		// A dry run is quiet, but must not consume the recovery notice owed
		// to an operator who already received this incident.
		healthy.Notified, healthy.Episode = true, health.Episode
	}
	if err = e.put(ctx, e.healthKey(), healthy, e.Job.ID); err != nil {
		return Outcome{}, err
	}
	return e.complete(ctx)
}
func (e *Engine) complete(ctx context.Context) (Outcome, error) {
	e.Job.Status = "complete"
	e.Job.CompletedAt = time.Now()
	return e.outcome("complete", 0), e.saveJob(ctx)
}
func (e *Engine) failure(ctx context.Context, cause error) (Outcome, error) {
	f := bank.Classify(cause)
	var health Health
	if _, err := e.get(ctx, e.healthKey(), &health); err != nil {
		return e.outcome("failed", 0), err
	}
	health.Failures++
	if e.Job.Purpose == "retrieve" && (f.Kind == bank.Invalid || f.Kind == bank.Uncertain) {
		health.ImportReview = true
	}
	health.Kind = f.Kind
	if health.Episode == "" {
		health.Episode = e.Job.ID
	}
	e.Job.Error = f.Kind
	e.Job.Operation = f.Operation
	e.Job.Attempt++
	if f.Kind == bank.Credentials || f.Kind == bank.Challenge {
		health.Blocked = true
	}
	retryable := f.Kind == bank.Temporary || f.Kind == bank.Throttled || f.Kind == bank.Maintenance
	wait := 30 * int(math.Pow(2, float64(min(health.Failures-1, 7))))
	if !f.RetryAt.IsZero() {
		wait = max(wait, int(math.Ceil(time.Until(f.RetryAt).Seconds())))
	}
	// Retry-After is authoritative, including delays beyond the usual backoff cap.
	health.RetryAt = time.Now().Add(time.Duration(wait) * time.Second)
	e.Job.Status = "failed"
	outcome := "failed"
	if retryable && e.Job.Attempt <= 2 {
		e.Job.Status = "waiting"
		outcome = "deferred"
	}
	if f.Kind == bank.Invalid || f.Kind == bank.Uncertain || health.Blocked {
		e.Job.Status = "review-required"
		outcome = "review-required"
	}
	shouldNotify := outcome != "deferred" && !health.Notified && !e.Job.DryRun
	if f.Kind == bank.Maintenance && e.Job.Source == "scheduled" {
		health.MaintenanceCount++
		shouldNotify = health.MaintenanceCount >= 2 && !health.Notified && !e.Job.DryRun
		outcome = "complete"
		e.Job.Status = "complete"
	}
	if shouldNotify {
		registration, _ := banks.Find(e.Job.Bank)
		name := registration.Name
		if name == "" {
			name = e.Job.Bank
		}
		message := fmt.Sprintf("%s: %s during %s. Review the linked job.", name, f.Kind, f.Operation)
		switch f.Kind {
		case bank.Credentials:
			message = name + " rejected the credentials. Automatic login is paused. Update the credentials, then resume this bank. [credentials-rejected]"
		case bank.Challenge:
			message = name + " requires an unsupported verification step. Automatic login is paused. Resolve the challenge, then resume this bank. [challenge-required]"
		case bank.Maintenance:
			message = name + " is undergoing maintenance. Retrieval is deferred; repeated logins will not be attempted. [bank-maintenance]"
		case bank.Uncertain:
			message = "YNAB may have accepted a write from " + name + ", but confirmation was lost. Review the identified operation before allowing another write. [uncertain-write]"
		}

		if f.Operation == "previous-import-missing" {
			message = "YNAB remembers this import, but its transaction was deleted or is no longer available. Review whether to restore or exclude it before importing again. [previous-import-missing]"
		}
		if err := e.notifyOnce(ctx, "incident#"+health.Episode, message, "Bank requires attention"); err != nil {
			return e.outcome("failed", 0), err
		}
		health.Notified = true
	}
	if err := e.put(ctx, e.healthKey(), health, e.Job.ID); err != nil {
		return e.outcome("failed", 0), err
	}
	if err := e.saveJob(ctx); err != nil {
		return e.outcome("failed", 0), err
	}
	return e.outcome(outcome, wait), nil
}
func (e *Engine) RecoverBrowser(ctx context.Context, r Request) (Outcome, error) {
	if err := e.loadJob(ctx, r.JobID, r.Bank); err != nil {
		return Outcome{}, err
	}
	if r.Execution == "" || e.Job.Execution != r.Execution || e.Job.Status != "browser" {
		return Outcome{}, bank.Fail(bank.Invalid, "stale-execution")
	}
	e.Lease = e.Job.Lease
	var result BrowserResult
	found, err := e.get(ctx, "BROWSER#"+e.Lease.Generation, &result)
	if err != nil {
		return Outcome{}, err
	}
	if found && result.Generation == e.Lease.Generation {
		return e.Run(ctx, r)
	}
	if err = e.Store.Assert(ctx, e.Lease); err != nil {
		return Outcome{}, err
	}
	defer e.Store.Release(ctx, e.Lease)
	e.Job.Attempt = 2 // A launch/timeout failure is infrastructure failure, not rejected credentials.
	return e.failure(ctx, bank.Fail(bank.Temporary, "browser-task-incomplete"))
}
func decode[T any](raw json.RawMessage) (T, error) {
	var out T
	err := json.Unmarshal(raw, &out)
	return out, err
}

// A token exchange is never blindly replayed after an uncertain transport result.
// An explicit reset is required before discarding uncertain renewal state.
func (e *Engine) renew(ctx context.Context, adapter bank.Adapter, session bank.Session) (bank.Session, error) {
	oldAccess, oldRefresh, oldExpiry := session.Headers["authorization"], session.Headers["refreshtoken"], session.ExpiresAt
	// Fence the exchange before sending it. A process crash after the bank
	// rotates tokens must not leave a seemingly reusable old session behind.
	session.RenewalUncertain = true
	if err := e.saveSession(ctx, session); err != nil {
		return session, err
	}
	renewed, err := adapter.Renew(ctx, session)
	slog.Info("session-renewal", "bank", e.Job.Bank, "success", err == nil,
		"accessRotated", oldAccess != renewed.Headers["authorization"], "refreshRotated", oldRefresh != renewed.Headers["refreshtoken"],
		"expiryExtended", renewed.ExpiresAt.After(oldExpiry))
	if err != nil {
		failure := bank.Classify(err)
		// Keep the safe underlying category and operation when a renewal is
		// held for review; never log exchange bodies or authentication material.
		slog.Warn("session-renewal-failed", "bank", e.Job.Bank, "kind", failure.Kind,
			"operation", failure.Operation, "exchangeUncertain", failure.ExchangeUncertain)
		if failure.ExchangeUncertain || failure.Kind == bank.Invalid {
			renewed.RenewalUncertain = true
			return renewed, bank.Fail(bank.Invalid, "renewal-outcome-unknown")
		}
	}
	renewed.RenewalUncertain = false
	return renewed, err
}

func (e *Engine) healthKey() string {
	if e.Job.Source == "notification" {
		return "HEALTH#notifications"
	}
	return "HEALTH"
}
