package bank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/k3a/html2text"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type MailChallenge struct {
	After           time.Time
	Sender, Subject string
	Prefix          bool
	Length          int
}

func ReadCode(ctx context.Context, token string, c MailChallenge) (string, error) {
	// Fastmail receivedAt is observed at whole-second precision. Keep the
	// same precision on both sides so a fast code in the request's second
	// is not discarded as older than a fractional-second client timestamp.
	c.After = c.After.UTC().Truncate(time.Second)
	slog.Info("email verification waiting", "sender", c.Sender, "after", c.After)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(endpoint string, body any, out any) error {
		u, e := url.Parse(endpoint)
		if e != nil || u.Scheme != "https" || (u.Host != "api.fastmail.com" && !strings.HasSuffix(u.Host, ".api.fastmail.com")) {
			return Fail(Invalid, "mail-origin")
		}
		var r io.Reader
		method := "GET"
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
			method = "POST"
		}
		req, e := http.NewRequestWithContext(ctx, method, endpoint, r)
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			return Fail(Temporary, "mail")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return Fail(Temporary, "mail")
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(out)
	}
	var session struct {
		API      string            `json:"apiUrl"`
		Accounts map[string]string `json:"primaryAccounts"`
	}
	if e := request("https://api.fastmail.com/jmap/session", nil, &session); e != nil {
		return "", e
	}
	account := session.Accounts["urn:ietf:params:jmap:mail"]
	if account == "" || c.Length < 4 || c.Length > 12 {
		return "", Fail(Invalid, "mail-config")
	}
	call := func(method string, args map[string]any, out any) error {
		args["accountId"] = account
		var response struct {
			Methods [][]json.RawMessage `json:"methodResponses"`
		}
		e := request(session.API, map[string]any{"using": []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"}, "methodCalls": [][]any{{method, args, "request"}}}, &response)
		if e != nil {
			return e
		}
		if len(response.Methods) != 1 || len(response.Methods[0]) < 2 {
			return Fail(Invalid, "mail-response")
		}
		var got string
		_ = json.Unmarshal(response.Methods[0][0], &got)
		if got != method {
			return Fail(Temporary, "mail-method")
		}
		return json.Unmarshal(response.Methods[0][1], out)
	}
	pattern := regexp.MustCompile(fmt.Sprintf(`\b[0-9]{%d}\b`, c.Length))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var query struct {
			IDs []string `json:"ids"`
		}
		if e := call("Email/query", map[string]any{"filter": map[string]any{"after": c.After.UTC().Format(time.RFC3339Nano), "from": c.Sender, "subject": c.Subject}, "sort": []any{map[string]any{"property": "receivedAt", "isAscending": false}}, "limit": 100}, &query); e != nil {
			return "", e
		}
		if len(query.IDs) > 0 {
			var result struct {
				List []struct {
					Subject  string    `json:"subject"`
					Received time.Time `json:"receivedAt"`
					From     []struct {
						Email string `json:"email"`
					} `json:"from"`
					Text []struct {
						Part string `json:"partId"`
					} `json:"textBody"`
					HTML []struct {
						Part string `json:"partId"`
					} `json:"htmlBody"`
					Bodies map[string]struct {
						Value     string `json:"value"`
						Truncated bool   `json:"isTruncated"`
					} `json:"bodyValues"`
				} `json:"list"`
			}
			if e := call("Email/get", map[string]any{"ids": query.IDs, "properties": []string{"id", "subject", "receivedAt", "from", "textBody", "htmlBody", "bodyValues"}, "fetchTextBodyValues": true, "fetchHTMLBodyValues": true, "maxBodyValueBytes": 100000}, &result); e != nil {
				return "", e
			}
			for _, mail := range result.List {
				sender := false
				for _, f := range mail.From {
					sender = sender || strings.EqualFold(f.Email, c.Sender)
				}
				if !sender || mail.Received.Before(c.After) || (mail.Subject != c.Subject && !(c.Prefix && strings.HasPrefix(mail.Subject, c.Subject))) {
					continue
				}
				codes := map[string]bool{}
				for _, part := range append(mail.Text, mail.HTML...) {
					v := mail.Bodies[part.Part]
					if v.Truncated {
						continue
					}
					for _, code := range pattern.FindAllString(html2text.HTML2Text(v.Value), -1) {
						codes[code] = true
					}
				}
				if len(codes) == 1 {
					for code := range codes {
						return code, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", Fail(Temporary, "email-code-timeout")
		case <-ticker.C:
		}
	}
}
