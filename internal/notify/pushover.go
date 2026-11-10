package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/config"
)

// client posts to Pushover and never follows a redirect.
var client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type NotificationOptions struct {
	Priority int
	Title    string
	Sound    string
	URL      string
	URLTitle string
}

// SendNotification sends one Pushover message.
func SendNotification(ctx context.Context, message string, opts NotificationOptions) error {
	secrets, err := config.GetSecrets(ctx)
	if err != nil {
		return fmt.Errorf("getting secrets: %w", err)
	}
	form := url.Values{"token": {secrets.PushoverToken}, "user": {secrets.PushoverUser}, "message": {message}}
	for name, value := range map[string]string{"title": opts.Title, "sound": opts.Sound, "url": opts.URL, "url_title": opts.URLTitle} {
		if value != "" {
			form.Set(name, value)
		}
	}
	if opts.Priority != 0 {
		form.Set("priority", strconv.Itoa(opts.Priority))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pushover.net/1/messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending notification: %w", err)
	}
	defer resp.Body.Close()
	if _, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024)); err != nil {
		return fmt.Errorf("reading notification response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pushover: status %d", resp.StatusCode)
	}
	return nil
}
