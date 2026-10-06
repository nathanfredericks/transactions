package eq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/bank"
)

const api = "https://web-api.eqbank.ca/web/v1.1"
const personal = "130742998"
const card = "302911525"

type Adapter struct{ Dependencies bank.Dependencies }
type auth struct {
	ClientID string `json:"clientId"`
}

func (a *Adapter) Renew(ctx context.Context, s bank.Session) (bank.Session, error) {
	// EQ's observed renewal flow extends a still-valid access token. Once it
	// expires, start a fresh browser session instead of sending an expired
	// bearer to the exchange and turning a normal expiry into a review hold.
	if (!s.ExpiresAt.IsZero() && !s.ExpiresAt.After(time.Now())) ||
		(!s.MaximumExpiresAt.IsZero() && !s.MaximumExpiresAt.After(time.Now())) {
		return s, bank.Fail(bank.Authentication, "renew")
	}
	var c auth
	if json.Unmarshal(s.Auth, &c) != nil || c.ClientID == "" {
		return s, bank.Fail(bank.Authentication, "renew")
	}
	body, _ := json.Marshal(map[string]string{"client_id": c.ClientID, "grant_type": "refresh_token"})
	var out struct {
		Access string `json:"access_token"`
	}
	err := newHTTP(&s).JSON(ctx, "POST", "https://api.eqbank.ca/auth/v3/access-token", bank.Headers(s, map[string]string{"content-type": "application/json"}), body, &out)
	if err == nil {
		if out.Access == "" {
			return s, bank.Fail(bank.Invalid, "renew-token")
		}
		s.Headers["authorization"] = "Bearer " + out.Access
		bank.TokenTiming(&s)
	}
	return s, err
}
func (a *Adapter) Fetch(ctx context.Context, s bank.Session, r bank.FetchRequest) (bank.Snapshot, bank.Session, error) {
	out := bank.Snapshot{At: time.Now()}
	h := newHTTP(&s)
	var accounts []struct {
		Number string `json:"accountNumber"`
		Name   string `json:"accountType"`
	}
	if e := h.JSON(ctx, "GET", api+"/accounts/v2/accounts", s.Headers, nil, &accounts); e != nil {
		return out, s, e
	}
	for _, n := range []string{personal, card} {
		found := false
		for _, v := range accounts {
			if v.Number == n {
				found = true
				out.Accounts = append(out.Accounts, bank.Account{ID: bank.AccountID(n), Number: n, Name: v.Name})
			}
		}
		if !found {
			return out, s, bank.Fail(bank.Invalid, "account-identity")
		}
	}
	if r.AccountsOnly {
		return out, s, nil
	}
	if _, e := time.Parse("2006-01-02", r.StartDate); e != nil {
		return out, s, bank.Fail(bank.Invalid, "history-start")
	}
	zone, _ := time.LoadLocation("America/Halifax")
	end := time.Now().In(zone).Format("2006-01-02")
	records, e := personalHistory(ctx, h, r.StartDate, end)
	if e != nil {
		return out, s, e
	}
	out.Records = append(out.Records, records...)
	records, e = cardHistory(ctx, h, r.StartDate, end)
	out.Records = append(out.Records, records...)
	return out, s, e
}
func personalHistory(ctx context.Context, h *bank.HTTP, start, end string) ([]bank.Record, error) {
	var response struct {
		Data struct {
			Rows []struct {
				ID     string `json:"TransactionId"`
				Amount struct {
					Value    string `json:"Amount"`
					Currency string `json:"Currency"`
				} `json:"Amount"`
				Direction   string `json:"CreditDebitIndicator"`
				Status      string `json:"Status"`
				Date        string `json:"BookingDateTime"`
				Description string `json:"TransactionInformation"`
			} `json:"Transaction"`
		} `json:"Data"`
		Meta struct {
			End bool `json:"ListEnd"`
		} `json:"Meta"`
	}
	q := url.Values{"fromBookingDate": {start}, "toBookingDate": {end}}
	e := h.JSON(ctx, "GET", api+"/accounts/"+personal+"/transactions?"+q.Encode(), bank.Headers(*h.Session, map[string]string{"size": "1000"}), nil, &response)
	if e != nil {
		return nil, e
	}
	if !response.Meta.End || len(response.Data.Rows) >= 1000 {
		if start == end {
			return nil, bank.Fail(bank.Invalid, "incomplete-personal-history")
		}
		lo, _ := time.Parse("2006-01-02", start)
		hi, _ := time.Parse("2006-01-02", end)
		mid := lo.Add(hi.Sub(lo) / 2)
		left, e := personalHistory(ctx, h, start, mid.Format("2006-01-02"))
		if e != nil {
			return nil, e
		}
		right, e := personalHistory(ctx, h, mid.AddDate(0, 0, 1).Format("2006-01-02"), end)
		return append(left, right...), e
	}
	var records []bank.Record
	for _, v := range response.Data.Rows {
		if len(v.Date) < 10 || v.ID == "" || v.Description == "" || v.Status != "Booked" || v.Amount.Currency != "CAD" || (v.Direction != "Debit" && v.Direction != "Credit") {
			return nil, bank.Fail(bank.Invalid, "personal-record")
		}
		n, e := bank.Milliunits(v.Amount.Value)
		if e != nil {
			return nil, e
		}
		if v.Direction == "Debit" {
			n = -n
		}
		day := v.Date[:10]
		if day < start || day > end {
			continue
		}
		id := v.ID
		records = append(records, bank.Record{AccountID: bank.AccountID(personal), AccountNumber: personal, BankID: &id, ProvisionalID: id, References: []string{id}, Amount: n, Date: day, Description: v.Description, Status: "posted"})
	}
	return records, nil
}

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currencyCd"`
}
type charge struct {
	Amount   *string `json:"amount"`
	Currency *string `json:"currencyCd"`
}

func cardHistory(ctx context.Context, h *bank.HTTP, start, end string) ([]bank.Record, error) {
	var records []bank.Record
	occurrences := map[string]int{}
	pages := 1
	for page := 1; page <= pages; page++ {
		var response struct {
			Page  int `json:"page"`
			Pages int `json:"pageCount"`
			Rows  []struct {
				ID          *string `json:"transactionId"`
				Status      string  `json:"status"`
				Booked      string  `json:"bookingDateTime"`
				Valued      string  `json:"valueDateTime"`
				Description string  `json:"transactionInformation"`
				AuthID      any     `json:"auth_id"`
				SourceID    any     `json:"source_id"`
				Amount      money   `json:"amount"`
				Charge      *charge `json:"chargeAmount"`
			} `json:"transactions"`
		}
		q := url.Values{"startDate": {start}, "endDate": {end}, "limit": {"100"}, "offset": {fmt.Sprint(page)}}
		if e := h.JSON(ctx, "GET", api+"/transactions/v1.0.0/vs1/card-history?"+q.Encode(), bank.Headers(*h.Session, map[string]string{"accountId": card}), nil, &response); e != nil {
			return nil, e
		}
		if response.Page != page || response.Pages > 1000 || response.Pages < 0 || (page > 1 && pages != response.Pages) || (page < response.Pages && len(response.Rows) != 100) {
			return nil, bank.Fail(bank.Invalid, "card-pagination")
		}
		pages = response.Pages
		for _, v := range response.Rows {
			day := v.Booked
			if day == "" {
				day = v.Valued
			}
			if len(day) < 10 || v.Description == "" || v.Amount.Currency != "CAD" || (v.Status != "PENDING" && v.Status != "POSTED") {
				return nil, bank.Fail(bank.Invalid, "card-record")
			}
			day = day[:10]
			if _, e := time.Parse("2006-01-02", day); e != nil {
				return nil, bank.Fail(bank.Invalid, "card-date")
			}
			if day < start || day > end {
				continue
			}
			// Match the original JS JSON.stringify field order and escaping for stable IDs.
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			_ = enc.Encode([]any{v.Booked, v.Description, v.Charge, v.Amount})
			seed := string(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
			occurrences[seed]++
			amount, e := bank.Milliunits(v.Amount.Amount)
			if e != nil {
				return nil, e
			}
			r := bank.Record{AccountID: bank.AccountID(card), AccountNumber: card, BankID: v.ID, ProvisionalID: bank.Hash(fmt.Sprintf("%s:%d", seed, occurrences[seed])), Amount: amount, Date: day, Description: v.Description, Status: "posted", References: []string{}}
			if v.Status == "PENDING" {
				r.Status = "pending"
			}
			if v.ID != nil {
				r.References = append(r.References, *v.ID)
			}
			for _, ref := range []any{v.AuthID, v.SourceID} {
				if ref != nil {
					r.References = append(r.References, fmt.Sprint(ref))
				}
			}
			if v.Charge != nil {
				r.OriginalCurrency = v.Charge.Currency
				if v.Charge.Amount != nil && *v.Charge.Amount != "" {
					n, e := bank.Milliunits(*v.Charge.Amount)
					if e != nil {
						return nil, e
					}
					r.OriginalAmount = &n
				}
			}
			records = append(records, r)
		}
	}
	return records, nil
}

func newHTTP(s *bank.Session) *bank.HTTP {
	h := bank.NewHTTP(s)
	h.Check = func(status int, body []byte) error {
		lower := strings.ToLower(string(body))
		if strings.Contains(lower, "scheduled maintenance") && strings.Contains(lower, "eqbankstatus.ca") {
			return bank.Fail(bank.Maintenance, "bank-api")
		}
		return nil
	}
	return h
}
