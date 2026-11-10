package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/ynab"
)

// learningPK holds the importer's suggestion records by YNAB transaction ID.
const learningPK = "LEARN"

func (e *Engine) today() string { return e.Settings.Today().Format("2006-01-02") }

// outcomes counts, among changed transactions the user has approved, those the
// importer suggested and how many suggested fields the user kept.
func outcomes(m *learn.Mirror, ids []string) (approved int, confirmed map[string]int) {
	confirmed = map[string]int{}
	for _, id := range ids {
		t, s := m.Transactions[id], m.Suggestions[id]
		if t == nil || s == nil || !t.Approved || t.Deleted {
			continue
		}
		approved++
		if s.PayeeID != "" && deref(t.PayeeID) == s.PayeeID {
			confirmed["payee"]++
		}
		if s.CategoryID != "" && deref(t.CategoryID) == s.CategoryID {
			confirmed["category"]++
		}
		if s.Memo != "" && deref(t.Memo) == s.Memo {
			confirmed["memo"]++
		}
	}
	return approved, confirmed
}

// learning is one job's view of the budget, read live from YNAB, and the
// predictor built from it.
type learning struct {
	mirror *learn.Mirror
	model  *learn.Model
}

func (e *Engine) excludedPayees() []string {
	if e.Settings.AdjustmentPayeeID == "" {
		return nil
	}
	return []string{e.Settings.AdjustmentPayeeID}
}

func buildLearning(m *learn.Mirror, excluded []string) *learning {
	var sched []*learn.Scheduled
	for _, s := range m.Scheduled {
		sched = append(sched, s)
	}
	model := learn.Build(m.Examples(excluded...), m.Payees, m.Categories, sched, m.Pending(), learn.DefaultOptions())
	model.WithCategorySeen(m.CategorySeen)
	return &learning{mirror: m, model: model}
}

// live is the budget as last read from YNAB by this process. Nothing is stored:
// a cold start reads the history in full, and a warm one asks YNAB only for
// what changed since its last read. A Lambda runs one invocation at a time, so
// the lock only guards against misuse.
var live struct {
	sync.Mutex
	budget string
	mirror *learn.Mirror
}

// loadLearning reads the budget from YNAB, live, and the importer's own
// suggestion records, and builds the learned model from them.
func loadLearning(ctx context.Context, e *Engine) (*learning, error) {
	live.Lock()
	defer live.Unlock()
	if live.mirror == nil || live.budget != e.Settings.BudgetID {
		live.mirror, live.budget = learn.NewMirror(), e.Settings.BudgetID
	}
	changed, err := e.syncMirror(ctx, live.mirror, e.today())
	if err != nil {
		live.mirror = nil
		return nil, err
	}
	// The importer's own records keep the bank descriptor YNAB may not.
	rows, err := e.Store.List(ctx, learningPK, "TX#")
	if err != nil {
		slog.Warn("suggestion records unavailable", "error", err)
	}
	for _, raw := range rows {
		var s learn.Suggestion
		if json.Unmarshal(raw, &s) == nil && s.TransactionID != "" {
			live.mirror.Suggestions[s.TransactionID] = &s
		}
	}
	approved, confirmed := outcomes(live.mirror, changed)
	slog.Info("ynab-learning", "job", e.Job.ID, "changed", len(changed), "transactions", len(live.mirror.Transactions), "approvedWithSuggestion", approved, "confirmedFields", confirmed)
	return buildLearning(live.mirror, e.excludedPayees()), nil
}

// Decision is what an import writes and the record of why.
type Decision struct {
	Enrichment ynab.Fields
	Suggestion learn.Suggestion
}

// Source is the payee's source, for logs and the write ledger.
func (d Decision) Source() string { return d.Suggestion.PayeeSource }

// enrich decides payee, category and memo for one bank transaction. The user's
// approved history decides when it agrees; otherwise the model chooses among
// lookalike payees or names a new one. Enrichment never blocks a valid import:
// if evidence or the model is unavailable, the descriptor is kept and the
// unapproved transaction waits for review in YNAB.
func (e *Engine) enrich(ctx context.Context, cache **learning, descriptor string, amount int64, date, account string) Decision {
	d := Decision{Suggestion: learn.Suggestion{Descriptor: descriptor, AccountID: account, Date: date, Amount: amount, Bank: e.Job.Bank, JobID: e.Job.ID, Version: cmp.Or(e.Env.Commit, "dev"), At: time.Now().UTC()}}
	fallback := func(reason string, err error) Decision {
		slog.Warn("enrichment unavailable", "job", e.Job.ID, "reason", reason, "error", err)
		d.Enrichment = ynab.Fields{Payee: ynab.PayeeChoice{Name: descriptor}}
		d.Suggestion.PayeeName, d.Suggestion.PayeeSource = descriptor, "fallback"
		return d
	}
	if *cache == nil {
		l, err := loadLearning(ctx, e)
		if err != nil {
			return fallback("evidence", err)
		}
		*cache = l
	}
	l := *cache
	q := learn.Query{Descriptor: descriptor, Amount: amount, Date: date, AccountID: account}
	var payee ynab.PayeeChoice
	modelCategory := ""
	if r, ok := l.model.Payee(descriptor, date); ok {
		payee = ynab.PayeeChoice{ID: r.ID, Name: r.Name}
		d.Suggestion.PayeeSource = r.Source
	} else {
		candidates := l.model.Candidates(descriptor, 20)
		out, model, err := e.chooseWithModel(ctx, l.model.Input(q, "", candidates))
		d.Suggestion.Model = model
		if err != nil {
			return fallback("model", err)
		}
		if out.PayeeID == "none" {
			payee = ynab.PayeeChoice{Name: out.NewPayeeName}
			d.Suggestion.PayeeSource = "model-new"
		} else {
			for _, c := range candidates {
				if c.ID == out.PayeeID {
					payee = ynab.PayeeChoice{ID: c.ID, Name: c.Name}
				}
			}
			if payee.ID == "" {
				return fallback("model", errors.New("model chose a payee outside the candidates"))
			}
			d.Suggestion.PayeeSource = "model"
		}
		modelCategory = out.CategoryID
	}
	d.Enrichment.Payee = payee
	d.Suggestion.PayeeID, d.Suggestion.PayeeName = payee.ID, payee.Name
	if payee.ID != "" && l.model.Known(payee.ID) {
		f := l.model.Complete(payee.ID, q)
		d.Enrichment.CategoryID, d.Enrichment.Memo = f.CategoryID, f.Memo
		d.Suggestion.CategorySource, d.Suggestion.MemoSource = f.CategorySource, f.MemoSource
	} else if modelCategory != "" {
		d.Enrichment.CategoryID = modelCategory
		d.Suggestion.CategorySource = "model"
	}
	d.Suggestion.CategoryID, d.Suggestion.Memo = d.Enrichment.CategoryID, d.Enrichment.Memo
	return d
}

// recordSuggestion stores the suggestion for a written transaction, so the
// mirror can join it to the user's final approved state. It carries no
// financial authority and never fails the import.
func (e *Engine) recordSuggestion(ctx context.Context, d Decision, tx *ynab.Transaction, importID string) {
	if tx == nil || tx.ID == "" || e.Job.DryRun {
		return
	}
	s := d.Suggestion
	s.TransactionID, s.ImportID = tx.ID, importID
	if err := e.Store.PutPlain(ctx, learningPK, "TX#"+tx.ID, s); err != nil {
		slog.Warn("suggestion record not saved", "job", e.Job.ID, "transaction", tx.ID, "error", err)
	}
}
