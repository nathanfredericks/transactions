// Command evaluate replays approved YNAB history to choose and validate the
// import predictors and the production model. It reads a mirror snapshot and
// never writes to YNAB.
//
//	go run ./cmd/evaluate -ynab-dir DIR deterministic
//	go run ./cmd/evaluate -ynab-dir DIR -out DIR models
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nathanfredericks/transactions/internal/learn"
	"github.com/nathanfredericks/transactions/internal/types"
)

func main() {
	dir := flag.String("ynab-dir", "", "Directory with YNAB API dumps: transactions.json, payees.json, categories.json, scheduled.json")
	mirrorFile := flag.String("mirror", "", "Mirror JSON file (alternative to -ynab-dir)")
	out := flag.String("out", ".", "Directory for reports and case files")
	exclude := flag.String("exclude-payee", "", "Payee ID that is never a label (the balance adjustment payee)")
	flag.Parse()
	m, err := load(*dir, *mirrorFile)
	if err != nil {
		log.Fatal(err)
	}
	var excluded []string
	if *exclude != "" {
		excluded = append(excluded, *exclude)
	}
	for id, p := range m.Payees {
		// The adjustment payee balances investment accounts and is never a purchase.
		if p.Name == "Reconciliation Balance Adjustment" || strings.HasPrefix(p.Name, "Starting Balance") {
			excluded = append(excluded, id)
		}
	}
	ex := m.Examples(excluded...)
	switch flag.Arg(0) {
	case "deterministic":
		deterministic(m, ex, *out)
	case "models":
		models(m, ex, *out)
	default:
		log.Fatal("usage: evaluate [flags] deterministic|models")
	}
}

func load(dir, mirrorFile string) (*learn.Mirror, error) {
	if mirrorFile != "" {
		b, err := os.ReadFile(mirrorFile)
		if err != nil {
			return nil, err
		}
		m := learn.NewMirror()
		if err := json.Unmarshal(b, m); err != nil {
			return nil, err
		}
		m.Fill()
		return m, nil
	}
	m := learn.NewMirror()
	var tx struct {
		Data struct {
			Transactions []*types.YNABTransaction `json:"transactions"`
		} `json:"data"`
	}
	var payees struct {
		Data struct {
			Payees []*learn.Payee `json:"payees"`
		} `json:"data"`
	}
	var cats struct {
		Data struct {
			Groups []struct {
				Name       string            `json:"name"`
				Categories []*learn.Category `json:"categories"`
			} `json:"category_groups"`
		} `json:"data"`
	}
	var sched struct {
		Data struct {
			Scheduled []*learn.Scheduled `json:"scheduled_transactions"`
		} `json:"data"`
	}
	for name, v := range map[string]any{"transactions.json": &tx, "payees.json": &payees, "categories.json": &cats, "scheduled.json": &sched} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, v); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	m.ApplyTransactions(tx.Data.Transactions)
	m.ApplyPayees(payees.Data.Payees)
	for _, g := range cats.Data.Groups {
		for _, c := range g.Categories {
			c.GroupName = g.Name
		}
		m.ApplyCategories(g.Categories, "")
	}
	m.ApplyScheduled(sched.Data.Scheduled)
	return m, nil
}

// windows splits labelled examples with descriptors into a warm-up, a dev window
// (50–80%) for choosing variants and a locked holdout (last 20%).
func windows(ex []learn.Example) (devFrom, holdFrom string) {
	var dates []string
	for _, e := range ex {
		if e.Descriptor != "" {
			dates = append(dates, e.Date)
		}
	}
	sort.Strings(dates)
	return dates[len(dates)*50/100], dates[len(dates)*80/100]
}

type summary struct {
	name                                 string
	payee, decided, memory, inTop        learn.Rate
	category, baseline, memo, split      learn.Rate
	memoEmpty                            learn.Rate
	seenCat, seenBase, unseenCat         learn.Rate
	unseenDecidedWrong, unseenFirstVisit int
}

func summarize(name string, preds []learn.Prediction) summary {
	s := summary{name: name}
	var pc, pn, dc, dn, ic, in, cc, cn, bc, mc, mn, me, sc, sn, sc2, sn2, sb2, uc, un int
	for _, p := range preds {
		e := p.Example
		pn++
		if p.PayeeSource != "" {
			dn++
			if learn.Correct(p.PayeeID, e.PayeeID) {
				dc++
				pc++
			} else if !p.Seen {
				s.unseenDecidedWrong++
			}
		} else {
			in++
			if p.InTop {
				ic++
			}
		}
		if !p.Seen {
			s.unseenFirstVisit++
		}
		sn++
		if p.SplitCorrect {
			sc++
		}
		if !e.Split() {
			cn++
			mn++
			if p.CategoryCorrect {
				cc++
			}
			if p.BaselineCorrect {
				bc++
			}
			if p.MemoCorrect {
				mc++
			}
			if e.Memo == "" {
				me++
			}
			if p.Seen {
				sn2++
				if p.CategoryCorrect {
					sc2++
				}
				if p.BaselineCorrect {
					sb2++
				}
			} else {
				un++
				if p.CategoryCorrect {
					uc++
				}
			}
		}
	}
	s.payee, s.decided, s.inTop = learn.Wilson(dc, pn), learn.Wilson(dc, dn), learn.Wilson(ic, in)
	s.memory = learn.Wilson(dn, pn)
	s.category, s.baseline, s.memo, s.split = learn.Wilson(cc, cn), learn.Wilson(bc, cn), learn.Wilson(mc, mn), learn.Wilson(sc, sn)
	s.memoEmpty = learn.Wilson(me, mn)
	s.seenCat, s.seenBase, s.unseenCat = learn.Wilson(sc2, sn2), learn.Wilson(sb2, sn2), learn.Wilson(uc, un)
	return s
}

func pct(r learn.Rate) string {
	if r.N == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%% [%.1f–%.1f] n=%d", 100*r.P, 100*r.Low, 100*r.High, r.N)
}

func (s summary) print(w *strings.Builder) {
	fmt.Fprintf(w, "### %s\n\n", s.name)
	fmt.Fprintf(w, "| Measure | Result |\n|---|---|\n")
	fmt.Fprintf(w, "| Payee decided without the model (coverage) | %s |\n", pct(s.memory))
	fmt.Fprintf(w, "| Accuracy of those decisions | %s |\n", pct(s.decided))
	fmt.Fprintf(w, "| True payee among model candidates (rest) | %s |\n", pct(s.inTop))
	fmt.Fprintf(w, "| Category (ours), unsplit | %s |\n", pct(s.category))
	fmt.Fprintf(w, "| Category (YNAB default), unsplit | %s |\n", pct(s.baseline))
	fmt.Fprintf(w, "| Category, seen payees: ours vs YNAB default | %s vs %s |\n", pct(s.seenCat), pct(s.seenBase))
	fmt.Fprintf(w, "| Category, new payees (neighbour) | %s |\n", pct(s.unseenCat))
	fmt.Fprintf(w, "| Memo, unsplit (empty counts) | %s |\n", pct(s.memo))
	fmt.Fprintf(w, "| Memo baseline: always empty | %s |\n", pct(s.memoEmpty))
	fmt.Fprintf(w, "| Split decision | %s |\n", pct(s.split))
	fmt.Fprintf(w, "| New payees wrongly decided without model | %d of %d |\n\n", s.unseenDecidedWrong, s.unseenFirstVisit)
}

func base(m *learn.Mirror, ex []learn.Example, opt learn.Options, from, to string) learn.ReplayInput {
	var sched []*learn.Scheduled
	for _, s := range m.Scheduled {
		sched = append(sched, s)
	}
	return learn.ReplayInput{Examples: ex, Payees: m.Payees, Categories: m.Categories, Scheduled: sched, Options: opt, From: from, To: to}
}

func deterministic(m *learn.Mirror, ex []learn.Example, out string) {
	devFrom, holdFrom := windows(ex)
	var w strings.Builder
	fmt.Fprintf(&w, "# Deterministic replay\n\nExamples with a descriptor: dev %s to %s, locked holdout from %s. Payees are graded exactly.\n\n", devFrom, holdFrom, holdFrom)
	variants := map[string]func(*learn.Options){
		"default":            func(*learn.Options) {},
		"memory=unanimous":   func(o *learn.Options) { o.Memory = "unanimous" },
		"memo=mode":          func(o *learn.Options) { o.MemoMode = "mode" },
		"splits":             func(o *learn.Options) { o.Splits = true },
		"no-trip":            func(o *learn.Options) { o.TripContext = false },
		"no-whole-trip":      func(o *learn.Options) { o.WholeTrip = false },
		"no-editions":        func(o *learn.Options) { o.Editions = false },
		"no-kernel":          func(o *learn.Options) { o.AmountKernel = false },
		"half-life=60":       func(o *learn.Options) { o.HalfLifeDays = 60 },
		"half-life=365":      func(o *learn.Options) { o.HalfLifeDays = 365 },
		"cat=smart-last":     func(o *learn.Options) { o.CategoryMode = "smart-last" },
		"plain (no context)": func(o *learn.Options) { o.TripContext, o.WholeTrip, o.Editions = false, false, false },
	}
	names := make([]string, 0, len(variants))
	for n := range variants {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(&w, "## Dev window: variants\n\n| Variant | Payee coverage | Payee accuracy (decided) | Category | Category (YNAB default) | Memo | Split |\n|---|---|---|---|---|---|---|\n")
	for _, n := range names {
		opt := learn.DefaultOptions()
		variants[n](&opt)
		s := summarize(n, learn.Replay(base(m, ex, opt, devFrom, holdFrom)))
		fmt.Fprintf(&w, "| %s | %s | %s | %s | %s | %s | %s |\n", n, pct(s.memory), pct(s.decided), pct(s.category), pct(s.baseline), pct(s.memo), pct(s.split))
	}
	fmt.Fprintln(&w)
	opt := learn.DefaultOptions()
	hold := learn.Replay(base(m, ex, opt, holdFrom, ""))
	fmt.Fprintf(&w, "## Locked holdout (default options)\n\n")
	summarize("holdout", hold).print(&w)

	// Wrong decisions, so they can be read and fixed generally.
	fmt.Fprintf(&w, "## Holdout payee decisions that were wrong\n\n| Date | Descriptor | Source | Chose | Approved |\n|---|---|---|---|---|\n")
	for _, p := range hold {
		if p.PayeeSource != "" && !learn.Correct(p.PayeeID, p.Example.PayeeID) {
			fmt.Fprintf(&w, "| %s | %s | %s | %s | %s |\n", p.Example.Date, p.Example.Descriptor, p.PayeeSource, p.PayeeName, m.Payees[p.Example.PayeeID].Name)
		}
	}
	fmt.Fprintln(&w)

	// Rename test: a letter substitution keeps every string relationship but
	// removes real merchant names. Deterministic accuracy must not change.
	renamed := rename(m, ex)
	rm := renamed.mirror
	fmt.Fprintf(&w, "## Rename test (letters substituted in every payee, descriptor and category name)\n\n")
	summarize("holdout, renamed", learn.Replay(base(rm, renamed.examples, opt, holdFrom, ""))).print(&w)

	// New merchants: hide a sample of payees and replay each first visit.
	hide := sampleHidden(ex, devFrom, 80)
	in := base(m, ex, opt, devFrom, "")
	in.Hide = hide
	var first []learn.Prediction
	for _, p := range learn.Replay(in) {
		if hide[p.Example.PayeeID] {
			first = append(first, p)
		}
	}
	fmt.Fprintf(&w, "## Hidden-payee first visits (%d payees)\n\n", len(hide))
	summarize("first visits", first).print(&w)

	// Robustness: mechanical variants of holdout descriptors.
	fmt.Fprintf(&w, "## Robustness (holdout descriptors with generated variations)\n\n| Variation | Decided | Decided correctly | True payee in candidates |\n|---|---|---|---|\n")
	for _, v := range variations {
		var vex []learn.Example
		for _, e := range ex {
			if e.Date >= holdFrom && e.Descriptor != "" {
				e.Descriptor = v.apply(e.Descriptor)
			}
			vex = append(vex, e)
		}
		s := summarize(v.name, learn.Replay(base(m, vex, opt, holdFrom, "")))
		fmt.Fprintf(&w, "| %s | %s | %s | %s |\n", v.name, pct(s.memory), pct(s.decided), pct(s.inTop))
	}
	fmt.Fprintln(&w)
	write(out, "deterministic.md", w.String())
	devPreds := learn.Replay(base(m, ex, opt, devFrom, holdFrom))
	var devFirst, holdFirst []learn.Prediction
	for _, p := range first {
		if p.Example.Date < holdFrom {
			devFirst = append(devFirst, p)
		} else {
			holdFirst = append(holdFirst, p)
		}
	}
	devCases := residual(m, append(devPreds, devFirst...))
	cases := residual(m, append(hold, holdFirst...))
	b, _ := json.MarshalIndent(devCases, "", " ")
	write(out, "cases-dev.json", string(b))
	b, _ = json.MarshalIndent(cases, "", " ")
	write(out, "cases-holdout.json", string(b))
	fmt.Printf("%d dev cases, ", len(devCases))
	fmt.Print(w.String())
	fmt.Printf("\n%d residual cases written for the model comparison\n", len(cases))
}

func write(dir, name, s string) {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
		log.Fatal(err)
	}
}

func sampleHidden(ex []learn.Example, from string, n int) map[string]bool {
	count := map[string]int{}
	later := map[string]bool{}
	for _, e := range ex {
		count[e.PayeeID]++
		if e.Date >= from && e.Descriptor != "" {
			later[e.PayeeID] = true
		}
	}
	var ids []string
	for id, c := range count {
		if c >= 2 && later[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	r := rand.New(rand.NewPCG(7, 11))
	r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	out := map[string]bool{}
	for _, id := range ids[:min(n, len(ids))] {
		out[id] = true
	}
	return out
}

type variation struct {
	name  string
	apply func(string) string
}

var variations = []variation{
	{"store number added", func(s string) string { return s + " #4821" }},
	{"location suffix added", func(s string) string { return s + " HALIFAX NS" }},
	{"truncated to 14 characters", func(s string) string {
		if len(s) > 14 {
			return s[:14]
		}
		return s
	}},
	{"processor prefix added", func(s string) string { return "SQ *" + s }},
	{"lower case", strings.ToLower},
}

type renamedData struct {
	mirror   *learn.Mirror
	examples []learn.Example
}

func rename(m *learn.Mirror, ex []learn.Example) renamedData {
	r := rand.New(rand.NewPCG(3, 5))
	letters := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	perm := append([]rune(nil), letters...)
	r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	sub := func(s string) string {
		return strings.Map(func(c rune) rune {
			switch {
			case c >= 'A' && c <= 'Z':
				return perm[c-'A']
			case c >= 'a' && c <= 'z':
				return perm[c-'a'] + ('a' - 'A')
			}
			return c
		}, s)
	}
	nm := learn.NewMirror()
	for id, p := range m.Payees {
		q := *p
		q.Name = sub(p.Name)
		nm.Payees[id] = &q
	}
	for id, c := range m.Categories {
		q := *c
		q.Name = sub(c.Name)
		nm.Categories[id] = &q
	}
	nm.Scheduled = m.Scheduled
	var nex []learn.Example
	for _, e := range ex {
		e.Descriptor = sub(e.Descriptor)
		e.Memo = sub(e.Memo)
		var lines []learn.Line
		for _, l := range e.Lines {
			l.Memo = sub(l.Memo)
			lines = append(lines, l)
		}
		e.Lines = lines
		nex = append(nex, e)
	}
	return renamedData{nm, nex}
}

// Case is one transaction the model would be asked about.
type Case struct {
	ID         string            `json:"id"`
	Date       string            `json:"date"`
	Descriptor string            `json:"descriptor"`
	Amount     int64             `json:"amount"`
	Account    string            `json:"account"`
	Seen       bool              `json:"seen"`
	Label      string            `json:"label"`
	LabelName  string            `json:"labelName"`
	LabelCat   string            `json:"labelCategory"`
	Candidates []learn.Candidate `json:"candidates"`
	Reachable  bool              `json:"reachable"`
	Input      *learn.ModelInput `json:"input"`
}

func residual(m *learn.Mirror, preds []learn.Prediction) []Case {
	var out []Case
	seen := map[string]bool{}
	for _, p := range preds {
		if p.PayeeSource != "" || seen[p.Example.ID] || p.Example.Split() {
			continue
		}
		seen[p.Example.ID] = true
		e := p.Example
		inTop := false
		for _, c := range p.Candidates {
			inTop = inTop || c.ID == e.PayeeID
		}
		out = append(out, Case{ID: e.ID, Date: e.Date, Descriptor: e.Descriptor, Amount: e.Amount, Account: e.AccountID, Seen: p.Seen, Reachable: inTop, Label: e.PayeeID, LabelName: m.Payees[e.PayeeID].Name, LabelCat: e.CategoryID, Candidates: p.Candidates, Input: p.Input})
	}
	return out
}
