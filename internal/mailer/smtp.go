// Package mailer sends the invitation email (plain text and HTML, both base64
// encoded) over SMTP with the standard library.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	ttemplate "text/template"
	"time"
	"unicode"

	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/domain"
)

const sendTimeout = 30 * time.Second

// SMTP delivers invitation emails. TLS is required unless the configuration
// explicitly allows an unencrypted loopback server.
type SMTP struct {
	cfg  config.MailConfig
	from string
	now  func() time.Time
}

var _ domain.Mailer = (*SMTP)(nil)

// NewSMTP validates cfg and returns a mailer. It fails for a configuration
// without a host.
func NewSMTP(cfg config.MailConfig) (*SMTP, error) {
	if !cfg.Enabled() {
		return nil, domain.ErrMailNotConfigured
	}
	from, err := cfg.ParseFrom()
	if err != nil {
		return nil, fmt.Errorf("mail sender: %w", err)
	}
	return &SMTP{cfg: cfg, from: from, now: time.Now}, nil
}

// SendInvitation emails the invitation. The recipient must be a single plain
// address; anything else is refused before a connection is made.
func (m *SMTP) SendInvitation(ctx context.Context, to string, invite domain.InvitationMail) error {
	recipient, err := plainAddress(to)
	if err != nil {
		return fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	body, err := m.message(recipient, invite)
	if err != nil {
		return err
	}
	return m.deliver(ctx, recipient, body)
}

func plainAddress(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n<>\"") {
		return "", errors.New("email address must be a plain address")
	}
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Name != "" {
		return "", errors.New("email address is not valid")
	}
	return addr.Address, nil
}

func (m *SMTP) message(to string, invite domain.InvitationMail) ([]byte, error) {
	var id, boundary [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("message id: %w", err)
	}
	if _, err := rand.Read(boundary[:]); err != nil {
		return nil, fmt.Errorf("message boundary: %w", err)
	}
	inviter := cleanText(invite.InvitedBy)
	if inviter == "" {
		inviter = "a workspace owner"
	}
	view := mailView{
		Inviter: inviter, Role: string(invite.Role), ServerURL: invite.ServerURL, Token: invite.Token,
		Command: "machinarchy login " + invite.ServerURL + " --invite",
		Expires: invite.ExpiresAt.UTC().Format("2006-01-02 15:04 MST"),
	}
	var text, page bytes.Buffer
	if err := textTemplate.Execute(&text, view); err != nil {
		return nil, fmt.Errorf("render text: %w", err)
	}
	if err := htmlTemplate.Execute(&page, view); err != nil {
		return nil, fmt.Errorf("render html: %w", err)
	}
	sep := hex.EncodeToString(boundary[:])

	var b strings.Builder
	header := func(name, value string) { b.WriteString(name + ": " + value + "\r\n") }
	header("From", m.from)
	header("To", to)
	header("Subject", "You are invited to Machinarchy")
	header("Date", m.now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id[:])+"@machinarchy>")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+sep+`"`)
	b.WriteString("\r\n")
	part := func(contentType string, body []byte) {
		b.WriteString("--" + sep + "\r\nContent-Type: " + contentType + "; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		encoded := base64.StdEncoding.EncodeToString(body)
		for len(encoded) > 76 {
			b.WriteString(encoded[:76] + "\r\n")
			encoded = encoded[76:]
		}
		b.WriteString(encoded + "\r\n")
	}
	part("text/plain", text.Bytes())
	part("text/html", page.Bytes())
	b.WriteString("--" + sep + "--\r\n")
	return []byte(b.String()), nil
}

type mailView struct {
	Inviter, Role, ServerURL, Token, Command, Expires string
}

var textTemplate = ttemplate.Must(ttemplate.New("text").Parse(`{{.Inviter}} invited you to Machinarchy as {{.Role}}.

Token (one-time, valid until {{.Expires}}):

  {{.Token}}

Then run:

  {{.Command}}

It asks for the token, then for the username and password you want.
Nobody at Machinarchy will ask you for this token. If you did not expect this invitation, ignore the message.
`))

func cleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

func (m *SMTP) deliver(ctx context.Context, to string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	address := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	tlsConfig := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("%w: connect: %w", domain.ErrMailFailed, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return fmt.Errorf("%w: set deadline: %w", domain.ErrMailFailed, err)
		}
	}
	if m.cfg.TLS == "tls" {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%w: greeting: %w", domain.ErrMailFailed, err)
	}
	defer func() { _ = client.Close() }()

	if m.cfg.TLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%w: the server does not offer STARTTLS", domain.ErrMailFailed)
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("%w: STARTTLS: %w", domain.ErrMailFailed, err)
		}
	}
	if m.cfg.Username != "" {
		// PlainAuth refuses to send the password over an unencrypted connection
		// to anything but localhost.
		if err := client.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("%w: authentication: %w", domain.ErrMailFailed, err)
		}
	}
	if err := client.Mail(m.from); err != nil {
		return fmt.Errorf("%w: sender: %w", domain.ErrMailFailed, err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("%w: recipient: %w", domain.ErrMailFailed, err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: data: %w", domain.ErrMailFailed, err)
	}
	if _, err := writer.Write(body); err != nil {
		return fmt.Errorf("%w: write: %w", domain.ErrMailFailed, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("%w: send: %w", domain.ErrMailFailed, err)
	}
	if err := client.Quit(); err != nil {
		// The message was accepted at DATA; a failed goodbye does not undo it.
		slog.Debug("smtp quit", "error", err)
	}
	return nil
}
