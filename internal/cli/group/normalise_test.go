package group

import "testing"

func TestNormalise(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, in, want string }{
		{"plain text untouched", "active\n", "active\n"},
		{"ISO with T and Z", "at 2026-11-26T09:14:03Z done", "at <time> done"},
		{"ISO with space and fraction", "2026-11-26 09:14:03.123456 started", "<time> started"},
		{"ISO with offset", "2026-11-26T09:14:03+01:00", "<time>"},
		{"ISO with short offset", "2026-11-26T09:14:03-0500 x", "<time> x"},
		{"clock time", "ran at 14:02:11 today", "ran at <time> today"},
		{"clock time with fraction", "t=14:02:11.250", "t=<time>"},
		{"syslog time", "Nov  6 14:02:11 host sshd", "<time> host sshd"},
		{"syslog time two-digit day", "Dec 26 01:02:03 x", "<time> x"},
		{"uuid", "id 123e4567-e89b-12d3-a456-426614174000 ok", "id <uuid> ok"},
		{"uuid uppercase", "123E4567-E89B-12D3-A456-426614174000", "<uuid>"},
		{"pid equals", "worker pid=4242 ready", "worker pid=<pid> ready"},
		{"pid with space", "Worker PID 4242 ready", "Worker pid=<pid> ready"},
		{"bracket pid", "sshd[1234]: accepted", "sshd[<pid>]: accepted"},
		{"address", "at 0x7ffd5a3b2c10 fault", "at <addr> fault"},
		{"short hex is not an address", "0x1f stays", "0x1f stays"},
		{"version numbers stay", "nginx/1.24.0 build 7", "nginx/1.24.0 build 7"},
		{"load average stays", "load average: 0.12, 0.10, 0.09", "load average: 0.12, 0.10, 0.09"},
		{"date alone stays", "released 2026-11-26", "released 2026-11-26"},
		{"trailing spaces and tabs go", "a  \t\nb \n", "a\nb\n"},
		{"CRLF becomes LF", "a\r\nb\r\n", "a\nb\n"},
		{"trailing whitespace at the end of the text", "x   ", "x"},
		{"inner spaces stay", "a  b", "a  b"},
		{"word that contains pid is not a pid", "rapid=3", "rapid=3"},
		{"hex word is not a uuid", "deadbeef-dead", "deadbeef-dead"},
		{"month word alone stays", "Nov is a month", "Nov is a month"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := Normalise(tc.in); got != tc.want {
				t.Fatalf("Normalise(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func FuzzNormalise(f *testing.F) {
	for _, seed := range []string{
		"", "a", "2026-11-26T09:14:03Z", "pid=1 [2] 0xdeadbeef", "Nov  6 14:02:11", "123e4567-e89b-12d3-a456-426614174000",
		"x \t\r\n y", "[1][1][1][1][1]", "[", "[12", "Dec", "0x", "pid", "pid=",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := Normalise(in)
		if again := Normalise(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, out, again)
		}
		if len(out) > 3*len(in)+16 { // "[1]" becomes "[<pid>]": at most 7/3 growth
			t.Fatalf("output grew unreasonably: %d -> %d", len(in), len(out))
		}
	})
}
