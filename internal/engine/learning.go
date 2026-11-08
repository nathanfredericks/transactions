package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/service"
	"github.com/nathanfredericks/transactions/internal/types"
)

// mirrorKey is the S3 object holding the YNAB mirror.
const mirrorKey = "ynab/mirror.json.gz"

// learningPK holds the importer's suggestion records by YNAB transaction ID.
const learningPK = "LEARN"

func (e *Engine) today() string {
	zone, err := time.LoadLocation(e.Settings.Timezone)
	if err != nil {
		zone = time.UTC
	}
	return time.Now().In(zone).Format("2006-01-02")
}

func (e *Engine) loadMirror(ctx context.Context) (*learn.Mirror, string, error) {
	body, etag, found, err := e.Store.GetObject(ctx, mirrorKey)
	if err != nil || !found {
		return learn.NewMirror(), "", err
	}
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("reading mirror: %w", err)
	}
	m := learn.NewMirror()
	if err := json.NewDecoder(io.LimitReader(r, 256*1024*1024)).Decode(m); err != nil {
		return nil, "", fmt.Errorf("decoding mirror: %w", err)
	}
	m.Fill()
	return m, etag, nil
}

func (e *Engine) saveMirror(ctx context.Context, m *learn.Mirror, etag string) error {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if err := json.NewEncoder(w).Encode(m); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return e.Store.PutObjectIf(ctx, mirrorKey, buf.Bytes(), "application/gzip", etag)
}

// attachSuggestions joins the importer's suggestion records to changed
// transactions that do not have one yet.
func (e *Engine) attachSuggestions(ctx context.Context, m *learn.Mirror, ids []string) error {
	var keys []string
	for _, id := range ids {
		if m.Suggestions[id] == nil {
			keys = append(keys, "TX#"+id)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := e.Store.GetMany(ctx, learningPK, keys)
	if err != nil {
		return err
	}
	for k, raw := range rows {
		var s learn.Suggestion
		if json.Unmarshal(raw, &s) == nil {
			m.Suggestions[k[len("TX#"):]] = &s
		}
	}
	return nil
}

// SyncYNAB keeps the stored mirror current. It runs on a schedule, so each
// import only needs a small delta on top of it.
func (e *Engine) SyncYNAB(ctx context.Context) (any, error) {
	m, etag, err := e.loadMirror(ctx)
	if err != nil {
		return nil, err
	}
	first := len(m.Transactions) == 0
	changed, err := syncMirror(ctx, m, e.today())
	if err != nil {
		return nil, err
	}
	if err := e.attachSuggestions(ctx, m, changed); err != nil {
		return nil, err
	}
	m.Prune(time.Now().AddDate(-service.MirrorYears, 0, -60).Format("2006-01-02"))
	m.SyncedAt = time.Now().UTC()
	if len(changed) == 0 && !first {
		// Nothing new: keep the stored object rather than writing a new version.
		return map[string]any{"changed": 0}, nil
	}
	if err := e.saveMirror(ctx, m, etag); err != nil {
		return nil, fmt.Errorf("saving mirror: %w", err)
	}
	approved, confirmed := outcomes(m, changed)
	slog.Info("ynab-sync", "changed", len(changed), "transactions", len(m.Transactions), "approvedWithSuggestion", approved, "confirmedFields", confirmed)
	return map[string]any{"changed": len(changed), "transactions": len(m.Transactions)}, nil
}

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

// learning is one job's view of the budget: the stored mirror brought up to
// date in memory, and the predictor built from it.
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

// Seams for tests; production loads the mirror and calls the configured model.
var (
	loadLearning = func(ctx context.Context, e *Engine) (*learning, error) {
		m, _, err := e.loadMirror(ctx)
		if err != nil {
			return nil, err
		}
		// Apply a fresh delta in memory so this import sees every approval made
		// since the last scheduled sync. It is not saved.
		changed, err := syncMirror(ctx, m, e.today())
		if err != nil {
			return nil, err
		}
		if err := e.attachSuggestions(ctx, m, changed); err != nil {
			slog.Warn("suggestion records unavailable", "error", err)
		}
		return buildLearning(m, e.excludedPayees()), nil
	}
	chooseWithModel = service.ChooseWithModel
	syncMirror      = service.SyncMirror
)

// Decision is what an import writes and the record of why.
type Decision struct {
	Enrichment types.Enrichment
	Suggestion learn.Suggestion
}

// Source is the payee's source, for logs and the write ledger.
func (d Decision) Source() string { return d.Suggestion.PayeeSource }

func version() string {
	if v := os.Getenv("DEPLOYMENT_COMMIT"); v != "" {
		return v
	}
	return "dev"
}

// enrich decides payee, category and memo for one bank transaction. The user's
// approved history decides when it agrees; otherwise the model chooses among
// lookalike payees or names a new one. Enrichment never blocks a valid import:
// if evidence or the model is unavailable, the descriptor is kept and the
// unapproved transaction waits for review in YNAB.
func (e *Engine) enrich(ctx context.Context, cache **learning, descriptor string, amount int64, date, account string) Decision {
	d := Decision{Suggestion: learn.Suggestion{Descriptor: descriptor, AccountID: account, Date: date, Amount: amount, Bank: e.Job.Bank, JobID: e.Job.ID, Version: version(), At: time.Now().UTC()}}
	fallback := func(reason string, err error) Decision {
		slog.Warn("enrichment unavailable", "job", e.Job.ID, "reason", reason, "error", err)
		d.Enrichment = types.Enrichment{Payee: types.PayeeChoice{Name: descriptor}}
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
	var payee types.PayeeChoice
	modelCategory := ""
	if r, ok := l.model.Payee(descriptor, date); ok {
		payee = types.PayeeChoice{ID: r.ID, Name: r.Name}
		d.Suggestion.PayeeSource = r.Source
	} else {
		candidates := l.model.Candidates(descriptor, 20)
		out, model, err := chooseWithModel(ctx, l.model.Input(q, "", candidates))
		d.Suggestion.Model = model
		if err != nil {
			return fallback("model", err)
		}
		if out.PayeeID == "none" {
			payee = types.PayeeChoice{Name: out.NewPayeeName}
			d.Suggestion.PayeeSource = "model-new"
		} else {
			for _, c := range candidates {
				if c.ID == out.PayeeID {
					payee = types.PayeeChoice{ID: c.ID, Name: c.Name}
				}
			}
			if payee.ID == "" {
				return fallback("model", fmt.Errorf("model chose a payee outside the candidates"))
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

// ruled records that a rule, not learning, filled the transaction.
func (e *Engine) ruled(descriptor string, amount int64, date, account string, rule *types.TransactionOverride) Decision {
	return Decision{Suggestion: learn.Suggestion{Descriptor: descriptor, AccountID: account, Date: date, Amount: amount, Bank: e.Job.Bank, JobID: e.Job.ID, Version: version(), At: time.Now().UTC(), PayeeID: rule.Payee, PayeeSource: "rule", CategoryID: rule.Category, CategorySource: "rule", Memo: rule.Memo, MemoSource: "rule"}}
}

// recordSuggestion stores the suggestion for a written transaction, so the
// mirror can join it to the user's final approved state. It carries no
// financial authority and never fails the import.
func (e *Engine) recordSuggestion(ctx context.Context, d Decision, tx *types.YNABTransaction, importID string) {
	if tx == nil || tx.ID == "" || e.Job.DryRun {
		return
	}
	s := d.Suggestion
	s.TransactionID, s.ImportID = tx.ID, importID
	if err := e.Store.PutPlain(ctx, learningPK, "TX#"+tx.ID, s); err != nil {
		slog.Warn("suggestion record not saved", "job", e.Job.ID, "transaction", tx.ID, "error", err)
	}
}
