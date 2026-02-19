package notify

import (
	"context"
	"fmt"

	"github.com/gregdel/pushover"
	"github.com/nathanfredericks/transactions/internal/config"
)

type NotificationOptions struct {
	Priority int    `json:"priority,omitempty"`
	Title    string `json:"title,omitempty"`
	Sound    string `json:"sound,omitempty"`
	URL      string `json:"url,omitempty"`
	URLTitle string `json:"url_title,omitempty"`
}

func SendNotification(ctx context.Context, message string, opts NotificationOptions) error {
	secrets, err := config.GetSecrets(ctx)
	if err != nil {
		return fmt.Errorf("getting secrets: %w", err)
	}

	app := pushover.New(secrets.PushoverToken)
	recipient := pushover.NewRecipient(secrets.PushoverUser)

	var msg *pushover.Message
	if opts.Title != "" {
		msg = pushover.NewMessageWithTitle(message, opts.Title)
	} else {
		msg = pushover.NewMessage(message)
	}

	if opts.Priority != 0 {
		msg.Priority = opts.Priority
	}
	if opts.Sound != "" {
		msg.Sound = opts.Sound
	}
	if opts.URL != "" {
		msg.URL = opts.URL
	}
	if opts.URLTitle != "" {
		msg.URLTitle = opts.URLTitle
	}

	_, err = app.SendMessage(msg, recipient)
	if err != nil {
		return fmt.Errorf("sending notification: %w", err)
	}

	return nil
}
