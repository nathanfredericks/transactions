package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/override"
	"github.com/nathanfredericks/transactions/internal/types"
)

var (
	rulesFile  = flag.String("rules", "", "Rules export (JSON with a \"rules\" array) for the rules check")
	recentDays = flag.Int("recent-days", 365, "Window of recent history the rules check judges by")
)

type ruleMatch struct {
	e       learn.Example
	p       learn.Prediction
	memo    string
	agrees  bool // your approval matches the rule
	learned bool // history-based filling reproduces the rule
}

// rules checks each rule against approved history: does the user still approve
// what it sets, and would history-based filling set the same thing without it?
func rules(m *learn.Mirror, ex []learn.Example, out string) {
	if *rulesFile == "" {
		log.Fatal("-rules is required")
	}
	b, err := os.ReadFile(*rulesFile)
	if err != nil {
		log.Fatal(err)
	}
	var export struct {
		Rules []types.TransactionOverride `json:"rules"`
	}
	if err := json.Unmarshal(b, &export); err != nil {
		log.Fatal(err)
	}
	all := export.Rules
	// Production tries the most recently updated rule first.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].UpdatedAt == all[j].UpdatedAt {
			return all[i].ID < all[j].ID
		}
		return all[i].UpdatedAt > all[j].UpdatedAt
	})
	zone, _ := time.LoadLocation("America/Halifax")
	var dates []string
	for _, e := range ex {
		dates = append(dates, e.Date)
	}
	sort.Strings(dates)
	last, _ := time.Parse("2006-01-02", dates[len(dates)-1])
	from := last.AddDate(0, 0, -*recentDays).Format("2006-01-02")

	// Forward replay with no rules: what learning alone would have filled.
	preds := map[string]learn.Prediction{}
	for _, p := range learn.Replay(base(m, ex, learn.DefaultOptions(), from, "")) {
		preds[p.Example.ID] = p
	}
	matches := map[string][]ruleMatch{}
	allTime := map[string]int{}
	for _, e := range ex {
		if e.Descriptor == "" || e.Amount >= 0 {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", e.Date, zone)
		if err != nil {
			continue
		}
		r := override.Match(all, float64(-e.Amount)/1000, e.Descriptor, day)
		if r == nil {
			continue
		}
		allTime[r.ID]++
		p, ok := preds[e.ID]
		if !ok {
			continue
		}
		memo := ""
		if r.Memo != "" {
			memo, _ = override.RenderMemo(r.Memo, e.Date)
		}
		rm := ruleMatch{e: e, p: p, memo: memo}
		rm.agrees = e.PayeeID == r.Payee && (r.Category == "" || (!e.Split() && e.CategoryID == r.Category)) && (r.Memo == "" || strings.EqualFold(strings.TrimSpace(e.Memo), strings.TrimSpace(memo)))
		rm.learned = p.PayeeID == r.Payee && (r.Category == "" || p.CategoryID == r.Category) && (r.Memo == "" || strings.EqualFold(strings.TrimSpace(p.Memo), strings.TrimSpace(memo)))
		matches[r.ID] = append(matches[r.ID], rm)
	}
	name := func(id string) string {
		if p := m.Payees[id]; p != nil {
			return p.Name
		}
		if c := m.Categories[id]; c != nil {
			return c.Name
		}
		if id == "" {
			return "—"
		}
		return "(" + id[:8] + ")"
	}
	var w strings.Builder
	fmt.Fprintf(&w, "# Rules check\n\nRecent window: %s to %s. \"Matches\" are approved purchases the rule's query selects. \"You approved it\" means your approved transaction has the rule's payee, category and memo. \"Learning reproduces it\" replays history forward in time with no rules, and counts matches where learning alone (history, no model) sets the same payee, category and memo, among those you approved as the rule says.\n\n", from, dates[len(dates)-1])
	fmt.Fprintf(&w, "| Rule | Sets | Matches (2 yrs / recent) | You approved it (recent) | Learning reproduces it | Verdict |\n|---|---|---|---|---|---|\n")
	type verdict struct{ id, text string }
	var details []string
	for _, r := range all {
		ms := matches[r.ID]
		agree, learned := 0, 0
		for _, x := range ms {
			if x.agrees {
				agree++
				if x.learned {
					learned++
				}
			}
		}
		sets := []string{"payee " + name(r.Payee)}
		if r.Category != "" {
			sets = append(sets, "category "+name(r.Category))
		}
		if r.Memo != "" {
			sets = append(sets, fmt.Sprintf("memo %q", r.Memo))
		}
		v := ""
		switch {
		case len(ms) == 0 && allTime[r.ID] == 0:
			v = "Unused: matched nothing in 2 years"
		case len(ms) == 0:
			v = "No recent matches"
		case 2*agree < len(ms):
			v = "Outdated: you mostly approve something else"
		case learned == agree:
			v = "Covered"
		case 10*learned >= 8*agree:
			v = "Mostly covered"
		default:
			v = "Not reproduced"
		}
		fmt.Fprintf(&w, "| %s | %s | %d / %d | %d of %d | %d of %d | %s |\n", r.Name, strings.Join(sets, "; "), allTime[r.ID], len(ms), agree, len(ms), learned, agree, v)
		if v != "Covered" && len(ms) > 0 {
			var lines []string
			for _, x := range ms {
				if x.agrees && x.learned {
					continue
				}
				what := "learning"
				if !x.agrees {
					what = "you approved"
				}
				got := fmt.Sprintf("payee %s (%s), category %s, memo %q", name(x.p.PayeeID), orDash(x.p.PayeeSource, "needs model"), name(x.p.CategoryID), x.p.Memo)
				if !x.agrees {
					got = fmt.Sprintf("payee %s, category %s, memo %q", name(x.e.PayeeID), name(x.e.CategoryID), x.e.Memo)
				}
				lines = append(lines, fmt.Sprintf("  - %s %q $%.2f — %s: %s", x.e.Date, x.e.Descriptor, float64(-x.e.Amount)/1000, what, got))
			}
			if len(lines) > 8 {
				lines = append(lines[:8], fmt.Sprintf("  - … %d more", len(lines)-8))
			}
			details = append(details, fmt.Sprintf("- **%s** (%s)\n%s", r.Name, v, strings.Join(lines, "\n")))
		}
	}
	fmt.Fprintf(&w, "\n## Where rules and learning differ\n\n%s\n", strings.Join(details, "\n"))
	write(out, "rules.md", w.String())
	fmt.Print(w.String())
}

func orDash(s, dash string) string {
	if s == "" {
		return dash
	}
	return s
}
