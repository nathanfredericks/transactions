package email

import (
	"bytes"
	"fmt"
	_ "github.com/emersion/go-message/charset"
	message "github.com/emersion/go-message/mail"
	"github.com/k3a/html2text"
	"io"
	"strings"
	"time"
)

type ParsedEmail struct {
	MessageID, From, Subject, HTML, Text string
	Date                                 time.Time
}

func Parse(raw []byte) (*ParsedEmail, error) {
	r, e := message.CreateReader(bytes.NewReader(raw))
	if e != nil {
		return nil, fmt.Errorf("reading email: %w", e)
	}
	defer r.Close()
	from, e := r.Header.AddressList("From")
	if e != nil || len(from) != 1 {
		return nil, fmt.Errorf("invalid email sender")
	}
	date, e := r.Header.Date()
	if e != nil {
		return nil, e
	}
	subject, e := r.Header.Subject()
	if e != nil {
		return nil, e
	}
	result := &ParsedEmail{MessageID: strings.Trim(r.Header.Get("Message-ID"), "<> \t"), From: from[0].Address, Subject: subject, Date: date}
	for {
		part, e := r.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if h, ok := part.Header.(*message.InlineHeader); ok {
			kind, _, e := h.ContentType()
			if e != nil {
				return nil, e
			}
			if kind != "text/plain" && kind != "text/html" {
				continue
			}
			body, e := io.ReadAll(io.LimitReader(part.Body, 1024*1024+1))
			if e != nil || len(body) > 1024*1024 {
				return nil, fmt.Errorf("email part too large or unreadable")
			}
			if kind == "text/html" {
				result.HTML = string(body)
			} else {
				result.Text = string(body)
			}
		}
	}
	if result.Text == "" {
		result.Text = HTMLToText(result.HTML)
	}
	if result.Text == "" {
		return nil, fmt.Errorf("email has no readable body")
	}
	return result, nil
}
func HTMLToText(value string) string {
	return strings.Join(strings.Fields(html2text.HTML2Text(value)), " ")
}
