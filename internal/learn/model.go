package learn

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options select between predictor variants. The replay harness compares them
// on held-out history; production uses the winners.
type Options struct {
	// Memory is "unanimous" (every approved example of a descriptor agrees) or
	// "majority" (a recency-weighted share of at least 75% over 2+ examples).
	Memory string
	// HalfLifeDays weights category and memo history by recency.
	HalfLifeDays float64
	// TripContext maps a payee's usual category into the event category the user
	// is currently using, such as "<Trip> Eating Out" while travelling.
	TripContext bool
	// AmountKernel weights a payee's past categories by closeness in amount.
	AmountKernel bool
	// CategoryMode is "weighted" (recency-weighted vote), "last" (YNAB's own
	// default: the payee's most recent category) or "smart-last" (most recent,
	// skipping an event category whose event has ended).
	CategoryMode string
	// WholeTrip lets an event category with no base name, in use for several
	// payees right now, take every purchase during the event.
	WholeTrip bool
	// Editions maps a category named for a past edition of a recurring event
	// ("<event> 2025") to its newest edition ("<event> 2026").
	Editions bool
	// MemoMode is "agree" (all same-amount examples agree) or "mode".
	MemoMode string
	// MemoDescriptor uses the memo every past use of the same bank text shares.
	MemoDescriptor bool
	// MemoSeries tells same-price subscriptions apart by the day they recur.
	MemoSeries bool
	// MemoPayee uses the memo every recent charge from the payee shares, for
	// charges whose amount varies.
	MemoPayee bool
	// DescriptorWeight multiplies the weight of a payee's past categories that
	// came with the same bank text (0 or 1 for no preference).
	DescriptorWeight float64
	// Splits enables splitting a charge into recurring components.
	Splits bool
}

// DefaultOptions are the production settings chosen by the replay harness.
func DefaultOptions() Options {
	return Options{Memory: "majority", HalfLifeDays: 180, TripContext: false, WholeTrip: false, Editions: true, AmountKernel: true, CategoryMode: "weighted", MemoMode: "agree", MemoDescriptor: true, MemoSeries: true, MemoPayee: true, DescriptorWeight: 3, Splits: false}
}

// Query is a transaction to enrich. Amount is signed milliunits (outflow < 0).
type Query struct {
	Descriptor string
	Amount     int64
	Date       string
	AccountID  string
}

// Candidate is a payee offered to the model.
type Candidate struct {
	ID       string
	Name     string
	Uses     int
	Score    float64
	Examples []string
}

// PayeeResult is a payee decided without a model.
type PayeeResult struct {
	ID     string
	Name   string
	Source string // "memory" or "pending"
}

// Fields are the non-payee fields for a transaction whose payee is known.
type Fields struct {
	CategoryID     string
	CategorySource string
	Memo           string
	MemoSource     string
	Lines          []Line
}

type vote struct {
	payee string
	date  string
}

// Model is built from history before a point in time.
type Model struct {
	opt        Options
	payees     map[string]*Payee
	categories map[string]*Category
	memory     map[string][]vote
	pending    map[string]string
	index      *Index
	byPayee    map[string][]Example
	uses       map[string]int
	catFirst   map[string]string
	catLast    map[string]string
	catUses    []catUse
	scheduled  []*Scheduled
	catSeen    map[string]string
}

type catUse struct {
	date     string
	category string
	payee    string
}

func days(a, b string) float64 {
	ta, ea := time.Parse("2006-01-02", a)
	tb, eb := time.Parse("2006-01-02", b)
	if ea != nil || eb != nil {
		return 0
	}
	return tb.Sub(ta).Hours() / 24
}

func addDate(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// Build makes a model from labelled history (all strictly before the query
// date in replay). Pending are the importer's suggestions not yet approved.
func Build(history []Example, payees map[string]*Payee, categories map[string]*Category, scheduled []*Scheduled, pending []*Suggestion, opt Options) *Model {
	m := &Model{opt: opt, payees: payees, categories: categories, memory: map[string][]vote{}, pending: map[string]string{}, byPayee: map[string][]Example{}, uses: map[string]int{}, catFirst: map[string]string{}, catLast: map[string]string{}, scheduled: scheduled}
	texts := map[string]map[string]bool{}
	add := func(p, t string) {
		if t == "" {
			return
		}
		if texts[p] == nil {
			texts[p] = map[string]bool{}
		}
		texts[p][t] = true
	}
	for _, e := range history {
		m.byPayee[e.PayeeID] = append(m.byPayee[e.PayeeID], e)
		m.uses[e.PayeeID]++
		if k := Normalize(e.Descriptor); k != "" {
			m.memory[k] = append(m.memory[k], vote{payee: e.PayeeID, date: e.Date})
			add(e.PayeeID, k)
		}
		cats := []string{e.CategoryID}
		for _, l := range e.Lines {
			cats = append(cats, l.CategoryID)
		}
		for _, c := range cats {
			if c == "" {
				continue
			}
			if m.catFirst[c] == "" || e.Date < m.catFirst[c] {
				m.catFirst[c] = e.Date
			}
			if e.Date > m.catLast[c] {
				m.catLast[c] = e.Date
			}
			m.catUses = append(m.catUses, catUse{date: e.Date, category: c, payee: e.PayeeID})
		}
	}
	for id, p := range payees {
		if p == nil || p.Deleted || p.TransferAccountID != nil {
			continue
		}
		add(id, Normalize(p.Name))
	}
	m.index = NewIndex(texts)
	for _, s := range pending {
		if s == nil || s.PayeeID != "" || s.PayeeName == "" {
			continue
		}
		if k := Normalize(s.Descriptor); k != "" {
			m.pending[k] = s.PayeeName
		}
	}
	return m
}

// WithCategorySeen tells the model when each category first appeared in the
// budget, so a category created ahead of an event can be used before its first
// transaction. Without it a category counts as existing once it has been used.
func (m *Model) WithCategorySeen(seen map[string]string) *Model {
	m.catSeen = seen
	return m
}

// exists reports whether a category existed (and is usable) on a date.
func (m *Model) exists(id, date string) bool {
	c := m.category(id)
	if c == nil {
		return false
	}
	if m.catSeen != nil {
		if d, ok := m.catSeen[id]; ok {
			return d <= date
		}
	}
	return m.catFirst[id] != "" && m.catFirst[id] < date
}

func (m *Model) weight(date, asOf string) float64 {
	if m.opt.HalfLifeDays <= 0 {
		return 1
	}
	return math.Pow(0.5, math.Max(0, days(date, asOf))/m.opt.HalfLifeDays)
}

func (m *Model) usable(id string) bool {
	p := m.payees[id]
	return p != nil && !p.Deleted && p.TransferAccountID == nil
}

// Payee decides a payee from the user's approved history alone: the payee the
// user approved for this same descriptor. ok=false leaves it to the model.
func (m *Model) Payee(descriptor, asOf string) (PayeeResult, bool) {
	key := Normalize(descriptor)
	if key == "" {
		return PayeeResult{}, false
	}
	if votes := m.memory[key]; len(votes) > 0 {
		if id, ok := m.decide(votes, asOf); ok && m.usable(id) {
			return PayeeResult{ID: id, Name: m.payees[id].Name, Source: "memory"}, true
		}
	}
	if name := m.pending[key]; name != "" {
		return PayeeResult{Name: name, Source: "pending"}, true
	}
	return PayeeResult{}, false
}

func (m *Model) decide(votes []vote, asOf string) (string, bool) {
	if m.opt.Memory == "majority" {
		w := map[string]float64{}
		var total float64
		for _, v := range votes {
			x := m.weight(v.date, asOf)
			w[v.payee] += x
			total += x
		}
		best, bw := "", 0.0
		for p, x := range w {
			if x > bw || (x == bw && p < best) {
				best, bw = p, x
			}
		}
		if len(votes) == 1 || (len(votes) >= 2 && bw/total >= 0.75) {
			return best, true
		}
		return "", false
	}
	first := votes[0].payee
	for _, v := range votes[1:] {
		if v.payee != first {
			return "", false
		}
	}
	return first, true
}

// Candidates returns the payees most similar to a descriptor.
func (m *Model) Candidates(descriptor string, n int) []Candidate {
	var out []Candidate
	for _, s := range m.index.Search(Normalize(descriptor), n*2) {
		if !m.usable(s.PayeeID) {
			continue
		}
		c := Candidate{ID: s.PayeeID, Name: m.payees[s.PayeeID].Name, Uses: m.uses[s.PayeeID], Score: s.Score}
		for _, t := range s.Texts {
			if t != Normalize(c.Name) && len(c.Examples) < 3 {
				c.Examples = append(c.Examples, t)
			}
		}
		out = append(out, c)
		if len(out) == n {
			break
		}
	}
	return out
}

// Known reports whether a payee has approved history.
func (m *Model) Known(id string) bool { return m.uses[id] > 0 }

func (m *Model) category(id string) *Category {
	if c := m.categories[id]; c != nil && !c.Deleted {
		return c
	}
	return nil
}

// event reports whether a category behaves like an event or trip category: all
// of its use falls within a short span.
func (m *Model) event(id, asOf string) bool {
	if c := m.category(id); c != nil && dated(c.Name) {
		return true
	}
	first, last := m.catFirst[id], m.catLast[id]
	if first == "" {
		return false
	}
	return days(first, last) <= 45 && days(first, asOf) <= 400
}

// YNABDefault is the category YNAB fills when none is sent: the payee's most
// recent category. It is the baseline any category prediction must beat.
func (m *Model) YNABDefault(payeeID string) string {
	h := m.byPayee[payeeID]
	for i := len(h) - 1; i >= 0; i-- {
		if !h[i].Split() && h[i].CategoryID != "" {
			return h[i].CategoryID
		}
	}
	return ""
}

// Complete predicts category, memo and split lines once the payee is known.
func (m *Model) Complete(payeeID string, q Query) Fields {
	var f Fields
	if m.opt.Splits {
		if lines := m.split(payeeID, q); len(lines) > 0 {
			f.Lines = lines
			f.CategorySource, f.MemoSource = "split", "split"
			return f
		}
	}
	f.CategoryID, f.CategorySource = m.Category(payeeID, q)
	f.Memo, f.MemoSource = m.Memo(payeeID, q)
	return f
}

// Category predicts a category for a known payee. It returns "" for a payee with
// no history; the caller asks the model or uses Neighbour.
func (m *Model) Category(payeeID string, q Query) (string, string) {
	h := m.byPayee[payeeID]
	base := ""
	if m.opt.CategoryMode == "last" || m.opt.CategoryMode == "smart-last" {
		for i := len(h) - 1; i >= 0; i-- {
			e := h[i]
			if e.Split() || e.CategoryID == "" || m.category(e.CategoryID) == nil {
				continue
			}
			if m.opt.CategoryMode == "smart-last" && m.event(e.CategoryID, q.Date) && days(m.catLast[e.CategoryID], q.Date) > 7 {
				continue
			}
			base = e.CategoryID
			break
		}
	}
	if base == "" {
		scores := map[string]float64{}
		eventScores := map[string]float64{}
		for _, e := range h {
			if e.Split() || e.CategoryID == "" || m.category(e.CategoryID) == nil {
				continue
			}
			w := m.weight(e.Date, q.Date)
			if m.opt.AmountKernel && e.Amount != 0 && q.Amount != 0 {
				r := math.Log(math.Abs(float64(e.Amount)) / math.Abs(float64(q.Amount)))
				w *= math.Exp(-r * r)
			}
			if m.opt.DescriptorWeight > 1 && q.Descriptor != "" && Normalize(e.Descriptor) == Normalize(q.Descriptor) {
				w *= m.opt.DescriptorWeight
			}
			if m.event(e.CategoryID, q.Date) {
				eventScores[e.CategoryID] += w
			} else {
				scores[e.CategoryID] += w
			}
		}
		if len(scores) == 0 {
			scores = eventScores
		}
		base = best(scores)
	}
	if base == "" {
		return "", "none"
	}
	if m.opt.Editions {
		if e := m.edition(base, q.Date); e != base {
			return e, "edition"
		}
	}
	if m.opt.TripContext && q.Amount < 0 {
		if c := m.tripCategory(base, q.Date); c != "" && c != base {
			return c, "trip"
		}
	}
	return base, "history"
}

// dated reports whether a category name carries a year, as event and trip
// categories in this budget do ("<event> 2026").
func dated(name string) bool {
	for _, w := range strings.Fields(name) {
		if len(w) == 4 && (strings.HasPrefix(w, "19") || strings.HasPrefix(w, "20")) && strings.Trim(w, "0123456789") == "" {
			return true
		}
	}
	return false
}

func year(name string) string {
	y := ""
	for _, w := range strings.Fields(name) {
		if dated(w) && w > y {
			y = w
		}
	}
	return y
}

// edition returns the newest existing edition of a dated category: the same
// name once digits are ignored, with the latest year.
func (m *Model) edition(id, date string) string {
	c := m.category(id)
	if c == nil || !dated(c.Name) {
		return id
	}
	key, bestID, bestYear := Normalize(c.Name), id, year(c.Name)
	for _, o := range m.categories {
		if o == nil || o.Deleted || Normalize(o.Name) != key || !m.exists(o.ID, date) {
			continue
		}
		if y := year(o.Name); y > bestYear {
			bestID, bestYear = o.ID, y
		}
	}
	return bestID
}

func best(scores map[string]float64) string {
	id, w := "", 0.0
	for c, x := range scores {
		if x > w || (x == w && c < id) {
			id, w = c, x
		}
	}
	return id
}

// eventPrefix splits an event category name into its event name and the base
// category it repeats ("<event> Eating Out"). whole is true when it has no base.
func (m *Model) eventPrefix(c *Category, date string) (prefix string, whole bool) {
	longest := ""
	for _, other := range m.categories {
		if other == nil || other.Deleted || other.ID == c.ID || m.event(other.ID, date) {
			continue
		}
		if len(other.Name) > len(longest) && strings.HasSuffix(strings.ToLower(c.Name), " "+strings.ToLower(other.Name)) {
			longest = other.Name
		}
	}
	if longest == "" {
		return c.Name, true
	}
	return strings.TrimSpace(c.Name[:len(c.Name)-len(longest)]), false
}

// tripCategory returns the event variant of base the user is using right now.
// An event is current when its categories were used for at least two different
// payees in the last two days; base then maps to "<event> <base>" when that
// category exists, or (with WholeTrip) to the event's own category.
func (m *Model) tripCategory(base, date string) string {
	b := m.category(base)
	if b == nil || m.event(base, date) {
		return ""
	}
	from := addDate(date, -2)
	type group struct {
		prefix string
		payees map[string]bool
		last   string
		whole  string
	}
	groups := map[string]*group{}
	all := map[string]bool{}
	for i := len(m.catUses) - 1; i >= 0; i-- {
		u := m.catUses[i]
		if u.date < from {
			break
		}
		if u.date > date {
			continue
		}
		all[u.payee] = true
		if !m.event(u.category, date) {
			continue
		}
		c := m.category(u.category)
		if c == nil {
			continue
		}
		prefix, whole := m.eventPrefix(c, date)
		k := strings.ToLower(prefix)
		g := groups[k]
		if g == nil {
			g = &group{prefix: prefix, payees: map[string]bool{}}
			groups[k] = g
		}
		g.payees[u.payee] = true
		if u.date > g.last {
			g.last = u.date
		}
		if whole {
			g.whole = c.ID
		}
	}
	var active *group
	for _, g := range groups {
		// The event must account for at least half of the recent payees: the
		// user is spending in "event mode", not just settling an old event.
		if len(g.payees) >= 2 && 2*len(g.payees) >= len(all) && (active == nil || g.last > active.last || (g.last == active.last && g.prefix < active.prefix)) {
			active = g
		}
	}
	if active == nil {
		return ""
	}
	want := strings.ToLower(active.prefix + " " + b.Name)
	for _, other := range m.categories {
		if other != nil && strings.ToLower(other.Name) == want && m.exists(other.ID, date) {
			return other.ID
		}
	}
	if m.opt.WholeTrip && active.whole != "" && len(active.payees) >= 3 {
		return active.whole
	}
	return ""
}

// Neighbour is the deterministic category for a new payee: the usual category
// of the most similar known payee.
func (m *Model) Neighbour(descriptor string, q Query) string {
	for _, c := range m.Candidates(descriptor, 5) {
		if cat, _ := m.Category(c.ID, q); cat != "" {
			return cat
		}
	}
	return ""
}

// Memo predicts the memo: a scheduled transaction's memo for the same payee and
// amount, or the memo past same-amount transactions agree on. Most purchases
// have no memo, and "" is a prediction like any other.
func (m *Model) Memo(payeeID string, q Query) (string, string) {
	target := payeeID
	for _, s := range m.scheduled {
		if s == nil || s.Deleted || s.PayeeID == nil || *s.PayeeID != target || s.Amount != q.Amount {
			continue
		}
		if memo := str(s.Memo); memo != "" {
			return memo, "scheduled"
		}
	}
	var recent, same, sameText []Example
	key := Normalize(q.Descriptor)
	for _, e := range m.byPayee[target] {
		if e.Split() {
			continue
		}
		// The same bank text keeps its memo however long ago it was seen.
		if key != "" && Normalize(e.Descriptor) == key {
			sameText = append(sameText, e)
		}
		if days(e.Date, q.Date) > 400 {
			continue
		}
		recent = append(recent, e)
		if e.Amount == q.Amount {
			same = append(same, e)
		}
	}
	// The same bank text has always carried the same memo.
	if m.opt.MemoDescriptor {
		if memo, ok := agreed(sameText, 2); ok {
			return memo, "descriptor"
		}
	}
	if len(same) > 0 {
		if m.opt.MemoMode == "mode" {
			w := map[string]float64{}
			for _, e := range same {
				w[strings.ToLower(e.Memo)] += m.weight(e.Date, q.Date)
			}
			pick := best(w)
			for i := len(same) - 1; i >= 0; i-- {
				if strings.ToLower(same[i].Memo) == pick {
					return same[i].Memo, "history"
				}
			}
		}
		if memo, ok := agreed(same, 1); ok {
			return memo, "history"
		}
		// Several subscriptions at one price: the one that recurs monthly on
		// this day of the month.
		if m.opt.MemoSeries {
			if memo := series(same, q.Date); memo != "" {
				return memo, "series"
			}
		}
		return "", "none"
	}
	// Every recent charge from this payee carries the same memo, whatever its
	// amount (such as a subscription billed in another currency).
	if m.opt.MemoPayee {
		if memo, ok := agreed(recent, 3); ok {
			return memo, "payee"
		}
	}
	return "", "none"
}

// agreed returns the memo every example shares, given at least n examples. An
// empty shared memo is a valid answer.
func agreed(ex []Example, n int) (string, bool) {
	if len(ex) < n || len(ex) == 0 {
		return "", false
	}
	memo := ex[len(ex)-1].Memo
	for _, e := range ex {
		if !strings.EqualFold(strings.TrimSpace(e.Memo), strings.TrimSpace(memo)) {
			return "", false
		}
	}
	return memo, true
}

// series picks, among same-amount charges with different memos, the memo whose
// latest charge was about a month ago on nearly the same day of the month.
func series(same []Example, date string) string {
	q, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	latest := map[string]Example{}
	for _, e := range same {
		k := strings.ToLower(strings.TrimSpace(e.Memo))
		if k != "" && e.Date > latest[k].Date {
			latest[k] = e
		}
	}
	found := ""
	for _, e := range latest {
		d, err := time.Parse("2006-01-02", e.Date)
		if err != nil {
			continue
		}
		gap := q.Sub(d).Hours() / 24
		dom := q.Day() - d.Day()
		if dom < 0 {
			dom = -dom
		}
		if dom > 15 {
			dom = 31 - dom
		}
		if gap >= 25 && gap <= 37 && dom <= 3 {
			if found != "" {
				return ""
			}
			found = e.Memo
		}
	}
	return found
}

// split finds the one combination of 2–4 of the payee's recurring components
// (past same-payee charges or split lines with a memo) that sums exactly to the
// charge. Zero or several combinations mean no split.
func (m *Model) split(payeeID string, q Query) []Line {
	target := payeeID
	type comp struct {
		Line
		date string
	}
	seen := map[string]int{}
	var comps []comp
	put := func(l Line, date string) {
		if l.Memo == "" || l.Amount >= 0 || q.Amount >= 0 {
			return
		}
		k := strings.ToLower(l.Memo) + "\x00" + strconv.FormatInt(l.Amount, 10)
		if i, ok := seen[k]; ok {
			if date > comps[i].date {
				comps[i] = comp{l, date}
			}
			return
		}
		seen[k] = len(comps)
		comps = append(comps, comp{l, date})
	}
	for _, e := range m.byPayee[target] {
		if days(e.Date, q.Date) > 400 {
			continue
		}
		if e.Split() {
			for _, l := range e.Lines {
				if l.PayeeID == "" || l.PayeeID == target {
					put(Line{Amount: l.Amount, CategoryID: l.CategoryID, Memo: l.Memo}, e.Date)
				}
			}
		} else {
			put(Line{Amount: e.Amount, CategoryID: e.CategoryID, Memo: e.Memo}, e.Date)
		}
	}
	for _, c := range comps {
		if c.Amount == q.Amount {
			return nil
		}
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i].date > comps[j].date })
	if len(comps) > 16 {
		comps = comps[:16]
	}
	var found [][]int
	var walk func(start int, sum int64, picked []int)
	walk = func(start int, sum int64, picked []int) {
		if len(found) > 1 {
			return
		}
		if len(picked) >= 2 && sum == q.Amount {
			found = append(found, append([]int(nil), picked...))
			return
		}
		if len(picked) == 4 {
			return
		}
		for i := start; i < len(comps); i++ {
			// Never pick two components with the same memo.
			dup := false
			for _, j := range picked {
				if strings.EqualFold(comps[j].Memo, comps[i].Memo) {
					dup = true
					break
				}
			}
			if !dup {
				walk(i+1, sum+comps[i].Amount, append(picked, i))
			}
		}
	}
	walk(0, 0, nil)
	if len(found) != 1 {
		return nil
	}
	var lines []Line
	for _, i := range found[0] {
		lines = append(lines, comps[i].Line)
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Amount < lines[j].Amount })
	return lines
}
