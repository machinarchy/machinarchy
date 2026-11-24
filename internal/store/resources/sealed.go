package resources

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/pkg/crypto"
)

// Operation data at rest (AUDIT S-07, Phase 16). Specs, per-host output and
// live progress are sealed like Secret Store values: a fresh data key per
// value, wrapped by the key provider under a named key, and AAD that binds
// the ciphertext to its column, workspace, Operation and host, so a value
// cannot be moved to another row.
//
// Stored form (field order fixed by the struct, which the rewrap's prefix
// match relies on):
//
//	{"v":2,"k":"local:bootstrap-v1","w":"<wrapped data key>","c":"<value>"}
//
// Values written before Phase 16 are base64 AES-GCM under one key derived
// from the encryption secret, with no key ID or AAD. They stay readable with
// the legacy keys and are resealed by the rewrap.

const sealedPrefix = `{"v":2,"k":`

type sealedValue struct {
	Version int    `json:"v"`
	KeyID   string `json:"k"`
	Wrapped []byte `json:"w"`
	Value   []byte `json:"c"`
}

// DataCipher seals Operation data.
type DataCipher struct {
	keys   secrets.KeyProvider
	legacy [][]byte
	random io.Reader
}

// NewDataCipher uses keys for new values and legacy keys to read values
// written before Phase 16.
func NewDataCipher(keys secrets.KeyProvider, legacy [][]byte) (*DataCipher, error) {
	if keys == nil {
		return nil, errors.New("operation data cipher requires a key provider")
	}
	for _, key := range legacy {
		if len(key) != 32 {
			return nil, errors.New("legacy operation data keys must be 32 bytes")
		}
	}
	return &DataCipher{keys: keys, legacy: legacy, random: rand.Reader}, nil
}

// Column names bound into the AAD.
const (
	columnSpec   = "spec"
	columnOutput = "output"
	columnLive   = "live"
)

// dataAAD binds a value to where it is stored.
func dataAAD(column string, workspaceID int, operationID, hostID string) string {
	return fmt.Sprintf("machinarchy/operation-data/v2/%s/%d/%s/%s", column, workspaceID, operationID, hostID)
}

// Seal encrypts plain for the given place.
func (c *DataCipher) Seal(ctx context.Context, plain []byte, aad string) (string, error) {
	keyID, err := c.keys.CurrentKeyID(ctx)
	if err != nil {
		return "", fmt.Errorf("operation data key: %w", err)
	}
	dek := make([]byte, 32)
	if _, err := io.ReadFull(c.random, dek); err != nil {
		return "", err
	}
	defer wipe(dek)
	value, err := crypto.EncryptBytes(plain, dek, []byte("value:"+aad))
	if err != nil {
		return "", err
	}
	wrapped, err := c.keys.Wrap(ctx, keyID, dek, []byte("dek:"+aad+"/"+keyID))
	if err != nil {
		return "", fmt.Errorf("wrap operation data key: %w", err)
	}
	encoded, err := json.Marshal(sealedValue{Version: 2, KeyID: keyID, Wrapped: wrapped, Value: value})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Open decrypts a value sealed for the given place, or a legacy value.
func (c *DataCipher) Open(ctx context.Context, text, aad string) ([]byte, error) {
	if !strings.HasPrefix(text, sealedPrefix) {
		return c.openLegacy(text)
	}
	var sealed sealedValue
	if err := json.Unmarshal([]byte(text), &sealed); err != nil || sealed.Version != 2 || sealed.KeyID == "" {
		return nil, errors.New("operation data is malformed")
	}
	dek, err := c.keys.Unwrap(ctx, sealed.KeyID, sealed.Wrapped, []byte("dek:"+aad+"/"+sealed.KeyID))
	if err != nil {
		return nil, fmt.Errorf("unwrap operation data key %s: %w", sealed.KeyID, err)
	}
	defer wipe(dek)
	plain, err := crypto.DecryptBytes(sealed.Value, dek, []byte("value:"+aad))
	if err != nil {
		return nil, errors.New("operation data does not open for this row")
	}
	return plain, nil
}

func (c *DataCipher) openLegacy(text string) ([]byte, error) {
	for _, key := range c.legacy {
		if plain, err := crypto.Decrypt(text, key); err == nil {
			return []byte(plain), nil
		}
	}
	return nil, errors.New("legacy operation data does not open with any configured secret")
}

// Rewrap returns text with its data key wrapped under the current key, the
// value unchanged; a legacy value is resealed. It reports false when text is
// already under the current key.
func (c *DataCipher) Rewrap(ctx context.Context, text, aad string) (string, bool, error) {
	current, err := c.keys.CurrentKeyID(ctx)
	if err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(text, sealedPrefix) {
		plain, err := c.openLegacy(text)
		if err != nil {
			return "", false, err
		}
		sealed, err := c.Seal(ctx, plain, aad)
		wipe(plain)
		return sealed, err == nil, err
	}
	var sealed sealedValue
	if err := json.Unmarshal([]byte(text), &sealed); err != nil || sealed.Version != 2 {
		return "", false, errors.New("operation data is malformed")
	}
	if sealed.KeyID == current {
		return text, false, nil
	}
	dek, err := c.keys.Unwrap(ctx, sealed.KeyID, sealed.Wrapped, []byte("dek:"+aad+"/"+sealed.KeyID))
	if err != nil {
		return "", false, fmt.Errorf("unwrap operation data key %s: %w", sealed.KeyID, err)
	}
	defer wipe(dek)
	if sealed.Wrapped, err = c.keys.Wrap(ctx, current, dek, []byte("dek:"+aad+"/"+current)); err != nil {
		return "", false, err
	}
	sealed.KeyID = current
	encoded, err := json.Marshal(sealed)
	return string(encoded), err == nil, err
}

// keyPrefixes are the stored prefixes of values under keyID, in every
// spelling of it (bare local IDs written before Phase 16 are never used for
// Operation data, but the canonical and bare forms are both matched).
func keyPrefixes(keyID string) ([]string, error) {
	ids := []string{keyID}
	if bare, ok := strings.CutPrefix(keyID, secrets.LocalKeyIDPrefix); ok {
		ids = append(ids, bare)
	} else if !strings.Contains(keyID, ":") {
		ids = append(ids, secrets.LocalKeyIDPrefix+keyID)
	}
	prefixes := make([]string, len(ids))
	for i, id := range ids {
		// The same encoder Seal uses (it escapes <, > and &), so the prefix
		// matches the stored text exactly.
		encoded, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		prefixes[i] = sealedPrefix + string(encoded) + ","
	}
	return prefixes, nil
}

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
