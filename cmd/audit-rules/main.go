// audit-rules evaluates saved records without contacting YNAB or creating transactions.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	types "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/nathanfredericks/transactions/internal/override"
	"os"
	"time"
)

func main() {
	rulesPath := flag.String("rules", "", "AWS scan JSON backup")
	casesPath := flag.String("cases", "testdata/contract.json", "contract cases")
	flag.Parse()
	raw, e := os.ReadFile(*rulesPath)
	must(e)
	var saved struct {
		Items []map[string]struct {
			S    *string
			NULL bool
		}
	}
	must(json.Unmarshal(raw, &saved))
	items := make([]map[string]types.AttributeValue, 0, len(saved.Items))
	for _, item := range saved.Items {
		values := map[string]types.AttributeValue{}
		for k, v := range item {
			if v.S != nil {
				values[k] = &types.AttributeValueMemberS{Value: *v.S}
			} else if v.NULL {
				values[k] = &types.AttributeValueMemberNULL{Value: true}
			}
		}
		items = append(items, values)
	}
	rules := override.DecodeOverrides(items)
	raw, e = os.ReadFile(*casesPath)
	must(e)
	var fixture struct {
		Cases []struct {
			Merchant       string
			Amount         float64
			Date, Expected string
		}
	}
	must(json.Unmarshal(raw, &fixture))
	zone, e := time.LoadLocation("America/Halifax")
	must(e)
	for _, r := range rules {
		var q any
		must(json.Unmarshal([]byte(r.Query), &q))
		if r.Payee == "" {
			must(fmt.Errorf("%s missing payee", r.Name))
		}
		_, e = override.RenderMemo(r.Memo, "2026-03-31")
		must(e)
	}
	for _, c := range fixture.Cases {
		d, e := time.ParseInLocation("2006-01-02", c.Date, zone)
		must(e)
		r := override.Match(rules, c.Amount, c.Merchant, d)
		name := ""
		if r != nil {
			name = r.Name
		}
		if name != c.Expected {
			must(fmt.Errorf("expected %q got %q for %s %.2f %s", c.Expected, name, c.Merchant, c.Amount, c.Date))
		}
	}
	fmt.Printf("Validated %d live rules and %d matching cases; no transactions written.\n", len(rules), len(fixture.Cases))
}
func must(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
