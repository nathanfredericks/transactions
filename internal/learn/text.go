package learn

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

// Normalize reduces a descriptor to a comparison key that ignores case,
// punctuation, spacing, and digit-bearing tokens such as store or order numbers.
func Normalize(s string) string {
	s = strings.ReplaceAll(strings.ToUpper(s), "'", "")
	s = strings.ReplaceAll(s, "’", "")
	var words []string
	for _, w := range strings.Fields(nonAlnum.ReplaceAllString(s, " ")) {
		if !strings.ContainsAny(w, "0123456789") {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

func trigrams(s string) []string {
	r := []rune(" " + s + " ")
	seen := map[string]bool{}
	var out []string
	for i := 0; i+3 <= len(r); i++ {
		g := string(r[i : i+3])
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, g := range a {
		set[g] = true
	}
	inter := 0
	for _, g := range b {
		if set[g] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// Similar reports whether two descriptors name the same merchant closely enough
// to treat a bank credit as the source of an alert.
func Similar(a, b string) bool {
	na, nb := Normalize(a), Normalize(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || jaccard(trigrams(na), trigrams(nb)) >= 0.6
}

type doc struct {
	payee string
	text  string
	norm  float64
}

// Index ranks payees by IDF-weighted character-trigram cosine similarity between
// a query and each payee's name and approved descriptors.
type Index struct {
	docs     []doc
	postings map[string][]int
	idf      map[string]float64
}

// NewIndex builds an index over (payee, text) documents. Texts must already be keys.
func NewIndex(texts map[string]map[string]bool) *Index {
	ix := &Index{postings: map[string][]int{}, idf: map[string]float64{}}
	payees := make([]string, 0, len(texts))
	for p := range texts {
		payees = append(payees, p)
	}
	sort.Strings(payees)
	for _, p := range payees {
		var ts []string
		for t := range texts[p] {
			if t != "" {
				ts = append(ts, t)
			}
		}
		sort.Strings(ts)
		for _, t := range ts {
			ix.docs = append(ix.docs, doc{payee: p, text: t})
		}
	}
	df := map[string]int{}
	for i, d := range ix.docs {
		for _, g := range trigrams(d.text) {
			ix.postings[g] = append(ix.postings[g], i)
			df[g]++
		}
	}
	n := float64(len(ix.docs))
	for g, c := range df {
		ix.idf[g] = math.Log(1 + n/float64(c))
	}
	for i := range ix.docs {
		var sum float64
		for _, g := range trigrams(ix.docs[i].text) {
			sum += ix.idf[g] * ix.idf[g]
		}
		ix.docs[i].norm = math.Sqrt(sum)
	}
	return ix
}

// Scored is a payee with its best similarity and the texts that matched.
type Scored struct {
	PayeeID string
	Score   float64
	Texts   []string
}

// Search returns the top n payees for a query key.
func (ix *Index) Search(key string, n int) []Scored {
	q := trigrams(key)
	if len(q) == 0 || len(ix.docs) == 0 {
		return nil
	}
	var qnorm float64
	dot := map[int]float64{}
	for _, g := range q {
		w := ix.idf[g]
		if w == 0 {
			// An unseen trigram still counts toward the query's length.
			w = math.Log(1 + float64(len(ix.docs)))
		}
		qnorm += w * w
		for _, i := range ix.postings[g] {
			dot[i] += w * w
		}
	}
	qnorm = math.Sqrt(qnorm)
	best := map[string]*Scored{}
	for i, v := range dot {
		d := ix.docs[i]
		s := v / (qnorm * d.norm)
		b := best[d.payee]
		if b == nil {
			b = &Scored{PayeeID: d.payee}
			best[d.payee] = b
		}
		if s > b.Score {
			b.Score = s
		}
		b.Texts = append(b.Texts, d.text)
	}
	out := make([]Scored, 0, len(best))
	for _, b := range best {
		sort.Strings(b.Texts)
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].PayeeID < out[j].PayeeID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
