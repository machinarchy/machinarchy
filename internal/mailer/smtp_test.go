package mailer

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/config"
	"github.com/machinarchy/machinarchy/internal/domain"
)

// fakeSMTP is a minimal plaintext server on loopback. It records the envelope
// and the message, and advertises STARTTLS only when asked.
type fakeSMTP struct {
	ln       net.Listener
	starttls bool
	from, to string
	data     string
	done     chan struct{}
}

func newFakeSMTP(t *testing.T, starttls bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, starttls: starttls, done: make(chan struct{})}
	t.Cleanup(func() { _ = ln.Close() })
	go f.serve()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve() {
	defer close(f.done)
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "EHLO"):
			if f.starttls {
				say("250-fake\r\n250 STARTTLS")
			} else {
				say("250 fake")
			}
		case strings.HasPrefix(line, "MAIL FROM:"):
			f.from = line
			say("250 ok")
		case strings.HasPrefix(line, "RCPT TO:"):
			f.to = line
			say("250 ok")
		case line == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.data = b.String()
			say("250 queued")
		case line == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func testMailer(t *testing.T, tlsMode string, port int) *SMTP {
	t.Helper()
	m, err := NewSMTP(config.MailConfig{Host: "127.0.0.1", Port: port, From: "noreply@example.test", TLS: tlsMode, PublicURL: "https://m.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sampleMail() domain.InvitationMail {
	return domain.InvitationMail{ServerURL: "https://m.example.test", Token: "tok_ABC-123", Role: domain.RoleOperator,
		InvitedBy: "ali\r\nBcc: evil@example.test", ExpiresAt: time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)}
}

func TestSendInvitationDeliversTheMessage(t *testing.T) {
	srv := newFakeSMTP(t, false)
	m := testMailer(t, "none", srv.port())
	if err := m.SendInvitation(context.Background(), "new@example.test", sampleMail()); err != nil {
		t.Fatal(err)
	}
	<-srv.done
	if !strings.Contains(srv.from, "<noreply@example.test>") || !strings.Contains(srv.to, "<new@example.test>") {
		t.Errorf("envelope from=%q to=%q", srv.from, srv.to)
	}
	for _, want := range []string{"To: new@example.test", "Subject: You are invited to Machinarchy", "tok_ABC-123",
		"as operator", "https://m.example.test/api/auth/invitations/accept", "2026-12-04 10:00 UTC"} {
		if !strings.Contains(srv.data, want) {
			t.Errorf("message lacks %q:\n%s", want, srv.data)
		}
	}
	// Control characters in the inviter's name cannot add a header or line.
	if strings.Contains(srv.data, "\r\nBcc:") {
		t.Errorf("header injection through the inviter name:\n%s", srv.data)
	}
}

func TestSendInvitationRefusesUnsafeRecipientsBeforeConnecting(t *testing.T) {
	m := testMailer(t, "none", 1) // nothing listens on port 1: a connection attempt would fail differently
	for _, to := range []string{"a@example.test\r\nBcc: x@example.test", "Bob <b@example.test>", "<b@example.test>", "not-an-address", ""} {
		if err := m.SendInvitation(context.Background(), to, sampleMail()); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%q: error = %v, want ErrInvalidInput", to, err)
		}
	}
}

func TestStartTLSIsRequired(t *testing.T) {
	srv := newFakeSMTP(t, false) // does not offer STARTTLS
	m := testMailer(t, "starttls", srv.port())
	err := m.SendInvitation(context.Background(), "new@example.test", sampleMail())
	if !errors.Is(err, domain.ErrMailFailed) || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error = %v, want ErrMailFailed naming STARTTLS", err)
	}
	if srv.data != "" {
		t.Error("a message was sent over an unencrypted connection although STARTTLS is required")
	}
}

func TestSendInvitationFailsCleanlyWhenTheServerIsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	err = testMailer(t, "none", port).SendInvitation(context.Background(), "new@example.test", sampleMail())
	if !errors.Is(err, domain.ErrMailFailed) {
		t.Fatalf("error = %v, want ErrMailFailed", err)
	}
	if strings.Contains(err.Error(), "tok_ABC-123") {
		t.Error("the token must not appear in errors")
	}
	_ = strconv.Itoa(port)
}

func TestNewSMTPNeedsAHostAndASender(t *testing.T) {
	if _, err := NewSMTP(config.MailConfig{}); !errors.Is(err, domain.ErrMailNotConfigured) {
		t.Errorf("no host: %v", err)
	}
	if _, err := NewSMTP(config.MailConfig{Host: "h", From: "Name <a@b>"}); err == nil {
		t.Error("a display-name sender must be refused")
	}
}
