package engine

import (
	"cmp"
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
)

func (e *Engine) Run(ctx context.Context, request Request) (out Outcome, err error) {
	started := time.Now()
	defer func() {
		slog.Info("bank-job", "bank", e.Job.Bank, "jobId", e.Job.ID, "purpose", e.Job.Purpose, "outcome", out.Outcome, "browserUsed", e.Job.BrowserAttempted, "durationMs", time.Since(started).Milliseconds())
	}()
	if err = e.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return out, err
	}
	if e.Job.finished() {
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
		if hold {
			return
		}
		// An unreleased lease expires on its own, so a failed release is only logged.
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		if release := e.Store.Release(cleanup, e.Lease); release != nil {
			slog.Warn("lease not released", "bank", e.Job.Bank, "job", e.Job.ID, "error", release)
		}
	}()
	// Reload after acquiring the lease: another job may have completed while we waited.
	if err = e.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return out, err
	}
	if e.Job.finished() {
		return e.outcome(e.Job.Status, 0), nil
	}
	e.Job.Lease = e.Lease
	if err = e.saveJob(ctx); err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			cleanup, cancel := cleanupContext(ctx)
			defer cancel()
			out, err = e.failure(cleanup, err)
		}
	}()
	var health Health
	if _, err = e.get(ctx, e.healthKey(), &health); err != nil {
		return out, err
	}
	// A bank whose credentials were rejected waits for bank.resume. A bank
	// backing off skips scheduled runs quietly; the next one after RetryAt tries.
	if health.Blocked || (health.RetryAt.After(time.Now()) && e.Job.Source == "scheduled") {
		e.Job.Status = "paused"
		e.Job.Error = health.Kind
		err = e.saveJob(ctx)
		return e.outcome("paused", 0), err
	}
	if health.RetryAt.After(time.Now()) {
		return e.outcome("deferred", int(math.Ceil(time.Until(health.RetryAt).Seconds()))), nil
	}
	if !e.Job.DryRun && !e.Settings.ImportsEnabled {
		e.Job.Status = "paused"
		err = e.saveJob(ctx)
		return e.outcome("paused", 0), err
	}
	if e.Job.Source == "notification" && e.Job.Purpose == "retrieve" {
		var wait int
		wait, err = e.notificationImport(ctx)
		if err != nil {
			return out, err
		}
		if wait > 0 {
			e.Job.Status = "waiting"
			err = e.saveJob(ctx)
			return e.outcome("deferred", wait), err
		}
		return e.recovered(ctx, "Transaction imports", health)
	}
	// Amount-only purchase alerts are written directly; they never sign in to
	// the bank or call a model. Scheduled cleared imports supply the details.
	if e.Job.Source == "alert" && e.Job.Purpose == "retrieve" {
		if err = e.alertImport(ctx); err != nil {
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
			// The browser task failed to start, timed out or lost its callback.
			// That is infrastructure, not a rejected login: end this job.
			e.Job.Attempt = 2
			return out, bank.Fail(bank.Temporary, "browser-task-incomplete")
		}
		if result.Generation != e.Lease.Generation {
			return out, bank.Fail(bank.Invalid, "stale-browser")
		}
		if result.Error != nil {
			if result.Error.Kind == bank.Capacity {
				return e.deferCapacity(ctx)
			}
			// This job already used its one browser attempt. Re-entering the
			// retrieval loop with no session would misclassify the same failure
			// as a rejected fresh login. End it; later jobs respect bank backoff.
			e.Job.Attempt = 2
			return out, result.Error
		}
		e.Job.CapacityWaits = 0
		e.Job.Status = "running"
	}
	session, se := e.session(ctx)
	needsAuth := se != nil && bank.Classify(se).Kind == bank.Authentication
	if se != nil && !needsAuth {
		return out, se
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
	snapshot, updated, fetchErr := adapter.Fetch(ctx, session, bank.FetchRequest{StartDate: settings.StartDate})
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
				snapshot, updated, fetchErr = adapter.Fetch(ctx, renewed, bank.FetchRequest{StartDate: settings.StartDate})
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
	switch registration.Strategy {
	case "posted":
		err = e.posted(ctx, snapshot)
	case "balances":
		err = e.balances(ctx, snapshot)
	default:
		err = bank.Fail(bank.Invalid, "import-strategy")
	}
	if err != nil {
		return out, err
	}
	return e.recovered(ctx, registration.Name, health)
}

func (e *Engine) recovered(ctx context.Context, name string, health Health) (Outcome, error) {
	healthy := Health{}
	if e.Job.DryRun {
		// A dry run is quiet and leaves the recovery notice to a real run.
		healthy.Failing = health.Failing
	} else if health.Failing {
		e.send(ctx, name+" recovered", name+" is working again.", false)
	}
	if health != healthy {
		if err := e.put(ctx, e.healthKey(), healthy, e.Job.ID); err != nil {
			return Outcome{}, err
		}
	}
	return e.complete(ctx)
}

func (e *Engine) complete(ctx context.Context) (Outcome, error) {
	e.Job.Status = "complete"
	e.Job.CompletedAt = time.Now()
	return e.outcome("complete", 0), e.saveJob(ctx)
}

// failure retries a temporary failure once within the job, then records the
// failed job against the bank's health. The operator is told when the bank
// starts failing or fails differently. Rejected credentials block the bank
// until bank.resume; a challenge retries a day later and is reported each time.
func (e *Engine) failure(ctx context.Context, cause error) (Outcome, error) {
	f := bank.Classify(cause)
	e.Job.Error = f.Kind
	e.Job.Operation = f.Operation
	e.Job.Attempt++
	retryable := f.Kind == bank.Temporary || f.Kind == bank.Throttled || f.Kind == bank.Maintenance
	if retryable && e.Job.Attempt <= 2 {
		wait := 60
		if !f.RetryAt.IsZero() {
			wait = max(wait, int(math.Ceil(time.Until(f.RetryAt).Seconds())))
		}
		e.Job.Status = "waiting"
		return e.outcome("deferred", wait), e.saveJob(ctx)
	}
	var health Health
	if _, err := e.get(ctx, e.healthKey(), &health); err != nil {
		return e.outcome("failed", 0), err
	}
	e.Job.Status = "failed"
	outcome := "failed"
	notify := !health.Failing || health.Kind != f.Kind || f.Kind == bank.Challenge
	switch f.Kind {
	case bank.Credentials:
		health.Blocked = true
	case bank.Challenge:
		health.RetryAt = time.Now().Add(24 * time.Hour)
	case bank.Maintenance:
		// Scheduled runs report maintenance only when it outlasts two of them.
		if e.Job.Source == "scheduled" {
			health.Maintenance++
			notify = !health.Failing && health.Maintenance >= 2
			e.Job.Status, outcome = "complete", "complete"
		}
	case bank.Capacity, bank.Authentication, bank.Throttled, bank.Temporary, bank.Invalid:
	}
	// A bank's own Retry-After or maintenance window is authoritative.
	if f.RetryAt.After(health.RetryAt) {
		health.RetryAt = f.RetryAt
	}
	health.Kind = f.Kind
	notify = notify && !e.Job.DryRun
	if notify {
		health.Failing = true
	}
	// Save first: the push is best-effort and must not use up the cleanup time.
	if err := e.put(ctx, e.healthKey(), health, e.Job.ID); err != nil {
		return e.outcome("failed", 0), err
	}
	if err := e.saveJob(ctx); err != nil {
		return e.outcome("failed", 0), err
	}
	if notify {
		registration, _ := banks.Find(e.Job.Bank)
		title, message := failureNotice(cmp.Or(registration.Name, e.Job.Bank), f)
		e.send(ctx, title, message, false)
	}
	return e.outcome(outcome, 0), nil
}

// cleanupContext outlives the job's own deadline, so failure handling and the
// lease release still run after the work was cut off.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

// renew exchanges the session's tokens. A renewal whose outcome is unknown
// counts as expired authentication, so the job signs in through the browser.
func (e *Engine) renew(ctx context.Context, adapter bank.Adapter, session bank.Session) (bank.Session, error) {
	oldAccess, oldRefresh, oldExpiry := session.Headers["authorization"], session.Headers["refreshtoken"], session.ExpiresAt
	renewed, err := adapter.Renew(ctx, session)
	slog.Info("session-renewal", "bank", e.Job.Bank, "success", err == nil,
		"accessRotated", oldAccess != renewed.Headers["authorization"], "refreshRotated", oldRefresh != renewed.Headers["refreshtoken"],
		"expiryExtended", renewed.ExpiresAt.After(oldExpiry))
	if err != nil {
		failure := bank.Classify(err)
		// Never log exchange bodies or authentication material.
		slog.Warn("session-renewal-failed", "bank", e.Job.Bank, "kind", failure.Kind,
			"operation", failure.Operation, "exchangeUncertain", failure.ExchangeUncertain)
		if failure.ExchangeUncertain || failure.Kind == bank.Invalid {
			return renewed, bank.Fail(bank.Authentication, "renewal-outcome-unknown")
		}
	}
	return renewed, err
}

func (e *Engine) healthKey() string {
	// Alert-driven writes must not hold a bank's scheduled imports for review.
	if e.Job.Source == "notification" || e.Job.Source == "alert" {
		return "HEALTH#notifications"
	}
	return "HEALTH"
}

// deferCapacity waits for the shared browser seat: 120 s at a time, five times,
// then the job fails as temporary and the next schedule tries again.
func (e *Engine) deferCapacity(ctx context.Context) (Outcome, error) {
	if e.Job.CapacityWaits >= 5 {
		e.Job.Attempt = 2
		return e.failure(ctx, bank.Fail(bank.Temporary, "browser-capacity"))
	}
	e.Job.CapacityWaits++
	e.Job.BrowserAttempted = false
	e.Job.Status = "waiting"
	e.Job.Error = ""
	e.Job.Operation = ""
	return e.outcome("deferred", 120), e.saveJob(ctx)
}
