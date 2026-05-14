// Package email sends transactional mail (verify, password reset, welcome).
//
// If SMTP_HOST is empty, Send becomes a no-op that logs the link to stdout —
// useful for local dev without a real SMTP server.
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"fmt"
	"html/template"
	"log"
	"strings"
	"time"

	"github.com/wneessen/go-mail"
)

//go:embed templates/*.html
var tplFS embed.FS

var tpls = template.Must(template.ParseFS(tplFS, "templates/*.html"))

type Config struct {
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	TLSMode  string // "starttls" | "tls" | "none"
	AppName  string // used in templates (default "СвязьОК")
}

type Sender struct {
	cfg Config
}

func New(cfg Config) *Sender {
	if cfg.AppName == "" {
		cfg.AppName = "СвязьОК"
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLSMode == "" {
		cfg.TLSMode = "starttls"
	}
	return &Sender{cfg: cfg}
}

func (s *Sender) Configured() bool {
	return s.cfg.Host != ""
}

type TemplateData struct {
	UserEmail string
	Link      string
	AppName   string
}

// Send dispatches a transactional email. If SMTP is not configured, logs
// the link and returns nil (treated as success in dev).
func (s *Sender) Send(ctx context.Context, to, templateName, subject string, data TemplateData) error {
	data.AppName = s.cfg.AppName

	if !s.Configured() {
		log.Printf("[email skipped: SMTP_HOST empty] to=%s template=%s link=%s", to, templateName, data.Link)
		return nil
	}

	htmlBody, err := render(templateName+".html", data)
	if err != nil {
		return fmt.Errorf("render html: %w", err)
	}
	textBody := stripTags(htmlBody)

	msg := mail.NewMsg()
	if err := msg.From(s.cfg.From); err != nil {
		return fmt.Errorf("set from: %w", err)
	}
	if err := msg.To(to); err != nil {
		return fmt.Errorf("set to: %w", err)
	}
	// Use the From-domain in Message-ID instead of go-mail's default
	// container hostname — `<...@ce169d0f2bd5>` looks fishy to Gmail/Yandex
	// and tanks reputation. We re-derive after From() since msg.GetMessageID
	// hasn't generated one yet here, but go-mail will use this hostname.
	if dom := domainOf(s.cfg.From); dom != "" {
		msg.SetMessageIDWithValue(makeMessageID(dom))
	}
	// Headers that mail filters look for on transactional mail to relax
	// scoring (List-Unsubscribe per RFC 8058, Auto-Submitted per RFC 3834,
	// Reply-To routing replies to a real human inbox).
	dom := domainOf(s.cfg.From)
	msg.SetGenHeader(mail.HeaderListUnsubscribe,
		"<mailto:postmaster@"+dom+"?subject=unsubscribe>, <https://"+dom+"/unsubscribe>")
	msg.SetGenHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	msg.SetGenHeader("Auto-Submitted", "auto-generated")
	msg.SetGenHeader("Precedence", "transactional")
	msg.SetGenHeader("X-Auto-Response-Suppress", "All")
	msg.SetGenHeader("Content-Language", "ru")
	_ = msg.ReplyTo("postmaster@" + dom)
	msg.Subject(subject)
	msg.SetBodyString(mail.TypeTextPlain, textBody)
	msg.AddAlternativeString(mail.TypeTextHTML, htmlBody)

	opts := []mail.Option{
		mail.WithPort(s.cfg.Port),
		mail.WithUsername(s.cfg.User),
		mail.WithPassword(s.cfg.Pass),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
	}
	switch strings.ToLower(s.cfg.TLSMode) {
	case "tls":
		// Implicit TLS (port 465) — wrap socket in TLS from the start.
		opts = append(opts, mail.WithSSL())
	case "starttls":
		// STARTTLS (port 587) — upgrade plain socket to TLS via SMTP command.
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	case "none":
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	}

	client, err := mail.NewClient(s.cfg.Host, opts...)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

func render(name string, data TemplateData) (string, error) {
	var buf bytes.Buffer
	if err := tpls.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// domainOf extracts the host portion of "Name <user@host>" or plain
// "user@host" — used for Message-ID and List-Unsubscribe URL synthesis.
func domainOf(from string) string {
	addr := from
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			addr = from[i+1 : j]
		}
	}
	if at := strings.LastIndex(addr, "@"); at >= 0 && at+1 < len(addr) {
		return addr[at+1:]
	}
	return ""
}

// makeMessageID builds an RFC-5322 Message-ID anchored on our own domain
// (Gmail/Yandex flag IDs ending in a docker container hex hostname).
// makeMessageID returns the bare local-part@domain (no angle brackets).
// go-mail's SetMessageIDWithValue wraps it in <...> itself — adding our own
// pair produces `<<...>>` which trips SpamAssassin's INVALID_MSGID rule.
func makeMessageID(domain string) string {
	var rb [12]byte
	_, _ = rand.Read(rb[:])
	return fmt.Sprintf("%d.%x@%s", time.Now().UnixNano(), rb, domain)
}

// stripTags is a poor-man's HTML→text fallback for the plain-text alt.
func stripTags(htmlStr string) string {
	out := make([]byte, 0, len(htmlStr))
	in := false
	for i := 0; i < len(htmlStr); i++ {
		c := htmlStr[i]
		switch {
		case c == '<':
			in = true
		case c == '>':
			in = false
		case !in:
			out = append(out, c)
		}
	}
	return strings.TrimSpace(string(out))
}
