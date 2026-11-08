package learn

import (
	"math"
	"strings"
	"testing"

	"github.com/nathanfredericks/transactions/internal/types"
)

func ptr(s string) *string { return &s }

func fixture() (map[string]*Payee, map[string]*Category) {
	payees := map[string]*Payee{
		"p-coffee": {ID: "p-coffee", Name: "Bean Counter"},
		"p-shop":   {ID: "p-shop", Name: "Corner Shop"},
		"p-cloud":  {ID: "p-cloud", Name: "Cloud Host"},
	}
	cats := map[string]*Category{
		"c-eat":   {ID: "c-eat", Name: "Eating Out", GroupName: "Frequent"},
		"c-groc":  {ID: "c-groc", Name: "Groceries", GroupName: "Frequent"},
		"c-subs":  {ID: "c-subs", Name: "Monthly Subscriptions", GroupName: "Bills"},
		"c-fest1": {ID: "c-fest1", Name: "Festival 2025", GroupName: "Events"},
		"c-fest2": {ID: "c-fest2", Name: "Festival 2026", GroupName: "Events"},
	}
	return payees, cats
}

func TestNormalizeIgnoresCaseDigitsAndPunctuation(t *testing.T) {
	for in, want := range map[string]string{
		"MCDONALD'S #4389":     "MCDONALDS",
		"McDonalds 4389":       "MCDONALDS",
		"WAL-MART # 3738":      "WAL MART",
		"  amzn   Mktp CA ":    "AMZN MKTP CA",
		"GRAMMARLY CO*Q2PXCDW": "GRAMMARLY CO",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilar(t *testing.T) {
	if !Similar("AMZN Mktp CA", "AMZN MKTP CA") || !Similar("STORE #12", "STORE #99") {
		t.Error("same merchant not similar")
	}
	if Similar("ALPHA STORE", "OMEGA CAFE") || Similar("", "X") {
		t.Error("different merchants similar")
	}
}

func TestMemoryDecidesOnlyFromApprovedHistory(t *testing.T) {
	payees, cats := fixture()
	h := []Example{
		{Date: "2026-01-01", Descriptor: "BEAN COUNTER 12", PayeeID: "p-coffee", CategoryID: "c-eat", Amount: -4000},
		{Date: "2026-01-05", Descriptor: "BEAN COUNTER 99", PayeeID: "p-coffee", CategoryID: "c-eat", Amount: -4500},
	}
	m := Build(h, payees, cats, nil, nil, DefaultOptions())
	r, ok := m.Payee("Bean Counter #7", "2026-02-01")
	if !ok || r.ID != "p-coffee" || r.Source != "memory" {
		t.Fatalf("memory: %+v %v", r, ok)
	}
	if _, ok := m.Payee("BEAN COUNTER ROASTERY", "2026-02-01"); ok {
		t.Fatal("a different descriptor must be left to the model")
	}
	cands := m.Candidates("BEAN COUNTER ROASTERY", 5)
	if len(cands) == 0 || cands[0].ID != "p-coffee" {
		t.Fatalf("retrieval should rank the lookalike first: %+v", cands)
	}
}

func TestMajorityMemoryNeedsAClearShare(t *testing.T) {
	payees, cats := fixture()
	h := []Example{
		{Date: "2026-01-01", Descriptor: "CORNER", PayeeID: "p-shop"},
		{Date: "2026-01-02", Descriptor: "CORNER", PayeeID: "p-coffee"},
	}
	m := Build(h, payees, cats, nil, nil, DefaultOptions())
	if _, ok := m.Payee("CORNER", "2026-01-03"); ok {
		t.Fatal("an even split must not decide")
	}
	h = append(h, Example{Date: "2026-01-02", Descriptor: "CORNER", PayeeID: "p-shop"}, Example{Date: "2026-01-02", Descriptor: "CORNER", PayeeID: "p-shop"}, Example{Date: "2026-01-02", Descriptor: "CORNER", PayeeID: "p-shop"})
	m = Build(h, payees, cats, nil, nil, DefaultOptions())
	if r, ok := m.Payee("CORNER", "2026-01-03"); !ok || r.ID != "p-shop" {
		t.Fatalf("a clear majority decides: %+v %v", r, ok)
	}
}

func TestPendingNewNameIsReusedForTheSameDescriptor(t *testing.T) {
	payees, cats := fixture()
	m := Build(nil, payees, cats, nil, []*Suggestion{{Descriptor: "NEW PLACE 22", PayeeName: "New Place"}}, DefaultOptions())
	r, ok := m.Payee("NEW PLACE 31", "2026-01-01")
	if !ok || r.Name != "New Place" || r.ID != "" || r.Source != "pending" {
		t.Fatalf("pending: %+v %v", r, ok)
	}
}

func TestCategoryWeighsAmountAndRecency(t *testing.T) {
	payees, cats := fixture()
	h := []Example{
		{Date: "2025-06-01", Descriptor: "CORNER", PayeeID: "p-shop", CategoryID: "c-groc", Amount: -80000},
		{Date: "2025-07-01", Descriptor: "CORNER", PayeeID: "p-shop", CategoryID: "c-groc", Amount: -90000},
		{Date: "2026-01-01", Descriptor: "CORNER", PayeeID: "p-shop", CategoryID: "c-eat", Amount: -3000},
	}
	m := Build(h, payees, cats, nil, nil, DefaultOptions())
	if c, _ := m.Category("p-shop", Query{Amount: -85000, Date: "2026-01-10"}); c != "c-groc" {
		t.Fatalf("a large basket follows past large baskets, got %s", c)
	}
	if c, _ := m.Category("p-shop", Query{Amount: -3500, Date: "2026-01-10"}); c != "c-eat" {
		t.Fatalf("a small purchase follows past small purchases, got %s", c)
	}
	if def := m.YNABDefault("p-shop"); def != "c-eat" {
		t.Fatalf("YNAB default is the most recent category, got %s", def)
	}
}

func TestEditionMovesToTheNewestYear(t *testing.T) {
	payees, cats := fixture()
	h := []Example{
		{Date: "2025-08-01", Descriptor: "TICKETS", PayeeID: "p-shop", CategoryID: "c-fest1", Amount: -10000},
		{Date: "2026-03-01", Descriptor: "OTHER", PayeeID: "p-coffee", CategoryID: "c-fest2", Amount: -5000},
	}
	m := Build(h, payees, cats, nil, nil, DefaultOptions())
	if c, src := m.Category("p-shop", Query{Amount: -10000, Date: "2026-04-01"}); c != "c-fest2" || src != "edition" {
		t.Fatalf("got %s (%s)", c, src)
	}
	// Before the new edition exists, the old one stays.
	if c, _ := m.Category("p-shop", Query{Amount: -10000, Date: "2026-02-01"}); c != "c-fest1" {
		t.Fatalf("got %s", c)
	}
}

func TestMemoNeedsAgreementAtTheSameAmount(t *testing.T) {
	payees, cats := fixture()
	h := []Example{
		{Date: "2026-01-01", Descriptor: "CLOUD", PayeeID: "p-cloud", CategoryID: "c-subs", Amount: -9990, Memo: "Storage plan"},
		{Date: "2026-02-01", Descriptor: "CLOUD", PayeeID: "p-cloud", CategoryID: "c-subs", Amount: -9990, Memo: "Storage plan"},
		{Date: "2026-02-03", Descriptor: "CLOUD", PayeeID: "p-cloud", CategoryID: "c-subs", Amount: -1990, Memo: "Domain"},
		{Date: "2026-02-04", Descriptor: "CLOUD", PayeeID: "p-cloud", CategoryID: "c-subs", Amount: -1990, Memo: "Backup"},
	}
	m := Build(h, payees, cats, nil, nil, DefaultOptions())
	if memo, _ := m.Memo("p-cloud", Query{Amount: -9990, Date: "2026-03-01"}); memo != "Storage plan" {
		t.Fatalf("got %q", memo)
	}
	if memo, _ := m.Memo("p-cloud", Query{Amount: -1990, Date: "2026-03-01"}); memo != "" {
		t.Fatalf("disagreeing memos give none, got %q", memo)
	}
	if memo, _ := m.Memo("p-cloud", Query{Amount: -5000, Date: "2026-03-01"}); memo != "" {
		t.Fatalf("an unseen amount gives none, got %q", memo)
	}
}

func TestExamplesPreferTheImportersOwnDescriptor(t *testing.T) {
	m := NewMirror()
	m.ApplyPayees([]*Payee{{ID: "p1", Name: "Shop"}})
	m.ApplyTransactions([]*types.YNABTransaction{
		{ID: "t1", Date: "2026-01-01", Approved: true, PayeeID: ptr("p1"), ImportPayeeName: ptr("Shop"), Amount: -1000},
		{ID: "t2", Date: "2026-01-02", Approved: false, PayeeID: ptr("p1"), Amount: -1000},
		{ID: "t3", Date: "2026-01-03", Approved: true, PayeeID: ptr("p1"), Amount: -2000, Subtransactions: []types.YNABSubtransaction{{Amount: -1500, CategoryID: ptr("c1")}, {Amount: -500, CategoryID: ptr("c2"), Deleted: true}}},
	})
	m.Suggestions["t1"] = &Suggestion{Descriptor: "SHOP 123 TOWN"}
	ex := m.Examples()
	if len(ex) != 2 {
		t.Fatalf("unapproved transactions are not labels: %d", len(ex))
	}
	if ex[0].Descriptor != "SHOP 123 TOWN" {
		t.Fatalf("got %q", ex[0].Descriptor)
	}
	if !ex[1].Split() || len(ex[1].Lines) != 1 {
		t.Fatalf("deleted split lines are dropped: %+v", ex[1].Lines)
	}
}

func TestPromptAndParseOnlyAcceptOfferedChoices(t *testing.T) {
	in := ModelInput{Descriptor: "X", Amount: -1000, Date: "2026-01-01", Candidates: []Candidate{{ID: "p1", Name: "One"}}, Categories: []*Category{{ID: "c1", Name: "Food", GroupName: "G"}}}
	_, msg, schema := Prompt(in)
	if !strings.Contains(msg, "p1 | One") || !strings.Contains(msg, "$1.00 purchase") {
		t.Fatalf("message: %s", msg)
	}
	enum := schema["properties"].(map[string]any)["payee_id"].(map[string]any)["enum"].([]string)
	if len(enum) != 2 || enum[0] != "none" {
		t.Fatalf("enum: %v", enum)
	}
	if _, err := ParseOutput(`{"payee_id":"p9","new_payee_name":"","category_id":"c1"}`, in); err == nil {
		t.Fatal("a payee outside the candidates must be rejected")
	}
	if _, err := ParseOutput(`{"payee_id":"none","new_payee_name":"  ","category_id":"c1"}`, in); err == nil {
		t.Fatal("none needs a name")
	}
	out, err := ParseOutput(`{"payee_id":"none","new_payee_name":" New   Place ","category_id":"c1"}`, in)
	if err != nil || out.NewPayeeName != "New Place" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestStatistics(t *testing.T) {
	r := Wilson(90, 100)
	if math.Abs(r.P-0.9) > 1e-9 || r.Low > 0.83 || r.Low < 0.82 || r.High < 0.94 || r.High > 0.95 {
		t.Fatalf("wilson: %+v", r)
	}
	if p := McNemar(10, 0); p > 0.01 {
		t.Fatalf("10 vs 0 should be significant: %f", p)
	}
	if p := McNemar(5, 5); p != 1 {
		t.Fatalf("balanced discordance: %f", p)
	}
}
