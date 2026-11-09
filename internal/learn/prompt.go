package learn

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ModelInput is everything the model sees for one transaction. It holds only
// the bank descriptor, amount, date and the user's own payees and categories.
type ModelInput struct {
	Descriptor string
	Amount     int64
	Date       string
	Account    string
	Candidates []Candidate
	// CandidateCategories is each candidate's usual category name, aligned with Candidates.
	CandidateCategories []string
	Categories          []*Category
	// Recent are categories the user used in the last few days, newest first.
	Recent []*Category
}

// ModelOutput is the model's answer.
type ModelOutput struct {
	PayeeID      string `json:"payee_id"`
	NewPayeeName string `json:"new_payee_name"`
	CategoryID   string `json:"category_id"`
}

const instructions = `You file one bank transaction in the user's YNAB budget.

1. Payee. Return payee_id of the candidate that is the same merchant as the bank descriptor, judging from the candidate's name and the bank descriptors the user already approved for it. The candidates are only lookalikes found by spelling: none of them may be the merchant, and choosing a different merchant is worse than choosing none. If no candidate is the same merchant, return payee_id "none" and set new_payee_name to the merchant's name as a person would write it: its common brand or business name in normal capitalisation, without store numbers, locations, order numbers or payment-processor prefixes. When you choose a candidate, set new_payee_name to "".

2. Category. Return category_id for the category the user would most likely use for this transaction, from the category list. Use the candidates' usual categories only as hints about similar merchants. Categories the user used in the last few days show what the user is doing now; an event or trip category among them may apply to this transaction too.`

// Prompt returns the instructions, the user message and the strict JSON schema.
func Prompt(in ModelInput) (string, string, map[string]any) {
	var b strings.Builder
	sign := "purchase (money out)"
	amount := -in.Amount
	if in.Amount > 0 {
		sign, amount = "credit (money in)", in.Amount
	}
	fmt.Fprintf(&b, "Bank descriptor: %s\nAmount: $%d.%02d %s\nDate: %s\n", in.Descriptor, amount/1000, (amount%1000)/10, sign, in.Date)
	if in.Account != "" {
		fmt.Fprintf(&b, "Account: %s\n", in.Account)
	}
	b.WriteString("\nCandidate payees (id | name | times approved | usual category | bank descriptors approved for it):\n")
	if len(in.Candidates) == 0 {
		b.WriteString("(none)\n")
	}
	ids := []string{"none"}
	for i, c := range in.Candidates {
		cat := ""
		if i < len(in.CandidateCategories) {
			cat = in.CandidateCategories[i]
		}
		fmt.Fprintf(&b, "%s | %s | %d | %s | %s\n", c.ID, c.Name, c.Uses, cat, strings.Join(c.Examples, "; "))
		ids = append(ids, c.ID)
	}
	b.WriteString("\nCategories (id | group: name):\n")
	cats := append([]*Category(nil), in.Categories...)
	sort.Slice(cats, func(i, j int) bool {
		if cats[i].GroupName != cats[j].GroupName {
			return cats[i].GroupName < cats[j].GroupName
		}
		return cats[i].Name < cats[j].Name
	})
	var catIDs []string
	for _, c := range cats {
		fmt.Fprintf(&b, "%s | %s: %s\n", c.ID, c.GroupName, c.Name)
		catIDs = append(catIDs, c.ID)
	}
	if len(in.Recent) > 0 {
		b.WriteString("\nCategories used in the last few days, newest first:\n")
		for _, c := range in.Recent {
			fmt.Fprintf(&b, "%s | %s\n", c.ID, c.Name)
		}
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"payee_id":       map[string]any{"type": "string", "enum": ids},
			"new_payee_name": map[string]any{"type": "string"},
			"category_id":    map[string]any{"type": "string", "enum": catIDs},
		},
		"required":             []string{"payee_id", "new_payee_name", "category_id"},
		"additionalProperties": false,
	}
	return instructions, b.String(), schema
}

// ParseOutput validates a model answer against the offered choices.
func ParseOutput(raw string, in ModelInput) (ModelOutput, error) {
	var out ModelOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, fmt.Errorf("parsing model output: %w", err)
	}
	if out.PayeeID != "none" {
		ok := false
		for _, c := range in.Candidates {
			ok = ok || c.ID == out.PayeeID
		}
		if !ok {
			return out, errors.New("model chose a payee outside the candidates")
		}
		out.NewPayeeName = ""
	} else {
		out.NewPayeeName = strings.Join(strings.Fields(out.NewPayeeName), " ")
		if out.NewPayeeName == "" {
			return out, errors.New("model returned no payee name")
		}
	}
	ok := false
	for _, c := range in.Categories {
		ok = ok || c.ID == out.CategoryID
	}
	if !ok {
		return out, errors.New("model chose a category outside the list")
	}
	return out, nil
}

// Context returns the categories the user could assign on a date (not deleted,
// already in use, not a credit-card payment category) and those used in the
// last three days, newest first.
func (m *Model) Context(date string) (all []*Category, recent []*Category) {
	for id, c := range m.categories {
		if c == nil || c.Deleted || c.GroupName == "Credit Card Payments" || c.GroupName == "Internal Master Category" && c.Name != "Inflow: Ready to Assign" {
			continue
		}
		if m.exists(id, date) || (m.catSeen == nil && !c.Hidden && m.catFirst[id] == "") {
			all = append(all, c)
		}
	}
	from := addDate(date, -3)
	seen := map[string]bool{}
	for i := len(m.catUses) - 1; i >= 0 && len(recent) < 8; i-- {
		u := m.catUses[i]
		if u.date < from {
			break
		}
		if u.date > date || seen[u.category] {
			continue
		}
		if c := m.category(u.category); c != nil {
			seen[u.category] = true
			recent = append(recent, c)
		}
	}
	return all, recent
}

// Input assembles the model input for a query from this model's history.
func (m *Model) Input(q Query, account string, candidates []Candidate) ModelInput {
	all, recent := m.Context(q.Date)
	in := ModelInput{Descriptor: q.Descriptor, Amount: q.Amount, Date: q.Date, Account: account, Candidates: candidates, Categories: all, Recent: recent}
	for _, c := range candidates {
		name := ""
		if id, _ := m.Category(c.ID, q); id != "" {
			if cat := m.category(id); cat != nil {
				name = cat.Name
			}
		}
		in.CandidateCategories = append(in.CandidateCategories, name)
	}
	return in
}
