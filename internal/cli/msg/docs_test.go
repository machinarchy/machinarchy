package msg

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updateDocs = flag.Bool("update", false, "rewrite docs/cli/messages.md from the catalogue")

const messagesDoc = "../../../docs/cli/messages.md"

// renderMessagesDoc builds the reference of every message that ends a command
// or warns, straight from the catalogue, so the page cannot drift.
func renderMessagesDoc() string {
	var b strings.Builder
	b.WriteString("# CLI messages\n\n")
	b.WriteString("<!-- Generated from internal/cli/msg by `go test ./internal/cli/msg -run TestMessagesDoc -update`. Do not edit. -->\n\n")
	b.WriteString("Every error and warning the CLI prints, with the reason and the next step. `{name}` is replaced by a value when the message is shown.\n")
	for _, kind := range []Kind{KindError, KindSecurity, KindWarning} {
		first := true
		for _, m := range All() {
			if m.Kind != kind {
				continue
			}
			if first {
				b.WriteString("\n## " + strings.ToUpper(kind.String()[:1]) + kind.String()[1:] + "s\n")
				first = false
			}
			b.WriteString("\n### `" + m.ID + "`\n\n" + m.Primary + "\n")
			if m.Why != "" {
				b.WriteString("\n*Why:* " + m.Why + "\n")
			}
			if m.Next != "" {
				b.WriteString("\n*Next:*\n\n```text\n" + m.Next + "\n```\n")
			}
		}
	}

	return b.String()
}

// The messages reference in docs/cli is generated from the catalogue.
func TestMessagesDoc(t *testing.T) {
	want := renderMessagesDoc()
	if *updateDocs {
		if err := os.WriteFile(messagesDoc, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}

		return
	}
	got, err := os.ReadFile(messagesDoc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatal("docs/cli/messages.md is out of date: run `go test ./internal/cli/msg -run TestMessagesDoc -update`")
	}
}
