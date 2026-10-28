package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/sshclient"
)

// bulkFakeStore implements only what the bulk path uses; any other HostStore
// method panics through the nil embedded interface, which would flag a change
// in what BulkRegisterHosts touches.
type bulkFakeStore struct {
	domain.HostStore
	mu       sync.Mutex
	existing []*domain.Host
	created  []*domain.Host
	nextID   int
	// createErrs makes CreateHost fail for hosts with the given name.
	createErrs map[string]error
}

func (f *bulkFakeStore) ListAllHosts(_ context.Context, _ int) ([]*domain.Host, error) {
	return f.existing, nil
}

func (f *bulkFakeStore) CreateHost(_ context.Context, h *domain.Host) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.createErrs[h.Name]; ok {
		return err
	}
	f.nextID++
	h.ID = strconv.Itoa(f.nextID)
	f.created = append(f.created, h)
	return nil
}

// bulkFakeTester decides the outcome from the target address.
type bulkFakeTester struct {
	delay    time.Duration
	calls    atomic.Int32
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *bulkFakeTester) TestConnection(ctx context.Context, params sshclient.DialParams) error {
	f.calls.Add(1)
	now := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		prev := f.maxSeen.Load()
		if now <= prev || f.maxSeen.CompareAndSwap(prev, now) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	switch params.IP {
	case "bad-auth.example":
		return domain.ErrAuthenticationFailed
	case "unknown-key.example":
		return &domain.ErrHostKeyUnknown{Host: params.IP, Algorithm: "ssh-ed25519", Fingerprint: "SHA256:test"}
	case "boom.example":
		return errors.New("dial tcp 10.0.0.5:22: secret internal detail")
	}
	return nil
}

func newBulkService(t *testing.T, store *bulkFakeStore, tester *bulkFakeTester, capacity int) HostService {
	t.Helper()
	limit, err := NewSSHCapacity(capacity)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewHostServiceWithTesterAndCapacity(store, nil, tester, limit)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func bulkHost(name, address string) domain.BulkHostEntry {
	return domain.BulkHostEntry{Host: &domain.Host{
		Name: name, IPAddress: address,
		SSH: &domain.SSHAccess{
			Username: "deploy",
			AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: "pw", ExpectedFingerprint: "SHA256:abc"},
		},
	}}
}

func TestBulkRegisterPartialSuccessKeepsOrderAndIsolatesFailures(t *testing.T) {
	store := &bulkFakeStore{}
	tester := &bulkFakeTester{}
	svc := newBulkService(t, store, tester, 8)

	entries := []domain.BulkHostEntry{
		bulkHost("web-01", "10.0.0.1"),
		bulkHost("web-02", "bad-auth.example"),
		{PreError: errors.New("name, host address, and SSH username are required")},
		bulkHost("web-03", "unknown-key.example"),
		bulkHost("web-04", "boom.example"),
		bulkHost("web-05", "10.0.0.5"),
	}
	res, err := svc.BulkRegisterHosts(context.Background(), 1, entries, BulkRegisterOptions{})
	if err != nil {
		t.Fatalf("BulkRegisterHosts() error = %v", err)
	}
	if len(res.Results) != len(entries) || res.TotalProcessed != len(entries) {
		t.Fatalf("want one result per record, got %+v", res)
	}
	want := []domain.HostResultStatus{
		domain.StatusSuccess, domain.StatusFailed, domain.StatusFailed,
		domain.StatusFailed, domain.StatusFailed, domain.StatusSuccess,
	}
	for i, status := range want {
		if res.Results[i].Index != i || res.Results[i].Status != status {
			t.Errorf("result[%d] = %+v, want index %d status %s", i, res.Results[i], i, status)
		}
	}
	if res.SuccessCount != 2 || res.FailureCount != 4 {
		t.Errorf("counters = %+v", res)
	}
	if len(store.created) != 2 {
		t.Errorf("persisted %d hosts, want 2 (failures must not be stored)", len(store.created))
	}
	if !strings.Contains(res.Results[3].Error, "SHA256:test") {
		t.Errorf("unknown-key result should carry the fingerprint for operator review: %q", res.Results[3].Error)
	}
	if strings.Contains(res.Results[4].Error, "10.0.0.5") || strings.Contains(res.Results[4].Error, "secret") {
		t.Errorf("unexpected errors must not leak internals: %q", res.Results[4].Error)
	}
	if res.Results[0].ID == "" || store.created[0].WorkspaceID != 1 {
		t.Errorf("created host must carry its ID and the request workspace: %+v", res.Results[0])
	}
}

func TestBulkRegisterDetectsDuplicatesInBatchAndInventory(t *testing.T) {
	existing := &domain.Host{Name: "db-01", IPAddress: "10.0.1.1", SSH: &domain.SSHAccess{Port: 22}}
	store := &bulkFakeStore{existing: []*domain.Host{existing}}
	tester := &bulkFakeTester{}
	svc := newBulkService(t, store, tester, 8)

	entries := []domain.BulkHostEntry{
		bulkHost("web-01", "10.0.0.1"),
		bulkHost("WEB-01", "10.0.0.2"), // same name, different case
		bulkHost("web-02", "10.0.0.1"), // same address:port as the first record
		bulkHost("db-new", "10.0.1.1"), // same address:port as an existing host
		bulkHost("db-01", "10.0.1.99"), // same name as an existing host
		bulkHost("web-03", "10.0.0.1"), // same address again, but...
	}
	entries[5].Host.SSH.Port = 2222 // ...a different port is a different target

	res, err := svc.BulkRegisterHosts(context.Background(), 1, entries, BulkRegisterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.HostResultStatus{
		domain.StatusSuccess, domain.StatusDuplicate, domain.StatusDuplicate,
		domain.StatusDuplicate, domain.StatusDuplicate, domain.StatusSuccess,
	}
	for i, status := range want {
		if res.Results[i].Status != status {
			t.Errorf("result[%d] status = %s, want %s", i, res.Results[i].Status, status)
		}
	}
	if res.DuplicateCount != 4 || res.SuccessCount != 2 {
		t.Errorf("counters = %+v", res)
	}
	if got := tester.calls.Load(); got != 2 {
		t.Errorf("duplicates must not be contacted: %d connection tests, want 2", got)
	}
}

func TestBulkRegisterValidateOnlyContactsAndStoresNothing(t *testing.T) {
	store := &bulkFakeStore{}
	tester := &bulkFakeTester{}
	svc := newBulkService(t, store, tester, 8)

	entries := []domain.BulkHostEntry{
		bulkHost("web-01", "10.0.0.1"),
		bulkHost("web-01", "10.0.0.2"),
		{Host: &domain.Host{Name: "no-ssh-user", IPAddress: "10.0.0.3"}},
	}
	res, err := svc.BulkRegisterHosts(context.Background(), 1, entries, BulkRegisterOptions{ValidateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ValidateOnly || res.ValidCount != 1 || res.DuplicateCount != 1 || res.FailureCount != 1 || res.SuccessCount != 0 {
		t.Fatalf("unexpected response: %+v", res)
	}
	if tester.calls.Load() != 0 || len(store.created) != 0 {
		t.Fatalf("validate_only must not connect (%d) or persist (%d)", tester.calls.Load(), len(store.created))
	}
}

func TestBulkRegisterRespectsConcurrencyBound(t *testing.T) {
	store := &bulkFakeStore{}
	tester := &bulkFakeTester{delay: 15 * time.Millisecond}
	svc := newBulkService(t, store, tester, 32)

	entries := make([]domain.BulkHostEntry, 40)
	for i := range entries {
		entries[i] = bulkHost("host-"+strconv.Itoa(i), "10.1.0."+strconv.Itoa(i+1))
	}
	res, err := svc.BulkRegisterHosts(context.Background(), 1, entries, BulkRegisterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.SuccessCount != len(entries) {
		t.Fatalf("expected all hosts to register: %+v", res)
	}
	if got := tester.maxSeen.Load(); got > bulkRegisterWorkers {
		t.Fatalf("max concurrent verifications = %d, limit %d", got, bulkRegisterWorkers)
	}
	if tester.maxSeen.Load() < 2 {
		t.Fatalf("expected some parallelism, max concurrent = %d", tester.maxSeen.Load())
	}
}

func TestBulkRegisterCancellationReportsUnprocessedRecords(t *testing.T) {
	store := &bulkFakeStore{}
	tester := &bulkFakeTester{delay: time.Second}
	svc := newBulkService(t, store, tester, 32)

	entries := make([]domain.BulkHostEntry, 30)
	for i := range entries {
		entries[i] = bulkHost("host-"+strconv.Itoa(i), "10.2.0."+strconv.Itoa(i+1))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := svc.BulkRegisterHosts(ctx, 1, entries, BulkRegisterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation did not stop the batch promptly: %s", elapsed)
	}
	if res.TotalProcessed != len(entries) || res.SuccessCount != 0 {
		t.Fatalf("cancelled batch must still report every record, none successful: %+v", res)
	}
	for i, r := range res.Results {
		if r.Status != domain.StatusFailed || r.Error == "" {
			t.Fatalf("result[%d] = %+v, want a failed result with a reason", i, r)
		}
	}
}

func TestBulkRegisterRejectsInvalidBatchShape(t *testing.T) {
	svc := newBulkService(t, &bulkFakeStore{}, &bulkFakeTester{}, 8)
	if _, err := svc.BulkRegisterHosts(context.Background(), 1, nil, BulkRegisterOptions{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("empty batch error = %v, want ErrInvalidInput", err)
	}
	tooMany := make([]domain.BulkHostEntry, MaxBulkRegisterHosts+1)
	if _, err := svc.BulkRegisterHosts(context.Background(), 1, tooMany, BulkRegisterOptions{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("oversized batch error = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.BulkRegisterHosts(context.Background(), 0, []domain.BulkHostEntry{bulkHost("a", "b")}, BulkRegisterOptions{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("missing workspace error = %v, want ErrInvalidInput", err)
	}
}

func TestBulkRegisterReportsInsertTimeDuplicateAsDuplicate(t *testing.T) {
	// The up-front check passes, but the database constraint rejects the insert
	// (a concurrent registration won the race). That must read as a duplicate,
	// not as a generic failure, and must not affect the other records.
	store := &bulkFakeStore{createErrs: map[string]error{
		"raced": fmt.Errorf("%w: UNIQUE constraint failed", domain.ErrHostAlreadyExists),
	}}
	svc := newBulkService(t, store, &bulkFakeTester{}, 8)

	res, err := svc.BulkRegisterHosts(context.Background(), 1, []domain.BulkHostEntry{
		bulkHost("fresh-1", "10.9.0.1"),
		bulkHost("raced", "10.9.0.2"),
		bulkHost("fresh-2", "10.9.0.3"),
	}, BulkRegisterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.SuccessCount != 2 || res.DuplicateCount != 1 || res.FailureCount != 0 {
		t.Fatalf("unexpected counters: %+v", res)
	}
	if res.Results[1].Status != domain.StatusDuplicate || res.Results[1].Error == "" || res.Results[1].ID != "" {
		t.Errorf("raced record = %+v, want a duplicate with a reason and no ID", res.Results[1])
	}
	if strings.Contains(res.Results[1].Error, "UNIQUE") {
		t.Errorf("database error text must not reach clients: %q", res.Results[1].Error)
	}
}
