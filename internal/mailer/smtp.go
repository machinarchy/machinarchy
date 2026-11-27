// Package mailer sends the invitation email over SMTP with the standard library.
package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
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
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("message id: %w", err)
	}
	inviter := cleanText(invite.InvitedBy)
	if inviter == "" {
		inviter = "a workspace owner"
	}
	var b strings.Builder
	header := func(name, value string) { b.WriteString(name + ": " + value + "\r\n") }
	header("From", m.from)
	header("To", to)
	header("Subject", "You are invited to Machinarchy")
	header("Date", m.now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id[:])+"@machinarchy>")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	b.WriteString("\r\n")
	lines := []string{
		inviter + " invited you to Machinarchy at " + invite.ServerURL + " as " + string(invite.Role) + ".",
		"",
		"Choose your username and password with this one-time token. It works once and expires " +
			invite.ExpiresAt.UTC().Format("2006-01-02 15:04 MST") + ".",
		"",
		"  Token: " + invite.Token,
		"",
		"  curl -X POST " + invite.ServerURL + "/api/auth/invitations/accept \\",
		"    -H 'Content-Type: application/json' \\",
		"    -d '{\"token\":\"" + invite.Token + "\",\"username\":\"YOUR-NAME\",\"password\":\"AT-LEAST-12-CHARACTERS\"}'",
		"",
		"Then sign in with: machinarchy login " + invite.ServerURL,
		"",
		"Nobody at Machinarchy will ask you for this token. If you did not expect this invitation, ignore the message.",
	}
	b.WriteString(strings.Join(lines, "\r\n") + "\r\n")
	return []byte(b.String()), nil
}

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
