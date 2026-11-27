package managedresource_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type answerChecker struct{ reason string }

func (a answerChecker) ReconcileHost(context.Context, *resource.Host) (resource.HostObservation, error) {
	status := resource.ConditionFalse
	if a.reason == "SSHVerified" {
		status = resource.ConditionTrue
	}
	return resource.HostObservation{Conditions: []resource.Condition{
		{Type: "Ready", Status: status, Reason: a.reason}, {Type: "Reachable", Status: resource.ConditionTrue, Reason: "Reachable"},
	}}, nil
}

func TestCheckHostAPI(t *testing.T) {
	f := newAPI(t)
	id := string(f.hosts[0].Metadata.ID)
	path := "/managed/hosts/" + id + "/check"

	f.expect(f.do("", http.MethodPost, path, nil), http.StatusUnauthorized, "anonymous")
	f.expect(f.do(domain.RoleReader, http.MethodPost, path, nil), http.StatusForbidden, "reader")
	f.expect(f.do(domain.RoleOperator, http.MethodPost, path, nil), http.StatusServiceUnavailable, "no checker in this process")

	f.app.UseHostChecker(answerChecker{reason: "HostKeyChanged"})
	f.expect(f.do(domain.RoleOperator, http.MethodPost, "/managed/hosts/not-an-id/check", nil), http.StatusBadRequest, "bad id")
	unknown, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	f.expect(f.do(domain.RoleOperator, http.MethodPost, "/managed/hosts/"+string(unknown)+"/check", nil), http.StatusNotFound, "unknown host")

	got := f.expect(f.do(domain.RoleOperator, http.MethodPost, path, nil), http.StatusOK, "operator check")
	if got.body["ready"] != false || got.body["reason"] != "HostKeyChanged" || got.body["hostId"] != id || got.body["checkedAt"] == nil {
		t.Fatalf("answer = %s", got.raw)
	}
	f.app.UseHostChecker(answerChecker{reason: "SSHVerified"})
	got = f.expect(f.do(domain.RoleOwner, http.MethodPost, path, nil), http.StatusOK, "owner check")
	if got.body["ready"] != true || got.body["reason"] != "SSHVerified" {
		t.Fatalf("answer = %s", got.raw)
	}
}
