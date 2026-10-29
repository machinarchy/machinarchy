package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type targetFakeHosts struct {
	domain.HostStore
	hosts    []*domain.Host
	err      error
	lastWork int
}

func (f *targetFakeHosts) ListAllHosts(_ context.Context, workspaceID int) ([]*domain.Host, error) {
	f.lastWork = workspaceID
	return f.hosts, f.err
}

type targetFakeEnvs struct {
	domain.EnvironmentStore
	envs []*domain.Environment
}

func (f *targetFakeEnvs) ListEnvironments(context.Context, int) ([]*domain.Environment, error) {
	return f.envs, nil
}

func targetInventory() []*domain.Host {
	mk := func(id int, name, role string, env int) *domain.Host {
		return &domain.Host{
			ID: strconv.Itoa(id), Name: name, Hostname: name + ".example.test", IPAddress: "10.0.0." + strconv.Itoa(id),
			EnvironmentID: env, Labels: map[string]string{"role": role},
			SSH: &domain.SSHAccess{
				Username: "deploy", Status: domain.HostStatusActive,
				AuthType: domain.SSHAuthType{Method: domain.AuthTypePassword, Password: "TOP-SECRET-PASSWORD", PrivateKey: "TOP-SECRET-KEY", Passphrase: "TOP-SECRET-PASS", ExpectedFingerprint: "SHA256:fp"},
			},
		}
	}
	return []*domain.Host{mk(1, "web-01", "web", 1), mk(2, "web-02", "web", 2), mk(3, "db-01", "db", 1)}
}

func newTargetSvc(t *testing.T, hosts []*domain.Host) (domain.TargetService, *targetFakeHosts) {
	t.Helper()
	store := &targetFakeHosts{hosts: hosts}
	svc, err := NewTargetService(store, &targetFakeEnvs{envs: []*domain.Environment{
		{ID: 1, Name: "production"}, {ID: 2, Name: "staging"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store
}

var readerPrincipal = domain.Principal{UserID: 2, WorkspaceID: 7, Role: domain.RoleReader}

func TestTargetPreviewResolvesRuleForTheCallersWorkspace(t *testing.T) {
	svc, store := newTargetSvc(t, targetInventory())
	preview, err := svc.Preview(context.Background(), readerPrincipal, domain.TargetRule{
		Selector: "environment=production,role=web", Include: []string{"db-01"}, Exclude: []string{"ghost"},
	})
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if store.lastWork != 7 {
		t.Errorf("inventory was read for workspace %d, want the principal's 7", store.lastWork)
	}
	if preview.Total != 2 || preview.Truncated || len(preview.Hosts) != 2 ||
		preview.Hosts[0].Name != "db-01" || preview.Hosts[1].Name != "web-01" {
		t.Fatalf("preview = %+v, want db-01 and web-01 (web-02 is in staging)", preview)
	}
	if preview.Hosts[1].Environment != "production" || preview.Hosts[1].Labels["role"] != "web" {
		t.Errorf("host view = %+v", preview.Hosts[1])
	}
	if len(preview.UnknownExcludes) != 1 || preview.UnknownExcludes[0] != "ghost" ||
		preview.UnknownIncludes == nil || preview.Excluded == nil {
		t.Errorf("preview lists must be present (never null): %+v", preview)
	}
}

func TestTargetPreviewNeverContainsCredentials(t *testing.T) {
	svc, _ := newTargetSvc(t, targetInventory())
	preview, err := svc.Preview(context.Background(), readerPrincipal, domain.TargetRule{All: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"TOP-SECRET", "password", "privateKey", "fingerprint"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(secret)) {
			t.Errorf("preview JSON mentions %q: %s", secret, encoded)
		}
	}
}

func TestTargetPreviewAuthorizationAndValidation(t *testing.T) {
	svc, store := newTargetSvc(t, targetInventory())
	ctx := context.Background()

	for name, principal := range map[string]domain.Principal{
		"no role":      {UserID: 1, WorkspaceID: 7},
		"no workspace": {UserID: 1, Role: domain.RoleOwner},
	} {
		if _, err := svc.Preview(ctx, principal, domain.TargetRule{All: true}); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Errorf("%s: error = %v, want ErrPermissionDenied", name, err)
		}
	}
	if store.lastWork != 0 {
		t.Error("a denied caller must not reach the inventory")
	}
	for name, rule := range map[string]domain.TargetRule{
		"empty rule":   {},
		"bad selector": {Selector: "role in ("},
		"all + select": {All: true, Selector: "role=web"},
	} {
		if _, err := svc.Preview(ctx, readerPrincipal, rule); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestTargetPreviewTruncatesLargeResultsButKeepsTheTotal(t *testing.T) {
	hosts := make([]*domain.Host, domain.MaxPreviewHosts+25)
	for i := range hosts {
		id := i + 1
		hosts[i] = &domain.Host{ID: strconv.Itoa(id), Name: "host-" + strconv.Itoa(1000+id), SSH: &domain.SSHAccess{}}
	}
	svc, _ := newTargetSvc(t, hosts)
	preview, err := svc.Preview(context.Background(), readerPrincipal, domain.TargetRule{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != len(hosts) || len(preview.Hosts) != domain.MaxPreviewHosts || !preview.Truncated {
		t.Fatalf("total=%d listed=%d truncated=%v", preview.Total, len(preview.Hosts), preview.Truncated)
	}
}

func TestNewTargetServiceRequiresStores(t *testing.T) {
	if _, err := NewTargetService(nil, &targetFakeEnvs{}); err == nil {
		t.Error("expected an error without a host store")
	}
	if _, err := NewTargetService(&targetFakeHosts{}, nil); err == nil {
		t.Error("expected an error without an environment store")
	}
}
