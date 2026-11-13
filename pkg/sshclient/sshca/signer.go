package sshca

import (
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

func LoadCASigner(
	privateKeyPath string,
	passphrase []byte,
) (ssh.Signer, error) {
	key, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH CA private key: %w", err)
	}

	var signer ssh.Signer

	if len(passphrase) > 0 {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, passphrase)
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}

	if err != nil {
		return nil, fmt.Errorf("parse SSH CA private key: %w", err)
	}

	return signer, nil
}
