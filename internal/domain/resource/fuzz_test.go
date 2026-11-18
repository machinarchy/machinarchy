package resource

import (
	"strings"
	"testing"
)

// FuzzParseOperationCursor: decoding never panics and a decoded cursor
// re-encodes to an equivalent one.
func FuzzParseOperationCursor(f *testing.F) {
	id, _ := NewResourceID()
	f.Add(OperationCursor("2026-11-18T00:00:00Z", id))
	f.Add("!!")
	f.Add("")
	f.Fuzz(func(t *testing.T, cursor string) {
		created, parsed, err := ParseOperationCursor(cursor)
		if err != nil {
			return
		}
		again, id2, err := ParseOperationCursor(OperationCursor(created, parsed))
		if err != nil || again != created || id2 != parsed {
			t.Fatalf("round trip of %q failed: %q %q %v", cursor, again, id2, err)
		}
	})
}

// FuzzParseResourceID: accepted IDs are the lower-cased, trimmed input and
// validate as canonical.
func FuzzParseResourceID(f *testing.F) {
	id, _ := NewResourceID()
	f.Add(string(id))
	f.Add("0F3C")
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := ParseResourceID(value)
		if err == nil && (parsed.Validate() != nil || string(parsed) != strings.ToLower(strings.TrimSpace(value))) {
			t.Fatalf("accepted %q as %q", value, parsed)
		}
	})
}
