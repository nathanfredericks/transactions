package config

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nathanfredericks/transactions/internal/types"
	"testing"
)

func TestInvocationCacheIsolation(t *testing.T) {
	oldC, oldP, oldS := loadLatestConfiguration, loadParameters, loadSecretPayload
	t.Cleanup(func() { loadLatestConfiguration = oldC; loadParameters = oldP; loadSecretPayload = oldS })
	calls := 0
	loadLatestConfiguration = func(context.Context, *types.Env) ([]byte, error) {
		calls++
		return []byte(`{"email":[],"webhook":[]}`), nil
	}
	loadParameters = func(context.Context) (map[string]string, error) {
		return map[string]string{timezoneParameterName: "America/Halifax"}, nil
	}
	loadSecretPayload = func(context.Context, string) ([]byte, error) { return []byte(`{"YNAB_ACCESS_TOKEN":"dummy"}`), nil }
	ctx := WithInvocationCache(context.Background())
	for i := 0; i < 2; i++ {
		if _, e := GetConfig(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e := GetParameters(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e := GetSecrets(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	GetConfig(WithInvocationCache(context.Background()))
	if calls != 2 {
		t.Fatal("cache leaked across invocations")
	}
	if WithInvocationCache(ctx) != ctx {
		t.Fatal("replaced cache")
	}
}
func TestDefaultsAndSecretFormats(t *testing.T) {
	p := parametersFromValues(map[string]string{paymentProcessorsParameterName: " PAYPAL, , SQ "})
	if p.Timezone != "UTC" || len(p.PaymentProcessors) != 2 || p.OpenAIModel == "" {
		t.Fatal(p)
	}
	s := `{"YNAB_ACCESS_TOKEN":"dummy"}`
	for _, tc := range []struct {
		text   *string
		binary []byte
	}{{&s, nil}, {nil, []byte(s)}} {
		raw, e := parseSecretPayload(tc.text, tc.binary)
		if e != nil || !json.Valid(raw) {
			t.Fatal(e)
		}
	}
	if _, e := parseSecretPayload(nil, nil); e == nil {
		t.Fatal("empty secret accepted")
	}
}
func TestConfigFailuresNotCached(t *testing.T) {
	old := loadLatestConfiguration
	t.Cleanup(func() { loadLatestConfiguration = old })
	ctx := WithInvocationCache(context.Background())
	loadLatestConfiguration = func(context.Context, *types.Env) ([]byte, error) { return nil, errors.New("offline") }
	if _, e := GetConfig(ctx); e == nil {
		t.Fatal("expected load error")
	}
	loadLatestConfiguration = func(context.Context, *types.Env) ([]byte, error) { return []byte(`bad`), nil }
	if _, e := GetConfig(ctx); e == nil {
		t.Fatal("expected JSON error")
	}
}
