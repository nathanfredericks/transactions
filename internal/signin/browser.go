package signin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/nathanfredericks/transactions/internal/bank"
	"github.com/nathanfredericks/transactions/internal/chromium"
	"github.com/nathanfredericks/transactions/internal/human"
)

// Browser is the sign-in's browser context and page. Human is the humanized
// input for CloakBrowser banks; without it, input is plain Playwright input.
type Browser struct {
	Context playwright.BrowserContext
	Page    playwright.Page
	Human   *human.Cursor
}

// Exchange is one request the page made and what became of it.
type Exchange struct {
	Sequence     int
	Request      playwright.Request
	Response     playwright.Response // nil until a response arrives
	Finished     bool
	Failed       bool
	NetworkError string
}

// Recorder keeps the page's requests in order. Its handlers run on
// Playwright's event goroutine, so they only record; bodies are read later.
type Recorder struct {
	mu       sync.Mutex
	items    map[playwright.Request]*Exchange
	changed  chan struct{}
	sequence int
}

var netError = regexp.MustCompile(`^net::ERR_[A-Z_]+$`)

// Record starts recording the page's requests.
func Record(p playwright.Page) *Recorder {
	r := &Recorder{items: map[playwright.Request]*Exchange{}, changed: make(chan struct{}, 1)}
	update := func(req playwright.Request, fn func(*Exchange)) {
		r.mu.Lock()
		if x := r.items[req]; x != nil {
			fn(x)
		}
		r.mu.Unlock()
		select {
		case r.changed <- struct{}{}:
		default:
		}
	}
	p.OnRequest(func(req playwright.Request) {
		r.mu.Lock()
		r.sequence++
		r.items[req] = &Exchange{Request: req, Sequence: r.sequence}
		r.mu.Unlock()
	})
	p.OnResponse(func(resp playwright.Response) {
		update(resp.Request(), func(x *Exchange) { x.Response = resp })
	})
	p.OnRequestFinished(func(req playwright.Request) {
		update(req, func(x *Exchange) { x.Finished = true })
	})
	p.OnRequestFailed(func(req playwright.Request) {
		update(req, func(x *Exchange) {
			x.Failed = true
			if err := req.Failure(); err != nil && netError.MatchString(err.Error()) {
				x.NetworkError = err.Error()
			}
		})
	})
	return r
}

// Latest is the most recent exchange that matches.
func (r *Recorder) Latest(match func(Exchange) bool) (Exchange, bool) {
	matches := r.Matches(match)
	if len(matches) == 0 {
		return Exchange{}, false
	}
	return matches[len(matches)-1], true
}

// Matches is every exchange that matches, in request order.
func (r *Recorder) Matches(match func(Exchange) bool) []Exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []Exchange
	for _, x := range r.items {
		if match(*x) {
			result = append(result, *x)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}

// Wait returns the latest matching exchange once there is one.
func (r *Recorder) Wait(ctx context.Context, match func(Exchange) bool) (Exchange, error) {
	for {
		if x, ok := r.Latest(match); ok {
			return x, nil
		}
		select {
		case <-ctx.Done():
			// Silence from an endpoint is not evidence of an unsupported challenge.
			return Exchange{}, bank.Fail(bank.Temporary, "browser-response-timeout")
		case <-r.changed:
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Diagnostics reports endpoint names and status only, never URLs, tokens or bodies.
func (r *Recorder) Diagnostics(hostSuffix string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []map[string]any
	for _, x := range r.items {
		u, err := url.Parse(x.Request.URL())
		if err != nil || !strings.HasSuffix(u.Hostname(), hostSuffix) {
			continue
		}
		operation := path.Base(u.Path)
		if u.Hostname() == "iiroc.investments.apis.bnc.ca" && strings.HasPrefix(u.Path, "/orion-api/") {
			operation = "wealth-api"
			if u.Path == "/orion-api/v1/1/users/session" {
				operation = "wealth-session"
			}
		}
		if operation != "wealth-session" && operation != "wealth-api" && operation != "summary" && operation != "token" && operation != "authn" && operation != "verify" && (hostSuffix != "eqbank.ca" || (operation != "accounts" && operation != "access-token")) {
			continue
		}
		status := 0
		if x.Response != nil {
			status = x.Response.Status()
		}
		result = append(result, map[string]any{"operation": operation, "method": x.Request.Method(), "status": status, "finished": x.Finished, "networkFailed": x.Failed, "networkError": x.NetworkError})
	}
	return result
}

// PostData is the request's body. Never log it.
func PostData(x Exchange) (string, error) {
	data, err := x.Request.PostData()
	if err != nil {
		return "", bank.Fail(bank.Invalid, "request-body-capture")
	}
	return data, nil
}

// Body decodes the response's JSON body.
func Body(x Exchange, out any) error {
	if x.Response == nil {
		return bank.Fail(bank.Invalid, "browser-response")
	}
	data, err := x.Response.Body()
	if err != nil {
		return bank.Fail(bank.Invalid, "browser-response")
	}
	if json.Unmarshal(data, out) != nil {
		return bank.Fail(bank.Invalid, "browser-json")
	}
	return nil
}

// SafeHeaders keeps the headers a session replays from the Lambda, without
// cookies and transport headers.
func SafeHeaders(h map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		k = strings.ToLower(k)
		switch k {
		case "host", "cookie", "content-length", "connection", "accept-encoding":
			continue
		}
		if !strings.HasPrefix(k, ":") {
			out[k] = v
		}
	}
	return out
}

// BrowserSession captures the session's headers, auth state and cookies.
func BrowserSession(b *Browser, id string, headers map[string]string, auth any) (bank.Session, error) {
	raw, err := json.Marshal(auth)
	if err != nil {
		return bank.Session{}, err
	}
	s := bank.Session{Version: 1, Bank: id, Headers: headers, Auth: raw}
	cookies, err := b.Context.Cookies()
	if err != nil {
		return s, bank.Fail(bank.Temporary, "cookies")
	}
	for _, c := range cookies {
		cookie := http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HttpOnly: c.HttpOnly}
		if strings.HasPrefix(c.Domain, ".") {
			cookie.Domain = c.Domain
		}
		// Playwright gives a session cookie an expiry of -1.
		if c.Expires > 0 {
			cookie.Expires = time.Unix(int64(c.Expires), 0)
		}
		s.Cookies = append(s.Cookies, bank.Cookie{URL: "https://" + strings.TrimPrefix(c.Domain, ".") + c.Path, Value: cookie})
	}
	bank.TokenTiming(&s)
	return s, nil
}

// commit returns from Goto once the server answers, as Rod's Navigate did;
// the next locator wait covers the rest of the load.
var commit = playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateCommit}

// visible is the first visible element matching selector.
func visible(b *Browser, selector string) playwright.Locator {
	return b.Page.Locator(selector).Filter(playwright.LocatorFilterOptions{Visible: new(true)}).First()
}

// errNotFound means no matching control appeared in time.
var errNotFound = errors.New("signin: control not found")

// labelled waits up to the timeout for the first visible, enabled element
// matching selector whose label matches: its aria-label, else its rendered
// text, trimmed. Rendered text keeps line breaks between blocks, which a
// label such as "Receive by email" followed by an address relies on. The
// reads run in Playwright's isolated world, not the page's.
func labelled(ctx context.Context, b *Browser, selector, label string, timeout time.Duration) (playwright.Locator, error) {
	pattern := regexp.MustCompile(label)
	deadline := time.Now().Add(timeout)
	for {
		all := b.Page.Locator(selector)
		n, err := all.Count()
		if err != nil {
			return nil, err
		}
		for i := range n {
			l := all.Nth(i)
			if ok, err := matchesLabel(l, pattern); err == nil && ok {
				return l, nil
			}
		}
		if !time.Now().Before(deadline) {
			return nil, errNotFound
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func matchesLabel(l playwright.Locator, pattern *regexp.Regexp) (bool, error) {
	short := new(500.0)
	if visible, err := l.IsVisible(); err != nil || !visible {
		return false, err
	}
	if enabled, err := l.IsEnabled(playwright.LocatorIsEnabledOptions{Timeout: short}); err != nil || !enabled {
		return false, err
	}
	text, err := l.GetAttribute("aria-label", playwright.LocatorGetAttributeOptions{Timeout: short})
	if err != nil {
		return false, err
	}
	if text == "" {
		if text, err = l.InnerText(playwright.LocatorInnerTextOptions{Timeout: short}); err != nil {
			return false, err
		}
	}
	return pattern.MatchString(strings.TrimSpace(text)), nil
}

// waitVisible waits up to the timeout for the locator to be visible.
func waitVisible(l playwright.Locator, timeout time.Duration) error {
	return l.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: new(float64(timeout.Milliseconds()))})
}

// Input fills a field, clearing it first.
func Input(ctx context.Context, b *Browser, selector, value string) error {
	return enterText(ctx, b, selector, value, false)
}

// Type types into a field without clearing it.
func Type(ctx context.Context, b *Browser, selector, value string) error {
	return enterText(ctx, b, selector, value, true)
}

func enterText(ctx context.Context, b *Browser, selector, value string, typed bool) error {
	l := visible(b, selector)
	if err := waitVisible(l, 30*time.Second); err != nil {
		logInputs(b, selector)
		return bank.Fail(bank.Challenge, "input-missing")
	}
	if b.Human != nil {
		var err error
		if typed {
			err = b.Human.PressSequentially(ctx, l, value)
		} else {
			err = b.Human.Fill(ctx, l, value)
		}
		if err != nil {
			return bank.Fail(bank.Temporary, "input-events")
		}
	} else if err := l.Fill(value); err != nil {
		return bank.Fail(bank.Challenge, "input")
	}
	entered, err := l.InputValue()
	if err != nil || entered != value {
		return bank.Fail(bank.Challenge, "input-value")
	}
	return nil
}

// logInputs records the page's input fields by type, id and name only: no
// values, page bodies, URLs or screenshots.
func logInputs(b *Browser, selector string) {
	controls, err := b.Page.Evaluate(`()=>Array.from(document.querySelectorAll("input")).map(e=>({type:e.type,id:e.id,name:e.name,autocomplete:e.autocomplete,visible:!!e.getClientRects().length}))`)
	if err == nil {
		slog.Warn("browser input missing", "selector", selector, "controls", controls)
	}
}

// Click clicks the first visible control whose text matches label.
func Click(ctx context.Context, b *Browser, selector, label string) error {
	l, err := labelled(ctx, b, selector, label, 30*time.Second)
	if err != nil {
		LogControls(b)
		return bank.Fail(bank.Challenge, "button-missing")
	}
	if b.Human != nil {
		if err := b.Human.Click(ctx, l); err != nil {
			return bank.Fail(bank.Temporary, "button-events")
		}
		return nil
	}
	if err := l.Click(); err != nil {
		slog.Warn("browser control failed", "operation", "click", "errorKind", chromium.ErrorKind(err))
		LogControls(b)
		if errors.Is(err, playwright.ErrTimeout) {
			return bank.Fail(bank.Temporary, "button-timeout")
		}
		return bank.Fail(bank.Challenge, "button")
	}
	return nil
}

// ClickIfPresent clicks a known informational screen's control if it appears
// within the timeout, without delaying the sign-in otherwise.
func ClickIfPresent(ctx context.Context, b *Browser, selector, label string, timeout time.Duration) (bool, error) {
	l, err := labelled(ctx, b, selector, label, timeout)
	if err != nil {
		if errors.Is(err, errNotFound) && ctx.Err() == nil {
			return false, nil
		}
		return false, bank.Fail(bank.Temporary, "optional-control")
	}
	if b.Human != nil {
		err = b.Human.Click(ctx, l)
	} else {
		err = l.Click()
	}
	if err != nil {
		return false, bank.Fail(bank.Temporary, "optional-control")
	}
	return true, nil
}

// ClickLocator clicks a control the caller found, where it is.
func ClickLocator(ctx context.Context, b *Browser, l playwright.Locator) error {
	if b.Human != nil {
		return b.Human.ClickInPlace(ctx, l)
	}
	return l.Click()
}

// LogControls records visible control labels without input values or page bodies.
func LogControls(b *Browser) {
	controls, err := b.Page.Evaluate(`()=>Array.from(document.querySelectorAll("a,button,[role=link]")).filter(e=>e.getClientRects().length).map(e=>({tag:e.tagName,label:(e.getAttribute("aria-label")||e.innerText||"").replace(/[^\s@]+@[^\s@]+/g,"[email]").replace(/[0-9]{4,}/g,"[number]").trim().slice(0,100)}))`)
	if err == nil {
		slog.Warn("browser visible controls", "controls", controls)
	}
}
