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

// ExecutionPolicy decides whether commands may run at all, which programs
// are allowed and with which arguments (D-11/S-12). The zero value allows
// nothing.
//
// Each policy entry is a template: the program, then one pattern per
// argument, separated by spaces:
//
//	uptime                        uptime with no arguments
//	systemctl is-active {unit}    exactly these three words, a unit name last
//	df -h {path}                  an absolute path
//	backup.sh *                   any arguments (an explicit choice)
//	*                             any program with any arguments
//
// A literal must equal the argument. A placeholder matches one argument of
// its kind and never one that starts with '-', so an option cannot be slipped
// in where a value is expected. A final '*' matches any remaining arguments.
// A program may have several entries; the command must match one of them.
type ExecutionPolicy struct {
	Enabled  bool
	AllowAny bool
	Programs map[string][]ArgTemplate
	// Entries are the normalized entries the policy was built from.
	Entries []string
}

// ArgTemplate is the argument part of one policy entry.
type ArgTemplate struct {
	tokens []argToken
	rest   bool // a final '*': any further arguments
}

type argToken struct {
	literal     string
	placeholder string // "" for a literal
}

// Argument placeholders. None matches an argument starting with '-'.
var argPlaceholders = map[string]func(string) bool{
	// {unit}: a systemd unit, service or similar name.
	"unit": regexp.MustCompile(`^[A-Za-z0-9@_.:-]{1,256}$`).MatchString,
	// {word}: letters, digits, '.', '_' and '-'.
	"word": regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`).MatchString,
	// {int}: a non-negative decimal number.
	"int": regexp.MustCompile(`^[0-9]{1,18}$`).MatchString,
	// {path}: an absolute path without '..' segments.
	"path": func(arg string) bool {
		if !strings.HasPrefix(arg, "/") {
			return false
		}
		for _, segment := range strings.Split(arg, "/") {
			if segment == ".." {
				return false
			}
		}
		return true
	},
	// {any}: any single argument that is not an option.
	"any": func(string) bool { return true },
}

// NewExecutionPolicy builds a policy from template entries; "*" allows any
// program. Enabling execution with an empty list is an error so that allowing
// everything is always an explicit choice.
func NewExecutionPolicy(enabled bool, entries []string) (ExecutionPolicy, error) {
	policy := ExecutionPolicy{Enabled: enabled, Programs: make(map[string][]ArgTemplate)}
	for _, entry := range entries {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		normalized := strings.Join(fields, " ")
		if normalized == "*" {
			policy.AllowAny = true
			policy.Entries = append(policy.Entries, normalized)
			continue
		}
		program := fields[0]
		if !programNamePattern.MatchString(program) || len(program) > maxProgramNameLength {
			return ExecutionPolicy{}, fmt.Errorf("%w: %q is not a valid program name", ErrInvalidInput, truncateForError(program))
		}
		template, err := parseArgTemplate(fields[1:])
		if err != nil {
			return ExecutionPolicy{}, fmt.Errorf("%w: entry %q: %w", ErrInvalidInput, truncateForError(normalized), err)
		}
		policy.Programs[program] = append(policy.Programs[program], template)
		policy.Entries = append(policy.Entries, normalized)
	}
	if enabled && !policy.AllowAny && len(policy.Programs) == 0 {
		return ExecutionPolicy{}, fmt.Errorf("%w: execution is enabled but no programs are allowed; list the allowed programs or use * to allow any", ErrInvalidInput)
	}
	return policy, nil
}

func parseArgTemplate(fields []string) (ArgTemplate, error) {
	var template ArgTemplate
	if len(fields) > MaxRunArgs {
		return template, fmt.Errorf("more than %d argument patterns", MaxRunArgs)
	}
	for i, field := range fields {
		switch {
		case field == "*":
			if i != len(fields)-1 {
				return template, errors.New("'*' must be the last pattern")
			}
			template.rest = true
		case strings.HasPrefix(field, "{") && strings.HasSuffix(field, "}"):
			name := field[1 : len(field)-1]
			if _, ok := argPlaceholders[name]; !ok {
				return template, fmt.Errorf("unknown placeholder %s (use {unit}, {word}, {int}, {path} or {any})", field)
			}
			template.tokens = append(template.tokens, argToken{placeholder: name})
		case strings.ContainsAny(field, "{}"):
			return template, fmt.Errorf("%q: braces are only allowed around a whole placeholder", field)
		default:
			if len(field) > MaxRunArgBytes || !utf8.ValidString(field) || strings.IndexFunc(field, unicode.IsControl) >= 0 {
				return template, errors.New("a literal must be valid text without control characters")
			}
			template.tokens = append(template.tokens, argToken{literal: field})
		}
	}
	return template, nil
}

// matches reports whether args (argv without the program) fit the template.
func (t ArgTemplate) matches(args []string) bool {
	if len(args) < len(t.tokens) || (!t.rest && len(args) != len(t.tokens)) {
		return false
	}
	for i, arg := range args {
		if i == len(t.tokens) {
			break // the rest, after a final '*'
		}
		token := t.tokens[i]
		if token.placeholder == "" {
			if arg != token.literal {
				return false
			}
			continue
		}
		if arg == "" || strings.HasPrefix(arg, "-") || !argPlaceholders[token.placeholder](arg) {
			return false
		}
	}
	return true
}

// Check reports whether argv may run. Errors name the program, never the
// arguments, which can hold secrets.
func (p ExecutionPolicy) Check(argv []string) error {
	if !p.Enabled {
		return fmt.Errorf("%w: execution is disabled on this server", ErrExecutionNotAllowed)
	}
	if p.AllowAny {
		return nil
	}
	if len(argv) == 0 {
		return fmt.Errorf("%w: no program given", ErrExecutionNotAllowed)
	}
	templates, ok := p.Programs[argv[0]]
	if !ok {
		return fmt.Errorf("%w: program %q is not on the allowed list", ErrExecutionNotAllowed, truncateForError(argv[0]))
	}
	for _, template := range templates {
		if template.matches(argv[1:]) {
			return nil
		}
	}
	return fmt.Errorf("%w: these arguments for %q match no allowed pattern (allowed: %s)", ErrExecutionNotAllowed,
		truncateForError(argv[0]), strings.Join(p.patternsFor(argv[0]), "; "))
}

// patternsFor lists the entries for program, for error messages.
func (p ExecutionPolicy) patternsFor(program string) []string {
	var out []string
	for _, entry := range p.Entries {
		if entry == program || strings.HasPrefix(entry, program+" ") {
			out = append(out, entry)
		}
	}
	return out
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
