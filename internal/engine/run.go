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

func (t *task) run(ctx context.Context, request Request) (out Outcome, err error) {
	started := time.Now()
	defer func() {
		slog.Info("bank-job", "bank", t.Job.Bank, "jobId", t.Job.ID, "purpose", t.Job.Purpose, "outcome", out.Outcome, "browserUsed", t.Job.BrowserAttempted, "durationMs", time.Since(started).Milliseconds())
	}()
	if err = t.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return out, err
	}
	if t.Job.finished() {
		return t.outcome(t.Job.Status, 0), nil
	}
	if t.Job.Status == "browser" {
		t.Lease = t.Job.Lease
		if err = t.Store.Assert(ctx, t.Lease); err != nil {
			return t.outcome("failed", 0), err
		}
	} else {
		var acquired bool
		t.Lease, acquired, err = t.Store.Acquire(ctx, t.Job.Bank, t.Job.ID)
		if err != nil {
			return out, err
		}
		if !acquired {
			return t.outcome("deferred", 5), nil
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
		if release := t.Store.Release(cleanup, t.Lease); release != nil {
			slog.Warn("lease not released", "bank", t.Job.Bank, "job", t.Job.ID, "error", release)
		}
	}()
	// Reload after acquiring the lease: another job may have completed while we waited.
	if err = t.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return out, err
	}
	if t.Job.finished() {
		return t.outcome(t.Job.Status, 0), nil
	}
	t.Job.Lease = t.Lease
	if err = t.saveJob(ctx); err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			cleanup, cancel := cleanupContext(ctx)
			defer cancel()
			out, err = t.failure(cleanup, err)
		}
	}()
	var health Health
	if _, err = t.get(ctx, t.healthKey(), &health); err != nil {
		return out, err
	}
	// A bank whose credentials were rejected waits for bank.resume. A bank
	// backing off skips scheduled runs quietly; the next one after RetryAt tries.
	if health.Blocked || (health.RetryAt.After(time.Now()) && t.Job.Source == "scheduled") {
		t.Job.Status = "paused"
		t.Job.Error = health.Kind
		err = t.saveJob(ctx)
		return t.outcome("paused", 0), err
	}
	if health.RetryAt.After(time.Now()) {
		return t.outcome("deferred", int(math.Ceil(time.Until(health.RetryAt).Seconds()))), nil
	}
	if !t.Job.DryRun && !t.Settings.ImportsEnabled {
		t.Job.Status = "paused"
		err = t.saveJob(ctx)
		return t.outcome("paused", 0), err
	}
	if t.Job.Source == "notification" && t.Job.Purpose == "retrieve" {
		var wait int
		wait, err = t.notificationImport(ctx)
		if err != nil {
			return out, err
		}
		if wait > 0 {
			t.Job.Status = "waiting"
			err = t.saveJob(ctx)
			return t.outcome("deferred", wait), err
		}
		return t.recovered(ctx, "Transaction imports", health)
	}
	// Amount-only purchase alerts are written directly; they never sign in to
	// the bank or call a model. Scheduled cleared imports supply the details.
	if t.Job.Source == "alert" && t.Job.Purpose == "retrieve" {
		if err = t.alertImport(ctx); err != nil {
			return out, err
		}
		return t.recovered(ctx, "Transaction imports", health)
	}
	registration, ok := banks.Find(t.Job.Bank)
	if !ok {
		return out, bank.Fail(bank.Invalid, "unknown-bank")
	}
	settings, ok := t.Settings.Banks[t.Job.Bank]
	if !ok || !settings.Enabled {
		return out, bank.Fail(bank.Invalid, "bank-disabled")
	}
	adapter := registration.API
	if t.Job.Status == "browser" {
		var result BrowserResult
		found, re := t.get(ctx, "BROWSER#"+t.Lease.Generation, &result)
		if re != nil {
			return out, re
		}
		if !found {
			// The browser task failed to start, timed out or lost its callback.
			// That is infrastructure, not a rejected login: end this job.
			t.Job.Attempt = 2
			return out, bank.Fail(bank.Temporary, "browser-task-incomplete")
		}
		if result.Generation != t.Lease.Generation {
			return out, bank.Fail(bank.Invalid, "stale-browser")
		}
		if result.Error != nil {
			if result.Error.Kind == bank.Capacity {
				return t.deferCapacity(ctx)
			}
			// This job already used its one browser attempt. Re-entering the
			// retrieval loop with no session would misclassify the same failure
			// as a rejected fresh login. End it; later jobs respect bank backoff.
			t.Job.Attempt = 2
			return out, result.Error
		}
		t.Job.CapacityWaits = 0
		t.Job.Status = "running"
	}
	session, se := t.session(ctx)
	needsAuth := se != nil && bank.Classify(se).Kind == bank.Authentication
	if se != nil && !needsAuth {
		return out, se
	}
	if !needsAuth {
		if !session.MaximumExpiresAt.IsZero() && !session.MaximumExpiresAt.After(time.Now()) {
			needsAuth = true
		} else if !session.RenewAt.IsZero() && !session.RenewAt.After(time.Now()) {
			renewed, re := t.renew(ctx, adapter, session)
			persist := t.saveSession(ctx, renewed)
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
		if t.Job.BrowserAttempted {
			return out, bank.Fail(bank.Challenge, "fresh-login-not-usable")
		}
		t.Job.BrowserAttempted = true
		t.Job.BrowserDeadline = time.Now().Add(330 * time.Second)
		t.Job.Status = "browser"
		if err = t.saveJob(ctx); err != nil {
			return out, err
		}
		hold = true
		return t.outcome("authentication-required", 0), nil
	}
	snapshot, updated, fetchErr := adapter.Fetch(ctx, session, bank.FetchRequest{StartDate: t.historyStart(settings.StartDate)})
	if err = t.saveSession(ctx, updated); err != nil {
		return out, err
	}
	if fetchErr != nil {
		if bank.Classify(fetchErr).Kind == bank.Authentication && !t.Job.BrowserAttempted {
			renewed, re := t.renew(ctx, adapter, updated)
			if err = t.saveSession(ctx, renewed); err != nil {
				return out, err
			}
			if re == nil {
				snapshot, updated, fetchErr = adapter.Fetch(ctx, renewed, bank.FetchRequest{StartDate: t.historyStart(settings.StartDate)})
				if err = t.saveSession(ctx, updated); err != nil {
					return out, err
				}
			} else {
				fetchErr = re
			}
			if fetchErr != nil && bank.Classify(fetchErr).Kind == bank.Authentication {
				t.Job.BrowserAttempted = true
				t.Job.BrowserDeadline = time.Now().Add(330 * time.Second)
				t.Job.Status = "browser"
				if err = t.saveJob(ctx); err != nil {
					return out, err
				}
				hold = true
				return t.outcome("authentication-required", 0), nil
			}
		}
		if fetchErr != nil {
			return out, fetchErr
		}
	}
	if err = snapshot.Validate(settings.ExpectedAccounts); err != nil {
		return out, err
	}
	switch registration.Strategy {
	case "posted":
		err = t.posted(ctx, snapshot)
	case "balances":
		err = t.balances(ctx, snapshot)
	default:
		err = bank.Fail(bank.Invalid, "import-strategy")
	}
	if err != nil {
		return out, err
	}
	return t.recovered(ctx, registration.Name, health)
}

func (t *task) recovered(ctx context.Context, name string, health Health) (Outcome, error) {
	healthy := Health{}
	if t.Job.DryRun {
		// A dry run is quiet and leaves the recovery notice to a real run.
		healthy.Failing = health.Failing
	} else if health.Failing {
		t.send(ctx, name+" recovered", name+" is working again.", false)
	}
	if health != healthy {
		if err := t.put(ctx, t.healthKey(), healthy, t.Job.ID); err != nil {
			return Outcome{}, err
		}
	}
	return t.complete(ctx)
}

func (t *task) complete(ctx context.Context) (Outcome, error) {
	t.Job.Status = "complete"
	t.Job.CompletedAt = time.Now()
	return t.outcome("complete", 0), t.saveJob(ctx)
}

// failure retries a temporary failure once within the job, then records the
// failed job against the bank's health. The operator is told when the bank
// starts failing or fails differently. Rejected credentials block the bank
// until bank.resume; a challenge retries a day later and is reported each time.
func (t *task) failure(ctx context.Context, cause error) (Outcome, error) {
	f := bank.Classify(cause)
	t.Job.Error = f.Kind
	t.Job.Operation = f.Operation
	t.Job.Attempt++
	retryable := f.Kind == bank.Temporary || f.Kind == bank.Throttled || f.Kind == bank.Maintenance
	if retryable && t.Job.Attempt <= 2 {
		wait := 60
		if !f.RetryAt.IsZero() {
			wait = max(wait, int(math.Ceil(time.Until(f.RetryAt).Seconds())))
		}
		t.Job.Status = "waiting"
		return t.outcome("deferred", wait), t.saveJob(ctx)
	}
	var health Health
	if _, err := t.get(ctx, t.healthKey(), &health); err != nil {
		return t.outcome("failed", 0), err
	}
	t.Job.Status = "failed"
	outcome := "failed"
	notify := !health.Failing || health.Kind != f.Kind || f.Kind == bank.Challenge
	switch f.Kind {
	case bank.Credentials:
		health.Blocked = true
	case bank.Challenge:
		health.RetryAt = time.Now().Add(24 * time.Hour)
	case bank.Maintenance:
		// Scheduled runs report maintenance only when it outlasts two of them.
		if t.Job.Source == "scheduled" {
			health.Maintenance++
			notify = !health.Failing && health.Maintenance >= 2
			t.Job.Status, outcome = "complete", "complete"
		}
	case bank.Capacity, bank.Authentication, bank.Throttled, bank.Temporary, bank.Invalid:
	}
	// A bank's own Retry-After or maintenance window is authoritative.
	if f.RetryAt.After(health.RetryAt) {
		health.RetryAt = f.RetryAt
	}
	health.Kind = f.Kind
	notify = notify && !t.Job.DryRun
	if notify {
		health.Failing = true
	}
	// Save first: the push is best-effort and must not use up the cleanup time.
	if err := t.put(ctx, t.healthKey(), health, t.Job.ID); err != nil {
		return t.outcome("failed", 0), err
	}
	if err := t.saveJob(ctx); err != nil {
		return t.outcome("failed", 0), err
	}
	if notify {
		registration, _ := banks.Find(t.Job.Bank)
		title, message := failureNotice(cmp.Or(registration.Name, t.Job.Bank), f)
		t.send(ctx, title, message, false)
	}
	return t.outcome(outcome, 0), nil
}

// cleanupContext outlives the job's own deadline, so failure handling and the
// lease release still run after the work was cut off.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

func (t *task) healthKey() string {
	// Alert-driven writes must not hold a bank's scheduled imports for review.
	if t.Job.Source == "notification" || t.Job.Source == "alert" {
		return "HEALTH#notifications"
	}
	return "HEALTH"
}

// deferCapacity waits for the shared browser seat: 120 s at a time, five times,
// then the job fails as temporary and the next schedule tries again.
func (t *task) deferCapacity(ctx context.Context) (Outcome, error) {
	if t.Job.CapacityWaits >= 5 {
		t.Job.Attempt = 2
		return t.failure(ctx, bank.Fail(bank.Temporary, "browser-capacity"))
	}
	t.Job.CapacityWaits++
	t.Job.BrowserAttempted = false
	t.Job.Status = "waiting"
	t.Job.Error = ""
	t.Job.Operation = ""
	return t.outcome("deferred", 120), t.saveJob(ctx)
}

// historyStart is the first day a retrieval fetches: the last 60 days, never
// before the bank's configured start.
func (e *Engine) historyStart(configured string) string {
	return max(configured, e.Settings.Today().AddDate(0, 0, -60).Format("2006-01-02"))
}

// Health is a bank's standing across jobs. Blocked waits for bank.resume.
// Failing means the operator has been told the bank is failing, so a later
// success reports its recovery. RetryAt holds off scheduled runs.
type Health struct {
	Blocked     bool      `json:"blocked"`
	Failing     bool      `json:"failing"`
	RetryAt     time.Time `json:"retryAt"`
	Kind        bank.Kind `json:"kind,omitempty"`
	Maintenance int       `json:"maintenance,omitempty"`
}

type BrowserResult struct {
	Generation string        `json:"generation"`
	Error      *bank.Failure `json:"error,omitempty"`
	SessionKey string        `json:"sessionKey,omitempty"`
}
