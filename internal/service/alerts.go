package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nathanfredericks/transactions/internal/bank"
)

// Alert is what a purchase alert states. Amount is positive milliunits, Descriptor
// is the merchant text exactly as the bank wrote it, and Date is the purchase date
// the alert names ("" when the layout carries none).
type Alert struct {
	Amount     int64
	Descriptor string
	Date       string
	// Source names the parser that read the alert, or "model".
	Source string
}

type alertLayout struct {
	name string
	re   *regexp.Regexp
	// date is the submatch index of the stated date, or 0 when absent.
	amount, descriptor, date int
	dateLayout               string
}

// Bank alert layouts, keyed by the wording each bank uses. Each must capture the
// amount and descriptor verbatim; nothing here depends on any merchant.
var alertLayouts = []alertLayout{
	{
		name:       "rogers",
		re:         regexp.MustCompile(`(?:A recurring payment of )?\$([0-9][0-9,]*\.[0-9]{2}) on ([A-Z][a-z]{2} [0-9]{2}, [0-9]{4}) on your credit card ending in [0-9]{4}\. Details: (.+?)\. https?://`),
		amount:     1,
		date:       2,
		descriptor: 3,
		dateLayout: "Jan 02, 2006",
	},
	{
		name:       "bmo",
		re:         regexp.MustCompile(`Amount: \$([0-9][0-9,]*\.[0-9]{2}) Credit card: Ending in [0-9]{4} At: (.+?) Sign in to review`),
		amount:     1,
		descriptor: 2,
	},
}

// ParseAlert reads a recognized alert layout. It reports false when no layout
// matches exactly once, so an unfamiliar or ambiguous alert is never guessed.
func ParseAlert(text string) (Alert, bool) {
	text = strings.Join(strings.Fields(text), " ")
	for _, l := range alertLayouts {
		matches := l.re.FindAllStringSubmatch(text, -1)
		if len(matches) != 1 {
			continue
		}
		m := matches[0]
		amount, err := bank.Milliunits(strings.ReplaceAll(m[l.amount], ",", ""))
		if err != nil || amount <= 0 {
			continue
		}
		descriptor := strings.TrimSpace(m[l.descriptor])
		if descriptor == "" {
			continue
		}
		out := Alert{Amount: amount, Descriptor: descriptor, Source: "parser:" + l.name}
		if l.date > 0 {
			d, err := time.Parse(l.dateLayout, m[l.date])
			if err != nil {
				continue
			}
			out.Date = d.Format("2006-01-02")
		}
		return out, true
	}
	return Alert{}, false
}

var alertSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"amount":     map[string]any{"type": "string", "description": "The purchase amount exactly as written, without the currency symbol, e.g. 1,234.56"},
		"descriptor": map[string]any{"type": "string", "description": "The merchant text exactly as written in the alert"},
	},
	"required":             []string{"amount", "descriptor"},
	"additionalProperties": false,
}

// ExtractAlert reads an alert. Recognized layouts are parsed by code; the model
// reads only unfamiliar layouts, and both values it returns must appear verbatim
// in the alert or the extraction is rejected.
func ExtractAlert(ctx context.Context, text string) (Alert, error) {
	if a, ok := ParseAlert(text); ok {
		return a, nil
	}
	raw, err := callResponsesAPI(ctx, "Read the purchase alert. Copy the purchase amount and the merchant descriptor exactly as they appear. Do not shorten, expand, translate or rename the descriptor.", text, "alert", alertSchema)
	if err != nil {
		return Alert{}, fmt.Errorf("calling AI for alert details: %w", err)
	}
	var result struct {
		Amount     string `json:"amount"`
		Descriptor string `json:"descriptor"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return Alert{}, fmt.Errorf("parsing alert details: %w", err)
	}
	normalized := strings.Join(strings.Fields(text), " ")
	amountText := strings.TrimPrefix(strings.TrimSpace(result.Amount), "$")
	descriptor := strings.Join(strings.Fields(result.Descriptor), " ")
	if amountText == "" || !strings.Contains(normalized, amountText) {
		return Alert{}, fmt.Errorf("extracted amount %q is not in the alert", result.Amount)
	}
	if descriptor == "" || !strings.Contains(normalized, descriptor) {
		return Alert{}, fmt.Errorf("extracted descriptor %q is not in the alert", result.Descriptor)
	}
	amount, err := bank.Milliunits(strings.ReplaceAll(amountText, ",", ""))
	if err != nil || amount <= 0 {
		return Alert{}, fmt.Errorf("invalid extracted amount %q", result.Amount)
	}
	return Alert{Amount: amount, Descriptor: descriptor, Source: "model"}, nil
}
