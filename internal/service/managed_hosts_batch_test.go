package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

func batchItem(name string) HostCreate {
	return HostCreate{Name: name, Spec: resource.HostSpec{AccessTarget: resource.HostAccessTarget{Endpoint: "10.0.0.9:22"}}}
}

func TestBatchPartialSuccessAndDuplicates(t *testing.T) {
	svc, hosts, _ := newManagedService(t)
	ctx := context.Background()
	got, err := svc.CreateHostsBatch(ctx, principal(domain.RoleOperator), []HostCreate{
		batchItem("web-1"), batchItem("WEB-1"), {Name: "", Spec: resource.HostSpec{}}, batchItem("web-2"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{BatchCreated, BatchConflict, BatchInvalid, BatchCreated}
	for i, status := range want {
		if got.Results[i].Status != status || got.Results[i].Index != i {
			t.Fatalf("result %d = %+v, want status %s", i, got.Results[i], status)
		}
	}
	if got.Created != 2 || got.Failed != 2 || len(hosts.hosts) != 2 {
		t.Fatalf("created %d failed %d stored %d", got.Created, got.Failed, len(hosts.hosts))
	}
}

func TestBatchValidateOnlyWritesNothing(t *testing.T) {
	svc, hosts, _ := newManagedService(t)
	ctx := context.Background()
	got, err := svc.CreateHostsBatch(ctx, principal(domain.RoleOperator), []HostCreate{batchItem("web-1"), batchItem("web-2")}, true)
	if err != nil || got.Created != 0 || got.Failed != 0 || len(hosts.hosts) != 0 {
		t.Fatalf("validate-only: %+v err %v stored %d", got, err, len(hosts.hosts))
	}
	for _, r := range got.Results {
		if r.Status != BatchValid || r.ID != "" {
			t.Fatalf("result %+v", r)
		}
	}
}

func TestBatchValidateOnlyReportsExistingHost(t *testing.T) {
	svc, _, _ := newManagedService(t)
	ctx, op := context.Background(), principal(domain.RoleOperator)
	if _, err := svc.CreateHost(ctx, op, "web-1", nil, batchItem("x").Spec); err != nil {
		t.Fatal(err)
	}
	got, err := svc.CreateHostsBatch(ctx, op, []HostCreate{batchItem("web-1"), batchItem("web-2")}, true)
	if err != nil || got.Results[0].Status != BatchConflict || got.Results[1].Status != BatchValid || got.Failed != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestBatchSizeBounds(t *testing.T) {
	svc, _, _ := newManagedService(t)
	owner := principal(domain.RoleOwner)
	if _, err := svc.CreateHostsBatch(context.Background(), owner, nil, false); !errors.Is(err, ErrBatchSize) {
		t.Fatalf("empty: %v", err)
	}
	many := make([]HostCreate, MaxHostBatch+1)
	for i := range many {
		many[i] = batchItem(fmt.Sprintf("h-%d", i))
	}
	if _, err := svc.CreateHostsBatch(context.Background(), owner, many, false); !errors.Is(err, ErrBatchSize) {
		t.Fatalf("101 hosts: %v", err)
	}
	if got, err := svc.CreateHostsBatch(context.Background(), owner, many[:MaxHostBatch], false); err != nil || got.Created != MaxHostBatch {
		t.Fatalf("100 hosts: %+v %v", got, err)
	}
}

func TestBatchPermissions(t *testing.T) {
	svc, hosts, _ := newManagedService(t)
	ctx := context.Background()
	if _, err := svc.CreateHostsBatch(ctx, principal(domain.RoleReader), []HostCreate{batchItem("a")}, false); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("reader: %v", err)
	}
	if _, err := svc.CreateHostsBatch(ctx, domain.Principal{}, []HostCreate{batchItem("a")}, false); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("anonymous: %v", err)
	}
	bound := batchItem("bound")
	bound.Spec.AccessProfileID = mustID(t)
	got, err := svc.CreateHostsBatch(ctx, principal(domain.RoleOperator), []HostCreate{bound, batchItem("plain")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Results[0].Status != BatchForbidden || got.Results[1].Status != BatchCreated || len(hosts.hosts) != 1 {
		t.Fatalf("operator binding a profile: %+v", got.Results)
	}
	if strings.Contains(got.Results[0].Error, string(bound.Spec.AccessProfileID)) {
		t.Fatal("error leaks the profile id")
	}
}

func TestBatchCancelledContextCreatesNothing(t *testing.T) {
	svc, hosts, _ := newManagedService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := svc.CreateHostsBatch(ctx, principal(domain.RoleOwner), []HostCreate{batchItem("a"), batchItem("b")}, false)
	if err != nil || got.Created != 0 || got.Failed != 2 || len(hosts.hosts) != 0 || got.Results[0].Error != "cancelled" {
		t.Fatalf("%+v %v", got, err)
	}
}

// ListHosts answers the name lookup of a validate-only batch.
func (m *memHosts) ListHosts(_ context.Context, f resource.HostListFilter) ([]resource.Host, error) {
	var out []resource.Host
	for _, h := range m.hosts {
		if h.Metadata.WorkspaceID != f.WorkspaceID {
			continue
		}
		for _, name := range f.Lookup {
			if strings.EqualFold(h.Metadata.Name, name) {
				out = append(out, *h)
			}
		}
	}
	return out, nil
}
