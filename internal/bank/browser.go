package bank

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Exchange struct {
	Request  *proto.NetworkRequest
	Response *proto.NetworkResponse
	ID       proto.NetworkRequestID
	Finished bool
}
type Observer struct {
	Page    *rod.Page
	mu      sync.Mutex
	items   map[proto.NetworkRequestID]*Exchange
	changed chan struct{}
	cancel  context.CancelFunc
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
		o.items[e.RequestID] = &Exchange{Request: e.Request, ID: e.RequestID}
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
	})
	go wait()
	return o
}
func (o *Observer) Close() { o.cancel() }
func (o *Observer) Wait(ctx context.Context, match func(Exchange) bool) (Exchange, error) {
	for {
		o.mu.Lock()
		for _, v := range o.items {
			copy := *v
			if match(copy) {
				o.mu.Unlock()
				return copy, nil
			}
		}
		o.mu.Unlock()
		select {
		case <-ctx.Done():
			return Exchange{}, Fail(Challenge, "browser-response-timeout")
		case <-o.changed:
		}
	}
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
		cookie := http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if !c.Session {
			cookie.Expires = time.Unix(int64(c.Expires), 0)
		}
		s.Cookies = append(s.Cookies, Cookie{URL: "https://" + strings.TrimPrefix(c.Domain, ".") + c.Path, Value: cookie})
	}
	TokenTiming(&s)
	return s, nil
}
func Input(p *rod.Page, selector, value string) error {
	el, e := p.Timeout(30 * time.Second).Element(selector)
	if e != nil {
		return Fail(Challenge, "input-missing")
	}
	if e = el.WaitVisible(); e != nil {
		return Fail(Challenge, "input-hidden")
	}
	if e = el.Input(value); e != nil {
		return Fail(Challenge, "input")
	}
	return nil
}
func Click(p *rod.Page, selector, label string) error {
	el, e := p.Timeout(30*time.Second).ElementR(selector, label)
	if e != nil {
		return Fail(Challenge, "button-missing")
	}
	if e = el.Click(proto.InputMouseButtonLeft, 1); e != nil {
		return Fail(Challenge, "button")
	}
	return nil
}
func NewPage(ctx context.Context, b *rod.Browser) (*rod.Page, error) {
	p, e := b.Context(ctx).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if e != nil {
		return nil, Fail(Temporary, "browser-page")
	}
	if e = (proto.EmulationSetTimezoneOverride{TimezoneID: "America/Halifax"}).Call(p); e != nil {
		return nil, e
	}
	return p, nil
}
