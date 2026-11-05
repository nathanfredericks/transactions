package browserbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var ErrCapacity = errors.New("browser broker capacity unavailable")

type Client struct {
	URL, Token string
	HTTP       *http.Client
}

func FromEnv() (*Client, error) {
	u := os.Getenv("BROWSER_BROKER_URL")
	token := os.Getenv("BROWSER_BROKER_TOKEN")
	parsed, e := url.Parse(u)
	if e != nil || parsed.Scheme != "https" || parsed.Host == "" || len(token) < 32 {
		return nil, ErrCapacity
	}
	return &Client{URL: strings.TrimRight(u, "/"), Token: token, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, method, path string, input, output any) error {
	var data []byte
	if input != nil {
		data, _ = json.Marshal(input)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(data))
	if err != nil {
		return ErrCapacity
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Transactions-Browser-Broker/1.0")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return ErrCapacity
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ErrCapacity
	}
	if output != nil && json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(output) != nil {
		return ErrCapacity
	}
	return nil
}
func (c *Client) Acquire(ctx context.Context, q Request) (Reservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var r Reservation
	if err := c.call(ctx, "POST", "/v1/reservations", q, &r); err != nil {
		return r, err
	}
	granted := false
	defer func() {
		if !granted && r.ID != "" {
			clean, stop := context.WithTimeout(context.Background(), 12*time.Second)
			defer stop()
			_ = c.Release(clean, r.ID)
		}
	}()
	for {
		if r.Status == "granted" && r.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
			granted = true
			return r, nil
		}
		if r.Status != "queued" && r.Status != "releasing" && r.Status != "granted" {
			return r, ErrCapacity
		}
		select {
		case <-ctx.Done():
			return r, ErrCapacity
		case <-time.After(2 * time.Second):
		}
		if err := c.call(ctx, "GET", "/v1/reservations/"+r.ID, nil, &r); err != nil {
			return r, err
		}
	}
}
func (c *Client) Release(ctx context.Context, id string) error {
	return c.call(ctx, "DELETE", "/v1/reservations/"+id, nil, nil)
}
