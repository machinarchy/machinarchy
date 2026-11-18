package runner

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
)

// shells are the POSIX shells the runner is tested with: the system sh and,
// where installed, dash (Debian and Ubuntu's /bin/sh).
func shells(t *testing.T) []string {
	t.Helper()
	found := []string{"/bin/sh"}
	if path, err := exec.LookPath("dash"); err == nil {
		found = append(found, path)
	}
	return found
}

type host struct {
	t     *testing.T
	shell string
	root  string
	path  string
	env   []string
}

func newHost(t *testing.T, shell string) *host {
	t.Helper()
	dir, err := os.MkdirTemp("", "mr") // short: the root appears in every command
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return &host{t: t, shell: shell, root: filepath.Join(dir, "state"), path: os.Getenv("PATH")}
}

// run executes a remote command string the way sshd does: LOGIN_SHELL -c.
func (h *host) run(command string) string {
	h.t.Helper()
	cmd := exec.Command(h.shell, "-c", command)
	cmd.Env = append([]string{"PATH=" + h.path, "HOME=" + filepath.Dir(h.root)}, h.env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			h.t.Fatal(err)
		}
	}
	return out.String()
}

func (h *host) launch(ref Ref, timeout int, mode Mode, argv ...string) Launched {
	h.t.Helper()
	command, err := LaunchCommand(LaunchRequest{Root: h.root, Ref: ref, TimeoutSeconds: timeout, Mode: mode, Argv: argv})
	if err != nil {
		h.t.Fatal(err)
	}
	launched, err := ParseLaunch(h.run(command))
	if err != nil {
		h.t.Fatal(err)
	}
	return launched
}

func (h *host) status(queries ...Query) []Status {
	h.t.Helper()
	command, err := StatusCommand(h.root, queries)
	if err != nil {
		h.t.Fatal(err)
	}
	statuses, err := ParseStatus(h.run(command))
	if err != nil {
		h.t.Fatal(err)
	}
	if len(statuses) != len(queries) {
		h.t.Fatalf("status returned %d replies for %d queries", len(statuses), len(queries))
	}
	return statuses
}

// waitState polls until ref reaches want.
func (h *host) waitState(ref Ref, want State) Status {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status := h.status(Query{Ref: ref})[0]
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s state = %s, want %s", ref, status.State, want)
		}
		time.Sleep(100 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
}

func (h *host) dir(ref Ref) string {
	return filepath.Join(h.root, "ops", ref.OperationID, ref.TargetID)
}

func ref(n int) Ref { return Ref{OperationID: "op-1", TargetID: "t-" + strconv.Itoa(n)} }

func eachShell(t *testing.T, test func(*testing.T, *host)) {
	for _, shell := range shells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			t.Parallel()
			test(t, newHost(t, shell))
		})
	}
}

func TestLaunchIsIdempotentAndReportsExit(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		first := h.launch(ref(1), 60, ModeAuto, "sh", "-c", "echo out; echo err >&2; exit 7")
		if first.Existing || first.Mode != ModeSetsid || first.PID == 0 {
			t.Fatalf("first launch = %+v", first)
		}
		// A retry (the launch reply was lost) adopts the same process.
		second := h.launch(ref(1), 60, ModeAuto, "sh", "-c", "echo SECOND")
		if !second.Existing || second.PID != first.PID {
			t.Fatalf("relaunch = %+v, want the first launch adopted", second)
		}
		status := h.waitState(ref(1), StateExited)
		if *status.ExitCode != 7 || string(status.Stdout.Data) != "out\n" || string(status.Stderr.Data) != "err\n" || status.TimedOut || status.Cancelled {
			t.Fatalf("status = %+v (%q, %q)", status, status.Stdout.Data, status.Stderr.Data)
		}
		info, err := os.Stat(h.dir(ref(1)))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("target directory mode = %v, %v", info.Mode().Perm(), err)
		}
	})
}

func TestArgumentsArriveVerbatim(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		tricky := []string{"it's", "$HOME", "`id`", "a b", "\"q\"", "x;y", "*"}
		h.launch(ref(1), 60, ModeSetsid, append([]string{"printf", "[%s]"}, tricky...)...)
		status := h.waitState(ref(1), StateExited)
		var want strings.Builder
		for _, arg := range tricky {
			fmt.Fprintf(&want, "[%s]", arg)
		}
		if string(status.Stdout.Data) != want.String() {
			t.Fatalf("stdout = %q, want %q", status.Stdout.Data, want.String())
		}
	})
}

func TestOutputIsReadIncrementallyAndSkipsAhead(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		// 200 KiB of output: more than the head plus one chunk.
		h.launch(ref(1), 60, ModeSetsid, "sh", "-c", "i=0; while [ $i -lt 2048 ]; do printf '%099d\\n' $i; i=$((i+1)); done")
		status := h.waitState(ref(1), StateExited)
		const total = 2048 * 100
		if status.Stdout.Size != total || status.Stdout.From != 0 || len(status.Stdout.Data) != ChunkBytes {
			t.Fatalf("first chunk: size %d from %d len %d", status.Stdout.Size, status.Stdout.From, len(status.Stdout.Data))
		}
		// Past the head, a poll that fell behind gets the last ChunkBytes.
		later := h.status(Query{Ref: ref(1), StdoutFrom: HeadBytes + 1})[0]
		if later.Stdout.From != total-ChunkBytes || len(later.Stdout.Data) != ChunkBytes {
			t.Fatalf("skip-ahead chunk: from %d len %d", later.Stdout.From, len(later.Stdout.Data))
		}
		if !strings.HasSuffix(string(later.Stdout.Data), fmt.Sprintf("%099d\n", 2047)) {
			t.Fatal("the skip-ahead chunk does not end with the last line")
		}
		caughtUp := h.status(Query{Ref: ref(1), StdoutFrom: total})[0]
		if caughtUp.Stdout.From != total || len(caughtUp.Stdout.Data) != 0 {
			t.Fatalf("caught-up chunk: from %d len %d", caughtUp.Stdout.From, len(caughtUp.Stdout.Data))
		}
	})
}

func TestCancelKillsTheWholeTree(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		marker := "mr-cancel-" + strconv.Itoa(os.Getpid()) + "-" + filepath.Base(h.shell)
		h.launch(ref(1), 600, ModeSetsid, "sh", "-c", "sleep 300 & sleep 301; echo "+marker)
		h.waitState(ref(1), StateRunning)
		waitFor(t, "the command's children", func() bool { return countProcesses(t, "sleep 30[01]") >= 2 })
		command, err := CancelCommand(h.root, []Ref{ref(1)})
		if err != nil {
			t.Fatal(err)
		}
		acks, err := ParseAcks(h.run(command), "cancel")
		if err != nil || acks[ref(1)] != "sent" {
			t.Fatalf("cancel acks = %v, %v", acks, err)
		}
		status := h.waitState(ref(1), StateExited)
		if !status.Cancelled || *status.ExitCode == 0 {
			t.Fatalf("cancelled status = %+v", status)
		}
		waitFor(t, "every process of the tree to stop", func() bool { return countProcesses(t, "sleep 30[01]") == 0 })
	})
}

func TestHostSideTimeout(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		h.launch(ref(1), 1, ModeSetsid, "sleep", "120")
		status := h.waitState(ref(1), StateExited)
		if !status.TimedOut || *status.ExitCode == 0 {
			t.Fatalf("timed-out status = %+v", status)
		}
	})
}

func TestLostWhenTheSupervisorDies(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		h.launch(ref(1), 600, ModeSetsid, "sleep", "120")
		h.waitState(ref(1), StateRunning)
		// What a reboot or the OOM killer leaves: no exit file, nothing alive.
		for _, name := range []string{"pid", "pgid"} {
			raw, err := os.ReadFile(filepath.Join(h.dir(ref(1)), name))
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			if name == "pgid" {
				pid = -pid
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		status := h.waitState(ref(1), StateLost)
		if status.ExitCode != nil {
			t.Fatalf("a lost target reported an exit status: %+v", status)
		}
	})
}

func TestCleanupRemovesOnlyFinishedTargets(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		h.launch(ref(1), 60, ModeSetsid, "true")
		h.launch(ref(2), 600, ModeSetsid, "sleep", "120")
		h.waitState(ref(1), StateExited)
		h.waitState(ref(2), StateRunning)
		command, err := CleanupCommand(h.root, []Ref{ref(1), ref(2)})
		if err != nil {
			t.Fatal(err)
		}
		acks, err := ParseAcks(h.run(command), "cleanup")
		if err != nil || acks[ref(1)] != "done" || acks[ref(2)] != "busy" {
			t.Fatalf("cleanup acks = %v, %v", acks, err)
		}
		if _, err := os.Stat(h.dir(ref(1))); !os.IsNotExist(err) {
			t.Fatalf("finished target directory still exists: %v", err)
		}
		if h.status(Query{Ref: ref(1)})[0].State != StateMissing {
			t.Fatal("a cleaned-up target must be reported missing")
		}
		cancel, _ := CancelCommand(h.root, []Ref{ref(2)})
		h.run(cancel)
		h.waitState(ref(2), StateExited)
	})
}

// A fake systemd-run that starts the unit's command detached and records its
// arguments: proves the runner's systemd path and its fallback.
// fakeBin holds one fake script reached as loginctl, systemctl and
// systemd-run. It is created once per test process: on macOS the first run
// of every new executable is scanned, which costs seconds per file.
var (
	fakeBinOnce sync.Once
	fakeBinDir  string
	fakeBinErr  error
)

const fakeSystemd = `#!/bin/sh
case ${0##*/} in
loginctl) echo Linger=yes ;;
systemctl) exit 0 ;;
systemd-run)
	[ -n "${MR_FAKE_SYSTEMD_FAIL:-}" ] && exit 1
	printf '%s\n' "$@" >"$HOME/systemd-run.args"
	while [ $# -gt 0 ]; do case $1 in /bin/sh) break ;; *) shift ;; esac; done
	nohup "$@" </dev/null >/dev/null 2>&1 &
	;;
esac
`

func fakeBin(t *testing.T) string {
	t.Helper()
	fakeBinOnce.Do(func() {
		fakeBinDir, fakeBinErr = os.MkdirTemp("", "mrbin")
		if fakeBinErr != nil {
			return
		}
		script := filepath.Join(fakeBinDir, "fake")
		if fakeBinErr = os.WriteFile(script, []byte(fakeSystemd), 0o700); fakeBinErr != nil { // #nosec G306 -- test fixture executable
			return
		}
		for _, name := range []string{"loginctl", "systemctl", "systemd-run"} {
			if fakeBinErr = os.Symlink(script, filepath.Join(fakeBinDir, name)); fakeBinErr != nil {
				return
			}
		}
	})
	if fakeBinErr != nil {
		t.Fatal(fakeBinErr)
	}
	return fakeBinDir
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeBinDir != "" {
		_ = os.RemoveAll(fakeBinDir)
	}
	os.Exit(code)
}

// A fake systemd-run that starts the unit's command detached and records its
// arguments: proves the runner's systemd path and its fallback.
func TestSystemdModeAndFallback(t *testing.T) {
	t.Parallel()
	eachShell(t, func(t *testing.T, h *host) {
		args := filepath.Join(filepath.Dir(h.root), "systemd-run.args")
		h.path = fakeBin(t) + ":" + h.path
		launched := h.launch(ref(1), 60, ModeAuto, "echo", "via-systemd")
		if launched.Mode != ModeSystemd || launched.Unit != "machinarchy-op-1-t-1" {
			t.Fatalf("launch = %+v, want systemd", launched)
		}
		if status := h.waitState(ref(1), StateExited); string(status.Stdout.Data) != "via-systemd\n" {
			t.Fatalf("stdout = %q", status.Stdout.Data)
		}
		recorded, err := os.ReadFile(args)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--user", "--unit=machinarchy-op-1-t-1", "RuntimeMaxSec=120", "KillMode=control-group"} {
			if !strings.Contains(string(recorded), want) {
				t.Fatalf("systemd-run arguments %q lack %q", recorded, want)
			}
		}

		// A systemd-run that fails: auto falls back, systemd refuses.
		h.env = append(h.env, "MR_FAKE_SYSTEMD_FAIL=1")
		if launched := h.launch(ref(2), 60, ModeAuto, "true"); launched.Mode != ModeSetsid {
			t.Fatalf("fallback launch = %+v", launched)
		}
		command, _ := LaunchCommand(LaunchRequest{Root: h.root, Ref: ref(3), TimeoutSeconds: 60, Mode: ModeSystemd, Argv: []string{"true"}})
		var refused *RefusedError
		if _, err := ParseLaunch(h.run(command)); !errors.As(err, &refused) || refused.Code != "systemd_failed" {
			t.Fatalf("forced systemd with a failing systemd-run = %v", err)
		}
		if _, err := os.Stat(h.dir(ref(3))); !os.IsNotExist(err) {
			t.Fatal("a refused launch must not leave its directory behind (it would block a retry)")
		}
	})
}

func TestCraftedInputIsRefused(t *testing.T) {
	t.Parallel()
	for _, bad := range []Ref{{"../x", "t"}, {"op", "a/b"}, {"OP", "t"}, {"op", ""}, {"op", "t;rm"}} {
		if _, err := LaunchCommand(LaunchRequest{Ref: bad, TimeoutSeconds: 1, Argv: []string{"true"}}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("launch with %+v = %v", bad, err)
		}
		if _, err := StatusCommand("", []Query{{Ref: bad}}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("status with %+v = %v", bad, err)
		}
	}
	for _, root := range []string{"relative", "/a/../b", "/a b", "/a;b"} {
		if err := ValidateRoot(root); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("root %q accepted", root)
		}
	}
	if _, err := LaunchCommand(LaunchRequest{Ref: ref(1), TimeoutSeconds: MaxTimeoutSeconds + 1, Argv: []string{"true"}}); err == nil {
		t.Error("a timeout above 24 h was accepted")
	}
	// The script refuses the same input on its own, in case a caller skips
	// the Go checks.
	h := newHost(t, "/bin/sh")
	var refused *RefusedError
	if _, err := ParseLaunch(h.run(command("launch", h.root, "../x", "t", "1", "1048576", "auto", "7", "true"))); !errors.As(err, &refused) || refused.Code != "bad_id" {
		t.Fatalf("script with a crafted ID = %v", err)
	}
	if _, err := ParseStatus(h.run(command("status", "/etc/../tmp", "1", "1", "a/b:0:0"))); !errors.As(err, &refused) || refused.Code != "bad_root" {
		t.Fatalf("script with a crafted root = %v", err)
	}
}

func TestParsersIgnoreShellNoiseAndRejectGarbage(t *testing.T) {
	t.Parallel()
	launched, err := ParseLaunch("Welcome!\n@@MR1 launched setsid 42 - new\r\n")
	if err != nil || launched.PID != 42 || launched.Mode != ModeSetsid {
		t.Fatalf("launch with noise = %+v, %v", launched, err)
	}
	for _, bad := range []string{
		"@@MR1 status op/t running - - 1 0 - 0 0", // too few fields
		"@@MR1 status op/t weird - - 0 0 - 0 0 -",
		"@@MR1 status op/t exited - - 0 0 - 0 0 -",         // exited without a code
		"@@MR1 status op/t running - - 1 2 - 0 0 -",        // from > size
		"@@MR1 status op/t running - - 1 0 !!! 0 0 -",      // not base64
		"@@MR1 status op/t running - bogus 0 0 - 0 0 -",    // unknown flag
		"@@MR1 status ../t running - - 0 0 - 0 0 -",        // bad reference
		"@@MR1 status op/t exited 999 - 0 0 - 0 0 -",       // exit status out of range
		"@@MR1 status op/t running - - 4 0 YWJjZGVm 0 0 -", // data past size
	} {
		if _, err := ParseStatus(bad); !errors.Is(err, ErrProtocol) {
			t.Errorf("ParseStatus(%q) = %v, want ErrProtocol", bad, err)
		}
	}
	if _, err := ParseLaunch("@@MR1 launched tmux 1 - new"); !errors.Is(err, ErrProtocol) {
		t.Error("an unknown launch mode was accepted")
	}
	if _, err := ParseLaunch("no reply at all"); !errors.Is(err, ErrProtocol) {
		t.Error("a missing launch reply was accepted")
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond) // poll interval of a deadline-bounded wait
	}
}

func countProcesses(t *testing.T, pattern string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return 0 // pgrep exits 1 when nothing matches
	}
	return len(strings.Fields(string(out)))
}
