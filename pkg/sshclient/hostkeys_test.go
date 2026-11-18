package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
)

func newCA(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func scanParams(s *sshtest.Server) DialParams {
	return DialParams{IP: s.Host, Port: s.Port, Dialer: loopbackDialer}
}

func TestScanHostKeysReturnsEveryKeyWithoutAuthenticating(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw", ExtraECDSAHostKey: true})
	keys, err := ScanHostKeys(timeout(t, 10*time.Second), scanParams(server))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, key := range keys {
		got = append(got, ssh.FingerprintSHA256(key))
	}
	want := append([]string(nil), server.Fingerprints...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("scanned %v, want %v", got, want)
	}
	if attempts := server.AuthAttempts(); attempts != 0 {
		t.Fatalf("the scan made %d authentication attempts; it must stop before authentication", attempts)
	}
}

func TestScanHostKeysObeysTheEgressPolicy(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{})
	params := scanParams(server)
	params.Dialer = nil // netpolicy.Default() refuses loopback
	if _, err := ScanHostKeys(timeout(t, 5*time.Second), params); !errors.Is(err, domain.ErrTargetNotAllowed) {
		t.Fatalf("scan of a denied target = %v, want ErrTargetNotAllowed", err)
	}
	if server.Accepted() != 0 {
		t.Fatalf("a denied scan reached the server (%d connections)", server.Accepted())
	}
	if _, err := ScanHostKeys(timeout(t, time.Second), DialParams{IP: "", Port: 22}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("scan without a host = %v", err)
	}
}

func TestVerifyAgainstAFingerprintList(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw", ExtraECDSAHostKey: true})
	params := DialParams{IP: server.Host, Port: server.Port, Username: "deploy", AuthMethod: AuthPassword, SecretData: "pw",
		TrustedFingerprints: server.Fingerprints, Dialer: loopbackDialer}
	if err := TestConnection(timeout(t, 10*time.Second), params); err != nil {
		t.Fatalf("connection with every key trusted: %v", err)
	}
	params.TrustedFingerprints = []string{"SHA256:not-this-host"}
	var unknown *domain.ErrHostKeyUnknown
	if err := TestConnection(timeout(t, 10*time.Second), params); !errors.As(err, &unknown) {
		t.Fatalf("connection with an unrelated fingerprint = %v, want ErrHostKeyUnknown", err)
	}
	if attempts := server.AuthAttempts(); attempts != 1 {
		t.Fatalf("authentication attempts = %d, want only the trusted connection's", attempts)
	}
}

func TestVerifyHostCertificates(t *testing.T) {
	ca := newCA(t)
	server := sshtest.Start(t, sshtest.Options{Password: "pw", HostCertificateAuthority: ca})
	params := DialParams{IP: server.Host, Port: server.Port, Username: "deploy", AuthMethod: AuthPassword, SecretData: "pw",
		HostCertAuthorities: []ssh.PublicKey{ca.PublicKey()}, Dialer: loopbackDialer}
	if err := TestConnection(timeout(t, 10*time.Second), params); err != nil {
		t.Fatalf("connection to a host certified by the trusted CA: %v", err)
	}

	params.HostCertAuthorities = []ssh.PublicKey{newCA(t).PublicKey()}
	var unknown *domain.ErrHostKeyUnknown
	if err := TestConnection(timeout(t, 10*time.Second), params); !errors.As(err, &unknown) {
		t.Fatalf("certificate from another CA = %v, want ErrHostKeyUnknown", err)
	}

	plain := sshtest.Start(t, sshtest.Options{Password: "pw"})
	params.IP, params.Port = plain.Host, plain.Port
	params.HostCertAuthorities = []ssh.PublicKey{ca.PublicKey()}
	if err := TestConnection(timeout(t, 10*time.Second), params); !errors.As(err, &unknown) {
		t.Fatalf("uncertified host with only a CA trusted = %v, want ErrHostKeyUnknown", err)
	}
	params.TrustedFingerprints = []string{plain.Fingerprint}
	if err := TestConnection(timeout(t, 10*time.Second), params); err != nil {
		t.Fatalf("a pinned fingerprint must still work next to a CA: %v", err)
	}
}

func TestParseHostCertificateAuthorities(t *testing.T) {
	ca := newCA(t)
	line := string(ssh.MarshalAuthorizedKey(ca.PublicKey()))
	keys, err := ParseHostCertificateAuthorities([]string{line + " ops-ca"})
	if err != nil || len(keys) != 1 || ssh.FingerprintSHA256(keys[0]) != ssh.FingerprintSHA256(ca.PublicKey()) {
		t.Fatalf("parse = %v, %v", keys, err)
	}
	if _, err := ParseHostCertificateAuthorities([]string{"not a key"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("garbage = %v", err)
	}
	certificate := &ssh.Certificate{Key: ca.PublicKey(), CertType: ssh.HostCert, ValidPrincipals: []string{"host"}, ValidBefore: ssh.CertTimeInfinity}
	if err := certificate.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHostCertificateAuthorities([]string{string(ssh.MarshalAuthorizedKey(certificate))}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("a certificate as CA = %v", err)
	}
}

// dropDials closes the connections whose 1-based dial number is in drop,
// the way a server throttling unauthenticated connections drops a scan.
func dropDials(drop func(n int) bool) func(context.Context, string, string) (net.Conn, error) {
	var count atomic.Int32
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := loopbackDialer(ctx, network, addr)
		if err == nil && drop(int(count.Add(1))) {
			_ = conn.Close()
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		}
		return conn, err
	}
}

func TestScanHostKeysRetriesADroppedConnection(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw", ExtraECDSAHostKey: true})
	params := scanParams(server)
	params.Dialer = dropDials(func(n int) bool { return n == 2 }) // the first ECDSA attempt
	keys, err := ScanHostKeys(timeout(t, 10*time.Second), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != len(server.Fingerprints) {
		t.Fatalf("scanned %d keys, want %d", len(keys), len(server.Fingerprints))
	}
}

func TestScanHostKeysNeverReturnsAPartialSet(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: "pw", ExtraECDSAHostKey: true})
	params := scanParams(server)
	params.Dialer = dropDials(func(n int) bool { return n >= 2 }) // ECDSA always fails
	keys, err := ScanHostKeys(timeout(t, 10*time.Second), params)
	if err == nil {
		t.Fatalf("scan with one key type unreachable returned %d keys and no error", len(keys))
	}
	if keys != nil {
		t.Fatalf("a failed scan returned keys: %d", len(keys))
	}
}
