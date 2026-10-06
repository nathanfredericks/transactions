package bank

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

type Exchange struct {
	Request  *proto.NetworkRequest
	Response *proto.NetworkResponse
	ID       proto.NetworkRequestID
	Sequence int
	Finished bool
	Failed   bool
}
type Observer struct {
	Page     *rod.Page
	mu       sync.Mutex
	items    map[proto.NetworkRequestID]*Exchange
	changed  chan struct{}
	cancel   context.CancelFunc
	sequence int
}

func Observe(page *rod.Page) *Observer {
	ctx, cancel := context.WithCancel(page.GetContext())
	o := &Observer{Page: page, items: map[proto.NetworkRequestID]*Exchange{}, changed: make(chan struct{}, 1), cancel: cancel}
	signal := func() {
		select {
		case o.changed <- struct{}{}:
		default:
		}
	}
	wait := page.Context(ctx).EachEvent(func(e *proto.NetworkRequestWillBeSent) {
		o.mu.Lock()
		o.sequence++
		o.items[e.RequestID] = &Exchange{Request: e.Request, ID: e.RequestID, Sequence: o.sequence}
		o.mu.Unlock()
		signal()
	}, func(e *proto.NetworkResponseReceived) {
		o.mu.Lock()
		if v := o.items[e.RequestID]; v != nil {
			v.Response = e.Response
		}
		o.mu.Unlock()
		signal()
	}, func(e *proto.NetworkLoadingFinished) {
		o.mu.Lock()
		if v := o.items[e.RequestID]; v != nil {
			v.Finished = true
		}
		o.mu.Unlock()
		signal()
	}, func(e *proto.NetworkLoadingFailed) {
		o.mu.Lock()
		if v := o.items[e.RequestID]; v != nil {
			v.Failed = true
		}
		o.mu.Unlock()
		signal()
	})
	go wait()
	return o
}
func (o *Observer) Close() { o.cancel() }
func (o *Observer) Latest(match func(Exchange) bool) (Exchange, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var latest *Exchange
	for _, v := range o.items {
		copy := *v
		if match(copy) && (latest == nil || copy.Sequence > latest.Sequence) {
			latest = &copy
		}
	}
	if latest != nil {
		return *latest, true
	}
	return Exchange{}, false
}
func (o *Observer) Wait(ctx context.Context, match func(Exchange) bool) (Exchange, error) {
	for {
		if latest, ok := o.Latest(match); ok {
			return latest, nil
		}
		select {
		case <-ctx.Done():
			// Silence from an endpoint is not evidence of an unsupported challenge.
			return Exchange{}, Fail(Temporary, "browser-response-timeout")
		case <-o.changed:
		}
	}
}

// PostData retrieves bodies omitted by CDP's request event. Never log this value.
func (o *Observer) PostData(e Exchange) (string, error) {
	if e.Request.PostData != "" {
		return e.Request.PostData, nil
	}
	r, err := (proto.NetworkGetRequestPostData{RequestID: e.ID}).Call(o.Page)
	if err != nil {
		return "", Fail(Invalid, "request-body-capture")
	}
	return r.PostData, nil
}

// Diagnostics reports endpoint names and status only, never URLs, tokens or bodies.
func (o *Observer) Diagnostics(hostSuffix string) []map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	var result []map[string]any
	for _, x := range o.items {
		u, err := url.Parse(x.Request.URL)
		if err != nil || !strings.HasSuffix(u.Hostname(), hostSuffix) {
			continue
		}
		operation := path.Base(u.Path)
		if u.Hostname() == "iiroc.investments.apis.bnc.ca" && strings.HasPrefix(u.Path, "/orion-api/") {
			operation = "wealth-api"
		}
		if operation != "wealth-api" && operation != "summary" && operation != "token" && operation != "authn" && operation != "verify" {
			continue
		}
		status := 0
		if x.Response != nil {
			status = x.Response.Status
		}
		result = append(result, map[string]any{"operation": operation, "method": x.Request.Method, "status": status, "finished": x.Finished, "networkFailed": x.Failed})
	}
	return result
}
func (o *Observer) Body(e Exchange, out any) error {
	r, err := (proto.NetworkGetResponseBody{RequestID: e.ID}).Call(o.Page)
	if err != nil {
		return Fail(Invalid, "browser-response")
	}
	data := []byte(r.Body)
	if r.Base64Encoded {
		var err error
		data, err = base64.StdEncoding.DecodeString(r.Body)
		if err != nil {
			return Fail(Invalid, "browser-base64")
		}
	}
	if json.Unmarshal(data, out) != nil {
		return Fail(Invalid, "browser-json")
	}
	return nil
}
func SafeHeaders(h proto.NetworkHeaders) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		k = strings.ToLower(k)
		switch k {
		case "host", "cookie", "content-length", "connection", "accept-encoding":
			continue
		}
		if !strings.HasPrefix(k, ":") {
			out[k] = v.Str()
		}
	}
	return out
}
func BrowserSession(b *rod.Browser, id string, headers map[string]string, auth any) (Session, error) {
	raw, e := json.Marshal(auth)
	if e != nil {
		return Session{}, e
	}
	s := Session{Version: 1, Bank: id, Headers: headers, Auth: raw}
	cookies, e := b.GetCookies()
	if e != nil {
		return s, Fail(Temporary, "cookies")
	}
	for _, c := range cookies {
		cookie := http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if strings.HasPrefix(c.Domain, ".") {
			cookie.Domain = c.Domain
		}
		if !c.Session {
			cookie.Expires = time.Unix(int64(c.Expires), 0)
		}
		s.Cookies = append(s.Cookies, Cookie{URL: "https://" + strings.TrimPrefix(c.Domain, ".") + c.Path, Value: cookie})
	}
	TokenTiming(&s)
	return s, nil
}
func Input(p *rod.Page, selector, value string) error { return enterText(p, selector, value, false) }
func Type(p *rod.Page, selector, value string) error  { return enterText(p, selector, value, true) }
func enterText(p *rod.Page, selector, value string, typed bool) error {
	el, e := p.Timeout(30 * time.Second).ElementByJS(rod.Eval(`(selector)=>Array.from(document.querySelectorAll(selector)).find(el=>el.getClientRects().length && getComputedStyle(el).visibility!=="hidden")||null`, selector))
	if e != nil {
		// Field structure only: no input values, page bodies, URLs or screenshots.
		controls, err := p.Timeout(2 * time.Second).Eval(`()=>Array.from(document.querySelectorAll("input")).map(e=>({type:e.type,id:e.id,name:e.name,autocomplete:e.autocomplete,visible:!!e.getClientRects().length}))`)
		if err == nil {
			slog.Warn("browser input missing", "selector", selector, "controls", controls.Value)
		}
		return Fail(Challenge, "input-missing")
	}
	if e = el.WaitVisible(); e != nil {
		return Fail(Challenge, "input-hidden")
	}
	if e = el.SelectAllText(); e != nil {
		return Fail(Challenge, "input-selection")
	}
	if typed {
		if e = el.WaitEnabled(); e == nil {
			e = el.WaitWritable()
		}
		if e == nil {
			e = typeText(p, value)
		}
		if e != nil {
			return Fail(Temporary, "input-events")
		}
	} else if e = el.Input(value); e != nil {
		return Fail(Challenge, "input")
	}
	entered, e := el.Property("value")
	if e != nil || entered.Str() != value {
		return Fail(Challenge, "input-value")
	}
	return nil
}

// Use Rod's key mapping instead of reimplementing CDP key codes. These bounded
// timings sit within CloakBrowser 0.3.25's careful preset; no simulated mistakes
// or synthetic page keyboard events are used for credentials.
func typeText(p *rod.Page, value string) error {
	wait := func(duration time.Duration) error {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-p.GetContext().Done():
			return p.GetContext().Err()
		case <-timer.C:
			return nil
		}
	}
	if err := wait(time.Second); err != nil {
		return err
	}
	// Clear the selected value even when the requested replacement is empty.
	if err := p.Keyboard.Type(input.Backspace); err != nil {
		return err
	}
	for _, character := range value {
		if character < 32 || character > 126 {
			if err := p.InsertText(string(character)); err != nil {
				return err
			}
		} else {
			shift := character >= 'A' && character <= 'Z' || strings.ContainsRune("~!@#$%^&*()_+{}|:\"<>?", character)
			if shift {
				if err := p.Keyboard.Press(input.ShiftLeft); err != nil {
					return err
				}
				if err := wait(60 * time.Millisecond); err != nil {
					return err
				}
			}
			key := input.Key(character)
			if err := p.Keyboard.Press(key); err != nil {
				return err
			}
			if err := wait(30 * time.Millisecond); err != nil {
				return err
			}
			if err := p.Keyboard.Release(key); err != nil {
				return err
			}
			if shift {
				if err := wait(50 * time.Millisecond); err != nil {
					return err
				}
				if err := p.Keyboard.Release(input.ShiftLeft); err != nil {
					return err
				}
			}
		}
		if err := wait(100 * time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

const visibleButtonJS = `(selector,label)=>Array.from(document.querySelectorAll(selector)).find(el=>el.getClientRects().length && getComputedStyle(el).visibility!=="hidden" && getComputedStyle(el).pointerEvents!=="none" && !el.disabled && el.getAttribute("aria-disabled")!=="true" && new RegExp(label).test((el.getAttribute("aria-label")||el.innerText||el.textContent).trim()))||null`

func Click(p *rod.Page, selector, label string) error {
	el, e := p.Timeout(30 * time.Second).ElementByJS(rod.Eval(visibleButtonJS, selector, label))
	if e != nil {
		LogControls(p)
		return Fail(Challenge, "button-missing")
	}
	if e = el.Click(proto.InputMouseButtonLeft, 1); e != nil {
		slog.Warn("browser control failed", "operation", "click", "errorType", fmt.Sprintf("%T", e))
		LogControls(p)
		if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
			return Fail(Temporary, "button-timeout")
		}
		return Fail(Challenge, "button")
	}
	return nil
}

// LogControls records visible control labels without input values or page bodies.
func LogControls(p *rod.Page) {
	controls, err := p.Timeout(2 * time.Second).Eval(`()=>Array.from(document.querySelectorAll("a,button,[role=link]")).filter(e=>e.getClientRects().length).map(e=>({tag:e.tagName,label:(e.getAttribute("aria-label")||e.innerText||"").replace(/[^\s@]+@[^\s@]+/g,"[email]").replace(/[0-9]{4,}/g,"[number]").trim().slice(0,100)}))`)
	if err == nil {
		slog.Warn("browser visible controls", "controls", controls.Value)
	}
}

// ClickIfPresent handles known informational screens without delaying normal API access.
func ClickIfPresent(p *rod.Page, selector, label string, timeout time.Duration) (bool, error) {
	el, err := p.Timeout(timeout).ElementByJS(rod.Eval(visibleButtonJS, selector, label))
	if err != nil {
		if p.GetContext().Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return false, nil
		}
		return false, Fail(Challenge, "optional-control")
	}
	if err = el.Context(p.GetContext()).Timeout(15*time.Second).Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false, Fail(Challenge, "optional-control")
	}
	return true, nil
}
func NewPage(ctx context.Context, b *rod.Browser) (*rod.Page, error) {
	p, e := b.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if e != nil {
		return nil, Fail(Temporary, "browser-page")
	}
	return p, nil
}
