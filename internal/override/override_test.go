package override

import (
	"encoding/json"
	dt "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/nathanfredericks/transactions/internal/types"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

type ContractCase struct {
	Merchant string
	Amount   float64
	Date     string
	Expected string
	Memo     string
	Category string
	Payee    string
}

func TestAllRuleContracts(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/contract.json")
	if e != nil {
		t.Fatal(e)
	}
	var c struct {
		Rules []types.TransactionOverride
		Cases []ContractCase
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	zone, _ := time.LoadLocation("America/Halifax")
	for _, tc := range c.Cases {
		t.Run(tc.Merchant+tc.Date+tc.Expected, func(t *testing.T) {
			d, _ := time.ParseInLocation("2006-01-02", tc.Date, zone)
			r := Match(c.Rules, tc.Amount, tc.Merchant, d)
			got := ""
			if r != nil {
				got = r.Name
				if r.Memo != tc.Memo || r.Category != tc.Category || r.Payee != tc.Payee {
					t.Fatalf("metadata differs: %+v", r)
				}
			}
			if got != tc.Expected {
				t.Fatalf("got %q want %q", got, tc.Expected)
			}
		})
	}
}
func TestMalformedAndPrecedence(t *testing.T) {
	rules := []types.TransactionOverride{{ID: "bad", Payee: "p", Query: `{"==":[]}`}, {ID: "first", Payee: "p", Query: `{"and":[{"in":["APPLE.COM/BILL",{"var":"merchant"}]},{"==":[{"var":"amount"},"5.69"]}]}`}, {ID: "second", Payee: "p", Query: `true`}}
	r := Match(rules, 5.69, "apple.com/bill", time.Now())
	if r == nil || r.ID != "first" {
		t.Fatalf("%+v", r)
	}
	for _, n := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if Match(rules, n, "APPLE.COM/BILL", time.Now()) != nil {
			t.Fatal("invalid amount matched")
		}
	}
}
func TestDecodeNullAndOrder(t *testing.T) {
	items := []map[string]dt.AttributeValue{{"id": &dt.AttributeValueMemberS{Value: "old"}, "updatedAt": &dt.AttributeValueMemberS{Value: "2024-01-01T00:00:00Z"}, "memo": &dt.AttributeValueMemberNULL{Value: true}}, {"id": &dt.AttributeValueMemberS{Value: "new"}, "updatedAt": &dt.AttributeValueMemberS{Value: "2026-01-01T00:00:00Z"}}}
	r := DecodeOverrides(items)
	if r[0].ID != "new" || r[1].Memo != "" {
		t.Fatal(r)
	}
}
func TestMemoTemplates(t *testing.T) {
	for _, tc := range []struct{ memo, date, want string }{{"plain", "2026-03-31", "plain"}, {"{{.Date}}", "2026-03-31", "2026-03-31"}, {`{{formatDate .Date "January 2006"}}`, "2026-03-31", "March 2026"}, {`{{subtractMonthFromDate .Date}}`, "2026-03-31", "2026-02-28"}, {`{{subtractMonthFromDate .Date}}`, "2024-03-31", "2024-02-29"}, {`{{formatDate (subtractMonthFromDate .Date) "January 2006"}}`, "2026-01-31", "December 2025"}} {
		got, e := RenderMemo(tc.memo, tc.date)
		if e != nil || got != tc.want {
			t.Fatalf("%s: %q %v", tc.memo, got, e)
		}
	}
	for _, m := range []string{"{{date}}", "{{.Missing}}", `{{formatDate .Date "2006"`} {
		if _, e := RenderMemo(m, "2026-01-01"); e == nil {
			t.Fatal("invalid template accepted", m)
		}
	}
	if _, e := RenderMemo(`{{subtractMonthFromDate .Date}}`, "bad"); e == nil {
		t.Fatal("invalid date")
	}
	got, e := RenderMemoTemplate("{{.Date}}", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if e != nil || !strings.HasPrefix(got, "2026") {
		t.Fatal(got, e)
	}
}
