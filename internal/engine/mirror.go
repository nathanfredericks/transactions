package engine

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"strconv"
	"time"

	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/llm"
	"github.com/nathanfredericks/transactions/internal/ynab"
)

// MirrorYears is how much approved history the mirror keeps for learning.
const MirrorYears = 2

// syncMirror brings a mirror up to date with YNAB using delta requests: only
// what changed since each saved server knowledge is fetched. An empty mirror is
// filled with MirrorYears of transactions. It returns the IDs of transactions
// that changed.
func (e *Engine) syncMirror(ctx context.Context, m *learn.Mirror, today string) ([]string, error) {
	m.Fill()
	delta := func(kind, path string, extra url.Values, out any) error {
		v := url.Values{}
		maps.Copy(v, extra)
		if k := m.Knowledge[kind]; k > 0 {
			v.Set("last_knowledge_of_server", strconv.FormatInt(k, 10))
		}
		if err := e.YNAB.Get(ctx, path+"?"+v.Encode(), out); err != nil {
			return fmt.Errorf("fetching %s: %w", kind, err)
		}
		return nil
	}
	first := m.Knowledge["transactions"] == 0

	var tx struct {
		Data struct {
			Transactions    []*ynab.Transaction `json:"transactions"`
			ServerKnowledge int64               `json:"server_knowledge"`
		} `json:"data"`
	}
	extra := url.Values{}
	if first {
		extra.Set("since_date", time.Now().AddDate(-MirrorYears, 0, 0).Format("2006-01-02"))
	}
	if err := delta("transactions", "/transactions", extra, &tx); err != nil {
		return nil, err
	}
	var payees struct {
		Data struct {
			Payees          []*learn.Payee `json:"payees"`
			ServerKnowledge int64          `json:"server_knowledge"`
		} `json:"data"`
	}
	if err := delta("payees", "/payees", nil, &payees); err != nil {
		return nil, err
	}
	var cats struct {
		Data struct {
			Groups []struct {
				Name       string            `json:"name"`
				Categories []*learn.Category `json:"categories"`
			} `json:"category_groups"`
			ServerKnowledge int64 `json:"server_knowledge"`
		} `json:"data"`
	}
	if err := delta("categories", "/categories", nil, &cats); err != nil {
		return nil, err
	}
	var sched struct {
		Data struct {
			Scheduled       []*learn.Scheduled `json:"scheduled_transactions"`
			ServerKnowledge int64              `json:"server_knowledge"`
		} `json:"data"`
	}
	if err := delta("scheduled", "/scheduled_transactions", nil, &sched); err != nil {
		return nil, err
	}

	m.ApplyTransactions(tx.Data.Transactions)
	m.ApplyPayees(payees.Data.Payees)
	// Categories that already existed at the first sync count as long existing.
	seen := today
	if m.Knowledge["categories"] == 0 {
		seen = "2000-01-01"
	}
	for _, g := range cats.Data.Groups {
		for _, c := range g.Categories {
			c.GroupName = g.Name
		}
		m.ApplyCategories(g.Categories, seen)
	}
	m.ApplyScheduled(sched.Data.Scheduled)
	m.Knowledge["transactions"] = tx.Data.ServerKnowledge
	m.Knowledge["payees"] = payees.Data.ServerKnowledge
	m.Knowledge["categories"] = cats.Data.ServerKnowledge
	m.Knowledge["scheduled"] = sched.Data.ServerKnowledge
	var changed []string
	for _, t := range tx.Data.Transactions {
		if t != nil {
			changed = append(changed, t.ID)
		}
	}
	return changed, nil
}

// chooseWithModel asks the configured model to choose a payee (or name a new
// one) and a category, using the same prompt the replay harness evaluated. It
// returns the answer and the model that gave it.
func (e *Engine) chooseWithModel(ctx context.Context, in learn.ModelInput) (learn.ModelOutput, string, error) {
	instructions, message, schema := learn.Prompt(in)
	res, err := llm.Call(ctx, e.Model, instructions, message, "transaction_choice", schema)
	if err != nil {
		return learn.ModelOutput{}, e.Model.Model, fmt.Errorf("calling model: %w", err)
	}
	out, err := learn.ParseOutput(res.Text, in)
	return out, e.Model.Model, err
}
