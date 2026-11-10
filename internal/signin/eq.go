package signin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/banks/eq"
)

func signInEQ(ctx context.Context, d Dependencies, b *Browser) (bank.Session, error) {
	o := Record(b.Page)
	stage := "login-email"
	fail := func(e error) (bank.Session, error) {
		slog.Warn("EQ browser authentication failed", "stage", stage, "kind", bank.Classify(e).Kind, "exchanges", o.Diagnostics("eqbank.ca"))
		if value, err := b.Page.Locator("body").InnerText(playwright.LocatorInnerTextOptions{Timeout: playwright.Float(2000)}); err == nil {
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
	if _, e := b.Page.Goto("https://secure.eqbank.ca/", commit); e != nil {
		return fail(bank.Fail(bank.Temporary, "navigation"))
	}
	if e := Input(ctx, b, `input[type="email"], input[name="username"]`, d.Credentials.Username); e != nil {
		return fail(e)
	}
	if e := Click(ctx, b, "button", `^(?:[Ss]ign in|[Ll]og in)$`); e != nil {
		return fail(e)
	}
	stage = "login-password"
	if e := Input(ctx, b, `input[type="password"]`, d.Credentials.Password); e != nil {
		return fail(e)
	}
	after := time.Now()
	if e := Click(ctx, b, "button", `^(?:[Ss]ign in|[Ll]og in)$`); e != nil {
		return fail(e)
	}
	if e := b.Page.Locator(`input[autocomplete="one-time-code"]`).First().WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateAttached, Timeout: playwright.Float(25000)}); e != nil {
		return fail(bank.Fail(bank.Challenge, "verification-not-reached"))
	}
	stage = "verification-code"
	code, e := ReadCode(ctx, d.Credentials.MailToken, MailChallenge{After: after, Sender: "alert@eqbank.ca", Subject: "EQ Bank One Time Passcode - ", Prefix: true, Length: 6})
	if e != nil {
		return fail(e)
	}
	if e = Input(ctx, b, `input[autocomplete="one-time-code"]`, code); e != nil {
		return fail(e)
	}
	if e = Click(ctx, b, "button", `^Verify$`); e != nil {
		return fail(e)
	}
	stage = "account-capture"
	// Leave the page alive for maintenance classification and diagnostics.
	// Waiting until the whole authentication context expires prevents fail()
	// from inspecting the page and disguises maintenance as an ordinary timeout.
	captureCtx, cancelCapture := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCapture()
	waitResponse := func(method, endpoint string) (Exchange, error) {
		v, err := o.Wait(captureCtx, func(v Exchange) bool {
			return v.Request.Method() == method && v.Request.URL() == endpoint && (v.Failed || v.Response != nil)
		})
		if err != nil {
			return v, err
		}
		if v.Failed {
			return v, bank.Fail(bank.Temporary, "browser-network")
		}
		status := v.Response.Status()
		switch {
		case status == 200:
			return v, nil
		case status == 429:
			headers := SafeHeaders(v.Response.Headers())
			return v, bank.Throttle("browser-http", headers["retry-after"])
		case status == 401 || status == 419 || status == 440:
			return v, bank.Fail(bank.Authentication, "browser-http")
		case status >= 500:
			return v, bank.Fail(bank.Temporary, "browser-http")
		default:
			return v, bank.Fail(bank.Invalid, "browser-http-status-"+strconv.Itoa(status))
		}
	}
	discovery, e := waitResponse("GET", eq.Endpoint+"/accounts/v2/accounts")
	if e != nil {
		return fail(e)
	}
	stage = "token-capture"
	token, e := waitResponse("POST", "https://api.eqbank.ca/auth/v3/access-token")
	if e != nil {
		return fail(e)
	}
	var payload struct {
		ClientID string `json:"client_id"`
	}
	data, e := PostData(token)
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
	return BrowserSession(b, "eq-bank", SafeHeaders(discovery.Request.Headers()), eq.Auth{ClientID: payload.ClientID})
}
