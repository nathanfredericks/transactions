package nbdb

import (
	"context"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/nathanfredericks/transactions/internal/bank"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func (a *Adapter) Authenticate(ctx context.Context, b *rod.Browser) (session bank.Session, err error) {
	stage := "login"
	defer func() {
		if err != nil {
			f := bank.Classify(err)
			f.Operation = stage + "/" + f.Operation
			err = f
		}
	}()
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
	defer func() {
		if err != nil {
			slog.Info("NBDB authentication exchanges", "requests", o.Diagnostics("bnc.ca"))
		}
	}()
	if e = p.Navigate("https://client.bnc.ca/nbdb/login"); e != nil {
		return bank.Session{}, bank.Fail(bank.Temporary, "navigation")
	}
	if _, e = bank.ClickIfPresent(p, "button,a", `Ignore the update`, 5*time.Second); e != nil {
		return bank.Session{}, e
	}
	if e = bank.Type(p, "#username", a.Dependencies.Credentials.Username); e != nil {
		return bank.Session{}, e
	}
	if e = bank.Input(p, "#password-hidden", a.Dependencies.Credentials.Password); e != nil {
		return bank.Session{}, e
	}
	if e = bank.Click(p, "button", `^Sign in$`); e != nil {
		return bank.Session{}, e
	}
	stage = "login-response"
	login, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return v.Request.Method == "POST" && v.Request.URL == "https://api.bnc.ca/bnc/prod-okta/sso/api/v1/authn" && v.Response != nil && v.Finished
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
	if state.Error == "AK000001" {
		return bank.Session{}, bank.Fail(bank.Challenge, "browser-verification")
	}
	if state.Error == "E0000004" {
		return bank.Session{}, bank.Fail(bank.Credentials, "login")
	}
	if login.Response.Status == 429 {
		return bank.Session{}, bank.Fail(bank.Throttled, "login")
	}
	if login.Response.Status != 200 {
		code := "unrecognized"
		if regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(state.Error) {
			code = state.Error
		}
		var structure map[string]any
		_ = o.Body(login, &structure)
		keys := []string{}
		for k := range structure {
			if len(k) < 50 {
				keys = append(keys, k)
			}
		}
		slog.Warn("NBDB login response", "status", login.Response.Status, "code", code, "fields", keys)
		return bank.Session{}, bank.Fail(bank.Invalid, "login")
	}
	slog.Info("NBDB authentication accepted", "state", state.Status)
	if state.Status == "MFA_REQUIRED" {
		stage = "email-option"
		after := time.Now()
		if e = bank.Click(p, `a,button,[role="link"]`, `^(?:Receive by email|Email)(?:\s|$)`); e != nil {
			return bank.Session{}, e
		}
		stage = "email-code"
		code, e := bank.ReadCode(ctx, a.Dependencies.Credentials.MailToken, bank.MailChallenge{After: after, Sender: "noreply@appbnc.ca", Subject: "Here’s your verification code", Length: 6})
		if e != nil {
			return bank.Session{}, e
		}
		if e = bank.Input(p, `input[id="validation-code"], input[autocomplete="one-time-code"], input[name="passCode"]`, code); e != nil {
			return bank.Session{}, e
		}
		stage = "email-confirm"
		if e = bank.Click(p, "button", `^(?:Confirm|Verify|Continue)$`); e != nil {
			return bank.Session{}, e
		}
	}
	slog.Info("NBDB verification submitted")
	// New profiles can show two informational welcome screens after MFA.
	for _, label := range []string{`^Start using online trading$`, `^Continue to my portfolio$`} {
		clicked, clickErr := bank.ClickIfPresent(p, "button,a", label, 15*time.Second)
		if clickErr != nil {
			return bank.Session{}, clickErr
		}
		slog.Info("NBDB welcome screen", "control", label, "clicked", clicked)
	}
	stage = "account-capture"
	discovery, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return v.Request.Method == "GET" && strings.HasPrefix(v.Request.URL, summary) && v.Response != nil && v.Response.Status == 200
	})
	if e != nil {
		return bank.Session{}, e
	}
	token, e := o.Wait(ctx, func(v bank.Exchange) bool {
		return v.Request.Method == "POST" && strings.HasPrefix(v.Request.URL, "https://api.bnc.ca/bnc/prod-okta/sso/oauth2/") && strings.HasSuffix(v.Request.URL, "/v1/token") && v.Response != nil && v.Response.Status == 200 && v.Finished
	})
	if e != nil {
		return bank.Session{}, e
	}
	data, e := o.PostData(token)
	if e != nil {
		return bank.Session{}, e
	}
	form, e := url.ParseQuery(data)
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
