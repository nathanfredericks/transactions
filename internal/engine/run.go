package engine

import (
	"cmp"
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks"
	"github.com/nathanfredericks/transactions/internal/config"
)

func (t *task) run(ctx context.Context, request Request) (out Outcome, err error) {
	started := time.Now()
	defer func() {
		slog.Info("bank-job", "bank", t.Job.Bank, "jobId", t.Job.ID, "purpose", t.Job.Purpose, "outcome", out.Outcome, "browserUsed", t.Job.BrowserAttempted, "durationMs", time.Since(started).Milliseconds())
	}()
	if out, ok, err := t.begin(ctx, request); !ok {
		return out, err
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
	if out, ok, err := t.claim(ctx, request); !ok {
		return out, err
	}
	defer func() {
		if err != nil {
			cleanup, cancel := cleanupContext(ctx)
			defer cancel()
			out, err = t.failure(cleanup, err)
		}
	}()
	out, hold, err = t.work(ctx)
	return out, err
}

// begin loads the job and takes the bank lease, or continues the lease the
// browser task held for it. It reports false when there is nothing to run.
func (t *task) begin(ctx context.Context, request Request) (Outcome, bool, error) {
	if err := t.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return Outcome{}, false, err
	}
	if t.Job.finished() {
		return t.outcome(Next(t.Job.Status), 0), false, nil
	}
	if t.Job.Status == StatusBrowser {
		t.Lease = t.Job.Lease
		if err := t.Store.Assert(ctx, t.Lease); err != nil {
			return t.outcome(NextFailed, 0), false, err
		}
		return Outcome{}, true, nil
	}
	var acquired bool
	var err error
	t.Lease, acquired, err = t.Store.Acquire(ctx, t.Job.Bank, t.Job.ID)
	if err != nil {
		return Outcome{}, false, err
	}
	if !acquired {
		return t.outcome(NextDeferred, 5), false, nil
	}
	return Outcome{}, true, nil
}

// claim reloads the job under the lease, since another run may have finished
// it while this one waited, and records the lease on the job.
func (t *task) claim(ctx context.Context, request Request) (Outcome, bool, error) {
	if err := t.loadJob(ctx, request.JobID, request.Bank); err != nil {
		return Outcome{}, false, err
	}
	if t.Job.finished() {
		return t.outcome(Next(t.Job.Status), 0), false, nil
	}
	t.Job.Lease = t.Lease
	if err := t.saveJob(ctx); err != nil {
		return Outcome{}, false, err
	}
	return Outcome{}, true, nil
}

// work runs the claimed job. It reports hold when the job continues in the
// browser task, which then keeps the lease.
func (t *task) work(ctx context.Context) (out Outcome, hold bool, err error) {
	var health Health
	if _, err = t.get(ctx, t.healthKey(), &health); err != nil {
		return out, false, err
	}
	if out, stop, err := t.gate(ctx, health); stop {
		return out, false, err
	}
	if t.Job.Purpose == "retrieve" {
		switch t.Job.Source {
		case "notification":
			wait, err := t.notificationImport(ctx)
			if err != nil {
				return out, false, err
			}
			if wait > 0 {
				t.Job.Status = StatusWaiting
				return t.outcome(NextDeferred, wait), false, t.saveJob(ctx)
			}
			out, err = t.recovered(ctx, "Transaction imports", health)
			return out, false, err
		case "alert":
			// Amount-only purchase alerts are written directly; they never sign in
			// to the bank or call a model. Scheduled cleared imports supply the details.
			if err = t.alertImport(ctx); err != nil {
				return out, false, err
			}
			out, err = t.recovered(ctx, "Transaction imports", health)
			return out, false, err
		}
	}
	registration, ok := banks.Find(t.Job.Bank)
	if !ok {
		return out, false, bank.Fail(bank.Invalid, "unknown-bank")
	}
	settings, ok := t.Settings.Banks[t.Job.Bank]
	if !ok || !settings.Enabled {
		return out, false, bank.Fail(bank.Invalid, "bank-disabled")
	}
	if t.Job.Status == StatusBrowser {
		if out, stop, err := t.browserResult(ctx); stop {
			return out, false, err
		}
	}
	snapshot, needsBrowser, err := t.retrieve(ctx, registration.API, settings)
	if err != nil {
		return out, false, err
	}
	if needsBrowser {
		out, err = t.toBrowser(ctx)
		return out, err == nil, err
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
		return out, false, err
	}
	out, err = t.recovered(ctx, registration.Name, health)
	return out, false, err
}

// gate stops a job its bank's health or the import switch holds back.
func (t *task) gate(ctx context.Context, health Health) (Outcome, bool, error) {
	// A bank whose credentials were rejected waits for bank.resume. A bank
	// backing off skips scheduled runs quietly; the next one after RetryAt tries.
	if health.Blocked || (health.RetryAt.After(time.Now()) && t.Job.Source == "scheduled") {
		t.Job.Status = StatusPaused
		t.Job.Error = health.Kind
		return t.outcome(NextPaused, 0), true, t.saveJob(ctx)
	}
	if health.RetryAt.After(time.Now()) {
		return t.outcome(NextDeferred, int(math.Ceil(time.Until(health.RetryAt).Seconds()))), true, nil
	}
	if !t.Job.DryRun && !t.Settings.ImportsEnabled {
		t.Job.Status = StatusPaused
		return t.outcome(NextPaused, 0), true, t.saveJob(ctx)
	}
	return Outcome{}, false, nil
}

// browserResult reads what the browser task left for this job's lease. It
// reports true when the job stops here.
func (t *task) browserResult(ctx context.Context) (Outcome, bool, error) {
	var result BrowserResult
	found, err := t.get(ctx, "BROWSER#"+t.Lease.Generation, &result)
	if err != nil {
		return Outcome{}, true, err
	}
	if !found {
		// The browser task failed to start, timed out or lost its callback.
		// That is infrastructure, not a rejected login: end this job.
		t.Job.Attempt = 2
		return Outcome{}, true, bank.Fail(bank.Temporary, "browser-task-incomplete")
	}
	if result.Generation != t.Lease.Generation {
		return Outcome{}, true, bank.Fail(bank.Invalid, "stale-browser")
	}
	if result.Error != nil {
		if result.Error.Kind == bank.Capacity {
			out, err := t.deferCapacity(ctx)
			return out, true, err
		}
		// This job already used its one browser attempt. Re-entering the
		// retrieval loop with no session would misclassify the same failure
		// as a rejected fresh login. End it; later jobs respect bank backoff.
		t.Job.Attempt = 2
		return Outcome{}, true, result.Error
	}
	t.Job.CapacityWaits = 0
	t.Job.Status = StatusRunning
	return Outcome{}, false, nil
}

// retrieve fetches and validates the bank's snapshot with the saved session,
// renewing it when due or rejected. It reports true when only a browser
// sign-in can continue.
func (t *task) retrieve(ctx context.Context, api bank.API, settings config.BankSettings) (bank.Snapshot, bool, error) {
	session, err := t.session(ctx)
	needsAuth := err != nil && bank.Classify(err).Kind == bank.Authentication
	if err != nil && !needsAuth {
		return bank.Snapshot{}, false, err
	}
	if !needsAuth {
		if !session.MaximumExpiresAt.IsZero() && !session.MaximumExpiresAt.After(time.Now()) {
			needsAuth = true
		} else if !session.RenewAt.IsZero() && !session.RenewAt.After(time.Now()) {
			renewed, re := t.renew(ctx, api, session)
			if err := t.saveSession(ctx, renewed); err != nil {
				return bank.Snapshot{}, false, err
			}
			session = renewed
			if re != nil {
				if bank.Classify(re).Kind != bank.Authentication {
					return bank.Snapshot{}, false, re
				}
				needsAuth = true
			}
		}
	}
	if needsAuth {
		if t.Job.BrowserAttempted {
			return bank.Snapshot{}, false, bank.Fail(bank.Challenge, "fresh-login-not-usable")
		}
		return bank.Snapshot{}, true, nil
	}
	request := bank.FetchRequest{StartDate: t.historyStart(settings.StartDate)}
	snapshot, updated, fetchErr := api.Fetch(ctx, session, request)
	if err := t.saveSession(ctx, updated); err != nil {
		return bank.Snapshot{}, false, err
	}
	if fetchErr != nil && bank.Classify(fetchErr).Kind == bank.Authentication && !t.Job.BrowserAttempted {
		renewed, re := t.renew(ctx, api, updated)
		if err := t.saveSession(ctx, renewed); err != nil {
			return bank.Snapshot{}, false, err
		}
		fetchErr = re
		if re == nil {
			snapshot, updated, fetchErr = api.Fetch(ctx, renewed, request)
			if err := t.saveSession(ctx, updated); err != nil {
				return bank.Snapshot{}, false, err
			}
		}
		if fetchErr != nil && bank.Classify(fetchErr).Kind == bank.Authentication {
			return bank.Snapshot{}, true, nil
		}
	}
	if fetchErr != nil {
		return bank.Snapshot{}, false, fetchErr
	}
	return snapshot, false, snapshot.Validate(settings.ExpectedAccounts)
}

// toBrowser hands the job to the browser task for one sign-in attempt.
func (t *task) toBrowser(ctx context.Context) (Outcome, error) {
	t.Job.BrowserAttempted = true
	t.Job.BrowserDeadline = time.Now().Add(330 * time.Second)
	t.Job.Status = StatusBrowser
	if err := t.saveJob(ctx); err != nil {
		return Outcome{}, err
	}
	return t.outcome(NextBrowser, 0), nil
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
	t.Job.Status = StatusComplete
	t.Job.CompletedAt = time.Now()
	return t.outcome(NextComplete, 0), t.saveJob(ctx)
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
		t.Job.Status = StatusWaiting
		return t.outcome(NextDeferred, wait), t.saveJob(ctx)
	}
	var health Health
	if _, err := t.get(ctx, t.healthKey(), &health); err != nil {
		return t.outcome(NextFailed, 0), err
	}
	t.Job.Status = StatusFailed
	outcome := NextFailed
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
			t.Job.Status, outcome = StatusComplete, NextComplete
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
		return t.outcome(NextFailed, 0), err
	}
	if err := t.saveJob(ctx); err != nil {
		return t.outcome(NextFailed, 0), err
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
	t.Job.Status = StatusWaiting
	t.Job.Error = ""
	t.Job.Operation = ""
	return t.outcome(NextDeferred, 120), t.saveJob(ctx)
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
