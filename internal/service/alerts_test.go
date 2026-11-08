package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nathanfredericks/transactions/internal/types"
)

const rogersFooter = " https://rogersbank.com/en/help_centre_answers/managing_your_account#q174 Thank you for choosing Rogers Bank. This is an automated message."

func TestParseAlertLayouts(t *testing.T) {
	for _, tc := range []struct {
		text, descriptor, date, source string
		amount                         int64
	}{
		{"$16.68 on Nov 07, 2026 on your credit card ending in 0000. Details: WAL-MART # 3738." + rogersFooter, "WAL-MART # 3738", "2026-11-07", "parser:rogers", 16680},
		{"A recurring payment of $9.29 on Oct 29, 2026 on your credit card ending in 0000. Details: OPENROUTER, INC." + rogersFooter, "OPENROUTER, INC", "2026-10-29", "parser:rogers", 9290},
		{"$1,124.94 on Oct 19, 2026 on your credit card ending in 0000. Details: WALMART.CA." + rogersFooter, "WALMART.CA", "2026-10-19", "parser:rogers", 1124940},
		{"$18.10 on Oct 19, 2026 on your credit card ending in 0000. Details: CashBack / Remises." + rogersFooter, "CashBack / Remises", "2026-10-19", "parser:rogers", 18100},
		{"https://www.bmo.com/en-ca/main/personal/ New activity on your credit card There's been a transaction on your credit card for: Amount: $18.54 Credit card: Ending in 0000 At: IC* INSTACART*30 Sign in to review your updated balance", "IC* INSTACART*30", "", "parser:bmo", 18540},
	} {
		got, ok := ParseAlert(tc.text)
		if !ok || got.Amount != tc.amount || got.Descriptor != tc.descriptor || got.Date != tc.date || got.Source != tc.source {
			t.Errorf("ParseAlert(%.40q) = %+v, %v", tc.text, got, ok)
		}
	}
	if _, ok := ParseAlert("Your statement is ready."); ok {
		t.Error("unrelated text parsed as an alert")
	}
	twice := "$1.00 on Nov 07, 2026 on your credit card ending in 0000. Details: A." + rogersFooter + " $2.00 on Nov 07, 2026 on your credit card ending in 0000. Details: B." + rogersFooter
	if _, ok := ParseAlert(twice); ok {
		t.Error("ambiguous alert parsed")
	}
}

func stubModel(t *testing.T, reply func(instructions, input string, schema map[string]any) (string, error)) {
	t.Helper()
	old := callModel
	callModel = func(_ context.Context, instructions, input, _ string, schema map[string]any) (string, error) {
		return reply(instructions, input, schema)
	}
	t.Cleanup(func() { callModel = old })
}

func TestExtractAlertChecksModelOutputAgainstText(t *testing.T) {
	text := "Card purchase of CAD 12.34 at Example Merchant 55 was approved."
	for _, tc := range []struct {
		reply string
		ok    bool
	}{
		{`{"amount":"12.34","descriptor":"Example Merchant 55"}`, true},
		{`{"amount":"12.34","descriptor":"Example Merchant"}`, true},
		{`{"amount":"12.34","descriptor":"Different Merchant"}`, false},
		{`{"amount":"21.43","descriptor":"Example Merchant 55"}`, false},
		{`{"amount":"0.00","descriptor":"Example Merchant 55"}`, false},
		{`not json`, false},
	} {
		stubModel(t, func(string, string, map[string]any) (string, error) { return tc.reply, nil })
		got, err := ExtractAlert(context.Background(), text)
		if (err == nil) != tc.ok {
			t.Errorf("reply %s: got %+v, %v", tc.reply, got, err)
		}
		if tc.ok && (got.Amount != 12340 || got.Source != "model") {
			t.Errorf("reply %s: got %+v", tc.reply, got)
		}
	}
	stubModel(t, func(string, string, map[string]any) (string, error) { return "", errors.New("provider down") })
	if _, err := ExtractAlert(context.Background(), text); err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Errorf("provider error not kept: %v", err)
	}
}

func TestExtractAlertParsesKnownLayoutsWithoutModel(t *testing.T) {
	stubModel(t, func(string, string, map[string]any) (string, error) {
		t.Fatal("model called for a recognized layout")
		return "", nil
	})
	if _, err := ExtractAlert(context.Background(), "$3.90 on Nov 05, 2026 on your credit card ending in 0000. Details: SH Vending."+rogersFooter); err != nil {
		t.Fatal(err)
	}
}

func str(s string) *string { return &s }

func TestPayloadWritesExistingPayeeByID(t *testing.T) {
	p, err := eqTransactionPayload("account", "2026-11-01", -1000, types.Enrichment{Payee: types.PayeeChoice{ID: "p1", Name: "Alpha"}, CategoryID: "c1", Memo: "Plan"}, "", "pending", nil)
	if err != nil || p.PayeeID == nil || *p.PayeeID != "p1" || p.PayeeName != nil || p.CategoryID == nil || *p.CategoryID != "c1" || p.Memo == nil || *p.Memo != "Plan" {
		t.Fatalf("%+v %v", p, err)
	}
	p, _ = eqTransactionPayload("account", "2026-11-01", -1000, types.Enrichment{Payee: types.PayeeChoice{Name: "New Place"}}, "", "pending", nil)
	if p.PayeeID != nil || p.PayeeName == nil || *p.PayeeName != "New Place" || p.CategoryID != nil || p.Memo != nil {
		t.Fatalf("an empty category and memo are left for YNAB: %+v", p)
	}
	// A rule still overrides every learned field.
	p, _ = eqTransactionPayload("account", "2026-11-01", -1000, types.Enrichment{Payee: types.PayeeChoice{ID: "p1"}, CategoryID: "c1", Memo: "Plan"}, "", "pending", &types.TransactionOverride{Payee: "p2", Category: "c2"})
	if *p.PayeeID != "p2" || *p.CategoryID != "c2" || p.Memo != nil {
		t.Fatalf("rule: %+v", p)
	}
}

func TestAmountOnlyPayloadHasNoIdentityOrEnrichment(t *testing.T) {
	b, _ := json.Marshal(amountOnlyPayload("acct", "2026-11-06", -12340))
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	for _, field := range []string{"import_id", "payee_id", "payee_name", "category_id", "memo"} {
		if _, ok := got[field]; ok {
			t.Errorf("amount-only payload sets %s", field)
		}
	}
	if got["approved"] != false || got["cleared"] != "uncleared" || got["amount"] != float64(-12340) {
		t.Errorf("unexpected payload %s", b)
	}
}
