package resources

import (
	"context"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/secrets"
)

// The rewrap finds values by a text prefix; it must match what Seal stores
// even for key IDs that JSON escapes.
func TestKeyPrefixesMatchSealedText(t *testing.T) {
	for _, keyID := range []string{"bootstrap-v1", "a<b>&c", `quo"te`, "100%_x"} {
		provider := &secrets.LocalProvider{KeyID: keyID, Key: make([]byte, 32)}
		cipher, err := NewDataCipher(provider, nil)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := cipher.Seal(context.Background(), []byte("x"), dataAAD(columnSpec, 1, "op", ""))
		if err != nil {
			t.Fatal(err)
		}
		prefixes, err := keyPrefixes(keyID)
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, prefix := range prefixes {
			matched = matched || strings.HasPrefix(sealed, prefix)
		}
		if !matched {
			t.Errorf("key ID %q: no prefix %q matches %.60s", keyID, prefixes, sealed)
		}
	}
}
