package nbdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/nathanfredericks/transactions/internal/bank"
	"math"
	"net/url"
	"strings"
	"time"
)

const summary = "https://iiroc.investments.apis.bnc.ca/orion-api/v1/1/portfolios/summary"

type Adapter struct{ Dependencies bank.Dependencies }
type auth struct {
	TokenURL    string            `json:"tokenUrl"`
	ClientID    string            `json:"clientId"`
	RedirectURI string            `json:"redirectUri"`
	Scope       string            `json:"scope"`
	Headers     map[string]string `json:"headers"`
}

func (a *Adapter) Renew(ctx context.Context, s bank.Session) (bank.Session, error) {
	var c auth
	if json.Unmarshal(s.Auth, &c) != nil {
		return s, bank.Fail(bank.Authentication, "renew")
	}
	endpoint, e := url.Parse(c.TokenURL)
	redirect, re := url.Parse(c.RedirectURI)
	if e != nil || re != nil || endpoint.Scheme != "https" || endpoint.Host != "api.bnc.ca" || !strings.HasPrefix(endpoint.Path, "/bnc/prod-okta/sso/oauth2/") || !strings.HasSuffix(endpoint.Path, "/v1/token") || redirect.Scheme != "https" || redirect.Host != "client.bnc.ca" {
		return s, bank.Fail(bank.Invalid, "authorization-config")
	}
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		return s, e
	}
	verifier := base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(verifier))
	state := bank.Hash(verifier + "state")
	target := *endpoint
	target.Path = strings.TrimSuffix(target.Path, "/token") + "/authorize"
	target.RawQuery = url.Values{"client_id": {c.ClientID}, "redirect_uri": {c.RedirectURI}, "response_type": {"code"}, "response_mode": {"query"}, "scope": {c.Scope}, "prompt": {"none"}, "state": {state}, "nonce": {bank.Hash(verifier + "nonce")}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}.Encode()
	h := bank.NewHTTP(&s)
	for i := 0; i < 6; i++ {
		_, headers, status, e := h.Request(ctx, "GET", target.String(), map[string]string{"user-agent": s.Headers["user-agent"], "accept": "text/html"}, nil)
		if e != nil {
			return s, e
		}
		if status == 429 {
			return s, bank.Throttle("authorization", headers.Get("Retry-After"))
		}
		if status >= 500 {
			return s, bank.Fail(bank.Temporary, "authorization")
		}
		if status == 401 || status == 419 || status == 440 {
			return s, bank.Fail(bank.Authentication, "authorization")
		}
		if status < 300 || status >= 400 {
			return s, bank.Fail(bank.Invalid, "authorization-response")
		}
		location := headers.Get("Location")
		if location == "" {
			return s, bank.Fail(bank.Invalid, "authorization-redirect")
		}
		next, e := target.Parse(location)
		if e != nil {
			return s, bank.Fail(bank.Invalid, "redirect")
		}
		if next.Scheme == redirect.Scheme && next.Host == redirect.Host && next.Path == redirect.Path {
			if next.Query().Get("state") != state {
				return s, bank.Fail(bank.Invalid, "authorization-state")
			}
			switch next.Query().Get("error") {
			case "login_required", "interaction_required":
				return s, bank.Fail(bank.Authentication, "authorization")
			case "":
			default:
				return s, bank.Fail(bank.Invalid, "authorization-error")
			}
			if next.Query().Get("code") == "" {
				return s, bank.Fail(bank.Invalid, "authorization-code")
			}
			body := url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ClientID}, "redirect_uri": {c.RedirectURI}, "code_verifier": {verifier}, "code": {next.Query().Get("code")}}
			var data struct {
				Access string `json:"access_token"`
			}
			headers := c.Headers
			if headers == nil {
				headers = map[string]string{}
			}
			headers["content-type"] = "application/x-www-form-urlencoded"
			e = h.JSON(ctx, "POST", c.TokenURL, headers, []byte(body.Encode()), &data)
			if e != nil {
				return s, e
			}
			if data.Access == "" {
				return s, bank.Fail(bank.Invalid, "token")
			}
			s.Headers["authorization"] = "Bearer " + data.Access
			bank.TokenTiming(&s)
			return s, nil
		}
		if next.Scheme != endpoint.Scheme || next.Host != endpoint.Host || !strings.HasPrefix(next.Path, "/bnc/prod-okta/sso/") {
			return s, bank.Fail(bank.Authentication, "authorization")
		}
		target = *next
	}
	return s, bank.Fail(bank.Authentication, "authorization")
}
func (a *Adapter) Fetch(ctx context.Context, s bank.Session, r bank.FetchRequest) (bank.Snapshot, bank.Session, error) {
	out := bank.Snapshot{At: time.Now()}
	var data struct {
		Data struct {
			Portfolios []struct {
				Accounts []struct {
					Number string `json:"acctNo"`
					Name   string `json:"acctTypeDesc"`
					Value  struct {
						CAD *struct {
							Total json.Number `json:"total"`
						} `json:"CAD"`
					} `json:"accountSummaryEvalByCurrency"`
				} `json:"accountSummaries"`
			} `json:"portfolioSummaryList"`
		} `json:"data"`
	}
	e := bank.NewHTTP(&s).JSON(ctx, "GET", summary, s.Headers, nil, &data)
	if e != nil {
		return out, s, e
	}
	if len(data.Data.Portfolios) != 1 {
		return out, s, bank.Fail(bank.Invalid, "portfolio-count")
	}
	for _, v := range data.Data.Portfolios[0].Accounts {
		if v.Number == "" || v.Name == "" || v.Value.CAD == nil {
			return out, s, bank.Fail(bank.Invalid, "portfolio-account")
		}
		// Portfolio valuations can have fractional cents. Match the existing JS
		// importer's Math.round(value * 1000), including ties toward +infinity.
		value, err := v.Value.CAD.Total.Float64()
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value*1000) > (1<<53)-1 {
			return out, s, bank.Fail(bank.Invalid, "portfolio-value")
		}
		n := int64(math.Floor(value*1000 + 0.5))
		out.Accounts = append(out.Accounts, bank.Account{ID: bank.AccountID(v.Number), Number: v.Number, Name: v.Name, Balance: n})
	}
	if len(out.Accounts) == 0 {
		return out, s, bank.Fail(bank.Invalid, "empty-portfolio")
	}
	return out, s, nil
}
