//go:build systemd

package systemd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/machinarchy/machinarchy/internal/hostaccess/runner"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
)

const container = "machinarchy-systemd-test"

type host struct {
	t            *testing.T
	port         int
	key          string
	fingerprints []string
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func inside(t *testing.T, command string) string {
	t.Helper()
	out, _ := exec.Command("docker", "exec", container, "sh", "-c", command).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func startHost(t *testing.T) *host {
	t.Helper()
	image := os.Getenv("MACHINARCHY_SYSTEMD_IMAGE")
	if image == "" {
		image = "machinarchy-systemd-test:local"
	}
	_ = exec.Command("docker", "rm", "-f", container).Run()
	docker(t, "run", "-d", "--name", container, "--privileged", "--cgroupns=host", "-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"--tmpfs", "/run", "--tmpfs", "/run/lock", "-p", "127.0.0.1::22", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	deadline := time.Now().Add(60 * time.Second)
	for {
		state := inside(t, "systemctl is-system-running")
		if state == "running" || state == "degraded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("systemd did not start: %s", state)
		}
		time.Sleep(500 * time.Millisecond)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	for _, user := range []string{"root", "deploy", "nolinger"} {
		home := "/home/" + user
		if user == "root" {
			home = "/root"
		}
		inside(t, fmt.Sprintf("mkdir -p %[1]s/.ssh && echo '%[2]s' > %[1]s/.ssh/authorized_keys && chmod 700 %[1]s/.ssh && chmod 600 %[1]s/.ssh/authorized_keys && chown -R %[3]s %[1]s/.ssh", home, authorized, user))
	}
	_, portText, _ := net.SplitHostPort(docker(t, "port", container, "22/tcp"))
	port, _ := strconv.Atoi(portText)
	var fingerprints []string
	for _, line := range strings.Split(inside(t, "for k in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf $k; done"), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 {
			fingerprints = append(fingerprints, fields[1])
		}
	}
	for inside(t, "systemctl is-active ssh") != "active" {
		if time.Now().After(deadline) {
			t.Fatal("sshd did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return &host{t: t, port: port, key: string(pem.EncodeToMemory(block)), fingerprints: fingerprints}
}

func (h *host) run(user, command string) string {
	h.t.Helper()
	params := sshclient.DialParams{IP: "127.0.0.1", Port: h.port, Username: user, AuthMethod: sshclient.AuthPrivateKey, SecretData: h.key,
		TrustedFingerprints: h.fingerprints, Dialer: (netpolicy.Policy{AllowLoopback: true, AllowedPorts: []uint16{uint16(h.port)}}).DialContext}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := sshclient.ExecuteRemoteCommandDetailed(ctx, params, command)
	if _, isExit := sshclient.ExitStatus(err); err != nil && !isExit {
		h.t.Fatalf("ssh %s: %v", user, err)
	}
	return result.Stdout
}

func (h *host) launch(user string, ref runner.Ref, timeout int, argv ...string) runner.Launched {
	h.t.Helper()
	command, err := runner.LaunchCommand(runner.LaunchRequest{Ref: ref, TimeoutSeconds: timeout, Mode: runner.ModeAuto, Argv: argv})
	if err != nil {
		h.t.Fatal(err)
	}
	launched, err := runner.ParseLaunch(h.run(user, command))
	if err != nil {
		h.t.Fatal(err)
	}
	return launched
}

func (h *host) wait(user string, ref runner.Ref, want runner.State) runner.Status {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		command, _ := runner.StatusCommand("", []runner.Query{{Ref: ref}})
		statuses, err := runner.ParseStatus(h.run(user, command))
		if err != nil {
			h.t.Fatal(err)
		}
		if len(statuses) == 1 && statuses[0].State == want {
			return statuses[0]
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: state %+v, want %s", ref, statuses, want)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (h *host) cancel(user string, ref runner.Ref) {
	h.t.Helper()
	command, _ := runner.CancelCommand("", []runner.Ref{ref})
	if acks, err := runner.ParseAcks(h.run(user, command), "cancel"); err != nil || acks[ref] != "sent" {
		h.t.Fatalf("cancel = %v, %v", acks, err)
	}
}

// processes counts processes whose whole command line is exactly cmdline
// (so neither the command's own `sh -c` nor this probe's shell match).
func processes(t *testing.T, cmdline string) int {
	t.Helper()
	out := inside(t, "pgrep -fx '"+cmdline+"' | wc -l")
	n, _ := strconv.Atoi(out)
	return n
}

func waitProcesses(t *testing.T, want int, cmdlines ...string) {
	t.Helper()
	count := func() int {
		n := 0
		for _, cmdline := range cmdlines {
			n += processes(t, cmdline)
		}
		return n
	}
	deadline := time.Now().Add(30 * time.Second)
	for count() != want {
		if time.Now().After(deadline) {
			t.Fatalf("%d processes run %q, want %d", count(), cmdlines, want)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestRunnerOnRealSystemd(t *testing.T) {
	h := startHost(t)
	ref := func(n int) runner.Ref { return runner.Ref{OperationID: "op-1", TargetID: "t-" + strconv.Itoa(n)} }

	t.Run("root uses the system manager", func(t *testing.T) {
		launched := h.launch("root", ref(1), 60, "sh", "-c", "echo from-systemd; cat /proc/self/cgroup")
		if launched.Mode != runner.ModeSystemd || launched.Unit != "machinarchy-op-1-t-1" {
			t.Fatalf("launch = %+v", launched)
		}
		status := h.wait("root", ref(1), runner.StateExited)
		if *status.ExitCode != 0 || !strings.Contains(string(status.Stdout.Data), "from-systemd") ||
			!strings.Contains(string(status.Stdout.Data), "machinarchy-op-1-t-1.service") {
			t.Fatalf("status = %+v, stdout %q", status, status.Stdout.Data)
		}
	})

	t.Run("a lingering user uses its user manager", func(t *testing.T) {
		launched := h.launch("deploy", ref(2), 60, "sh", "-c", "cat /proc/self/cgroup")
		if launched.Mode != runner.ModeSystemd {
			t.Fatalf("launch = %+v", launched)
		}
		status := h.wait("deploy", ref(2), runner.StateExited)
		if !strings.Contains(string(status.Stdout.Data), "user@1000.service") {
			t.Fatalf("the command did not run under the user manager: %q", status.Stdout.Data)
		}
		if meta := inside(t, "cat /home/deploy/.local/state/machinarchy/ops/op-1/t-2/meta"); !strings.Contains(meta, "scope=user") {
			t.Fatalf("meta = %q", meta)
		}
	})

	t.Run("a user without lingering falls back to setsid", func(t *testing.T) {
		if launched := h.launch("nolinger", ref(3), 60, "true"); launched.Mode != runner.ModeSetsid {
			t.Fatalf("launch = %+v", launched)
		}
		h.wait("nolinger", ref(3), runner.StateExited)
	})

	// The reason to prefer systemd: a process that escapes the command's
	// process group (setsid) is still in the unit's cgroup and is killed.
	t.Run("cancel kills escaped processes under systemd", func(t *testing.T) {
		h.launch("root", ref(4), 600, "sh", "-c", "setsid sleep 9001 & sleep 9002")
		h.wait("root", ref(4), runner.StateRunning)
		waitProcesses(t, 2, "sleep 9001", "sleep 9002")
		h.cancel("root", ref(4))
		status := h.wait("root", ref(4), runner.StateExited)
		if !status.Cancelled {
			t.Fatalf("status = %+v", status)
		}
		waitProcesses(t, 0, "sleep 9001", "sleep 9002")
	})

	t.Run("setsid mode cannot reach an escaped process", func(t *testing.T) {
		h.launch("nolinger", ref(5), 600, "sh", "-c", "setsid sleep 9003 & sleep 9004")
		h.wait("nolinger", ref(5), runner.StateRunning)
		waitProcesses(t, 2, "sleep 9003", "sleep 9004")
		h.cancel("nolinger", ref(5))
		h.wait("nolinger", ref(5), runner.StateExited)
		waitProcesses(t, 0, "sleep 9004")
		if processes(t, "sleep 9003") != 1 {
			t.Fatal("expected the escaped process to survive in setsid mode (documented limitation)")
		}
		inside(t, "pkill -fx 'sleep 9003'")
	})

	t.Run("host-side timeout under systemd", func(t *testing.T) {
		h.launch("root", ref(6), 2, "sleep", "9005")
		status := h.wait("root", ref(6), runner.StateExited)
		if !status.TimedOut {
			t.Fatalf("status = %+v", status)
		}
		waitProcesses(t, 0, "sleep 9005")
	})

	t.Run("a stopped unit is lost", func(t *testing.T) {
		h.launch("root", ref(7), 600, "sleep", "9006")
		h.wait("root", ref(7), runner.StateRunning)
		inside(t, "systemctl stop machinarchy-op-1-t-7.service") // what a reboot does
		status := h.wait("root", ref(7), runner.StateLost)
		if status.ExitCode != nil {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("no unit is left behind", func(t *testing.T) {
		if units := inside(t, "systemctl list-units --all --no-legend 'machinarchy-*' | grep -v inactive | wc -l"); units != "0" {
			t.Fatalf("active machinarchy units remain: %s\n%s", units, inside(t, "systemctl list-units --all --no-legend 'machinarchy-*'"))
		}
	})
}
