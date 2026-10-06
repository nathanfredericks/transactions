package email

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestMIMEEncodingsAndHeaders(t *testing.T) {
	html := "<p>Purchase <b>$5.69</b> at APPLE.COM/BILL</p>"
	prefix := "From: Bank <alerts@example.com>\r\nSubject: =?UTF-8?Q?Purchase_alert?=\r\nDate: Tue, 27 Oct 2026 23:30:00 -0300\r\nMessage-ID: <original>\r\n"
	for _, tc := range []struct{ ct, encoding, body string }{{"text/html", "base64", base64.StdEncoding.EncodeToString([]byte(html))}, {"text/html", "quoted-printable", strings.ReplaceAll(html, "$", "=24")}, {`multipart/mixed; boundary="outer"`, "", "--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n--inner\r\nContent-Type: text/plain\r\n\r\nplain\r\n--inner\r\nContent-Type: text/html\r\n\r\n" + html + "\r\n--inner--\r\n--outer--\r\n"}} {
		raw := prefix + "Content-Type: " + tc.ct + "\r\nContent-Transfer-Encoding: " + tc.encoding + "\r\n\r\n" + tc.body
		p, e := Parse([]byte(raw))
		if e != nil || p.From != "alerts@example.com" || p.Subject != "Purchase alert" || p.MessageID != "original" || !strings.Contains(p.HTML, "APPLE.COM/BILL") {
			t.Fatal(p, e)
		}
		if !strings.Contains(HTMLToText(p.HTML), "$5.69") {
			t.Fatal(p.HTML)
		}
	}
}
func TestMalformedEmail(t *testing.T) {
	for _, raw := range []string{"bad", "From: x\r\nDate: bad\r\n\r\nbody", "From: x\r\nDate: Tue, 27 Oct 2026 23:30:00 -0300\r\nContent-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n%%%"} {
		if _, e := Parse([]byte(raw)); e == nil {
			t.Fatal("invalid mail accepted")
		}
	}
}
