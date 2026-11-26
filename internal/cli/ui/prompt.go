package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Errors a Prompter returns.
var (
	// ErrNoInput means nothing can be asked: input is not a terminal, or
	// --no-input was given. A command that gets it must say which flag
	// supplies the answer; it must never wait.
	ErrNoInput = errors.New("nothing can be asked: input is not a terminal or --no-input is set")
	// ErrBack means the person typed "back" to return to the previous question.
	ErrBack = errors.New("back")
	// ErrAborted means input ended before an answer (end of file).
	ErrAborted = errors.New("input ended before an answer was given")
)

// maxAttempts is how many times a question is asked again after an answer
// that cannot be used.
const maxAttempts = 3

// Prompter asks short questions on a terminal (docs/cli/spec/03-ux.md section
// 2). It is deliberately plain: typed answers, numbered choices, defaults in
// brackets, no arrow-key menus, so it works over any terminal and with a
// screen reader. Questions go to Out (stderr), never to stdout.
//
// One Prompter, and so one buffered reader, must serve a whole conversation:
// a reader per question would lose the input it had already buffered.
type Prompter struct {
	In *bufio.Reader
	// Out receives the questions.
	Out io.Writer
	// Interactive is false when input is not a terminal or --no-input is set;
	// every method then returns ErrNoInput without reading.
	Interactive bool
	// ReadSecret reads a line without echo. It is nil when there is no
	// terminal to read from.
	ReadSecret func(question string) (string, error)
}

// Line asks a question and returns the answer. An empty answer returns def.
// The question shows def in brackets when there is one. "back" returns
// ErrBack.
func (p *Prompter) Line(question, def string) (string, error) {
	if !p.Interactive {
		return "", ErrNoInput
	}

	prompt := question
	if def != "" {
		prompt += " [" + def + "]"
	}
	if _, err := fmt.Fprint(p.Out, prompt+": "); err != nil {
		return "", err
	}

	answer, err := p.readLine()
	if err != nil {
		return "", err
	}
	if strings.EqualFold(answer, "back") {
		return "", ErrBack
	}
	if answer == "" {
		return def, nil
	}

	return answer, nil
}

// Secret asks for a value that must not be shown, such as a password or a
// token. It returns ErrNoInput when there is no terminal to hide the typing on.
func (p *Prompter) Secret(question string) (string, error) {
	if !p.Interactive || p.ReadSecret == nil {
		return "", ErrNoInput
	}

	return p.ReadSecret(question + ": ")
}

// Choice shows a numbered list and returns the index of the option chosen.
// An empty answer returns def. An answer that is not a number from the list is
// asked again, up to three times.
func (p *Prompter) Choice(question string, options []string, def int) (int, error) {
	if !p.Interactive {
		return 0, ErrNoInput
	}

	if _, err := fmt.Fprintln(p.Out, question); err != nil {
		return 0, err
	}
	for i, option := range options {
		if _, err := fmt.Fprintf(p.Out, "  %d) %s\n", i+1, option); err != nil {
			return 0, err
		}
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if _, err := fmt.Fprintf(p.Out, "Choose [%d]: ", def+1); err != nil {
			return 0, err
		}
		answer, err := p.readLine()
		if err != nil {
			return 0, err
		}
		if strings.EqualFold(answer, "back") {
			return 0, ErrBack
		}
		if answer == "" {
			return def, nil
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		if _, err := fmt.Fprintf(p.Out, "Type a number from 1 to %d.\n", len(options)); err != nil {
			return 0, err
		}
	}

	return 0, errors.New("no valid choice was given")
}

// YesNo asks a yes or no question. An empty answer returns def; the default is
// shown as a capital letter.
func (p *Prompter) YesNo(question string, def bool) (bool, error) {
	if !p.Interactive {
		return false, ErrNoInput
	}

	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if _, err := fmt.Fprintf(p.Out, "%s %s: ", question, hint); err != nil {
			return false, err
		}
		answer, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "back":
			return false, ErrBack
		}
		if _, err := fmt.Fprintln(p.Out, "Type y or n."); err != nil {
			return false, err
		}
	}

	return false, errors.New("no valid answer was given")
}

// readLine reads one line without its line ending. End of input before any
// character is ErrAborted.
func (p *Prompter) readLine() (string, error) {
	line, err := p.In.ReadString('\n')
	if err != nil && line == "" {
		if errors.Is(err, io.EOF) {
			return "", ErrAborted
		}

		return "", fmt.Errorf("read the answer: %w", err)
	}

	return strings.TrimSpace(line), nil
}
