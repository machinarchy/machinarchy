// Package sshtest provides an in-process SSH server for integration tests. It
// listens on 127.0.0.1 only, uses a freshly generated ed25519 host key, and can
// misbehave on purpose (never answering, or speaking something other than SSH)
// so tests can exercise the failure paths of real SSH clients.
//
// It is test support code: import it only from _test.go files.
package sshtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Mode selects how the server treats incoming TCP connections.
type Mode int

const (
	// ModeNormal performs a real SSH handshake.
	ModeNormal Mode = iota
	// ModeSilent accepts the TCP connection and then never sends or answers
	// anything, like a firewalled or hung host.
	ModeSilent
	// ModeGarbage answers with an HTTP response instead of an SSH banner.
	ModeGarbage
)

// Options configures a server. With neither Password nor AuthorizedKeys set the
// server accepts no credentials, so every authentication attempt fails.
type Options struct {
	Mode           Mode
	Username       string // defaults to "deploy"
	Password       string // enables password authentication when non-empty
	AuthorizedKeys []ssh.PublicKey
	// Exec runs an "exec" request. It writes the command's output to out and
	// returns the exit status. ctx is cancelled when the client disconnects or
	// the server closes. A nil Exec succeeds without output.
	Exec func(ctx context.Context, command string, out io.Writer) int
}

// Server is a running test server.
type Server struct {
	// Host is always 127.0.0.1.
	Host string
	Port int
	// HostKeyType is the host key algorithm, e.g. "ssh-ed25519".
	HostKeyType string
	// Fingerprint is the SHA-256 host key fingerprint in the format clients pin.
	Fingerprint string

	opts     Options
	listener net.Listener
	cfg      *ssh.ServerConfig
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}

	accepted     atomic.Int32
	open         atomic.Int32
	authAttempts atomic.Int32
	execs        atomic.Int32
}

// Start launches a server and registers its shutdown with t.Cleanup.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	if opts.Username == "" {
		opts.Username = "deploy"
	}
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatalf("create host key signer: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		Host:        "127.0.0.1",
		Port:        listener.Addr().(*net.TCPAddr).Port,
		HostKeyType: hostSigner.PublicKey().Type(),
		Fingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey()),
		opts:        opts,
		listener:    listener,
		ctx:         ctx,
		cancel:      cancel,
		conns:       make(map[net.Conn]struct{}),
	}

	s.cfg = &ssh.ServerConfig{}
	s.cfg.AddHostKey(hostSigner)
	if opts.Password != "" {
		s.cfg.PasswordCallback = func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			s.authAttempts.Add(1)
			if meta.User() == opts.Username && subtle.ConstantTimeCompare(password, []byte(opts.Password)) == 1 {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("sshtest: password rejected")
		}
	}
	if len(opts.AuthorizedKeys) > 0 {
		s.cfg.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.authAttempts.Add(1)
			for _, authorized := range opts.AuthorizedKeys {
				if meta.User() == opts.Username && subtle.ConstantTimeCompare(key.Marshal(), authorized.Marshal()) == 1 {
					return &ssh.Permissions{}, nil
				}
			}
			return nil, errors.New("sshtest: key rejected")
		}
	}

	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// Addr returns host:port.
func (s *Server) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

// Accepted is the number of TCP connections accepted so far.
func (s *Server) Accepted() int { return int(s.accepted.Load()) }

// OpenConnections is the number of connections currently open on the server.
func (s *Server) OpenConnections() int { return int(s.open.Load()) }

// AuthAttempts counts credential checks. It stays zero when the client refuses
// to authenticate, for example because the host key did not match.
func (s *Server) AuthAttempts() int { return int(s.authAttempts.Load()) }

// Execs counts "exec" requests received.
func (s *Server) Execs() int { return int(s.execs.Load()) }

// Close stops the server and drops every connection. It is idempotent.
func (s *Server) Close() {
	s.cancel()
	_ = s.listener.Close()
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) track(conn net.Conn, add bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if add {
		s.conns[conn] = struct{}{}
	} else {
		delete(s.conns, conn)
	}
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.accepted.Add(1)
		s.track(conn, true)
		s.wg.Add(1)
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	s.open.Add(1)
	defer s.wg.Done()
	defer s.open.Add(-1)
	defer s.track(conn, false)
	defer conn.Close()

	switch s.opts.Mode {
	case ModeSilent:
		_, _ = io.Copy(io.Discard, conn) // returns when the client or Close drops the connection
		return
	case ModeGarbage:
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"))
		return
	}

	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.cfg)
	if err != nil {
		return
	}
	connCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		_ = serverConn.Wait()
		cancel()
	}()
	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.session(connCtx, channel, channelRequests)
	}
}

func (s *Server) session(ctx context.Context, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		s.execs.Add(1)
		status := 0
		if s.opts.Exec != nil {
			status = s.opts.Exec(ctx, payload.Command, channel)
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
		return
	}
}

// NewClientKey generates an ed25519 key pair for key-authentication tests. It
// returns the PEM-encoded private key (encrypted when passphrase is non-empty)
// and the public key to place in Options.AuthorizedKeys.
func NewClientKey(t testing.TB, passphrase string) (privatePEM string, public ssh.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(privateKey, "sshtest")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privateKey, "sshtest", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("encode client key: %v", err)
	}
	public, err = ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatalf("derive client public key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), public
}
