package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/k3a/html2text"
)

type ParsedEmail struct {
	From    string
	Subject string
	Date    time.Time
	HTML    string
}

func Parse(raw []byte) (*ParsedEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("reading email message: %w", err)
	}

	from := msg.Header.Get("From")
	if addr, err := mail.ParseAddress(from); err == nil {
		from = addr.Address
	}

	subject := msg.Header.Get("Subject")
	dec := new(mime.WordDecoder)
	if decoded, err := dec.DecodeHeader(subject); err == nil {
		subject = decoded
	}

	date, err := msg.Header.Date()
	if err != nil {
		return nil, fmt.Errorf("parsing email date: %w", err)
	}

	htmlBody, err := findHTMLBody(msg.Header, msg.Body)
	if err != nil {
		return nil, fmt.Errorf("finding HTML body: %w", err)
	}

	return &ParsedEmail{
		From:    from,
		Subject: subject,
		Date:    date,
		HTML:    htmlBody,
	}, nil
}

func findHTMLBody(header mail.Header, body io.Reader) (string, error) {
	contentType := header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("parsing content type: %w", err)
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		return walkMultipart(params["boundary"], body)
	}

	decoded, err := decodeBody(header.Get("Content-Transfer-Encoding"), body)
	if err != nil {
		return "", err
	}

	if mediaType == "text/html" {
		return decoded, nil
	}

	return "", nil
}

func walkMultipart(boundary string, body io.Reader) (string, error) {
	reader := multipart.NewReader(body, boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading MIME part: %w", err)
		}

		contentType := part.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/plain"
		}

		mediaType, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			continue
		}

		if strings.HasPrefix(mediaType, "multipart/") {
			result, err := walkMultipart(params["boundary"], part)
			if err != nil {
				return "", err
			}
			if result != "" {
				return result, nil
			}
			continue
		}

		if mediaType == "text/html" {
			decoded, err := decodeBody(part.Header.Get("Content-Transfer-Encoding"), part)
			if err != nil {
				return "", err
			}
			return decoded, nil
		}
	}
	return "", nil
}

func decodeBody(encoding string, r io.Reader) (string, error) {
	var reader io.Reader
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		reader = quotedprintable.NewReader(r)
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, r)
	default:
		reader = r
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("decoding body: %w", err)
	}
	return string(data), nil
}

func HTMLToText(htmlContent string) string {
	text := html2text.HTML2Text(htmlContent)

	spaceRe := regexp.MustCompile(`[ \t]+`)
	text = spaceRe.ReplaceAllString(text, " ")

	newlineRe := regexp.MustCompile(`\n{3,}`)
	text = newlineRe.ReplaceAllString(text, "\n\n")

	text = strings.TrimSpace(text)
	return text
}
