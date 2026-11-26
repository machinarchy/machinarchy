package ui

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newPrompter(input string) (*Prompter, *bytes.Buffer) {
	var out bytes.Buffer

	return &Prompter{In: bufio.NewReader(strings.NewReader(input)), Out: &out, Interactive: true}, &out
}

func TestLine(t *testing.T) {
	t.Parallel()

	p, out := newPrompter("https://machinarchy.example\n\n  padded  \n")
	if got, err := p.Line("Server address", ""); err != nil || got != "https://machinarchy.example" {
		t.Fatalf("Line = %q, %v", got, err)
	}
	if got, err := p.Line("Name for this server", "lab"); err != nil || got != "lab" {
		t.Fatalf("an empty answer must give the default, got %q, %v", got, err)
	}
	if got, err := p.Line("Email", ""); err != nil || got != "padded" {
		t.Fatalf("answers are trimmed, got %q, %v", got, err)
	}
	if want := "Server address: Name for this server [lab]: Email: "; out.String() != want {
		t.Fatalf("questions = %q, want %q", out.String(), want)
	}
}

func TestLineBackAndEndOfInput(t *testing.T) {
	t.Parallel()

	p, _ := newPrompter("BACK\n")
	if _, err := p.Line("Email", ""); !errors.Is(err, ErrBack) {
		t.Fatalf("err = %v, want ErrBack", err)
	}
	if _, err := p.Line("Email", ""); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted at the end of input", err)
	}
}

// Nothing is ever read or printed when nothing can be asked.
func TestNonInteractiveNeverReadsOrPrints(t *testing.T) {
	t.Parallel()

	p, out := newPrompter("answer\n")
	p.Interactive = false
	p.ReadSecret = func(string) (string, error) { t.Fatal("a secret was read"); return "", nil }

	if _, err := p.Line("Q", ""); !errors.Is(err, ErrNoInput) {
		t.Errorf("Line err = %v", err)
	}
	if _, err := p.Secret("Q"); !errors.Is(err, ErrNoInput) {
		t.Errorf("Secret err = %v", err)
	}
	if _, err := p.Choice("Q", []string{"a", "b"}, 0); !errors.Is(err, ErrNoInput) {
		t.Errorf("Choice err = %v", err)
	}
	if _, err := p.YesNo("Q", true); !errors.Is(err, ErrNoInput) {
		t.Errorf("YesNo err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q", out.String())
	}
}

func TestSecretUsesTheHiddenReader(t *testing.T) {
	t.Parallel()

	p, out := newPrompter("")
	var asked string
	p.ReadSecret = func(question string) (string, error) { asked = question; return "s3cret", nil }

	got, err := p.Secret("Password")
	if err != nil || got != "s3cret" || asked != "Password: " {
		t.Fatalf("Secret = %q, %v (asked %q)", got, err, asked)
	}
	if out.Len() != 0 {
		t.Fatalf("the prompter itself must not print a hidden question: %q", out.String())
	}

	p.ReadSecret = nil
	if _, err := p.Secret("Password"); !errors.Is(err, ErrNoInput) {
		t.Fatalf("without a hidden reader a secret cannot be asked: %v", err)
	}
}

func TestChoice(t *testing.T) {
	t.Parallel()

	p, out := newPrompter("2\n\nabc\n9\n1\n")
	options := []string{"Email and password", "API token"}

	if got, err := p.Choice("How do you want to sign in?", options, 0); err != nil || got != 1 {
		t.Fatalf("Choice = %d, %v", got, err)
	}
	if want := "How do you want to sign in?\n  1) Email and password\n  2) API token\nChoose [1]: "; out.String() != want {
		t.Fatalf("question = %q, want %q", out.String(), want)
	}
	out.Reset()

	if got, err := p.Choice("Q", options, 0); err != nil || got != 0 {
		t.Fatalf("an empty answer must give the default: %d, %v", got, err)
	}
	out.Reset()

	// "abc" and "9" are invalid and asked again; "1" is accepted.
	if got, err := p.Choice("Q", options, 1); err != nil || got != 0 {
		t.Fatalf("Choice = %d, %v", got, err)
	}
	if strings.Count(out.String(), "Type a number from 1 to 2.") != 2 {
		t.Fatalf("invalid answers must be explained: %q", out.String())
	}
}

func TestChoiceGivesUpAndGoesBack(t *testing.T) {
	t.Parallel()

	p, _ := newPrompter("x\ny\nz\n")
	if _, err := p.Choice("Q", []string{"a"}, 0); err == nil || errors.Is(err, ErrBack) {
		t.Fatalf("three invalid answers must fail, got %v", err)
	}
	p, _ = newPrompter("back\n")
	if _, err := p.Choice("Q", []string{"a"}, 0); !errors.Is(err, ErrBack) {
		t.Fatalf("err = %v", err)
	}
}

func TestYesNo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		def   bool
		want  bool
	}{{"y\n", false, true}, {"YES\n", false, true}, {"n\n", true, false}, {"No\n", true, false}, {"\n", true, true}, {"\n", false, false}}
	for _, tc := range tests {
		p, _ := newPrompter(tc.input)
		if got, err := p.YesNo("Stay signed in?", tc.def); err != nil || got != tc.want {
			t.Errorf("YesNo(%q, default %v) = %v, %v", tc.input, tc.def, got, err)
		}
	}

	p, out := newPrompter("maybe\ny\n")
	if got, err := p.YesNo("Send it?", true); err != nil || !got || !strings.Contains(out.String(), "Send it? [Y/n]: Type y or n.") {
		t.Fatalf("got %v, %v, %q", got, err, out.String())
	}

	p, out = newPrompter("\n")
	_, _ = p.YesNo("Delete it?", false)
	if !strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("the default must be shown as a capital: %q", out.String())
	}
}
