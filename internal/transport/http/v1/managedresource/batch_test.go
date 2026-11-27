package managedresource_test

import (
	"net/http"
	"testing"

	"github.com/machinarchy/machinarchy/internal/domain"
)

func batchBody(validateOnly bool, names ...string) map[string]any {
	hosts := make([]map[string]any, len(names))
	for i, name := range names {
		hosts[i] = map[string]any{"name": name, "spec": map[string]any{"accessTarget": map[string]any{"endpoint": "10.1.0.1:22"}}}
	}
	return map[string]any{"validateOnly": validateOnly, "hosts": hosts}
}

func TestBatchHostsAPI(t *testing.T) {
	f := newAPI(t)
	f.expect(f.do("", http.MethodPost, "/managed/hosts/batch", batchBody(false, "n1")), http.StatusUnauthorized, "anonymous")
	f.expect(f.do(domain.RoleReader, http.MethodPost, "/managed/hosts/batch", batchBody(false, "n1")), http.StatusForbidden, "reader")
	f.expect(f.do(domain.RoleOperator, http.MethodPost, "/managed/hosts/batch", batchBody(false)), http.StatusBadRequest, "empty batch")

	preview := f.expect(f.do(domain.RoleOperator, http.MethodPost, "/managed/hosts/batch", batchBody(true, "n1", "host-0")), http.StatusOK, "validate only")
	if preview.body["created"].(float64) != 0 || preview.body["failed"].(float64) != 1 {
		t.Fatalf("preview: %s", preview.raw)
	}
	before := len(items(f.do(domain.RoleOwner, http.MethodGet, "/managed/hosts", nil)))

	got := f.expect(f.do(domain.RoleOperator, http.MethodPost, "/managed/hosts/batch", batchBody(false, "n1", "host-0", "n2")), http.StatusOK, "apply")
	results := got.body["results"].([]any)
	statuses := []string{"created", "conflict", "created"}
	for i, want := range statuses {
		if s := results[i].(map[string]any)["status"]; s != want {
			t.Fatalf("result %d = %v, want %s (%s)", i, s, want, got.raw)
		}
	}
	after := len(items(f.do(domain.RoleOwner, http.MethodGet, "/managed/hosts", nil)))
	if after != before+2 {
		t.Fatalf("hosts %d -> %d, want +2", before, after)
	}
	// Re-running is safe: everything now conflicts.
	again := f.expect(f.do(domain.RoleOperator, http.MethodPost, "/managed/hosts/batch", batchBody(false, "n1", "n2")), http.StatusOK, "rerun")
	if again.body["created"].(float64) != 0 || again.body["failed"].(float64) != 2 {
		t.Fatalf("rerun: %s", again.raw)
	}
}
