// Package smtpmail is a thin STARTTLS SMTP sender tailored to the Google
// Workspace relay (smtp-relay.gmail.com:587). It's deliberately minimal —
// the briefnotify email deliverer owns MIME composition and template
// rendering; this package only speaks SMTP.
//
// Error classification is the package's main responsibility besides the
// actual send: 4xx responses are wrapped in briefnotify.Retryable so the
// worker retries them, while 5xx responses surface as-is (permanent).
// Network/TLS/dial failures are also considered retryable — they're
// almost always transient (DNS blip, TLS handshake race, brief outage).
package smtpmail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/services/briefnotify"
)

// Config carries the static SMTP credentials + endpoint. All fields
// are required except DialTimeout which has a sensible default.
type Config struct {
	Host         string        // e.g. smtp-relay.gmail.com
	Port         int           // e.g. 587
	AuthUser     string        // full email for SMTP auth (ör. admin@syncrun.net)
	AuthPassword string        // Google App Password
	DialTimeout  time.Duration // per-connection dial timeout (default 20s)
}

// Validate returns a non-nil error when the config is missing anything
// required to place a call. Call at wire-up time (main.go) — if the
// deploy is mis-configured we refuse to register the deliverer rather
// than failing every notify_brief job at run time.
func (c Config) Validate() error {
	switch {
	case c.Host == "":
		return errors.New("smtpmail: SMTP_HOST is required")
	case c.Port <= 0:
		return errors.New("smtpmail: SMTP_PORT must be > 0")
	case c.AuthUser == "":
		return errors.New("smtpmail: BOT_MAIL_AUTH_USER is required")
	case c.AuthPassword == "":
		return errors.New("smtpmail: BOT_MAIL_APP_PASSWORD is required")
	}
	return nil
}

// Payload is what Send accepts. Caller (email deliverer) is expected
// to have already composed the full RFC-5322 message body in Message,
// including headers and MIME boundaries — we don't post-process it.
// MessageID is the value we injected into the Message-Id header so the
// caller can log / persist it as provider_message_id.
type Payload struct {
	From      string   // RFC 5321 envelope sender (SMTP MAIL FROM)
	To        []string // RFC 5321 envelope recipients (SMTP RCPT TO)
	Message   []byte   // pre-composed RFC 5322 message (headers + body)
	MessageID string   // caller-generated, mirrored in headers
}

// Sender is the low-level send primitive. Production uses netSMTPSend;
// unit tests swap it out to return classified errors without touching
// the network.
type Sender func(ctx context.Context, cfg Config, payload Payload) error

// Client is the thin wrapper that adds validation + error
// classification over the raw Sender function.
type Client struct {
	cfg    Config
	sender Sender
}

// New builds a Client backed by the stdlib net/smtp implementation.
// The DialTimeout default is 20s — the Google relay is usually sub-
// second, so 20s leaves generous headroom for cold TLS handshakes.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 20 * time.Second
	}
	return &Client{cfg: cfg, sender: netSMTPSend}, nil
}

// WithSender swaps the send primitive — test-only hook.
func (c *Client) WithSender(s Sender) *Client {
	c.sender = s
	return c
}

// Send delivers a single pre-composed message to the SMTP relay. It
// returns the provider message id (echoes payload.MessageID so the
// caller doesn't have to track both) and a classified error.
func (c *Client) Send(ctx context.Context, payload Payload) (string, error) {
	if payload.From == "" {
		return "", errors.New("smtpmail: payload.From is required")
	}
	if len(payload.To) == 0 {
		return "", errors.New("smtpmail: payload.To is required")
	}
	if len(payload.Message) == 0 {
		return "", errors.New("smtpmail: payload.Message is required")
	}
	if err := c.sender(ctx, c.cfg, payload); err != nil {
		return "", ClassifyError(err)
	}
	return payload.MessageID, nil
}

// ClassifyError wraps transient failures in briefnotify.Retryable and
// returns permanent failures verbatim. Also bumps the SMTPErrors
// counter so ops dashboards can distinguish "greylisting blip" (4xx
// spike) from "bad DNS / TLS" (network) from "bad address rejected"
// (5xx). Exposed for tests and for the deliverer to reuse when
// composing multi-step errors.
func ClassifyError(err error) error {
	if err == nil {
		return nil
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		// SMTP reply codes: 4xx = transient, 5xx = permanent.
		// 421 (service not available) + 450/451/452 (mailbox busy /
		// greylisted / over quota) are the canonical retry cases.
		if te.Code >= 400 && te.Code < 500 {
			metrics.SMTPErrors.WithLabelValues(metrics.SMTPErrClass4xx).Inc()
			return briefnotify.Retryable(err)
		}
		metrics.SMTPErrors.WithLabelValues(metrics.SMTPErrClass5xx).Inc()
		return err
	}
	// Network + TLS + dial errors — treat as retryable. A backend
	// restart, brief DNS issue, or TLS handshake glitch is almost
	// always worth one more try; we cap attempts at 3 in the worker.
	metrics.SMTPErrors.WithLabelValues(metrics.SMTPErrClassNetwork).Inc()
	return briefnotify.Retryable(err)
}

// netSMTPSend is the default Sender. It dials the relay, upgrades to
// STARTTLS, authenticates with PLAIN, and streams the message. We
// build the dial string manually instead of using smtp.SendMail so we
// can honour a context deadline on the dial.
func netSMTPSend(ctx context.Context, cfg Config, payload Payload) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var dialer net.Dialer
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	} else {
		dialer.Timeout = cfg.DialTimeout
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtpmail: dial %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtpmail: smtp handshake: %w", err)
	}
	defer func() { _ = client.Quit() }()

	if err := client.Hello(clientHello(cfg)); err != nil {
		return fmt.Errorf("smtpmail: EHLO: %w", err)
	}

	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("smtpmail: relay does not advertise STARTTLS — refusing to send in plaintext")
	}
	if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("smtpmail: STARTTLS: %w", err)
	}

	auth := smtp.PlainAuth("", cfg.AuthUser, cfg.AuthPassword, cfg.Host)
	if ok, _ := client.Extension("AUTH"); ok {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtpmail: AUTH: %w", err)
		}
	}

	if err := client.Mail(payload.From); err != nil {
		return fmt.Errorf("smtpmail: MAIL FROM: %w", err)
	}
	for _, rcpt := range payload.To {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtpmail: RCPT TO %s: %w", rcpt, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtpmail: DATA: %w", err)
	}
	if _, err := w.Write(payload.Message); err != nil {
		return fmt.Errorf("smtpmail: write DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtpmail: close DATA: %w", err)
	}
	return nil
}

// clientHello picks a helo name that matches the auth account's
// domain (ör. "syncrun.net"). Google's relay relaxes this check but
// submitting a non-loopback identifier is still the correct behaviour.
func clientHello(cfg Config) string {
	for i, ch := range cfg.AuthUser {
		if ch == '@' && i+1 < len(cfg.AuthUser) {
			return cfg.AuthUser[i+1:]
		}
	}
	return "localhost"
}
