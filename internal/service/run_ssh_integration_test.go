package service

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/testutil/sshtest"
)

const runSSHPassword = "correct horse battery"

type commandLog struct {
	mu       sync.Mutex
	commands []string
}

func (l *commandLog) add(command string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.commands = append(l.commands, command)
}

func (l *commandLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.commands...)
}

func sshRunHost(id int, name string, server *sshtest.Server, fingerprint string) *domain.Host {
	h := runHost(id, name, server.Host, server.Port, nil)
	h.SSH.AuthType.Password = runSSHPassword
	h.SSH.AuthType.ExpectedFingerprint = fingerprint
	return h
}

func TestRunAgainstRealSSHServers(t *testing.T) {
	var sent commandLog
	echoing := func(_ context.Context, command string, out io.Writer) int {
		sent.add(command)
		if _, err := fmt.Fprintf(out, "ran: %s", command); err != nil {
			return 1
		}

		return 0
	}
	failing := func(_ context.Context, command string, out io.Writer) int {
		sent.add(command)
		_, _ = fmt.Fprint(out, "boom\x1b[31m red \x1b[0m\x00")
		return 3
	}
	hanging := func(ctx context.Context, command string, _ io.Writer) int {
		sent.add(command)
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
		return 0
	}

	good := sshtest.Start(t, sshtest.Options{Password: runSSHPassword, Exec: echoing})
	exits := sshtest.Start(t, sshtest.Options{Password: runSSHPassword, Exec: failing})
	imposter := sshtest.Start(t, sshtest.Options{Password: runSSHPassword, Exec: echoing})
	slow := sshtest.Start(t, sshtest.Options{Password: runSSHPassword, Exec: hanging})

	hosts := newRunHosts(
		sshRunHost(1, "a-ok", good, good.Fingerprint),
		sshRunHost(2, "b-exits", exits, exits.Fingerprint),
		sshRunHost(3, "c-wrong-key", imposter, "SHA256:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"),
		sshRunHost(4, "d-slow", slow, slow.Fingerprint),
	)
	store := newMemRunStore()
	capacity, err := NewSSHCapacity(8)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewRunService(store, hosts, noEnvironments{}, capacity, allowAllPrograms(t),
		WithTargetPolicy(loopbackPolicy(good.Port, exits.Port, imposter.Port, slow.Port)), WithRunTiming(20*time.Millisecond, 400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	argv := []string{"echo", "hello world", "it's", "$(touch /tmp/pwned)", "a;b", "`id`"}
	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{
		Argv: argv, Rule: domain.TargetRule{All: true}, Concurrency: 4, TimeoutSeconds: 2,
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	final := store.waitFinal(t, run.ID)
	byName := map[string]domain.RunHost{}
	for _, h := range final.Hosts {
		byName[h.Name] = h
	}

	if final.Status != domain.RunPartiallySucceeded || final.Counts.Succeeded != 1 || final.Counts.Failed != 2 || final.Counts.TimedOut != 1 {
		t.Fatalf("final run = %s %+v", final.Status, final.Counts)
	}

	// The remote shell receives every argument as one single-quoted word.
	want := domain.QuoteArgv(argv)
	if h := byName["a-ok"]; h.Status != domain.RunHostSucceeded || h.Output != "ran: "+want || h.ExitCode == nil || *h.ExitCode != 0 {
		t.Errorf("a-ok = %+v, want output %q", h, "ran: "+want)
	}
	for _, command := range sent.all() {
		if command != want {
			t.Errorf("a host received %q, want exactly %q", command, want)
		}
	}
	if !strings.Contains(want, `'$(touch /tmp/pwned)'`) || !strings.Contains(want, "'`id`'") || !strings.Contains(want, `'it'\''s'`) {
		t.Errorf("the command line is not safely quoted: %s", want)
	}

	// A non-zero exit status is a failed host that keeps its (sanitized) output.
	if h := byName["b-exits"]; h.Status != domain.RunHostFailed || h.ExitCode == nil || *h.ExitCode != 3 ||
		h.Output != "boom red " || h.Error != "the command exited with status 3" {
		t.Errorf("b-exits = %+v", h)
	}

	// A host presenting an unexpected key never sees credentials or the command.
	if h := byName["c-wrong-key"]; h.Status != domain.RunHostFailed || !strings.Contains(h.Error, imposter.Fingerprint) || h.ExitCode != nil {
		t.Errorf("c-wrong-key = %+v", h)
	}
	if imposter.AuthAttempts() != 0 || imposter.Execs() != 0 {
		t.Errorf("the imposter saw %d logins and %d commands", imposter.AuthAttempts(), imposter.Execs())
	}

	// A command that outlives its timeout is timed out, with the honest caveat.
	if h := byName["d-slow"]; h.Status != domain.RunHostTimedOut || !strings.Contains(h.Error, "may still be running") {
		t.Errorf("d-slow = %+v", h)
	}
}

func TestRunNeverReachesForbiddenAddresses(t *testing.T) {
	server := sshtest.Start(t, sshtest.Options{Password: runSSHPassword})
	hosts := newRunHosts(sshRunHost(1, "loopback-host", server, server.Fingerprint))
	store := newMemRunStore()
	capacity, _ := NewSSHCapacity(4)
	// The default target policy refuses loopback.
	svc, err := NewRunService(store, hosts, noEnvironments{}, capacity, allowAllPrograms(t),
		WithRunTiming(20*time.Millisecond, 400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"uptime"}, Rule: domain.TargetRule{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	final := store.waitFinal(t, run.ID)
	if final.Status != domain.RunFailed || !strings.Contains(final.Hosts[0].Error, "not permitted by the network policy") {
		t.Fatalf("final run = %s %+v", final.Status, final.Hosts)
	}
	if server.Accepted() != 0 {
		t.Errorf("the forbidden target accepted %d connections", server.Accepted())
	}
}

func TestRunWithManyHostsAgainstRealServers(t *testing.T) {
	const count = 12
	var hostList []*domain.Host
	for i := 1; i <= count; i++ {
		server := sshtest.Start(t, sshtest.Options{Password: runSSHPassword, Exec: func(_ context.Context, _ string, out io.Writer) int {
			_, _ = fmt.Fprint(out, "done")
			return 0
		}})
		hostList = append(hostList, sshRunHost(i, "host-"+strconv.Itoa(100+i), server, server.Fingerprint))
	}
	store := newMemRunStore()
	capacity, _ := NewSSHCapacity(4)
	ports := make([]int, 0, len(hostList))
	for _, host := range hostList {
		ports = append(ports, host.SSH.Port)
	}
	svc, err := NewRunService(store, newRunHosts(hostList...), noEnvironments{}, capacity, allowAllPrograms(t),
		WithTargetPolicy(loopbackPolicy(ports...)), WithRunTiming(20*time.Millisecond, 400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	run, err := svc.Submit(context.Background(), opsPrincipal, domain.RunRequest{Argv: []string{"true"}, Rule: domain.TargetRule{All: true}, Concurrency: 6})
	if err != nil {
		t.Fatal(err)
	}
	final := store.waitFinal(t, run.ID)
	if final.Status != domain.RunSucceeded || final.Counts.Succeeded != count {
		t.Fatalf("final run = %s %+v", final.Status, final.Counts)
	}
}
