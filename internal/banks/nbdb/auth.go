package nbdb

import (
	"context"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
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
	router := p.HijackRequests()
	if e = router.Add("https://sdk.privacy-center.org/*", "", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) }); e != nil {
		return bank.Session{}, e
	}
	go router.Run()
	defer router.Stop()
	o := bank.Observe(p)
	defer o.Close()
	if e = p.Navigate("https://client.bnc.ca/nbdb/login"); e != nil {
		return bank.Session{}, bank.Fail(bank.Temporary, "navigation")
	}
	if el, e := p.Timeout(5*time.Second).ElementR("button,a", `Ignore the update`); e == nil {
		if e = el.Click("left", 1); e != nil {
			return bank.Session{}, bank.Fail(bank.Challenge, "update-banner")
		}
	}
	if e = bank.Input(p, "#username", a.Dependencies.Credentials.Username); e != nil {
		return bank.Session{}, e
	}
	if e = bank.Input(p, "#password-hidden", a.Dependencies.Credentials.Password); e != nil {
		return bank.Session{}, e
	}
	if e = bank.Click(p, "button", `^Sign in$`); e != nil {
		return bank.Session{}, e
	}
	login, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return v.Request.URL == "https://api.bnc.ca/bnc/prod-okta/sso/api/v1/authn" && v.Response != nil && v.Finished
	})
	if e != nil {
		return bank.Session{}, e
	}
	var state struct {
		Status string `json:"status"`
		Error  string `json:"errorCode"`
	}
	if e = o.Body(login, &state); e != nil {
		return bank.Session{}, e
	}
	if state.Error == "E0000004" {
		return bank.Session{}, bank.Fail(bank.Credentials, "login")
	}
	if login.Response.Status == 429 {
		return bank.Session{}, bank.Fail(bank.Throttled, "login")
	}
	if login.Response.Status != 200 {
		return bank.Session{}, bank.Fail(bank.Invalid, "login")
	}
	if state.Status == "MFA_REQUIRED" {
		after := time.Now()
		if e = bank.Click(p, "a", `^Email$`); e != nil {
			return bank.Session{}, e
		}
		code, e := bank.ReadCode(ctx, a.Dependencies.Credentials.MailToken, bank.MailChallenge{After: after, Sender: "noreply@appbnc.ca", Subject: "Here’s your verification code", Length: 6})
		if e != nil {
			return bank.Session{}, e
		}
		if e = bank.Input(p, `input[autocomplete="one-time-code"], input[placeholder="Verification code"], input[name="passCode"]`, code); e != nil {
			return bank.Session{}, e
		}
		if e = bank.Click(p, "button", `^Confirm$`); e != nil {
			return bank.Session{}, e
		}
	}
	discovery, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return strings.HasPrefix(v.Request.URL, summary) && v.Response != nil && v.Response.Status == 200
	})
	if e != nil {
		return bank.Session{}, e
	}
	token, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return strings.HasPrefix(v.Request.URL, "https://api.bnc.ca/bnc/prod-okta/sso/oauth2/") && strings.HasSuffix(v.Request.URL, "/v1/token") && v.Response != nil && v.Response.Status == 200 && v.Finished
	})
	if e != nil {
		return bank.Session{}, e
	}
	form, e := url.ParseQuery(token.Request.PostData)
	if e != nil || form.Get("grant_type") != "authorization_code" {
		return bank.Session{}, bank.Fail(bank.Invalid, "token-capture")
	}
	var response struct {
		Scope string `json:"scope"`
	}
	if e = o.Body(token, &response); e != nil {
		return bank.Session{}, e
	}
	return bank.BrowserSession(b, "nbdb", bank.SafeHeaders(discovery.Request.Headers), auth{TokenURL: token.Request.URL, ClientID: form.Get("client_id"), RedirectURI: form.Get("redirect_uri"), Scope: response.Scope, Headers: bank.SafeHeaders(token.Request.Headers)})
}
