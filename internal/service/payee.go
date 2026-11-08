package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/types"
)

// Payee is an existing, non-transfer YNAB payee with its approved usage count.
type Payee struct {
	ID   string
	Name string
	Uses int
}

// PayeeExample is a bank descriptor whose approved YNAB transaction used PayeeID.
type PayeeExample struct {
	Descriptor string
	PayeeID    string
	Date       string
}

// PayeeContext is the evidence a payee decision may use. It comes only from the
// user's own YNAB payees and approved history.
type PayeeContext struct {
	Payees   []Payee
	Examples []PayeeExample
}

// PayeeDecision records how a payee was chosen, for logs and later evaluation.
type PayeeDecision struct {
	Choice     types.PayeeChoice
	Source     string // "memory", "model" or "fallback"
	Candidates int
}

var callModel = callResponsesAPI

const (
	historyYears    = 2
	maxCandidates   = 20
	examplesPerSlot = 3
)

// LoadPayeeContext reads payees and approved history from YNAB.
func LoadPayeeContext(ctx context.Context) (*PayeeContext, error) {
	client, err := newYNABClient(ctx)
	if err != nil {
		return nil, err
	}
	var payees struct {
		Data struct {
			Payees []*ynabPayee `json:"payees"`
		} `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, fmt.Sprintf("/budgets/%s/payees", client.budgetID), nil, &payees); err != nil {
		return nil, fmt.Errorf("fetching payees: %w", err)
	}
	var history struct {
		Data struct {
			Transactions []*types.YNABTransaction `json:"transactions"`
		} `json:"data"`
	}
	since := time.Now().AddDate(-historyYears, 0, 0).Format("2006-01-02")
	if err := client.do(ctx, http.MethodGet, fmt.Sprintf("/budgets/%s/transactions?since_date=%s", client.budgetID, since), nil, &history); err != nil {
		return nil, fmt.Errorf("fetching payee history: %w", err)
	}
	return buildPayeeContext(payees.Data.Payees, history.Data.Transactions), nil
}

func buildPayeeContext(payees []*ynabPayee, history []*types.YNABTransaction) *PayeeContext {
	valid := map[string]bool{}
	for _, p := range payees {
		if p != nil && !p.Deleted && p.TransferAccountID == nil {
			valid[p.ID] = true
		}
	}
	uses := map[string]int{}
	pc := &PayeeContext{}
	for _, tx := range history {
		if tx == nil || tx.Deleted || !tx.Approved || tx.TransferAccountID != nil || tx.PayeeID == nil || !valid[*tx.PayeeID] {
			continue
		}
		uses[*tx.PayeeID]++
		if d := importDescriptor(tx); d != "" {
			pc.Examples = append(pc.Examples, PayeeExample{Descriptor: d, PayeeID: *tx.PayeeID, Date: tx.Date})
		}
	}
	for _, p := range payees {
		if p != nil && valid[p.ID] {
			pc.Payees = append(pc.Payees, Payee{ID: p.ID, Name: p.Name, Uses: uses[p.ID]})
		}
	}
	return pc
}

func importDescriptor(tx *types.YNABTransaction) string {
	for _, s := range []*string{tx.ImportPayeeNameOriginal, tx.ImportPayeeName} {
		if s != nil && strings.TrimSpace(*s) != "" {
			return strings.TrimSpace(*s)
		}
	}
	return ""
}

var nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)

// NormalizeDescriptor reduces a descriptor to a comparison key that ignores case,
// punctuation, spacing, and digit-bearing tokens such as store or order numbers.
func NormalizeDescriptor(s string) string {
	s = strings.ReplaceAll(strings.ToUpper(s), "'", "")
	var words []string
	for _, w := range strings.Fields(nonAlnum.ReplaceAllString(s, " ")) {
		if !strings.ContainsAny(w, "0123456789") {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// ResolvePayee chooses the payee for a bank descriptor.
//
// Approved history that agrees on one payee for this descriptor decides it.
// Otherwise the payees most similar to the descriptor (by name and by the
// descriptors already approved for them) are offered to the model, which picks
// one by ID or says none applies and names a new payee. A payee whose name
// merely repeats the raw descriptor is never preferred for that reason alone.
func ResolvePayee(ctx context.Context, descriptor string, pc *PayeeContext) (PayeeDecision, error) {
	byID := map[string]Payee{}
	for _, p := range pc.Payees {
		byID[p.ID] = p
	}
	if p, ok := rememberedPayee(descriptor, pc, byID); ok {
		return PayeeDecision{Choice: types.PayeeChoice{ID: p.ID, Name: p.Name}, Source: "memory"}, nil
	}
	candidates := retrieveCandidates(descriptor, pc, maxCandidates)
	ids := []string{"none"}
	for _, c := range candidates {
		ids = append(ids, c.payee.ID)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"payee_id":       map[string]any{"type": "string", "enum": ids},
			"new_payee_name": map[string]any{"type": "string"},
		},
		"required":             []string{"payee_id", "new_payee_name"},
		"additionalProperties": false,
	}
	raw, err := callModel(ctx, payeeInstructions(candidates), descriptor, "payee_choice", schema)
	if err != nil {
		return PayeeDecision{}, fmt.Errorf("calling AI for payee choice: %w", err)
	}
	var result struct {
		PayeeID      string `json:"payee_id"`
		NewPayeeName string `json:"new_payee_name"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return PayeeDecision{}, fmt.Errorf("parsing payee choice: %w", err)
	}
	decision := PayeeDecision{Source: "model", Candidates: len(candidates)}
	if result.PayeeID != "none" {
		for _, c := range candidates {
			if c.payee.ID == result.PayeeID {
				decision.Choice = types.PayeeChoice{ID: c.payee.ID, Name: c.payee.Name}
				return decision, nil
			}
		}
		return PayeeDecision{}, fmt.Errorf("model chose payee %q outside the candidates", result.PayeeID)
	}
	name := strings.Join(strings.Fields(result.NewPayeeName), " ")
	if name == "" {
		return PayeeDecision{}, fmt.Errorf("model returned no payee")
	}
	decision.Choice = types.PayeeChoice{Name: name}
	return decision, nil
}

// FallbackPayee keeps the bank descriptor when no decision could be made, so a
// valid import still lands in YNAB for review.
func FallbackPayee(descriptor string) PayeeDecision {
	return PayeeDecision{Choice: types.PayeeChoice{Name: descriptor}, Source: "fallback"}
}

// rememberedPayee returns the payee every approved example of this descriptor
// agrees on. Disagreement, or agreement only on a payee that just repeats the
// descriptor, leaves the decision to the model.
func rememberedPayee(descriptor string, pc *PayeeContext, byID map[string]Payee) (Payee, bool) {
	key := NormalizeDescriptor(descriptor)
	if key == "" {
		return Payee{}, false
	}
	var found Payee
	for _, ex := range pc.Examples {
		if NormalizeDescriptor(ex.Descriptor) != key {
			continue
		}
		p, ok := byID[ex.PayeeID]
		if !ok {
			continue
		}
		if found.ID != "" && found.ID != p.ID {
			return Payee{}, false
		}
		found = p
	}
	if found.ID == "" || NormalizeDescriptor(found.Name) == key {
		return Payee{}, false
	}
	return found, true
}

type candidate struct {
	payee    Payee
	score    float64
	examples []string
}

// retrieveCandidates ranks payees by character-trigram similarity between the
// descriptor and each payee's name or approved descriptors.
func retrieveCandidates(descriptor string, pc *PayeeContext, n int) []candidate {
	target := trigrams(NormalizeDescriptor(descriptor))
	if len(target) == 0 {
		return nil
	}
	byID := map[string]*candidate{}
	for _, p := range pc.Payees {
		byID[p.ID] = &candidate{payee: p, score: jaccard(target, trigrams(NormalizeDescriptor(p.Name)))}
	}
	seen := map[string]bool{}
	for _, ex := range pc.Examples {
		c := byID[ex.PayeeID]
		key := ex.PayeeID + "\x00" + NormalizeDescriptor(ex.Descriptor)
		if c == nil || seen[key] {
			continue
		}
		seen[key] = true
		if s := jaccard(target, trigrams(NormalizeDescriptor(ex.Descriptor))); s > c.score {
			c.score = s
		}
		if len(c.examples) < examplesPerSlot {
			c.examples = append(c.examples, ex.Descriptor)
		}
	}
	var ranked []candidate
	for _, c := range byID {
		if c.score > 0 {
			ranked = append(ranked, *c)
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].payee.Uses != ranked[j].payee.Uses {
			return ranked[i].payee.Uses > ranked[j].payee.Uses
		}
		return ranked[i].payee.ID < ranked[j].payee.ID
	})
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	return ranked
}

func payeeInstructions(candidates []candidate) string {
	var b strings.Builder
	b.WriteString("You choose the YNAB payee for a bank transaction descriptor (the user message).\n\n")
	if len(candidates) == 0 {
		b.WriteString("No existing payee resembles this descriptor.\n")
	} else {
		b.WriteString("Candidate existing payees, as id | name | times the user approved it | bank descriptors the user approved for it:\n")
		for _, c := range candidates {
			fmt.Fprintf(&b, "%s | %s | %d | %s\n", c.payee.ID, c.payee.Name, c.payee.Uses, strings.Join(c.examples, "; "))
		}
	}
	b.WriteString("\nReturn payee_id for the candidate that is the same merchant as the descriptor. " +
		"Prefer a candidate whose approved descriptors match, then one the user approves often. " +
		"Some payees just copy a raw bank descriptor; do not choose one only because its text matches when another candidate is the same merchant with a cleaner name. " +
		"Processor or platform prefixes before an asterisk can name either the processor or the merchant; decide from the evidence. " +
		"If no candidate is the same merchant, return payee_id \"none\" and new_payee_name: a concise Title Case merchant name without store numbers, locations, order numbers or processor prefixes. " +
		"When you choose a candidate, set new_payee_name to an empty string.")
	return b.String()
}

func trigrams(s string) map[string]bool {
	r := []rune(" " + s + " ")
	out := map[string]bool{}
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// SimilarDescriptors reports whether two descriptors name the same merchant
// closely enough to treat a bank credit as the source of an alert.
func SimilarDescriptors(a, b string) bool {
	na, nb := NormalizeDescriptor(a), NormalizeDescriptor(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || jaccard(trigrams(na), trigrams(nb)) >= 0.6
}
