package bank

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTP deliberately does not retry: the workflow owns retry decisions.
type HTTP struct {
	Client  *http.Client
	Session *Session
	Check   func(int, []byte) error
}

func NewHTTP(s *Session) *HTTP {
	if s.Headers == nil {
		s.Headers = map[string]string{}
	}
	jar, _ := cookiejar.New(nil)
	for _, c := range s.Cookies {
		if u, e := url.Parse(c.URL); e == nil {
			copy := c.Value
			jar.SetCookies(u, []*http.Cookie{&copy})
		}
	}
	return &HTTP{Session: s, Client: &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (h *HTTP) Request(ctx context.Context, method, endpoint string, headers map[string]string, body []byte) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, Fail(Invalid, "request")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, nil, 0, &Failure{Kind: Temporary, Operation: "http", ExchangeUncertain: method != http.MethodGet && method != http.MethodHead}
	}
	defer resp.Body.Close()
	// Retain every Set-Cookie with its origin, including deletions, for the next invocation.
	for _, c := range resp.Cookies() {

		if c.Path == "" {
			c.Path = "/"
			if i := strings.LastIndex(req.URL.Path, "/"); i > 0 {
				c.Path = req.URL.Path[:i]
			}
		}
		if c.MaxAge > 0 {
			c.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
			c.MaxAge = 0
		}
		kept := h.Session.Cookies[:0]
		for _, old := range h.Session.Cookies {
			if old.URL != req.URL.Scheme+"://"+req.URL.Host+c.Path || old.Value.Name != c.Name || old.Value.Domain != c.Domain || old.Value.Path != c.Path {
				kept = append(kept, old)
			}
		}
		h.Session.Cookies = kept
		if c.MaxAge >= 0 && (c.Expires.IsZero() || c.Expires.After(time.Now())) {
			h.Session.Cookies = append(h.Session.Cookies, Cookie{URL: req.URL.Scheme + "://" + req.URL.Host + c.Path, Value: *c})
		}
	}
	// Rotation is captured before decoding or classifying the response.
	for _, name := range []string{"accesstoken", "refreshtoken"} {
		if v := resp.Header.Get(name); v != "" {
			h.Session.Headers[name] = v
			if name == "accesstoken" {
				h.Session.Headers["authorization"] = "Bearer " + v
			}
		}
	}
	if v := resp.Header.Get("expirytime"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil {
			if n < 1e12 {
				n *= 1000
			}
			h.Session.ExpiresAt = time.UnixMilli(n)
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
	if err != nil {
		return nil, resp.Header, resp.StatusCode, &Failure{Kind: Temporary, Operation: "read-response", ExchangeUncertain: method != http.MethodGet && method != http.MethodHead}
	}
	if len(data) > 16*1024*1024 {
		return nil, resp.Header, resp.StatusCode, Fail(Invalid, "response-too-large")
	}
	return data, resp.Header, resp.StatusCode, nil
}
func (h *HTTP) JSON(ctx context.Context, method, endpoint string, headers map[string]string, body []byte, out any) error {
	data, head, status, err := h.Request(ctx, method, endpoint, headers, body)
	if err != nil {
		return err
	}
	if h.Check != nil {
		if err := h.Check(status, data); err != nil {
			return err
		}
	}
	if status == 429 {
		return Throttle("http", head.Get("Retry-After"))
	}
	if status == 401 || status == 419 || status == 440 {
		return Fail(Authentication, "http")
	}
	var problem struct {
		Error  string          `json:"error"`
		Status json.RawMessage `json:"status"`
	}
	_ = json.Unmarshal(data, &problem)
	if string(problem.Status) == "440" || string(problem.Status) == `"440"` {
		return Fail(Authentication, "http")
	}
	switch problem.Error {
	case "invalid_grant", "invalid_token", "login_required", "session_expired":
		return Fail(Authentication, "token")
	}
	if status >= 500 {
		return &Failure{Kind: Temporary, Operation: "http", ExchangeUncertain: method != http.MethodGet && method != http.MethodHead}
	}
	if status < 200 || status >= 300 {
		return Fail(Invalid, "http-status-"+strconv.Itoa(status))
	}
	if json.Unmarshal(data, out) != nil {
		return Fail(Invalid, "json")
	}
	return nil
}

func Throttle(operation, retryAfter string) error {
	at := time.Now().Add(time.Minute)
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		at = time.Now().Add(time.Duration(seconds) * time.Second)
	} else if date, err := http.ParseTime(retryAfter); err == nil {
		at = date
	}
	return &Failure{Kind: Throttled, Operation: operation, RetryAt: at}
}
func Headers(s Session, extra map[string]string) map[string]string {
	h := map[string]string{}
	for k, v := range s.Headers {
		h[k] = v
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}
func TokenTiming(s *Session) {
	token := strings.TrimPrefix(s.Headers["authorization"], "Bearer ")
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if b, e := base64.RawURLEncoding.DecodeString(parts[1]); e == nil {
			var c map[string]any
			if json.Unmarshal(b, &c) == nil {
				if exp, ok := c["exp"].(float64); ok {
					s.ExpiresAt = time.Unix(int64(exp), 0)
				}
				switch v := c["https://eqbank.ca/session/max_lifetime"].(type) {
				case float64:
					s.MaximumExpiresAt = time.Unix(int64(v), 0)
				case string:
					s.MaximumExpiresAt, _ = time.Parse(time.RFC3339, v)
				}
			}
		}
	}
	if !s.ExpiresAt.IsZero() {
		s.RenewAt = s.ExpiresAt.Add(-2 * time.Minute)
	} else {
		s.RenewAt = time.Now().Add(5 * time.Minute)
	}
}
func Milliunits(s string) (int64, error) {
	sign := int64(1)
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	p := strings.Split(s, ".")
	if len(p) > 2 || len(p[0]) == 0 {
		return 0, Fail(Invalid, "amount")
	}
	fraction := ""
	if len(p) == 2 {
		fraction = p[1]
	}
	if len(fraction) > 3 {
		return 0, Fail(Invalid, "amount")
	}
	digits := p[0] + fraction + strings.Repeat("0", 3-len(fraction))
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, Fail(Invalid, "amount")
		}
	}
	n, e := strconv.ParseInt(digits, 10, 64)
	if e != nil {
		return 0, Fail(Invalid, "amount")
	}
	return sign * n, nil
}
