package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
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

func TestNormalizeDescriptor(t *testing.T) {
	for in, want := range map[string]string{
		"MCDONALD'S #4389":     "MCDONALDS",
		"McDonalds 4389":       "MCDONALDS",
		"WAL-MART # 3738":      "WAL MART",
		"  amzn   Mktp CA ":    "AMZN MKTP CA",
		"GRAMMARLY CO*Q2PXCDW": "GRAMMARLY CO",
	} {
		if got := NormalizeDescriptor(in); got != want {
			t.Errorf("NormalizeDescriptor(%q) = %q, want %q", in, got, want)
		}
	}
}

func str(s string) *string { return &s }

func history(examples ...[2]string) []*types.YNABTransaction {
	var out []*types.YNABTransaction
	for i, e := range examples {
		out = append(out, &types.YNABTransaction{ID: fmt.Sprint(i), Approved: true, Date: "2026-10-01", PayeeID: str(e[1]), ImportPayeeName: str(e[0])})
	}
	return out
}

func TestResolvePayeeUsesAgreedMemoryWithoutModel(t *testing.T) {
	stubModel(t, func(string, string, map[string]any) (string, error) {
		t.Fatal("model called despite agreed memory")
		return "", nil
	})
	pc := buildPayeeContext([]*ynabPayee{{ID: "p1", Name: "Clean Name"}}, history([2]string{"RAW DESC #12", "p1"}, [2]string{"RAW DESC #99", "p1"}))
	d, err := ResolvePayee(context.Background(), "raw desc #55", pc)
	if err != nil || d.Choice != (types.PayeeChoice{ID: "p1", Name: "Clean Name"}) || d.Source != "memory" {
		t.Fatal(d, err)
	}
}

// A payee that only repeats the raw descriptor must not win by text match: the
// model sees both candidates and chooses by ID.
func TestResolvePayeeDoesNotPreferRawDescriptorPayee(t *testing.T) {
	payees := []*ynabPayee{{ID: "raw", Name: "SHOP CONVENIENCE ST"}, {ID: "clean", Name: "Shop's"}}
	pc := buildPayeeContext(payees, history([2]string{"SHOP CONVENIENCE ST", "raw"}, [2]string{"SHOPS STORE 2", "clean"}, [2]string{"SHOPS STORE 3", "clean"}))
	var offered []string
	stubModel(t, func(instructions, input string, schema map[string]any) (string, error) {
		offered = schema["properties"].(map[string]any)["payee_id"].(map[string]any)["enum"].([]string)
		if input != "SHOP CONVENIENCE ST" || !strings.Contains(instructions, "clean | Shop's") {
			t.Errorf("model input %q, instructions %s", input, instructions)
		}
		return `{"payee_id":"clean","new_payee_name":""}`, nil
	})
	d, err := ResolvePayee(context.Background(), "SHOP CONVENIENCE ST", pc)
	if err != nil || d.Choice.ID != "clean" || d.Source != "model" {
		t.Fatal(d, err)
	}
	if fmt.Sprint(offered) != "[none raw clean]" && fmt.Sprint(offered) != "[none clean raw]" {
		t.Errorf("candidates offered: %v", offered)
	}
}

func TestResolvePayeeRejectsChoicesOutsideCandidates(t *testing.T) {
	pc := buildPayeeContext([]*ynabPayee{{ID: "p1", Name: "Alpha Store"}}, nil)
	for _, tc := range []struct {
		reply  string
		ok     bool
		choice types.PayeeChoice
	}{
		{`{"payee_id":"p1","new_payee_name":""}`, true, types.PayeeChoice{ID: "p1", Name: "Alpha Store"}},
		{`{"payee_id":"none","new_payee_name":"  Beta   Cafe "}`, true, types.PayeeChoice{Name: "Beta Cafe"}},
		{`{"payee_id":"invented","new_payee_name":""}`, false, types.PayeeChoice{}},
		{`{"payee_id":"none","new_payee_name":""}`, false, types.PayeeChoice{}},
	} {
		stubModel(t, func(string, string, map[string]any) (string, error) {
			// Simulate a provider that ignores the enum constraint.
			var v map[string]any
			_ = json.Unmarshal([]byte(tc.reply), &v)
			return tc.reply, nil
		})
		d, err := ResolvePayee(context.Background(), "ALPHA STORE 12", pc)
		if (err == nil) != tc.ok || d.Choice != tc.choice {
			t.Errorf("reply %s: %+v, %v", tc.reply, d, err)
		}
	}
}

// Renaming every payee and descriptor consistently must not change any
// memory or retrieval decision: nothing may depend on particular names.
func TestPayeeEvidenceIsNameIndependent(t *testing.T) {
	payees := []*ynabPayee{{ID: "a", Name: "Corner Grocer"}, {ID: "b", Name: "Fuel Stop"}, {ID: "c", Name: "Book Nook"}}
	examples := [][2]string{{"CORNER GROCER #12", "a"}, {"CORNER GROCER #19", "a"}, {"FUEL STOP 551", "b"}, {"BOOK NOOK ONLINE", "c"}}
	queries := []string{"CORNER GROCER #44", "FUEL STOP 902", "BOOK NOOK STORE", "UNSEEN PLACE"}

	rng := rand.New(rand.NewSource(1))
	words := map[string]string{}
	rename := func(s string) string {
		var out []string
		for _, w := range strings.Fields(s) {
			if NormalizeDescriptor(w) == "" {
				out = append(out, w)
				continue
			}
			key := strings.ToUpper(w)
			if words[key] == "" {
				b := make([]byte, len(key))
				for i := range b {
					b[i] = byte('A' + rng.Intn(26))
				}
				words[key] = string(b)
			}
			out = append(out, words[key])
		}
		return strings.Join(out, " ")
	}
	run := func(names func(string) string) []string {
		var ps []*ynabPayee
		for _, p := range payees {
			ps = append(ps, &ynabPayee{ID: p.ID, Name: names(p.Name)})
		}
		var ex [][2]string
		for _, e := range examples {
			ex = append(ex, [2]string{names(e[0]), e[1]})
		}
		pc := buildPayeeContext(ps, history(ex...))
		byID := map[string]Payee{}
		for _, p := range pc.Payees {
			byID[p.ID] = p
		}
		var out []string
		for _, q := range queries {
			p, ok := rememberedPayee(names(q), pc, byID)
			top := ""
			if c := retrieveCandidates(names(q), pc, 1); len(c) > 0 {
				top = c[0].payee.ID
			}
			out = append(out, fmt.Sprint(ok, p.ID, top))
		}
		return out
	}
	original, renamed := run(func(s string) string { return s }), run(rename)
	if fmt.Sprint(original) != fmt.Sprint(renamed) {
		t.Fatalf("decisions changed under renaming:\n%v\n%v", original, renamed)
	}
}

func TestPayloadWritesExistingPayeeByID(t *testing.T) {
	p, err := eqTransactionPayload("account", "2026-11-01", -1000, types.PayeeChoice{ID: "p1", Name: "Alpha"}, "", "pending", nil)
	if err != nil || p.PayeeID == nil || *p.PayeeID != "p1" || p.PayeeName != nil {
		t.Fatalf("%+v %v", p, err)
	}
	p, _ = eqTransactionPayload("account", "2026-11-01", -1000, types.PayeeChoice{Name: "New Place"}, "", "pending", nil)
	if p.PayeeID != nil || p.PayeeName == nil || *p.PayeeName != "New Place" {
		t.Fatalf("%+v", p)
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

func TestSimilarDescriptors(t *testing.T) {
	if !SimilarDescriptors("AMZN Mktp CA", "AMZN MKTP CA") || !SimilarDescriptors("STORE #12", "STORE #99") {
		t.Error("same merchant not similar")
	}
	if SimilarDescriptors("ALPHA STORE", "OMEGA CAFE") || SimilarDescriptors("", "X") {
		t.Error("different merchants similar")
	}
}
