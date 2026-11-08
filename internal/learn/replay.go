package learn

import (
	"math"
	"sort"
	"strings"
)

// Prediction is the replayed outcome for one labelled example.
type Prediction struct {
	Example Example
	// Seen reports whether the payee had approved history before this example.
	Seen bool
	// Payee decided from evidence alone ("" when the model would be asked).
	PayeeID     string
	PayeeName   string
	PayeeSource string
	// Candidates the model would see, and whether the true payee is among them.
	Candidates []Candidate
	InTop      bool
	// Input is what the model would be sent for this example.
	Input *ModelInput
	// Field predictions given the true payee.
	CategoryID      string
	CategorySource  string
	BaselineID      string
	Memo            string
	MemoSource      string
	Lines           []Line
	CategoryCorrect bool
	BaselineCorrect bool
	MemoCorrect     bool
	SplitCorrect    bool
}

// Correct reports whether a payee decision matches the label.
func Correct(pred, label string) bool { return pred != "" && pred == label }

// ReplayInput is everything a replay needs.
type ReplayInput struct {
	Examples   []Example
	Payees     map[string]*Payee
	Categories map[string]*Category
	Scheduled  []*Scheduled
	Options    Options
	// From and To bound the test dates (inclusive From, exclusive To; "" = open).
	From, To string
	// Hide removes these payees from history, so their first transaction in the
	// test window replays as a true first visit.
	Hide map[string]bool
	// Candidates is how many payees the model is offered.
	Candidates int
}

// Replay predicts every test example using only history strictly before its date.
func Replay(in ReplayInput) []Prediction {
	if in.Candidates == 0 {
		in.Candidates = 20
	}
	var out []Prediction
	var model *Model
	day := ""
	var history []Example
	next := 0
	hiddenSeen := map[string]bool{}
	for _, e := range in.Examples {
		if e.Descriptor == "" || (in.From != "" && e.Date < in.From) || (in.To != "" && e.Date >= in.To) {
			continue
		}
		if in.Hide[e.PayeeID] && hiddenSeen[e.PayeeID] {
			continue
		}
		if e.Date != day {
			for next < len(in.Examples) && in.Examples[next].Date < e.Date {
				if !in.Hide[in.Examples[next].PayeeID] {
					history = append(history, in.Examples[next])
				}
				next++
			}
			// Only payees with history before this day exist: a merchant's payee
			// is created by its first approved transaction.
			known := map[string]*Payee{}
			for _, h := range history {
				if p := in.Payees[h.PayeeID]; p != nil {
					known[h.PayeeID] = p
				}
			}
			for id, p := range in.Payees {
				if p != nil && p.TransferAccountID != nil {
					known[id] = p
				}
			}
			model = Build(history, known, in.Categories, in.Scheduled, nil, in.Options)
			day = e.Date
		}
		if in.Hide[e.PayeeID] {
			hiddenSeen[e.PayeeID] = true
		}
		q := Query{Descriptor: e.Descriptor, Amount: e.Amount, Date: e.Date, AccountID: e.AccountID}
		p := Prediction{Example: e, Seen: model.Known(e.PayeeID)}
		if r, ok := model.Payee(e.Descriptor, e.Date); ok {
			p.PayeeID, p.PayeeName, p.PayeeSource = r.ID, r.Name, r.Source
		} else {
			p.Candidates = model.Candidates(e.Descriptor, in.Candidates)
			for _, c := range p.Candidates {
				if Correct(c.ID, e.PayeeID) {
					p.InTop = true
				}
			}
			input := model.Input(q, "", p.Candidates)
			p.Input = &input
		}
		f := model.Complete(e.PayeeID, q)
		if !p.Seen && f.CategoryID == "" && len(f.Lines) == 0 {
			f.CategoryID, f.CategorySource = model.Neighbour(e.Descriptor, q), "neighbour"
		}
		p.CategoryID, p.CategorySource, p.Memo, p.MemoSource, p.Lines = f.CategoryID, f.CategorySource, f.Memo, f.MemoSource, f.Lines
		p.BaselineID = model.YNABDefault(e.PayeeID)
		if e.Split() {
			p.SplitCorrect = sameLines(f.Lines, e.Lines)
		} else {
			p.SplitCorrect = len(f.Lines) == 0
			p.CategoryCorrect = len(f.Lines) == 0 && f.CategoryID == e.CategoryID
			p.BaselineCorrect = p.BaselineID == e.CategoryID
			p.MemoCorrect = len(f.Lines) == 0 && strings.EqualFold(strings.TrimSpace(f.Memo), strings.TrimSpace(e.Memo))
		}
		out = append(out, p)
	}
	return out
}

func sameLines(a, b []Line) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(ls []Line) []string {
		var out []string
		for _, l := range ls {
			out = append(out, strings.ToLower(l.CategoryID+"|"+l.Memo)+"|"+itoa(l.Amount))
		}
		sort.Strings(out)
		return out
	}
	ka, kb := key(a), key(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func itoa(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// Rate is a proportion with its Wilson 95% interval.
type Rate struct {
	Hits, N int
	P       float64
	Low     float64
	High    float64
}

// Wilson returns the proportion and its Wilson score interval at 95%.
func Wilson(hits, n int) Rate {
	if n == 0 {
		return Rate{}
	}
	z := 1.959964
	p := float64(hits) / float64(n)
	nn := float64(n)
	den := 1 + z*z/nn
	centre := (p + z*z/(2*nn)) / den
	half := z * math.Sqrt(p*(1-p)/nn+z*z/(4*nn*nn)) / den
	return Rate{Hits: hits, N: n, P: p, Low: math.Max(0, centre-half), High: math.Min(1, centre+half)}
}

// McNemar returns the exact two-sided p-value for paired outcomes, where b is the
// number of cases only A got right and c the number only B got right.
func McNemar(b, c int) float64 {
	n := b + c
	if n == 0 {
		return 1
	}
	k := b
	if c < k {
		k = c
	}
	// P(X <= k) for X ~ Binomial(n, 0.5), doubled.
	var sum float64
	logHalf := float64(n) * math.Log(0.5)
	for i := 0; i <= k; i++ {
		lg := lgamma(n+1) - lgamma(i+1) - lgamma(n-i+1)
		sum += math.Exp(lg + logHalf)
	}
	return math.Min(1, 2*sum)
}

func lgamma(n int) float64 {
	v, _ := math.Lgamma(float64(n))
	return v
}
