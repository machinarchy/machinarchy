package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

var ErrInvalidKey = errors.New("cryptographic key must be at least 32 bytes")

// DeriveKey creates a purpose-specific AES-256 key from a high-entropy master
// secret. The purpose string provides domain separation between consumers.
func DeriveKey(secret, purpose string) ([]byte, error) {
	if len([]byte(secret)) < 32 {
		return nil, ErrInvalidKey
	}
	if purpose == "" {
		return nil, errors.New("key derivation purpose is required")
	}

	reader := hkdf.New(sha256.New, []byte(secret), []byte("machinarchy/key-derivation/v1"), []byte(purpose))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// Encrypt string plaintext using an absolute 32-byte secret key (AES-256-GCM)
func Encrypt(plaintext string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("AES-256-GCM key must be exactly 32 bytes")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt base64 cipher payload back to plaintext
func Decrypt(cryptoText string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("AES-256-GCM key must be exactly 32 bytes")
	}

	data, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("malformed encrypted payload data block size")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
