package smtpmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime/quotedprintable"
	"strings"
	"time"

	"mia-website/backend/internal/services/briefnotify"
)

// BriefAdapter wraps a *Client so it satisfies
// briefnotify.EmailTransport. It handles the MIME composition
// (multipart/alternative with quoted-printable text+html bodies) that
// the deliverer deliberately left out so briefnotify stays free of
// SMTP / MIME concerns.
//
// Every outgoing message is UTF-8 and quoted-printable encoded — this
// is the safest combination for Turkish characters across legacy mail
// clients and keeps individual lines under the 998-byte RFC 5322 limit.
type BriefAdapter struct {
	client *Client
}

// NewBriefAdapter returns an adapter. Client must be non-nil.
func NewBriefAdapter(client *Client) *BriefAdapter {
	return &BriefAdapter{client: client}
}

// Send composes the outgoing MIME envelope and delegates to the
// underlying SMTP client. Return values mirror briefnotify.EmailTransport
// verbatim — the message id is the same value that went into the
// outgoing Message-Id header (provider_message_id in the audit table).
func (a *BriefAdapter) Send(ctx context.Context, p briefnotify.EmailPayload) (string, error) {
	if a.client == nil {
		return "", fmt.Errorf("smtpmail: BriefAdapter has nil client")
	}
	if p.From == "" {
		return "", fmt.Errorf("smtpmail: EmailPayload.From required")
	}
	if len(p.To) == 0 {
		return "", fmt.Errorf("smtpmail: EmailPayload.To required")
	}

	msg, err := composeMIME(p)
	if err != nil {
		return "", fmt.Errorf("smtpmail: compose MIME: %w", err)
	}

	envelopeFrom := extractAddress(p.From)
	return a.client.Send(ctx, Payload{
		From:      envelopeFrom,
		To:        p.To,
		Message:   msg,
		MessageID: p.MessageID,
	})
}

// composeMIME produces an RFC 5322 / MIME message body as bytes.
// Structure:
//
//	Headers (From/To/Subject/Date/Message-Id + extras)
//	Content-Type: multipart/alternative; boundary="..."
//	<boundary> text/plain (quoted-printable)
//	<boundary> text/html  (quoted-printable)
//	<boundary--> closing
func composeMIME(p briefnotify.EmailPayload) ([]byte, error) {
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer

	// Top-level headers. CRLF line endings throughout per RFC 5322.
	writeHeader(&b, "From", p.From)
	writeHeader(&b, "To", strings.Join(p.To, ", "))
	writeHeader(&b, "Subject", p.Subject)
	writeHeader(&b, "Date", time.Now().UTC().Format(time.RFC1123Z))
	if p.MessageID != "" {
		writeHeader(&b, "Message-Id", p.MessageID)
	}
	for k, v := range p.ExtraHeaders {
		if strings.EqualFold(k, "Content-Type") {
			// Guard: we own Content-Type for the outer envelope.
			continue
		}
		writeHeader(&b, k, v)
	}
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n") // blank line terminates headers

	// text/plain part
	b.WriteString("--" + boundary + "\r\n")
	writeHeader(&b, "Content-Type", "text/plain; charset=UTF-8")
	writeHeader(&b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	if err := writeQP(&b, p.Text); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")

	// text/html part
	b.WriteString("--" + boundary + "\r\n")
	writeHeader(&b, "Content-Type", "text/html; charset=UTF-8")
	writeHeader(&b, "Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	if err := writeQP(&b, p.HTML); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")

	// Closing boundary
	b.WriteString("--" + boundary + "--\r\n")

	return b.Bytes(), nil
}

func writeHeader(b *bytes.Buffer, name, value string) {
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\r\n")
}

// writeQP encodes body as RFC 2045 quoted-printable. The stdlib
// quotedprintable writer handles line-length + special-char escaping.
func writeQP(b *bytes.Buffer, body string) error {
	qp := quotedprintable.NewWriter(b)
	if _, err := qp.Write([]byte(body)); err != nil {
		return err
	}
	return qp.Close()
}

// randomBoundary returns a MIME boundary of the form
// "====syncrun-<hex>====" — unlikely to clash with any content we
// ever send and long enough to satisfy every mail client's parser.
func randomBoundary() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "====syncrun-" + hex.EncodeToString(buf) + "====", nil
}

// extractAddress pulls the bare address out of a formatted From value
// like `"Syncrun" <brief@syncrun.net>`. SMTP MAIL FROM requires the
// envelope address without the display name.
func extractAddress(formatted string) string {
	if lt := strings.LastIndex(formatted, "<"); lt != -1 {
		if gt := strings.LastIndex(formatted, ">"); gt > lt {
			return formatted[lt+1 : gt]
		}
	}
	return formatted
}
