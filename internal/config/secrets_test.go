package config

import (
	"context"
	"strings"
	"testing"
)

const mergedSecret = `{
	"WEBHOOK_TOKEN": "w", "YNAB_ACCESS_TOKEN": "y", "OPENAI_API_KEY": "o",
	"PUSHOVER_TOKEN": "p", "PUSHOVER_USER": "u",
	"CLOAKBROWSER_LICENSE_KEY": "license", "BROWSER_BROKER_TOKEN": "broker",
	"BANKS": {
		"eq-bank": {"username": "eq-user", "password": "eq-pass", "mailToken": "eq-mail"},
		"nbdb": {"username": "nb-user", "password": "", "mailToken": "nb-mail"}
	}
}`

func withPayload(t *testing.T, payload string) {
	t.Helper()
	previous := loadSecretPayload
	loadSecretPayload = func(context.Context, string) ([]byte, error) { return []byte(payload), nil }
	t.Cleanup(func() { loadSecretPayload = previous })
}

func TestGetSecretsKeepsLambdaKeys(t *testing.T) {
	withPayload(t, mergedSecret)
	s, err := GetSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.WebhookToken != "w" || s.YNABAccessToken != "y" || s.OpenAIAPIKey != "o" || s.PushoverToken != "p" || s.PushoverUser != "u" {
		t.Fatalf("lambda keys changed: %+v", s)
	}
	if s.CloakBrowserLicenseKey != "license" || s.BrowserBrokerToken != "broker" {
		t.Fatalf("browser keys missing: %+v", s)
	}
}

func TestBankCredentials(t *testing.T) {
	withPayload(t, mergedSecret)
	c, err := BankCredentials(context.Background(), "eq-bank")
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "eq-user" || c.Password != "eq-pass" || c.MailToken != "eq-mail" {
		t.Fatalf("BankCredentials() = %+v", c)
	}
	for _, id := range []string{"nbdb", "rogers-bank"} {
		if _, err := BankCredentials(context.Background(), id); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("BankCredentials(%q) error = %v, want incomplete", id, err)
		}
	}
}
