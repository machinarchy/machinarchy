package domain

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestArgumentTemplates(t *testing.T) {
	policy, err := NewExecutionPolicy(true, []string{
		"uptime",
		"systemctl is-active {unit}",
		"systemctl status {unit}",
		"df -h",
		"df -h {path}",
		"journalctl -u {unit} -n {int}",
		"echo {any}",
		"backup.sh *",
		"tail -n {int} *",
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed := [][]string{
		{"uptime"},
		{"systemctl", "is-active", "nginx.service"},
		{"systemctl", "status", "getty@tty1.service"},
		{"df", "-h"},
		{"df", "-h", "/var/lib"},
		{"journalctl", "-u", "nginx", "-n", "50"},
		{"echo", "hello world"},
		{"backup.sh"},
		{"backup.sh", "--full", "--target", "s3://bucket"},
		{"tail", "-n", "20"},
		{"tail", "-n", "20", "/var/log/syslog"},
	}
	for _, argv := range allowed {
		if err := policy.Check(argv); err != nil {
			t.Errorf("Check(%q) = %v", argv, err)
		}
	}
	// Argument abuse of allowed programs (AUDIT/02 S-12).
	denied := map[string][]string{
		"extra argument":                 {"uptime", "-p"},
		"other systemctl verb":           {"systemctl", "stop", "sshd"},
		"verb with an option":            {"systemctl", "is-active", "--host=evil", "nginx"},
		"option where a unit belongs":    {"systemctl", "is-active", "--user"},
		"unit with shell syntax":         {"systemctl", "is-active", "nginx;reboot"},
		"unit with a space":              {"systemctl", "is-active", "a b"},
		"empty unit":                     {"systemctl", "is-active", ""},
		"trailing argument":              {"systemctl", "is-active", "nginx", "sshd"},
		"relative path":                  {"df", "-h", "var"},
		"path traversal":                 {"df", "-h", "/var/../etc"},
		"option as a path":               {"df", "-h", "-x"},
		"not a number":                   {"journalctl", "-u", "nginx", "-n", "all"},
		"negative number":                {"journalctl", "-u", "nginx", "-n", "-1"},
		"{any} refuses options":          {"echo", "-e"},
		"program not listed":             {"rm", "-rf", "/"},
		"same name, other path":          {"/usr/bin/uptime"},
		"case differs":                   {"Uptime"},
		"find -exec stays out":           {"find", "/", "-exec", "sh", "-c", "id", ";"},
		"interpreter not listed":         {"sh", "-c", "reboot"},
		"rest needs the leading literal": {"tail", "-f", "/var/log/syslog"},
	}
	for name, argv := range denied {
		err := policy.Check(argv)
		if !errors.Is(err, ErrExecutionNotAllowed) {
			t.Errorf("%s: Check(%q) = %v, want ErrExecutionNotAllowed", name, argv, err)
			continue
		}
		// Arguments can hold secrets: errors never echo them.
		for _, arg := range argv[1:] {
			if len(arg) > 3 && strings.Contains(err.Error(), arg) && !strings.Contains(strings.Join(policy.Entries, " "), arg) {
				t.Errorf("%s: the error repeats argument %q: %v", name, arg, err)
			}
		}
	}
	// The error lists the program's allowed patterns.
	if err := policy.Check([]string{"systemctl", "stop", "sshd"}); !strings.Contains(err.Error(), "systemctl is-active {unit}; systemctl status {unit}") {
		t.Errorf("error does not list the allowed patterns: %v", err)
	}
}

func TestArgumentTemplateParsing(t *testing.T) {
	policy, err := NewExecutionPolicy(true, []string{"  systemctl   is-active   {unit}  ", "*"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(policy.Entries, "|"); got != "systemctl is-active {unit}|*" {
		t.Errorf("entries = %q, want normalized spacing", got)
	}
	for _, entry := range []string{
		"tail * -n",     // '*' not last
		"cat {file}",    // unknown placeholder
		"cat {path",     // half a placeholder
		"cat pre{path}", // placeholder inside a literal
		"-rf",           // program starting with '-'
		"bad;program",   // shell syntax as program
		"ls " + "\x01",  // control character
	} {
		if _, err := NewExecutionPolicy(true, []string{entry}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("entry %q: err = %v, want ErrInvalidInput", entry, err)
		}
	}
}

// FuzzArgumentTemplates: a placeholder never accepts an option, and an entry
// without '*' never accepts more arguments than it has patterns.
func FuzzArgumentTemplates(f *testing.F) {
	f.Add("systemctl is-active {unit}", "is-active", "nginx")
	f.Add("df -h {path}", "-h", "/var/../etc")
	f.Add("echo {any}", "-e", "x")
	f.Add("tail -n {int} *", "-n", "5")
	f.Fuzz(func(t *testing.T, entry, first, second string) {
		policy, err := NewExecutionPolicy(true, []string{entry})
		if err != nil || policy.AllowAny {
			return
		}
		argv := append(strings.Fields(entry)[:1], first, second)
		if policy.Check(argv) != nil {
			return
		}
		patterns := strings.Fields(entry)[1:]
		rest := len(patterns) > 0 && patterns[len(patterns)-1] == "*"
		if !rest && len(patterns) != 2 {
			t.Fatalf("%q accepted %d arguments", entry, 2)
		}
		for i, pattern := range patterns {
			if i >= 2 || pattern == "*" {
				break
			}
			if strings.HasPrefix(pattern, "{") && strings.HasPrefix(argv[i+1], "-") {
				t.Fatalf("%q let option %q fill %s", entry, argv[i+1], pattern)
			}
			if !strings.HasPrefix(pattern, "{") && argv[i+1] != pattern {
				t.Fatalf("%q accepted %q for literal %q", entry, argv[i+1], pattern)
			}
		}
	})
}

// TestQuoteArgvThroughARealShell: what QuoteArgv sends is what a POSIX
// shell hands to the program, one word per argument (S-12).
func TestQuoteArgvThroughARealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	args := []string{"plain", "two words", "it's", `"double"`, "$(id)", "`id`", "${HOME}", "*", "a;b", "a|b", "a&&b",
		"> /tmp/x", "\\", "'", "''", "-n", "--", "ünïcode", "~", "!", "#comment", ""}
	argv := append([]string{"printf", `%s\n`}, args...)
	if err := ValidateArgv(argv); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-c", QuoteArgv(argv)).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(args) {
		t.Fatalf("the shell produced %d arguments, want %d: %q", len(got), len(args), got)
	}
	for i := range args {
		if got[i] != args[i] {
			t.Errorf("argument %d: sent %q, the program saw %q", i, args[i], got[i])
		}
	}
}
