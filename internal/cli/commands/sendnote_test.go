package commands

import (
	"strings"
	"testing"
)

// F3: the credential note must not claim HTTPS on a plain http:// server.
func TestSendNoteMatchesScheme(t *testing.T) {
	if got := noteFor("http://localhost:8081"); strings.Contains(got, "over HTTPS") || !strings.Contains(got, "plain HTTP") {
		t.Fatalf("http note = %q", got)
	}
	if got := noteFor("https://machinarchy.example"); !strings.Contains(got, "over HTTPS") {
		t.Fatalf("https note = %q", got)
	}
}
