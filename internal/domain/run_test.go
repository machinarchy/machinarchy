package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateArgvAcceptsOrdinaryCommands(t *testing.T) {
	for _, argv := range [][]string{
		{"uptime"},
		{"/usr/bin/systemctl", "restart", "nginx"},
		{"./deploy.sh", "--env=prod", "--verbose"},
		{"echo", "hello world", "it's", `quote"d`, "$HOME", "`id`", "a;b", "a|b", "a&&b", "*", "~"},
		{"printf", ""},
		{"echo", "ünïcödé ✓"},
	} {
		if err := ValidateArgv(argv); err != nil {
			t.Errorf("ValidateArgv(%q) = %v", argv, err)
		}
	}
}

func TestValidateArgvRejectsUnsafeOrOversizedCommands(t *testing.T) {
	long := strings.Repeat("a", MaxRunArgBytes+1)
	many := make([]string, MaxRunArgs+1)
	for i := range many {
		many[i] = "a"
	}
	big := []string{"echo"}
	for len(strings.Join(big, "")) <= MaxRunArgvBytes {
		big = append(big, strings.Repeat("x", MaxRunArgBytes))
	}
	for name, argv := range map[string][]string{
		"empty":                  nil,
		"empty program":          {""},
		"program with space":     {"rm -rf /"},
		"program with semicolon": {"ls;id"},
		"program with pipe":      {"cat|sh"},
		"program with dollar":    {"$(id)"},
		"program with backtick":  {"`id`"},
		"program starting dash":  {"-rf"},
		"program with newline":   {"ls\nid"},
		"program too long":       {strings.Repeat("a", 256)},
		"NUL in argument":        {"echo", "a\x00b"},
		"newline in argument":    {"echo", "a\nb"},
		"escape in argument":     {"echo", "\x1b[31m"},
		"tab in argument":        {"echo", "a\tb"},
		"invalid UTF-8":          {"echo", "\xff\xfe"},
		"argument too long":      {"echo", long},
		"too many arguments":     many,
		"command too long":       big,
	} {
		if err := ValidateArgv(argv); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestQuoteArgvNeutralizesShellSyntax(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"uptime"}, `'uptime'`},
		{[]string{"echo", "hello world"}, `'echo' 'hello world'`},
		{[]string{"echo", "it's"}, `'echo' 'it'\''s'`},
		{[]string{"echo", "$(id)", "`id`", "a;b", "*", "~", "$HOME", "a|b", "a&b", `a\b`, `"q"`}, `'echo' '$(id)' '` + "`id`" + `' 'a;b' '*' '~' '$HOME' 'a|b' 'a&b' 'a\b' '"q"'`},
		{[]string{"printf", ""}, `'printf' ''`},
		{[]string{"echo", "'; rm -rf / #"}, `'echo' ''\''; rm -rf / #'`},
	} {
		if got := QuoteArgv(tc.argv); got != tc.want {
			t.Errorf("QuoteArgv(%q) = %s, want %s", tc.argv, got, tc.want)
		}
	}
}

// unquote interprets a command line produced by QuoteArgv the way a POSIX shell
// would, with no expansion of any kind, to prove every argument round-trips.
func unquote(t *testing.T, line string) []string {
	t.Helper()
	var args []string
	i := 0
	for i < len(line) {
		if line[i] == ' ' {
			i++
			continue
		}
		var current strings.Builder
		for i < len(line) && line[i] != ' ' {
			switch line[i] {
			case '\'':
				j := strings.IndexByte(line[i+1:], '\'')
				if j < 0 {
					t.Fatalf("unterminated quote in %s", line)
				}
				current.WriteString(line[i+1 : i+1+j])
				i += j + 2
			case '\\':
				if i+1 >= len(line) {
					t.Fatalf("dangling backslash in %s", line)
				}
				current.WriteByte(line[i+1])
				i += 2
			default:
				t.Fatalf("unquoted character %q at %d of %s: the quoting leaked", line[i], i, line)
			}
		}
		args = append(args, current.String())
	}
	return args
}

func TestQuoteArgvRoundTrips(t *testing.T) {
	argv := []string{"/bin/echo", "", " ", "a b", "'", "''", "'a'", `\`, `\'`, "$(id)", "`id`", "${IFS}", "a;b", "a&&b", "*", "?", "~", "#", "!", "é", "x'y'z"}
	got := unquote(t, QuoteArgv(argv))
	if len(got) != len(argv) {
		t.Fatalf("round-trip produced %d arguments, want %d: %q", len(got), len(argv), got)
	}
	for i := range argv {
		if got[i] != argv[i] {
			t.Errorf("argument %d = %q, want %q", i, got[i], argv[i])
		}
	}
}

func TestArgvDigestIsUnambiguous(t *testing.T) {
	if ArgvDigest([]string{"ab", "c"}) == ArgvDigest([]string{"a", "bc"}) {
		t.Error("argument boundaries must be part of the digest")
	}
	if ArgvDigest([]string{"a"}) == ArgvDigest([]string{"bc"}) || len(ArgvDigest(nil)) != 64 {
		t.Error("the digest must be deterministic SHA-256 hex")
	}
}

func TestExecutionPolicy(t *testing.T) {
	if _, err := NewExecutionPolicy(true, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("enabling with no programs error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewExecutionPolicy(true, []string{"ok", "bad program"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("an invalid program name error = %v, want ErrInvalidInput", err)
	}
	disabled, err := NewExecutionPolicy(false, nil)
	if err != nil {
		t.Fatalf("a disabled policy needs no programs: %v", err)
	}
	if err := disabled.Check("uptime"); !errors.Is(err, ErrExecutionNotAllowed) {
		t.Errorf("disabled policy error = %v, want ErrExecutionNotAllowed", err)
	}
	if err := (ExecutionPolicy{}).Check("uptime"); !errors.Is(err, ErrExecutionNotAllowed) {
		t.Errorf("the zero policy must allow nothing, got %v", err)
	}

	listed, err := NewExecutionPolicy(true, []string{" uptime ", "/usr/bin/df", ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range []string{"uptime", "/usr/bin/df"} {
		if err := listed.Check(program); err != nil {
			t.Errorf("Check(%q) = %v", program, err)
		}
	}
	for _, program := range []string{"df", "rm", "UPTIME", "uptime2"} {
		if err := listed.Check(program); !errors.Is(err, ErrExecutionNotAllowed) {
			t.Errorf("Check(%q) = %v, want ErrExecutionNotAllowed (matching is exact)", program, err)
		}
	}
	wildcard, err := NewExecutionPolicy(true, []string{"*"})
	if err != nil || wildcard.Check("whatever") != nil {
		t.Errorf("* must allow any program explicitly: %v", err)
	}
}

func TestTargetsHash(t *testing.T) {
	a, b, c := &Host{ID: "1"}, &Host{ID: "2"}, &Host{ID: "3"}
	if TargetsHash([]*Host{a, b}) != TargetsHash([]*Host{b, a}) {
		t.Error("the hash must not depend on order")
	}
	if TargetsHash([]*Host{a, b}) == TargetsHash([]*Host{a, c}) || TargetsHash([]*Host{a}) == TargetsHash([]*Host{a, b}) {
		t.Error("different host sets must hash differently")
	}
	if TargetsHash([]*Host{{ID: "12"}, {ID: "3"}}) == TargetsHash([]*Host{{ID: "1"}, {ID: "23"}}) {
		t.Error("ID boundaries must be part of the hash")
	}
	if len(TargetsHash(nil)) != 64 {
		t.Error("the hash of no hosts must still be a digest")
	}
}

func TestAggregateRunStatus(t *testing.T) {
	ok, bad, late := RunHostSucceeded, RunHostFailed, RunHostTimedOut
	for _, tc := range []struct {
		name        string
		hosts       []RunHostStatus
		cancelled   bool
		interrupted bool
		want        RunStatus
	}{
		{"all succeeded", []RunHostStatus{ok, ok}, false, false, RunSucceeded},
		{"all failed", []RunHostStatus{bad, late}, false, false, RunFailed},
		{"mixed", []RunHostStatus{ok, bad}, false, false, RunPartiallySucceeded},
		{"mixed with timeout", []RunHostStatus{ok, late}, false, false, RunPartiallySucceeded},
		{"user cancel wins", []RunHostStatus{ok, RunHostCancelled}, true, false, RunCancelled},
		{"interruption wins over cancel", []RunHostStatus{ok, RunHostInterrupted}, true, true, RunInterrupted},
		{"unfinished hosts keep it running", []RunHostStatus{ok, RunHostRunning}, false, false, RunRunning},
		{"pending keeps it running", []RunHostStatus{RunHostPending}, false, false, RunRunning},
		{"no hosts is a failure", nil, false, false, RunFailed},
	} {
		if got := AggregateRunStatus(tc.hosts, tc.cancelled, tc.interrupted); got != tc.want {
			t.Errorf("%s: status = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestSanitizeOutput(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "hello\nworld\t!", "hello\nworld\t!"},
		{"color codes", "\x1b[31mred\x1b[0m done", "red done"},
		{"title change", "a\x1b]0;evil title\x07b", "ab"},
		{"hyperlink", "x\x1b]8;;http://e\x1b\\link\x1b]8;;\x1b\\y", "xlinky"},
		{"cursor movement", "keep\x1b[2J\x1b[Hgone", "keepgone"},
		{"control characters", "a\x00b\x07c\rd\x7fe", "abcde"},
		{"invalid utf-8", "ok\xffbad", "ok\uFFFDbad"},
		{"bare escape", "a\x1bb", "ab"},
	} {
		if got := SanitizeOutput(tc.in, 1024); got != tc.want {
			t.Errorf("%s: SanitizeOutput(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}

	long := strings.Repeat("é", 100) // two bytes per rune
	got := SanitizeOutput(long, 51)
	if !strings.HasSuffix(got, outputTruncationMarker) {
		t.Fatalf("truncated output must end with the marker: %q", got[len(got)-30:])
	}
	body := strings.TrimSuffix(got, outputTruncationMarker)
	if len(body) > 51 || strings.ContainsRune(body, '\uFFFD') {
		t.Errorf("truncation must stay within the limit (%d bytes) and never split a character", len(body))
	}
	if SanitizeOutput("short", 100) != "short" {
		t.Error("output within the limit must be unchanged")
	}
}

func TestNormalizeRunFilter(t *testing.T) {
	got, err := NormalizeRunFilter(RunFilter{})
	if err != nil || got.Limit != DefaultRunPageSize {
		t.Fatalf("default filter = %+v, %v", got, err)
	}
	if got, _ := NormalizeRunFilter(RunFilter{Limit: 100000}); got.Limit != MaxRunPageSize {
		t.Errorf("limit must be capped at %d, got %d", MaxRunPageSize, got.Limit)
	}
	if got, err := NormalizeRunFilter(RunFilter{Status: " failed "}); err != nil || got.Status != "failed" {
		t.Errorf("a valid status must be accepted and trimmed: %+v, %v", got, err)
	}
	for name, filter := range map[string]RunFilter{
		"negative limit":  {Limit: -1},
		"negative cursor": {BeforeID: -1},
		"unknown status":  {Status: "exploded"},
		"planned status":  {Status: "planned"},
	} {
		if _, err := NormalizeRunFilter(filter); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestRunStatusFinality(t *testing.T) {
	for _, s := range []RunStatus{RunSucceeded, RunPartiallySucceeded, RunFailed, RunCancelled, RunInterrupted} {
		if !s.Final() {
			t.Errorf("%s must be final", s)
		}
	}
	for _, s := range []RunStatus{RunQueued, RunRunning, RunPlanned} {
		if s.Final() {
			t.Errorf("%s must not be final", s)
		}
	}
	if RunHostPending.Final() || RunHostRunning.Final() || !RunHostTimedOut.Final() {
		t.Error("host status finality is wrong")
	}
}

func TestTargetsChangedErrorMatchesTheSentinel(t *testing.T) {
	var err error = &TargetsChangedError{TargetsHash: "abc", Total: 3}
	if !errors.Is(err, ErrTargetsChanged) {
		t.Error("errors.Is must match ErrTargetsChanged")
	}
	var typed *TargetsChangedError
	if !errors.As(err, &typed) || typed.Total != 3 {
		t.Error("errors.As must recover the current target set")
	}
}
