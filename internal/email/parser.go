package email

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	// Registers the character sets bank alert emails use.
	_ "github.com/emersion/go-message/charset"
	message "github.com/emersion/go-message/mail"
	"github.com/k3a/html2text"
)

type ParsedEmail struct {
	MessageID, From, Subject, HTML, Text string
	// Authentication is the topmost Authentication-Results header, which SES
	// adds on receipt; any copies a sender supplied come below it.
	Authentication string
	Date           time.Time
}

// SenderVerified reports whether SES found the message passes DMARC for the
// domain in its From address, so that address can be trusted.
func (p *ParsedEmail) SenderVerified() bool {
	_, domain, ok := strings.Cut(p.From, "@")
	if !ok {
		return false
	}
	fields := strings.FieldsFunc(strings.ToLower(p.Authentication), func(r rune) bool { return r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(fields) == 0 || fields[0] != "amazonses.com" {
		return false
	}
	for i, field := range fields {
		if field == "dmarc=pass" && i+1 < len(fields) && fields[i+1] == "header.from="+strings.ToLower(domain) {
			return true
		}
	}
	return false
}

func Parse(raw []byte) (*ParsedEmail, error) {
	r, e := message.CreateReader(bytes.NewReader(raw))
	if e != nil {
		return nil, fmt.Errorf("reading email: %w", e)
	}
	defer r.Close()
	from, e := r.Header.AddressList("From")
	if e != nil || len(from) != 1 {
		return nil, errors.New("invalid email sender")
	}
	date, e := r.Header.Date()
	if e != nil {
		return nil, e
	}
	subject, e := r.Header.Subject()
	if e != nil {
		return nil, e
	}
	result := &ParsedEmail{MessageID: strings.Trim(r.Header.Get("Message-ID"), "<> \t"), From: from[0].Address, Subject: subject, Authentication: r.Header.Get("Authentication-Results"), Date: date}
	for {
		part, e := r.NextPart()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, e
		}
		h, ok := part.Header.(*message.InlineHeader)
		if !ok {
			continue
		}
		kind, _, e := h.ContentType()
		if e != nil {
			return nil, e
		}
		if kind != "text/plain" && kind != "text/html" {
			continue
		}
		body, e := io.ReadAll(io.LimitReader(part.Body, 1024*1024+1))
		if e != nil || len(body) > 1024*1024 {
			return nil, errors.New("email part too large or unreadable")
		}
		if kind == "text/html" {
			result.HTML = string(body)
		} else {
			result.Text = string(body)
		}
	}
	if result.Text == "" {
		result.Text = HTMLToText(result.HTML)
	}
	if result.Text == "" {
		return nil, errors.New("email has no readable body")
	}
	return result, nil
}

func HTMLToText(value string) string {
	return strings.Join(strings.Fields(html2text.HTML2Text(value)), " ")
}
