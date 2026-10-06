package eq

import (
	"context"
	"encoding/json"
	"github.com/go-rod/rod"
	"github.com/nathanfredericks/transactions/internal/bank"
	"net/url"
	"strings"
	"time"
)

func (a *Adapter) Authenticate(ctx context.Context, b *rod.Browser) (bank.Session, error) {
	p, e := bank.NewPage(ctx, b)
	if e != nil {
		return bank.Session{}, e
	}
	o := bank.Observe(p)
	defer o.Close()
	stage := "login-email"
	fail := func(e error) (bank.Session, error) {
		text, _ := p.Timeout(2 * time.Second).Element("body")
		if text != nil {
			value, _ := text.Text()
			lower := strings.ToLower(value)
			if strings.Contains(lower, "scheduled maintenance") {
				return bank.Session{}, bank.Fail(bank.Maintenance, "login")
			}
			if strings.Contains(lower, "wrong password") || strings.Contains(lower, "invalid password") || strings.Contains(lower, "wrong email") {
				return bank.Session{}, bank.Fail(bank.Credentials, "login")
			}
		}
		f := bank.Classify(e)
		f.Operation = stage + "/" + f.Operation
		return bank.Session{}, f
	}
	if e = p.Navigate("https://secure.eqbank.ca/"); e != nil {
		return fail(bank.Fail(bank.Temporary, "navigation"))
	}
	if e = bank.Input(p, `input[type="email"], input[name="username"]`, a.Dependencies.Credentials.Username); e != nil {
		return fail(e)
	}
	if e = bank.Click(p, "button", `^[Ss]ign in$`); e != nil {
		return fail(e)
	}
	stage = "login-password"
	if e = bank.Input(p, `input[type="password"]`, a.Dependencies.Credentials.Password); e != nil {
		return fail(e)
	}
	after := time.Now()
	if e = bank.Click(p, "button", `^[Ss]ign in$`); e != nil {
		return fail(e)
	}
	if _, e = p.Timeout(25 * time.Second).Element(`input[autocomplete="one-time-code"]`); e != nil {
		return fail(bank.Fail(bank.Challenge, "verification-not-reached"))
	}
	stage = "verification-code"
	code, e := bank.ReadCode(ctx, a.Dependencies.Credentials.MailToken, bank.MailChallenge{After: after, Sender: "alert@eqbank.ca", Subject: "EQ Bank One Time Passcode - ", Prefix: true, Length: 6})
	if e != nil {
		return fail(e)
	}
	if e = bank.Input(p, `input[autocomplete="one-time-code"]`, code); e != nil {
		return fail(e)
	}
	if e = bank.Click(p, "button", `^Verify$`); e != nil {
		return fail(e)
	}
	stage = "account-capture"
	discovery, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return v.Request.Method == "GET" && v.Request.URL == api+"/accounts/v2/accounts" && v.Response != nil && v.Response.Status == 200
	})
	if e != nil {
		return fail(e)
	}
	token, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return v.Request.Method == "POST" && v.Request.URL == "https://api.eqbank.ca/auth/v3/access-token" && v.Response != nil && v.Response.Status == 200
	})
	if e != nil {
		return fail(e)
	}
	var payload struct {
		ClientID string `json:"client_id"`
	}
	data, e := o.PostData(token)
	if e != nil {
		return fail(e)
	}
	if json.Unmarshal([]byte(data), &payload) != nil {
		form, err := url.ParseQuery(data)
		if err == nil {
			payload.ClientID = form.Get("client_id")
		}
	}
	if payload.ClientID == "" {
		return fail(bank.Fail(bank.Invalid, "auth-capture"))
	}
	return bank.BrowserSession(b, "eq-bank", bank.SafeHeaders(discovery.Request.Headers), auth{ClientID: payload.ClientID})
}
