package notify

import (
	"context"
	"errors"
	"github.com/nathanfredericks/transactions/internal/types"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNotifications(t *testing.T) {
	old, client := getSecrets, http.DefaultClient
	t.Cleanup(func() { getSecrets = old; http.DefaultClient = client })
	getSecrets = func(context.Context) (*types.Secrets, error) { return nil, errors.New("missing") }
	if SendNotification(context.Background(), "hello", NotificationOptions{}) == nil {
		t.Fatal("missing secrets accepted")
	}
	getSecrets = func(context.Context) (*types.Secrets, error) {
		return &types.Secrets{PushoverToken: strings.Repeat("t", 30), PushoverUser: strings.Repeat("u", 30)}, nil
	}
	http.DefaultClient = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		r.ParseForm()
		if r.Form.Get("message") != "hello" || r.Form.Get("title") != "Title" || r.Form.Get("priority") != "1" || r.Form.Get("sound") != "cashregister" || r.Form.Get("url_title") != "View" {
			t.Error(r.Form)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Limit-App-Limit": []string{"10000"}, "X-Limit-App-Remaining": []string{"9999"}, "X-Limit-App-Reset": []string{"1791300000"}}, Body: io.NopCloser(strings.NewReader(`{"status":1,"request":"test"}`))}, nil
	})}
	if e := SendNotification(context.Background(), "hello", NotificationOptions{Title: "Title", Priority: 1, Sound: "cashregister", URL: "https://example.com", URLTitle: "View"}); e != nil {
		t.Fatal(e)
	}
	http.DefaultClient = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, errors.New("network") })}
	if SendNotification(context.Background(), "hello", NotificationOptions{}) == nil {
		t.Fatal("failure ignored")
	}
}
