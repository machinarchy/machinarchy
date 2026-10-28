package service

import (
	"context"
	"errors"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

func TestRegisterHostRejectsForbiddenLiteralBeforeConnecting(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "169.254.169.254", "::1", "0.0.0.0", "224.0.0.5"} {
		store := &bulkFakeStore{}
		tester := &bulkFakeTester{}
		svc := newBulkService(t, store, tester, 8)

		host := bulkHost("blocked", address).Host
		err := svc.RegisterHost(context.Background(), host)
		if !errors.Is(err, domain.ErrTargetNotAllowed) {
			t.Errorf("RegisterHost(%s) error = %v, want ErrTargetNotAllowed", address, err)
		}
		if tester.calls.Load() != 0 || len(store.created) != 0 {
			t.Errorf("RegisterHost(%s) contacted (%d) or stored (%d) a forbidden target", address, tester.calls.Load(), len(store.created))
		}
	}
}

func TestRegisterHostHonorsConfiguredPolicy(t *testing.T) {
	capacity, err := NewSSHCapacity(4)
	if err != nil {
		t.Fatal(err)
	}
	strict, err := NewHostServiceWithTesterAndCapacity(&bulkFakeStore{}, nil, &bulkFakeTester{}, capacity,
		WithTargetPolicy(netpolicy.Policy{})) // private networks disabled
	if err != nil {
		t.Fatal(err)
	}
	if err := strict.RegisterHost(context.Background(), bulkHost("lan", "192.168.1.10").Host); !errors.Is(err, domain.ErrTargetNotAllowed) {
		t.Errorf("private address under a strict policy error = %v, want ErrTargetNotAllowed", err)
	}

	store := &bulkFakeStore{}
	open, err := NewHostServiceWithTesterAndCapacity(store, nil, &bulkFakeTester{}, capacity,
		WithTargetPolicy(netpolicy.Policy{AllowLoopback: true}))
	if err != nil {
		t.Fatal(err)
	}
	if err := open.RegisterHost(context.Background(), bulkHost("local", "127.0.0.1").Host); err != nil {
		t.Errorf("explicitly allowed loopback was rejected: %v", err)
	}
	if len(store.created) != 1 {
		t.Errorf("stored %d hosts, want 1", len(store.created))
	}
}

func TestBulkRegisterReportsPolicyDenialsPerRecord(t *testing.T) {
	store := &bulkFakeStore{}
	tester := &bulkFakeTester{}
	svc := newBulkService(t, store, tester, 8)

	res, err := svc.BulkRegisterHosts(context.Background(), 1, []domain.BulkHostEntry{
		bulkHost("ok-1", "10.0.0.1"),
		bulkHost("metadata", "169.254.169.254"),
		bulkHost("ok-2", "10.0.0.2"),
	}, BulkRegisterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.SuccessCount != 2 || res.FailureCount != 1 || res.Results[1].Status != domain.StatusFailed {
		t.Fatalf("unexpected response: %+v", res)
	}
	if res.Results[1].Error != "target address is not permitted by the network policy" {
		t.Errorf("denial message = %q", res.Results[1].Error)
	}
	if tester.calls.Load() != 2 {
		t.Errorf("%d connection tests, want 2 (the forbidden target must not be contacted)", tester.calls.Load())
	}
}
