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
			bank.LogControls(p)
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
	if e = bank.Type(p, "#password-hidden", a.Dependencies.Credentials.Password); e != nil {
		return bank.Session{}, e
	}
	// The update notice can arrive while the credentials are being entered,
	// especially on a cold browser. It must be dismissed before Sign in.
	if _, e = bank.ClickIfPresent(p, "button,a", `Ignore the update`, 5*time.Second); e != nil {
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
		if e = bank.Type(p, `input[id="validation-code"], input[autocomplete="one-time-code"], input[name="passCode"]`, code); e != nil {
			return bank.Session{}, e
		}
		stage = "email-confirm"
		if e = bank.Click(p, "button", `^(?:Confirm|Verify|Continue)$`); e != nil {
			return bank.Session{}, e
		}
	}
	slog.Info("NBDB verification submitted")
	stage = "account-capture"
	// Wait on the observed request while handling each known welcome screen at
	// most once. A slow redirect must not make us permanently miss its button.
	captureCtx, cancelCapture := context.WithTimeout(ctx, 90*time.Second)
	defer cancelCapture()
	labels := []string{`^Start using online trading$`, `^Continue to my portfolio$`}
	clicked := map[string]bool{}
	var discovery bank.Exchange
	captured := false
	for !captured {
		for _, label := range labels {
			discovery, captured = o.Latest(func(v bank.Exchange) bool {
				// Native Fetch validates the expected accounts before saving the session,
				// even if the web app's own portfolio request fails.
				if !strings.HasPrefix(v.Request.URL, "https://iiroc.investments.apis.bnc.ca/orion-api/") {
					return false
				}
				// Portfolio navigation varies between sessions. Any authenticated
				// wealth request carries the same API headers; native account-only
				// validation remains the authority for accepting this session.
				for name, value := range v.Request.Headers {
					if strings.EqualFold(name, "authorization") && strings.HasPrefix(value.Str(), "Bearer ") {
						return true
					}
				}
				return false
			})
			if captured {
				break
			}
			if captureCtx.Err() != nil {
				return bank.Session{}, bank.Fail(bank.Temporary, "browser-response-timeout")
			}
			if !clicked[label] {
				clicked[label], e = bank.ClickIfPresent(p.Context(captureCtx), "button,a", label, time.Second)
				if captureCtx.Err() != nil {
					return bank.Session{}, bank.Fail(bank.Temporary, "browser-response-timeout")
				}
				if e != nil {
					return bank.Session{}, e
				}
				if clicked[label] {
					slog.Info("NBDB welcome screen", "control", label, "clicked", true)
				}
			}
		}
		if !captured {
			select {
			case <-captureCtx.Done():
				return bank.Session{}, bank.Fail(bank.Temporary, "browser-response-timeout")
			case <-time.After(time.Second):
			}
		}
	}

	token, e := o.Wait(captureCtx, func(v bank.Exchange) bool {
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
		Scope  string `json:"scope"`
		Access string `json:"access_token"`
	}
	if e = o.Body(token, &response); e != nil {
		return bank.Session{}, e
	}
	if response.Access == "" {
		return bank.Session{}, bank.Fail(bank.Invalid, "token-capture")
	}
	// The app can issue wealth requests before its token store has caught up.
	// Use their API metadata, but take authentication from the completed exchange.
	headers := bank.SafeHeaders(discovery.Request.Headers)
	headers["authorization"] = "Bearer " + response.Access
	return bank.BrowserSession(b, "nbdb", headers, auth{TokenURL: token.Request.URL, ClientID: form.Get("client_id"), RedirectURI: form.Get("redirect_uri"), Scope: response.Scope, Headers: bank.SafeHeaders(token.Request.Headers)})
}
