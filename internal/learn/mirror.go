// Package learn turns the user's own YNAB history into import suggestions.
//
// Everything here is pure: it reads a Mirror of the budget and the importer's
// own suggestion records and never calls YNAB, a bank or a model. The same code
// runs in production and in the offline replay harness, so what the harness
// measures is what ships.
package learn

import (
	"sort"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/ynab"
)

// Payee is a YNAB payee.
type Payee struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	TransferAccountID *string `json:"transfer_account_id"`
	Deleted           bool    `json:"deleted"`
}

// Category is a YNAB category, flattened from its group.
type Category struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GroupID   string `json:"category_group_id"`
	GroupName string `json:"category_group_name,omitempty"`
	Hidden    bool   `json:"hidden"`
	Deleted   bool   `json:"deleted"`
}

// Scheduled is a YNAB scheduled (recurring) transaction.
type Scheduled struct {
	ID                string  `json:"id"`
	DateFirst         string  `json:"date_first"`
	DateNext          string  `json:"date_next"`
	Frequency         string  `json:"frequency"`
	Amount            int64   `json:"amount"`
	Memo              *string `json:"memo"`
	AccountID         string  `json:"account_id"`
	PayeeID           *string `json:"payee_id"`
	CategoryID        *string `json:"category_id"`
	TransferAccountID *string `json:"transfer_account_id"`
	Deleted           bool    `json:"deleted"`
}

// Line is one line of a split.
type Line struct {
	Amount     int64  `json:"amount"`
	CategoryID string `json:"categoryId,omitempty"`
	Memo       string `json:"memo,omitempty"`
	PayeeID    string `json:"payeeId,omitempty"`
}

// Suggestion is what the importer proposed for one YNAB transaction, with the
// evidence behind each field. It is recorded at write time and joined to the
// transaction's final, approved state to produce labels.
type Suggestion struct {
	TransactionID  string    `json:"transactionId"`
	ImportID       string    `json:"importId,omitempty"`
	Bank           string    `json:"bank,omitempty"`
	JobID          string    `json:"jobId,omitempty"`
	AccountID      string    `json:"accountId"`
	Date           string    `json:"date"`
	Amount         int64     `json:"amount"`
	Descriptor     string    `json:"descriptor,omitempty"`
	PayeeID        string    `json:"payeeId,omitempty"`
	PayeeName      string    `json:"payeeName,omitempty"`
	PayeeSource    string    `json:"payeeSource,omitempty"`
	CategoryID     string    `json:"categoryId,omitempty"`
	CategorySource string    `json:"categorySource,omitempty"`
	Memo           string    `json:"memo,omitempty"`
	MemoSource     string    `json:"memoSource,omitempty"`
	Lines          []Line    `json:"lines,omitempty"`
	Model          string    `json:"model,omitempty"`
	Version        string    `json:"version,omitempty"`
	At             time.Time `json:"at"`
}

// Mirror is the budget as read from YNAB. A delta request updates it with only
// what changed since its last read.
type Mirror struct {
	Version      int                          `json:"version"`
	Knowledge    map[string]int64             `json:"knowledge"`
	Transactions map[string]*ynab.Transaction `json:"transactions"`
	Payees       map[string]*Payee            `json:"payees"`
	Categories   map[string]*Category         `json:"categories"`
	Scheduled    map[string]*Scheduled        `json:"scheduled"`
	// Suggestions are the importer's own records, keyed by YNAB transaction ID.
	Suggestions map[string]*Suggestion `json:"suggestions"`
	// CategorySeen is the date each category was first seen in the budget.
	CategorySeen map[string]string `json:"categorySeen"`
}

// NewMirror returns an empty mirror.
func NewMirror() *Mirror {
	return &Mirror{Version: 1, Knowledge: map[string]int64{}, Transactions: map[string]*ynab.Transaction{}, Payees: map[string]*Payee{}, Categories: map[string]*Category{}, Scheduled: map[string]*Scheduled{}, Suggestions: map[string]*Suggestion{}, CategorySeen: map[string]string{}}
}

// Fill replaces nil maps after decoding an older or partial mirror.
func (m *Mirror) Fill() {
	if m.Knowledge == nil {
		m.Knowledge = map[string]int64{}
	}
	if m.Transactions == nil {
		m.Transactions = map[string]*ynab.Transaction{}
	}
	if m.Payees == nil {
		m.Payees = map[string]*Payee{}
	}
	if m.Categories == nil {
		m.Categories = map[string]*Category{}
	}
	if m.Scheduled == nil {
		m.Scheduled = map[string]*Scheduled{}
	}
	if m.Suggestions == nil {
		m.Suggestions = map[string]*Suggestion{}
	}
	if m.CategorySeen == nil {
		m.CategorySeen = map[string]string{}
	}
}

// Pending returns the importer's suggestions for transactions still awaiting
// the user's approval.
func (m *Mirror) Pending() []*Suggestion {
	var out []*Suggestion
	for id, s := range m.Suggestions {
		if t := m.Transactions[id]; t != nil && !t.Deleted && !t.Approved {
			out = append(out, s)
		}
	}
	return out
}

// ApplyTransactions merges a delta. Deleted rows are kept, marked deleted, so a
// suggestion for a removed transaction can still be told apart from a missing one.
func (m *Mirror) ApplyTransactions(rows []*ynab.Transaction) {
	for _, t := range rows {
		if t != nil && t.ID != "" {
			m.Transactions[t.ID] = t
		}
	}
}

// ApplyPayees merges a payee delta.
func (m *Mirror) ApplyPayees(rows []*Payee) {
	for _, p := range rows {
		if p != nil && p.ID != "" {
			m.Payees[p.ID] = p
		}
	}
}

// ApplyCategories merges a category delta. seen dates categories not seen before.
func (m *Mirror) ApplyCategories(rows []*Category, seen string) {
	for _, c := range rows {
		if c != nil && c.ID != "" {
			m.Categories[c.ID] = c
			if _, ok := m.CategorySeen[c.ID]; !ok && seen != "" {
				m.CategorySeen[c.ID] = seen
			}
		}
	}
}

// ApplyScheduled merges a scheduled-transaction delta.
func (m *Mirror) ApplyScheduled(rows []*Scheduled) {
	for _, s := range rows {
		if s != nil && s.ID != "" {
			m.Scheduled[s.ID] = s
		}
	}
}

// Example is one labelled transaction: what the bank said and what the user
// finally approved.
type Example struct {
	ID         string
	Date       string
	Amount     int64
	AccountID  string
	Descriptor string
	PayeeID    string
	CategoryID string
	Memo       string
	Lines      []Line
	Suggestion *Suggestion
}

// Split reports whether the example is a split transaction.
func (e Example) Split() bool { return len(e.Lines) > 0 }

func str(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// Descriptor returns the bank text for a transaction. The importer's own record
// wins because, once a payee is written by ID, YNAB may keep no import payee name
// or keep the chosen name instead of what the bank sent.
func Descriptor(t *ynab.Transaction, s *Suggestion) string {
	if s != nil && strings.TrimSpace(s.Descriptor) != "" {
		return strings.TrimSpace(s.Descriptor)
	}
	if d := str(t.ImportPayeeNameOriginal); d != "" {
		return d
	}
	return str(t.ImportPayeeName)
}

// Examples returns every approved, non-transfer transaction with a payee, oldest
// first. Transfers and transactions written by excluded payees (such as the
// reconciliation adjustment payee) are not labels.
func (m *Mirror) Examples(excludedPayees ...string) []Example {
	excluded := map[string]bool{}
	for _, id := range excludedPayees {
		excluded[id] = true
	}
	var out []Example
	for _, t := range m.Transactions {
		if t == nil || t.Deleted || !t.Approved || t.TransferAccountID != nil || t.PayeeID == nil || excluded[*t.PayeeID] {
			continue
		}
		p := m.Payees[*t.PayeeID]
		if p == nil || p.Deleted || p.TransferAccountID != nil {
			continue
		}
		s := m.Suggestions[t.ID]
		e := Example{ID: t.ID, Date: t.Date, Amount: t.Amount, AccountID: t.AccountID, Descriptor: Descriptor(t, s), PayeeID: *t.PayeeID, Memo: str(t.Memo), Suggestion: s}
		var lines []Line
		for _, sub := range t.Subtransactions {
			if sub.Deleted {
				continue
			}
			lines = append(lines, Line{Amount: sub.Amount, CategoryID: str(sub.CategoryID), Memo: str(sub.Memo), PayeeID: str(sub.PayeeID)})
		}
		if len(lines) > 0 {
			e.Lines = lines
		} else {
			e.CategoryID = str(t.CategoryID)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ID < out[j].ID
	})
	return out
}
