package sshca

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type HostCertificateOptions struct {
	Serial     uint64
	KeyID      string
	Principals []string
	Validity   time.Duration
	Backdate   time.Duration
}

// SignHostCertificate creates an OpenSSH host certificate.
//
// This is the Go equivalent of:
//
//	ssh-keygen -s host_ca -I <keyID> -h -n <principals> \
//	  -V -5m:+30d host_key.pub
func SignHostCertificate(
	hostPublicKey []byte,
	caSigner ssh.Signer,
	opts HostCertificateOptions,
) ([]byte, error) {
	if caSigner == nil {
		return nil, errors.New("SSH CA signer is required")
	}

	if strings.TrimSpace(opts.KeyID) == "" {
		return nil, errors.New("certificate key ID is required")
	}

	if len(opts.Principals) == 0 {
		return nil, errors.New("at least one host principal is required")
	}

	for _, principal := range opts.Principals {
		if strings.TrimSpace(principal) == "" {
			return nil, errors.New("host principal cannot be empty")
		}
	}

	if opts.Validity == 0 {
		opts.Validity = 30 * 24 * time.Hour
	}

	if opts.Backdate == 0 {
		opts.Backdate = 5 * time.Minute
	}

	if opts.Validity < 0 || opts.Backdate < 0 {
		return nil, errors.New("certificate validity and backdate cannot be negative")
	}

	// Parse the server's existing OpenSSH host public key.
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse host public key: %w", err)
	}

	if _, ok := hostKey.(*ssh.Certificate); ok {
		return nil, errors.New("expected a raw host public key, received a certificate")
	}

	now := time.Now().UTC()

	validAfter := now.Add(-opts.Backdate).Unix()
	validBefore := now.Add(opts.Validity).Unix()

	if validAfter < 0 || validBefore <= validAfter {
		return nil, errors.New("invalid certificate validity interval")
	}

	// Construct the OpenSSH host certificate.
	certificate := &ssh.Certificate{
		Key:             hostKey,
		Serial:          opts.Serial,
		CertType:        ssh.HostCert,
		KeyId:           opts.KeyID,
		ValidPrincipals: opts.Principals,
		ValidAfter:      uint64(validAfter),
		ValidBefore:     uint64(validBefore),
	}

	// Sign with the trusted Host CA.
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		return nil, fmt.Errorf("sign SSH host certificate: %w", err)
	}

	// Return an OpenSSH-compatible authorized-key representation.
	return ssh.MarshalAuthorizedKey(certificate), nil
}
