package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits that keep one command bounded.
const (
	MaxRunTimeout          = 900 // seconds, per host
	MaxRunArgs             = 64
	MaxRunArgBytes         = 4096
	MaxRunArgvBytes        = 16384
	MaxRunHostOutputBytes  = 64 << 10
	maxProgramNameLength   = 255
	outputTruncationMarker = "\n[output truncated]"
)

var (
	// ErrExecutionNotAllowed reports that the execution policy refuses a command.
	ErrExecutionNotAllowed  = errors.New("command execution is not permitted")
	programNamePattern      = regexp.MustCompile(`^[A-Za-z0-9_./+][A-Za-z0-9_./+-]*$`)
	terminalEscapeSequences = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
)

// ExecutionPolicy decides whether commands may run at all and which programs
// are allowed. The zero value allows nothing.
type ExecutionPolicy struct {
	Enabled  bool
	AllowAny bool
	Programs map[string]struct{}
}

// NewExecutionPolicy builds a policy from a program list; "*" allows any
// program. Enabling execution with an empty list is an error so that allowing
// everything is always an explicit choice.
func NewExecutionPolicy(enabled bool, programs []string) (ExecutionPolicy, error) {
	policy := ExecutionPolicy{Enabled: enabled, Programs: make(map[string]struct{})}
	for _, program := range programs {
		program = strings.TrimSpace(program)
		switch {
		case program == "":
			continue
		case program == "*":
			policy.AllowAny = true
		case programNamePattern.MatchString(program) && len(program) <= maxProgramNameLength:
			policy.Programs[program] = struct{}{}
		default:
			return ExecutionPolicy{}, fmt.Errorf("%w: %q is not a valid program name", ErrInvalidInput, truncateForError(program))
		}
	}
	if enabled && !policy.AllowAny && len(policy.Programs) == 0 {
		return ExecutionPolicy{}, fmt.Errorf("%w: execution is enabled but no programs are allowed; list the allowed programs or use * to allow any", ErrInvalidInput)
	}
	return policy, nil
}

// Check reports whether program may run.
func (p ExecutionPolicy) Check(program string) error {
	if !p.Enabled {
		return fmt.Errorf("%w: execution is disabled on this server", ErrExecutionNotAllowed)
	}
	if p.AllowAny {
		return nil
	}
	if _, ok := p.Programs[program]; !ok {
		return fmt.Errorf("%w: program %q is not on the allowed list", ErrExecutionNotAllowed, truncateForError(program))
	}
	return nil
}

// ValidateArgv checks a program and its arguments. The program must be a plain
// name or path, so it can never be shell syntax; arguments may hold any text
// except control characters.
func ValidateArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("%w: a command needs at least a program name", ErrInvalidInput)
	}
	if len(argv) > MaxRunArgs {
		return fmt.Errorf("%w: a command may have at most %d arguments", ErrInvalidInput, MaxRunArgs)
	}
	if len(argv[0]) > maxProgramNameLength || !programNamePattern.MatchString(argv[0]) {
		return fmt.Errorf("%w: the program must be a plain name or path of letters, digits, '.', '_', '+', '-', or '/', not starting with '-'", ErrInvalidInput)
	}
	total := 0
	for _, arg := range argv {
		if len(arg) > MaxRunArgBytes {
			return fmt.Errorf("%w: an argument is longer than %d bytes", ErrInvalidInput, MaxRunArgBytes)
		}
		if !utf8.ValidString(arg) {
			return fmt.Errorf("%w: arguments must be valid UTF-8", ErrInvalidInput)
		}
		for _, r := range arg {
			if unicode.IsControl(r) {
				return fmt.Errorf("%w: arguments must not contain control characters", ErrInvalidInput)
			}
		}
		total += len(arg)
	}
	if total > MaxRunArgvBytes {
		return fmt.Errorf("%w: the command is longer than %d bytes in total", ErrInvalidInput, MaxRunArgvBytes)
	}
	return nil
}

// QuoteArgv renders argv as a command line for a POSIX shell in which every
// element is single-quoted, so the remote shell passes each one to the program
// as exactly one argument with no expansion, globbing, or command substitution.
// It assumes ValidateArgv succeeded and that the account's login shell is
// POSIX-compatible.
func QuoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func writeDigestField(digest hash.Hash, value string) {
	_, _ = digest.Write([]byte(strconv.Itoa(len(value)) + ":" + value + "|"))
}

// ArgvDigest returns a SHA-256 digest of argv that is unambiguous across
// argument boundaries. It is recorded instead of the arguments in audit events.
func ArgvDigest(argv []string) string {
	digest := sha256.New()

	for _, arg := range argv {
		writeDigestField(digest, arg)
	}

	return hex.EncodeToString(digest.Sum(nil))
}

// SanitizeOutput makes untrusted remote output safe to store and return:
// terminal escape sequences and control characters (other than newline and
// tab) are removed, invalid UTF-8 is replaced, and the result is cut at maxBytes
// on a character boundary with a marker.
func SanitizeOutput(output string, maxBytes int) string {
	output = terminalEscapeSequences.ReplaceAllString(output, "")
	output = strings.ToValidUTF8(output, "\uFFFD")
	output = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, output)
	if len(output) <= maxBytes {
		return output
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(output[cut]) {
		cut--
	}
	return output[:cut] + outputTruncationMarker
}
