package hostaccess

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type oneProfile struct {
	revision *resource.AccessProfileRevision
}

func (p oneProfile) GetAccessProfile(_ context.Context, ws int, id resource.ID) (*resource.AccessProfile, error) {
	return &resource.AccessProfile{ID: id, WorkspaceID: ws, CurrentRevision: 1}, nil
}

func (p oneProfile) GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error) {
	return p.revision, nil
}

// countingScanner presents keys and counts scans; Verify fails with
// verifyErr.
type countingScanner struct {
	keys      []string
	scans     int
	verifyErr error
}

func (c *countingScanner) Verify(context.Context, *resource.Host, *resource.AccessProfileRevision) error {
	return c.verifyErr
}

func (c *countingScanner) Execute(context.Context, *resource.Host, *resource.AccessProfileRevision, string, []string) (Result, error) {
	return Result{}, nil
}

func (c *countingScanner) ScanHostKeys(context.Context, *resource.Host) ([]resource.ObservedHostKey, error) {
	c.scans++
	now := time.Now().UTC()
	out := make([]resource.ObservedHostKey, 0, len(c.keys))
	for _, fp := range c.keys {
		out = append(out, resource.ObservedHostKey{Algorithm: "ssh-ed25519", Fingerprint: fp, FirstSeenAt: now, LastSeenAt: now})
	}
	return out, nil
}

const (
	fpA = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fpB = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

// reconcileAndStore runs one reconcile and stores the observation the way
// the Host controller does.
func reconcileAndStore(t *testing.T, r *HostReconciler, host *resource.Host) resource.HostObservation {
	t.Helper()
	observed, err := r.ReconcileHost(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Observe(host.Metadata.Generation, observed.Phase, observed.Conditions, observed.ObservedHostKeys, time.Now()); err != nil {
		t.Fatal(err)
	}
	return observed
}

// Every scan connection ends before authentication, which OpenSSH 9.8+
// penalises per source address. Seen live: rescanning a pending Host on
// every retry got the controller's address blocked by sshd. A pending Host
// is now scanned once per rescan interval.
func TestPendingHostReusesARecentScan(t *testing.T) {
	host, revision := testHostAndRevision(t, "192.0.2.10:22", fpA)
	host.Spec.AccessTarget.Trust = nil
	revision.Spec.Authentication = resource.AuthenticationPrivateKey
	scanner := &countingScanner{keys: []string{fpA}}
	clock := time.Now()
	r, err := NewHostReconciler(oneProfile{revision}, scanner, WithHostKeyRescanInterval(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return clock }

	for range 5 {
		if got := reconcileAndStore(t, r, host); got.Conditions[0].Reason != "HostKeyPendingApproval" {
			t.Fatalf("reason = %s", got.Conditions[0].Reason)
		} else if !strings.Contains(got.Conditions[0].Message, "machinarchy hosts trust ") || strings.Contains(got.Conditions[0].Message, "trust host") {
			t.Fatalf("message names a removed command: %q", got.Conditions[0].Message)
		}
	}
	if scanner.scans != 1 {
		t.Fatalf("5 reconciles of a pending Host scanned %d times, want 1", scanner.scans)
	}
	clock = clock.Add(2 * time.Minute)
	reconcileAndStore(t, r, host)
	if scanner.scans != 2 {
		t.Fatalf("after the rescan interval: %d scans, want 2", scanner.scans)
	}
	host.Metadata.Generation++ // e.g. an endpoint change
	reconcileAndStore(t, r, host)
	if scanner.scans != 3 {
		t.Fatalf("after a spec change: %d scans, want 3", scanner.scans)
	}
}

func TestChangedKeyRescansOnceThenReuses(t *testing.T) {
	host, revision := testHostAndRevision(t, "192.0.2.10:22", fpA)
	scanner := &countingScanner{keys: []string{fpA}}
	r, err := NewHostReconciler(oneProfile{revision}, scanner)
	if err != nil {
		t.Fatal(err)
	}
	// A recent scan that still shows the trusted key predates the change.
	if err := host.Observe(host.Metadata.Generation, resource.HostActive, nil, []resource.ObservedHostKey{
		{Algorithm: "ssh-ed25519", Fingerprint: fpA, FirstSeenAt: time.Now(), LastSeenAt: time.Now()}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	scanner.keys, scanner.verifyErr = []string{fpB}, ErrHostKey
	for range 3 {
		got := reconcileAndStore(t, r, host)
		if got.Conditions[0].Reason != "HostKeyChanged" {
			t.Fatalf("reason = %s", got.Conditions[0].Reason)
		}
		if m := got.Conditions[0].Message; !strings.Contains(m, "machinarchy hosts trust ") || !strings.Contains(m, " --replace") || strings.Contains(m, "trust host") {
			t.Fatalf("message names a removed command: %q", m)
		}
	}
	if scanner.scans != 1 {
		t.Fatalf("scans = %d, want 1: the stale scan must be replaced once, then reused", scanner.scans)
	}
	if keys := host.Status.ObservedHostKeys; len(keys) != 1 || keys[0].Fingerprint != fpB {
		t.Fatalf("observed keys = %+v", keys)
	}
}
