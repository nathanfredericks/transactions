package nbdb

import (
	"context"
	"encoding/json"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/cloak"
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
	if previous := a.Dependencies.Previous; previous != nil {
		var saved auth
		if json.Unmarshal(previous.Auth, &saved) != nil {
			return bank.Session{}, bank.Fail(bank.Invalid, "device-state")
		}
		cookies := saved.DeviceCookies
		if previous.BrowserSeed < 10000 || previous.BrowserSeed > 99999 {
			// Old cookies belong to an unknown randomized fingerprint. Establish
			// a fresh device once, then keep its validated seed and cookies together.
			cookies = nil
		}
		params := []*proto.NetworkCookieParam{}
		for _, cookie := range deviceCookies(cookies) {
			v := cookie.Value
			param := &proto.NetworkCookieParam{Name: v.Name, Value: v.Value, URL: cookie.URL, Domain: v.Domain, Path: v.Path, Secure: v.Secure, HTTPOnly: v.HttpOnly}
			if !v.Expires.IsZero() {
				param.Expires = proto.TimeSinceEpoch(float64(v.Expires.Unix()))
			}
			params = append(params, param)
		}
		if len(params) > 0 {
			if e = b.SetCookies(params); e != nil {
				return bank.Session{}, bank.Fail(bank.Temporary, "device-restore")
			}
		}
		slog.Info("NBDB remembered browser restored", "cookies", len(params))
	}
	router := cloak.NewRequestRouter(p)
	if e = router.Add("https://sdk.privacy-center.org/*", "", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) }); e != nil {
		return bank.Session{}, e
	}
	if e = cloak.StartRequestRouter(p, router); e != nil {
		return bank.Session{}, e
	}
	defer cloak.StopRequestRouter(p, router)
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
	// Match the wrapper's awaited navigation: visible inputs can appear before
	// the page's browser-access scripts finish loading.
	if e = p.Timeout(60 * time.Second).WaitLoad(); e != nil {
		return bank.Session{}, bank.Fail(bank.Temporary, "navigation-ready")
	}
	slog.Info("NBDB login page loaded")
	if _, e = bank.ClickIfPresent(p, "button,a", `Ignore the update`, 5*time.Second); e != nil {
		return bank.Session{}, e
	}
	if e = enterCredentials(p, "#username", a.Dependencies.Credentials.Username, bank.Type); e != nil {
		return bank.Session{}, e
	}
	if e = enterCredentials(p, "#password-hidden", a.Dependencies.Credentials.Password, bank.Input); e != nil {
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
		return bank.Session{}, bank.Fail(bank.Challenge, "browser-access-rejected")
	}
	if state.Error == "E0000004" {
		return bank.Session{}, bank.Fail(bank.Credentials, "login")
	}
	if login.Response.Status == 429 {
		return bank.Session{}, bank.Throttle("login", bank.SafeHeaders(login.Response.Headers)["retry-after"])
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
				if v.Request.Method != "GET" || !strings.HasPrefix(v.Request.URL, "https://iiroc.investments.apis.bnc.ca/orion-api/") {
					return false
				}
				// Portfolio navigation varies between sessions. Capture a read's
				// API bearer, excluding the separate session-creation OAuth client.
				// Native account validation remains the authority for this session.
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

	// Multiple OAuth clients can complete exchanges during the redirect. Match
	// a finished authorization-code response to the bearer actually used by a
	// wealth request; the first completed token can belong to the login client.
	stage = "token-capture"
	var token bank.Exchange
	var response struct {
		Scope  string `json:"scope"`
		Access string `json:"access_token"`
	}
	matched := false
	for !matched {
		wealth := o.Matches(func(v bank.Exchange) bool {
			return v.Request.Method == "GET" && strings.HasPrefix(v.Request.URL, "https://iiroc.investments.apis.bnc.ca/orion-api/")
		})
		tokens := o.Matches(func(v bank.Exchange) bool {
			return v.Sequence > login.Sequence && v.Request.Method == "POST" && strings.HasPrefix(v.Request.URL, "https://api.bnc.ca/bnc/prod-okta/sso/oauth2/") && strings.HasSuffix(v.Request.URL, "/v1/token") && v.Response != nil && v.Response.Status == 200 && v.Finished
		})
		for i := len(tokens) - 1; i >= 0 && !matched; i-- {
			candidate := tokens[i]
			body, err := o.PostData(candidate)
			if err != nil {
				return bank.Session{}, err
			}
			form, err := url.ParseQuery(body)
			if err != nil || form.Get("grant_type") != "authorization_code" {
				continue
			}
			if e = o.Body(candidate, &response); e != nil {
				return bank.Session{}, e
			}
			if response.Access == "" {
				continue
			}
			for j := len(wealth) - 1; j >= 0; j-- {
				headers := bank.SafeHeaders(wealth[j].Request.Headers)
				if headers["authorization"] == "Bearer "+response.Access {
					token = candidate
					discovery = wealth[j]
					matched = true
					break
				}
			}
		}
		if !matched {
			select {
			case <-captureCtx.Done():
				return bank.Session{}, bank.Fail(bank.Temporary, "token-correlation-timeout")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	// Token exchange and wealth-session establishment are separate requests.
	// The first portfolio GET can be aborted while the session POST is still
	// setting context cookies; do not snapshot cookies or validate the API yet.
	stage = "wealth-session"
	if _, e = o.Wait(captureCtx, func(v bank.Exchange) bool {
		u, err := url.Parse(v.Request.URL)
		return err == nil && v.Sequence > login.Sequence && v.Request.Method == "POST" && u.Scheme == "https" && u.Host == "iiroc.investments.apis.bnc.ca" && u.Path == "/orion-api/v1/1/users/session" && v.Response != nil && v.Response.Status == 200 && v.Finished
	}); e != nil {
		return bank.Session{}, e
	}
	slog.Info("NBDB wealth authentication correlated")
	data, e := o.PostData(token)
	if e != nil {
		return bank.Session{}, e
	}
	form, e := url.ParseQuery(data)
	if e != nil || form.Get("grant_type") != "authorization_code" {
		return bank.Session{}, bank.Fail(bank.Invalid, "token-capture")
	}
	// Both API metadata and the completed response now refer to the same token.
	headers := bank.SafeHeaders(discovery.Request.Headers)
	headers["authorization"] = "Bearer " + response.Access
	session, e = bank.BrowserSession(b, "nbdb", headers, nil)
	if e != nil {
		return bank.Session{}, e
	}
	session.Auth, e = json.Marshal(auth{TokenURL: token.Request.URL, ClientID: form.Get("client_id"), RedirectURI: form.Get("redirect_uri"), Scope: response.Scope, Headers: bank.SafeHeaders(token.Request.Headers), DeviceCookies: deviceCookies(session.Cookies)})
	return session, e
}

// Restore only NBDB's device and browser-access cookies. Expired authentication
// cookies are deliberately excluded; the ordinary login and MFA still run.
func deviceCookies(cookies []bank.Cookie) []bank.Cookie {
	var result []bank.Cookie
	seen := map[string]bool{}
	for _, cookie := range cookies {
		v := cookie.Value
		switch v.Name {
		case "DT", "_abck", "bm_sz", "ak_bmsc", "bm_sv":
		default:
			continue
		}
		u, err := url.Parse(cookie.URL)
		if err != nil || u.Scheme != "https" || (u.Hostname() != "bnc.ca" && !strings.HasSuffix(u.Hostname(), ".bnc.ca")) || (v.Domain != "" && strings.TrimPrefix(v.Domain, ".") != "bnc.ca" && !strings.HasSuffix(v.Domain, ".bnc.ca")) || (!v.Expires.IsZero() && !v.Expires.After(time.Now())) {
			continue
		}
		domain := v.Domain
		if domain == "" {
			domain = u.Hostname()
		}
		key := strings.TrimPrefix(domain, ".") + "\x00" + v.Path + "\x00" + v.Name
		if !seen[key] {
			seen[key] = true
			result = append(result, cookie)
		}
	}
	return result
}

// The known browser-update dialog can appear after initial page loading and
// intercept a credential-field click. Dismiss it and re-enter that field once;
// this happens before the authentication POST and never repeats a login request.
func enterCredentials(p *rod.Page, selector, value string, enter func(*rod.Page, string, string) error) error {
	err := enter(p, selector, value)
	if err == nil {
		return nil
	}
	failure := bank.Classify(err)
	if failure.Operation != "input-value" && failure.Operation != "input-events" {
		return err
	}
	dismissed, noticeErr := bank.ClickIfPresent(p, "button,a", `Ignore the update`, 2*time.Second)
	if noticeErr != nil {
		return noticeErr
	}
	if !dismissed {
		return err
	}
	return enter(p, selector, value)
}
