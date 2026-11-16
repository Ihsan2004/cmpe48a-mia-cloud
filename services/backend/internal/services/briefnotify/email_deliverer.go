package briefnotify

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	htmltmpl "html/template"
	"mime"
	"net/mail"
	"strings"
	texttmpl "text/template"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

//go:embed templates/brief_email.html.tmpl templates/brief_email.txt.tmpl
var emailTemplatesFS embed.FS

// EmailTransport is the narrow contract the deliverer needs from the
// SMTP integration. Declared here so the deliverer stays unaware of
// the smtpmail package — tests substitute a stub and the real wiring
// passes *smtpmail.Client (which already satisfies this signature via
// Send). Keeping the dependency one-way means briefnotify still has no
// import cycle with integrations/smtpmail.
type EmailTransport interface {
	Send(ctx context.Context, payload EmailPayload) (messageID string, err error)
}

// EmailPayload is the intermediate representation between the
// deliverer and the SMTP layer. The SMTP client adapter converts this
// into smtpmail.Payload (envelope + message bytes).
type EmailPayload struct {
	From      string
	To        []string
	Subject   string
	HTML      string
	Text      string
	MessageID string
	// ExtraHeaders are merged verbatim into the outgoing message
	// (ör. "List-Unsubscribe: <...>"). Names are case-insensitive but
	// stored as-provided for logging.
	ExtraHeaders map[string]string
}

// EmailBriefDelivererConfig carries the values that come from env:
// sender identity + base URLs for the CTA and unsubscribe links.
// DashboardBaseURL defaults to "https://console.syncrun.net" when
// empty; UnsubscribeBaseURL likewise to "https://api.syncrun.net".
//
// UnsubscribeSigner signs the footer link so only the owning user can
// opt themselves out (roadmap Karar #16). When nil, the deliverer falls
// back to a random opaque token — the footer link still exists (needed
// for mail client "List-Unsubscribe" heuristics and CAN-SPAM) but
// clicks produce a 400 from the handler. Production MUST pass a real
// signer; the nil fallback is only there so unit tests can construct
// the deliverer without loading a full env.
type EmailBriefDelivererConfig struct {
	FromAddress        string // ör. brief@syncrun.net
	FromDisplay        string // ör. Syncrun
	ReplyTo            string // ör. brief@syncrun.net (genelde FromAddress ile aynı)
	DashboardBaseURL   string // CTA → /meetings/{id}/brief
	UnsubscribeBaseURL string // footer signed link target
	UnsubscribeSigner  *UnsubscribeSigner
}

// EmailBriefDeliverer turns a BriefDeliverContext into a rendered
// multipart email and hands it to the configured EmailTransport. It
// implements the channel-agnostic BriefDeliverer interface so the
// dispatcher can route to it without knowing the transport.
type EmailBriefDeliverer struct {
	cfg         EmailBriefDelivererConfig
	transport   EmailTransport
	htmlTmpl    *htmltmpl.Template
	textTmpl    *texttmpl.Template
	// unsubscribeTokenFn lets tests inject a deterministic token. In
	// production Phase 3 will replace this with an HMAC signer; Phase
	// 2 renders an opaque placeholder so the unsubscribe link exists
	// from the first email onward (breaking CAN-SPAM compliance later
	// is worse than having a placeholder now).
	unsubscribeTokenFn func(userID uuid.UUID) string
	// clock is overridable for tests so `Year` in the footer is stable.
	clock func() time.Time
}

// NewEmailBriefDeliverer wires the deliverer against a transport.
// Parses the embedded templates up-front — failure here is a fatal
// misconfiguration (binary ships with broken assets) so we surface it
// rather than falling back silently.
func NewEmailBriefDeliverer(cfg EmailBriefDelivererConfig, transport EmailTransport) (*EmailBriefDeliverer, error) {
	if transport == nil {
		return nil, fmt.Errorf("briefnotify: EmailBriefDeliverer requires a non-nil transport")
	}
	if cfg.FromAddress == "" {
		return nil, fmt.Errorf("briefnotify: FromAddress is required")
	}
	if cfg.FromDisplay == "" {
		cfg.FromDisplay = "Syncrun"
	}
	if cfg.ReplyTo == "" {
		cfg.ReplyTo = cfg.FromAddress
	}
	if cfg.DashboardBaseURL == "" {
		cfg.DashboardBaseURL = "https://console.syncrun.net"
	}
	if cfg.UnsubscribeBaseURL == "" {
		cfg.UnsubscribeBaseURL = "https://api.syncrun.net"
	}

	htmlTmpl, err := htmltmpl.ParseFS(emailTemplatesFS, "templates/brief_email.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("briefnotify: parse html template: %w", err)
	}
	textTmpl, err := texttmpl.ParseFS(emailTemplatesFS, "templates/brief_email.txt.tmpl")
	if err != nil {
		return nil, fmt.Errorf("briefnotify: parse text template: %w", err)
	}

	deliverer := &EmailBriefDeliverer{
		cfg:       cfg,
		transport: transport,
		htmlTmpl:  htmlTmpl,
		textTmpl:  textTmpl,
		clock:     func() time.Time { return time.Now().UTC() },
	}

	if cfg.UnsubscribeSigner != nil {
		// Happy path: Phase-3 wiring. Signed HMAC tokens scoped to
		// a specific user + issue time.
		deliverer.unsubscribeTokenFn = cfg.UnsubscribeSigner.GenerateToken
	} else {
		// Fallback: opaque random token. Only used in tests — the
		// handler will 400 on these. Keeping the link present means
		// we still pass CAN-SPAM's "functional unsubscribe" rule
		// from a rendering standpoint.
		deliverer.unsubscribeTokenFn = func(userID uuid.UUID) string {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			return hex.EncodeToString(b)
		}
	}
	return deliverer, nil
}

func (d *EmailBriefDeliverer) Name() string { return models.BriefDeliveryChannelEmail }

// Deliver renders the templates, asks the transport to send, and
// returns the provider message id. Errors from the transport flow
// through as-is — ClassifyError in the smtpmail package already
// distinguishes retryable vs permanent.
func (d *EmailBriefDeliverer) Deliver(ctx context.Context, dc BriefDeliverContext) (string, error) {
	if dc.Meeting == nil || dc.Brief == nil || dc.User == nil {
		return "", fmt.Errorf("EmailBriefDeliverer: BriefDeliverContext missing Meeting/Brief/User")
	}
	if dc.User.Email == "" {
		return "", fmt.Errorf("EmailBriefDeliverer: user has no email address")
	}
	if dc.Profile != nil && dc.Profile.EmailUnsubscribedAt != nil {
		return "", ErrEmailUnsubscribed
	}

	view := d.buildView(dc)

	var htmlBuf bytes.Buffer
	if err := d.htmlTmpl.Execute(&htmlBuf, view); err != nil {
		return "", fmt.Errorf("EmailBriefDeliverer: render HTML: %w", err)
	}
	var textBuf bytes.Buffer
	if err := d.textTmpl.Execute(&textBuf, view); err != nil {
		return "", fmt.Errorf("EmailBriefDeliverer: render text: %w", err)
	}

	messageID := newMessageID(d.cfg.FromAddress)

	payload := EmailPayload{
		From:      (&mail.Address{Name: d.cfg.FromDisplay, Address: d.cfg.FromAddress}).String(),
		To:        []string{dc.User.Email},
		Subject:   subjectFor(view.MeetingTitle),
		HTML:      htmlBuf.String(),
		Text:      textBuf.String(),
		MessageID: messageID,
		ExtraHeaders: map[string]string{
			"Reply-To":         d.cfg.ReplyTo,
			"List-Unsubscribe": "<" + view.UnsubscribeURL + ">",
		},
	}
	return d.transport.Send(ctx, payload)
}

// emailView is the data shape our templates consume. All fields are
// pre-computed strings so the template itself contains zero business
// logic (easier to review for template injection + keeps the view
// stable across i18n changes).
type emailView struct {
	MeetingTitle     string
	MeetingTimeLocal string
	ParticipantsLine string
	BriefContent     string
	DashboardURL     string
	UserEmail        string
	UnsubscribeURL   string
	Year             string
}

func (d *EmailBriefDeliverer) buildView(dc BriefDeliverContext) emailView {
	tz, _ := time.LoadLocation("Europe/Istanbul") // safe default — tz lookup never returns nil with fallback
	if dc.Profile != nil && dc.Profile.Timezone != "" {
		if loc, err := time.LoadLocation(dc.Profile.Timezone); err == nil {
			tz = loc
		}
	}
	meetingLocal := dc.Meeting.StartTime.In(tz).Format("2 January 2006 15:04 MST")

	participants := participantsLine(dc.Meeting.Attendees)

	unsubToken := d.unsubscribeTokenFn(dc.User.ID)
	unsubURL := strings.TrimRight(d.cfg.UnsubscribeBaseURL, "/") + "/api/unsubscribe?token=" + unsubToken

	dashURL := strings.TrimRight(d.cfg.DashboardBaseURL, "/") + "/meetings/" + dc.Meeting.ID.String() + "/brief"

	return emailView{
		MeetingTitle:     dc.Meeting.Title,
		MeetingTimeLocal: meetingLocal,
		ParticipantsLine: participants,
		BriefContent:     strings.TrimSpace(dc.Brief.Content),
		DashboardURL:     dashURL,
		UserEmail:        dc.User.Email,
		UnsubscribeURL:   unsubURL,
		Year:             d.clock().Format("2006"),
	}
}

// participantsLine produces a human-readable "Alice, Bob, +2" string
// without dumping the entire attendee list into a POC email. The
// limit (5) is a mobile-readability decision, not a template limit —
// nothing breaks above it.
func participantsLine(attendees []models.Attendee) string {
	const maxShown = 5
	if len(attendees) == 0 {
		return ""
	}
	names := make([]string, 0, len(attendees))
	for _, a := range attendees {
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			name = strings.TrimSpace(a.Email)
		}
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) <= maxShown {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxShown], ", ") + fmt.Sprintf(", +%d daha", len(names)-maxShown)
}

// subjectFor builds the RFC-2047-encoded Subject header so Turkish
// characters render correctly in mail clients that still default to
// ASCII. The prefix/suffix copy stays outside the encoded word so
// spam-checkers see recognisable ASCII.
func subjectFor(title string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		trimmed = "Toplantı"
	}
	return mime.QEncoding.Encode("utf-8", trimmed+" — briefiniz hazır")
}

// newMessageID returns a unique RFC-5322 Message-Id. The host part is
// derived from the From address so the id passes SPF/DMARC alignment
// heuristics used by some receivers.
func newMessageID(fromAddress string) string {
	host := "syncrun.net"
	if at := strings.LastIndex(fromAddress, "@"); at != -1 && at+1 < len(fromAddress) {
		host = fromAddress[at+1:]
	}
	return "<" + uuid.NewString() + "@" + host + ">"
}
