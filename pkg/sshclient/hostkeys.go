package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

// hostKeyCallback accepts a host key whose SHA-256 fingerprint is trusted
// or, when certificate authorities are configured, a host certificate
// signed by one of them for the dialed host name.
func hostKeyCallback(params DialParams) ssh.HostKeyCallback {
	trusted := map[string]bool{}
	for _, fingerprint := range append([]string{params.ExpectedFingerprint}, params.TrustedFingerprints...) {
		if fingerprint = strings.TrimSpace(fingerprint); fingerprint != "" {
			trusted[fingerprint] = true
		}
	}
	byFingerprint := func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint := ssh.FingerprintSHA256(key)
		if trusted[fingerprint] {
			return nil
		}
		// A server with a host certificate may negotiate the certificate
		// form of its key. A pin is a pin of the key, which the server has
		// just proved it holds, so the key inside the certificate counts
		// (the certificate itself is judged only against a CA).
		if cert, ok := key.(*ssh.Certificate); ok && trusted[ssh.FingerprintSHA256(cert.Key)] {
			return nil
		}
		return &domain.ErrHostKeyUnknown{Host: hostname, Algorithm: key.Type(), Fingerprint: fingerprint}
	}
	if len(params.HostCertAuthorities) == 0 {
		return byFingerprint
	}
	authorities := params.HostCertAuthorities
	checker := &ssh.CertChecker{
		IsHostAuthority: func(authority ssh.PublicKey, _ string) bool {
			for _, candidate := range authorities {
				if bytes.Equal(candidate.Marshal(), authority.Marshal()) {
					return true
				}
			}
			return false
		},
		HostKeyFallback: byFingerprint,
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := checker.CheckHostKey(hostname, remote, key)
		var unknown *domain.ErrHostKeyUnknown
		if err == nil {
			return nil
		}
		// A certificate from another authority, for another principal, or
		// expired is not accepted as one, but a pinned key still is.
		if pinned := byFingerprint(hostname, remote, key); pinned == nil {
			return nil
		}
		if errors.As(err, &unknown) {
			return err
		}
		return &domain.ErrHostKeyUnknown{Host: hostname, Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)}
	}
}

// hostKeyAlgorithmGroups are scanned one at a time, so a server offering
// several key types reveals each of them.
var hostKeyAlgorithmGroups = [][]string{
	{ssh.KeyAlgoED25519},
	{ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521},
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
}

var errHostKeyCaptured = errors.New("sshclient: host key captured")

// ScanHostKeys returns the host's public keys, one per supported algorithm
// family. It stops every handshake right after the server proves possession
// of its host key, before authentication, so no credential is ever sent.
// Only params.IP, Port and Dialer are used; the egress policy applies as
// for any connection.
func ScanHostKeys(ctx context.Context, params DialParams) ([]ssh.PublicKey, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", domain.ErrInvalidInput)
	}
	if strings.TrimSpace(params.IP) == "" || params.Port < 1 || params.Port > 65535 {
		return nil, fmt.Errorf("%w: SSH host and port are required", domain.ErrInvalidInput)
	}
	var keys []ssh.PublicKey
	seen := map[string]bool{}
	offered := false
	for _, algorithms := range hostKeyAlgorithmGroups {
		key, err := scanGroup(ctx, params, algorithms)
		if isNoCommonAlgorithm(err) {
			continue // the server has no key of this type
		}
		if err != nil {
			// A partial result would pin or approve an incomplete set, and
			// a later connection that negotiates the missing type would
			// then fail verification. Fail the whole scan instead.
			return nil, err
		}
		offered = true
		if fingerprint := ssh.FingerprintSHA256(key); !seen[fingerprint] {
			seen[fingerprint] = true
			keys = append(keys, key)
		}
	}
	if !offered {
		return nil, errors.New("the server offered no supported host key algorithm")
	}
	return keys, nil
}

// scanRetryDelays space the retries of one algorithm group: a server under
// load (OpenSSH MaxStartups) may drop one of several back-to-back scans.
// There is only one retry, because every scan connection ends before
// authentication and OpenSSH 9.8+ PerSourcePenalties charges each one to the
// controller's address ("noauth"); retrying hard would get it blocked.
var scanRetryDelays = []time.Duration{500 * time.Millisecond}

func scanGroup(ctx context.Context, params DialParams, algorithms []string) (ssh.PublicKey, error) {
	for attempt := 0; ; attempt++ {
		key, err := scanOne(ctx, params, algorithms)
		if err == nil || isNoCommonAlgorithm(err) {
			return key, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// A policy denial or a refused connection does not change on retry.
		if errors.Is(err, domain.ErrTargetNotAllowed) || errors.Is(err, domain.ErrConnectionRefused) || attempt >= len(scanRetryDelays) {
			return nil, fmt.Errorf("scan %s host key: %w", algorithms[0], err)
		}
		timer := time.NewTimer(scanRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func scanOne(ctx context.Context, params DialParams, algorithms []string) (ssh.PublicKey, error) {
	addr := net.JoinHostPort(strings.TrimSpace(params.IP), strconv.Itoa(params.Port))
	dialContext := params.Dialer
	if dialContext == nil {
		dialContext = netpolicy.Default().DialContext
	}
	netConn, err := dialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, classifyNetworkError(ctx, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = netConn.Close() })
	defer func() {
		stop()
		_ = netConn.Close()
	}()
	deadline := time.Now().Add(handshakeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := netConn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set SSH scan deadline: %w", err)
	}
	var captured ssh.PublicKey
	config := &ssh.ClientConfig{
		User:              "machinarchy-host-key-scan",
		HostKeyAlgorithms: algorithms,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return errHostKeyCaptured
		},
		Timeout: handshakeTimeout,
	}
	conn, _, _, err := ssh.NewClientConn(netConn, addr, config)
	if conn != nil {
		_ = conn.Close()
	}
	if captured != nil {
		return captured, nil
	}
	if err == nil {
		return nil, errors.New("handshake completed without a host key")
	}
	return nil, err
}

func isNoCommonAlgorithm(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no common algorithm")
}

// ParseHostCertificateAuthorities parses CA public keys in authorized_keys
// format ("ssh-ed25519 AAAA… comment").
func ParseHostCertificateAuthorities(lines []string) ([]ssh.PublicKey, error) {
	keys := make([]ssh.PublicKey, 0, len(lines))
	for _, line := range lines {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
		if err != nil {
			return nil, fmt.Errorf("%w: host certificate authority is not an SSH public key", domain.ErrInvalidInput)
		}
		if _, isCert := key.(*ssh.Certificate); isCert {
			return nil, fmt.Errorf("%w: a host certificate authority must be a public key, not a certificate", domain.ErrInvalidInput)
		}
		keys = append(keys, key)
	}
	return keys, nil
}
