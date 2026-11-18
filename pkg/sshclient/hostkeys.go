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
		if !trusted[fingerprint] {
			return &domain.ErrHostKeyUnknown{Host: hostname, Algorithm: key.Type(), Fingerprint: fingerprint}
		}
		return nil
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
		if err == nil || errors.As(err, &unknown) {
			return err
		}
		// A certificate from another authority, for another principal, or
		// expired: report it as an untrusted host key.
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
	var firstErr error
	for _, algorithms := range hostKeyAlgorithmGroups {
		key, err := scanOne(ctx, params, algorithms)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if firstErr == nil && !isNoCommonAlgorithm(err) {
				firstErr = err
			}
			// A network or policy failure is the same for every group.
			if errors.Is(err, domain.ErrTargetNotAllowed) || errors.Is(err, domain.ErrConnectionRefused) {
				return nil, err
			}
			continue
		}
		if fingerprint := ssh.FingerprintSHA256(key); !seen[fingerprint] {
			seen[fingerprint] = true
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		if firstErr == nil {
			firstErr = errors.New("the server offered no supported host key algorithm")
		}
		return nil, firstErr
	}
	return keys, nil
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
	deadline := time.Now().Add(defaultHandshakeTimeout)
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
		Timeout: defaultHandshakeTimeout,
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
