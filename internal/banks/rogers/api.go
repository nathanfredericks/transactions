package rogers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/nathanfredericks/transactions/internal/bank"
	"net/url"
	"regexp"
	"time"
)

type Adapter struct{ Dependencies bank.Dependencies }
type auth struct {
	DeviceStorage map[string]string `json:"deviceStorage,omitempty"`
	AccountID     string            `json:"accountId"`
	CustomerID    string            `json:"customerId"`
}

func (a *Adapter) Renew(ctx context.Context, s bank.Session) (bank.Session, error) {
	var c auth
	_ = json.Unmarshal(s.Auth, &c)
	if c.AccountID == "" || c.CustomerID == "" || s.Headers["accesstoken"] == "" || s.Headers["refreshtoken"] == "" || s.Headers["deviceid"] == "" {
		return s, bank.Fail(bank.Authentication, "renew")
	}
	b, _ := json.Marshal(map[string]string{"accountId": c.AccountID, "customerId": c.CustomerID, "deviceId": s.Headers["deviceid"], "channel": s.Headers["channel"], "accesstoken": s.Headers["accesstoken"], "refreshtoken": s.Headers["refreshtoken"], "flow": "PATH_REGEN_TOKEN_API"})
	var out struct {
		Status json.RawMessage `json:"status"`
	}
	e := bank.NewHTTP(&s).JSON(ctx, "POST", "https://selfserve.apis.rogersbank.com/v1/authenticate/regeneratetoken/", bank.Headers(s, map[string]string{"content-type": "application/json"}), b, &out)
	bank.TokenTiming(&s)
	if e == nil && string(bytes.Trim(out.Status, `"`)) == "440" {
		e = bank.Fail(bank.Authentication, "renew")
	}
	if e == nil && !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(time.Now()) {
		e = bank.Fail(bank.Authentication, "renew")
	}
	return s, e
}
func (a *Adapter) Fetch(ctx context.Context, s bank.Session, r bank.FetchRequest) (bank.Snapshot, bank.Session, error) {
	out := bank.Snapshot{At: time.Now()}
	var c auth
	if json.Unmarshal(s.Auth, &c) != nil || !regexp.MustCompile(`^\d+$`).MatchString(c.AccountID) || !regexp.MustCompile(`^\d+$`).MatchString(c.CustomerID) {
		return out, s, bank.Fail(bank.Invalid, "account-identity")
	}
	h := bank.NewHTTP(&s)
	base := "https://selfserve.apis.rogersbank.com/corebank/v1/account/" + c.AccountID + "/customer/" + c.CustomerID
	var detail struct {
		Account struct {
			ID      string `json:"accountId"`
			Name    string `json:"productName"`
			Balance struct {
				Value json.Number `json:"value"`
			} `json:"currentBalance"`
			Customer struct {
				ID    string `json:"customerId"`
				Last4 string `json:"cardLast4"`
			} `json:"customer"`
		} `json:"accountDetail"`
	}
	if e := h.JSON(ctx, "GET", base+"/detail", s.Headers, nil, &detail); e != nil {
		return out, s, e
	}
	d := detail.Account
	if d.ID != c.AccountID || d.Customer.ID != c.CustomerID {
		return out, s, bank.Fail(bank.Invalid, "account-identity")
	}
	balance, e := bank.Milliunits(string(d.Balance.Value))
	if e != nil {
		return out, s, e
	}
	id := bank.AccountID(d.ID)
	out.Accounts = []bank.Account{{ID: id, Number: d.ID, Name: d.Name + " (" + d.Customer.Last4 + ")", Balance: -balance}}
	if r.AccountsOnly {
		return out, s, nil
	}
	zone, _ := time.LoadLocation("America/Halifax")
	now := time.Now().In(zone)
	q := url.Values{"fromDate": {now.AddDate(0, 0, -10).Format("2006-01-02")}, "toDate": {now.Format("2006-01-02")}}
	var data struct {
		Summary *struct {
			Rows []struct {
				Amount struct {
					Value json.Number `json:"value"`
				} `json:"amount"`
				Date     string `json:"date"`
				Posted   string `json:"postedDate"`
				Merchant struct {
					Name string `json:"name"`
				} `json:"merchant"`
			} `json:"activities"`
		} `json:"activitySummary"`
	}
	if e = h.JSON(ctx, "GET", base+"/transactions?"+q.Encode(), s.Headers, nil, &data); e != nil {
		return out, s, e
	}
	if data.Summary == nil || data.Summary.Rows == nil {
		return out, s, bank.Fail(bank.Invalid, "activity-summary")
	}
	occ := map[string]int{}
	for _, row := range data.Summary.Rows {
		if row.Posted == "" {
			continue
		}
		amount, e := bank.Milliunits(string(row.Amount.Value))
		if e != nil {
			return out, s, e
		}
		amount = -amount
		date := row.Date
		if amount > 0 {
			date = row.Posted
		}
		if _, e = time.Parse("2006-01-02", date); e != nil || row.Merchant.Name == "" {
			return out, s, bank.Fail(bank.Invalid, "transaction")
		}
		seed := fmt.Sprintf("%d:%s", amount, date)
		occ[seed]++
		identity := fmt.Sprintf("YNAB:%s:%d", seed, occ[seed])
		out.Records = append(out.Records, bank.Record{AccountID: id, AccountNumber: d.ID, ProvisionalID: identity, Amount: amount, Date: date, Description: row.Merchant.Name, Status: "posted"})
	}
	return out, s, nil
}
