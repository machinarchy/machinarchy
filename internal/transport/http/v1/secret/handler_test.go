package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/store"
)

type denyAll struct{}

func (denyAll) AuthorizeSecretResolution(context.Context, secrets.ResolutionRequest) error {
	return secrets.ErrUnauthorized
}

func newRouter(t *testing.T, principal domain.Principal) (*echo.Echo, *secrets.Service) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stores, err := store.Open(context.Background(), store.Config{Engine: store.EngineSQLite, SQLitePath: filepath.Join(dir, "s.db"),
		EncryptionSecret: "secret-handler-test-key-material-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	service, err := secrets.New(stores.Management, &secrets.StaticKeyProvider{KeyID: "k1", Key: bytes.Repeat([]byte{7}, 32)}, denyAll{})
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	group := e.Group("/api/v1/secrets", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", principal)
			return next(c)
		}
	})
	RegisterRoutes(group, NewHandler(service))
	return e, service
}

func do(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

const value = "-----BEGIN OPENSSH PRIVATE KEY-----\nTOP-SECRET-MATERIAL\n-----END OPENSSH PRIVATE KEY-----\n"

// The Secret Store API is write-only: no response ever carries a value.
func TestSecretAPIIsWriteOnly(t *testing.T) {
	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	e, _ := newRouter(t, owner)
	var responses []string
	record := func(r *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		responses = append(responses, r.Body.String())
		return r
	}

	created := record(do(e, http.MethodPost, "/api/v1/secrets",
		`{"name":"deploy-key","purpose":"ssh-credential","value":`+mustJSON(value)+`}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var meta struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &meta); err != nil || meta.ID == "" {
		t.Fatalf("create response %s: %v", created.Body, err)
	}
	if meta.Version == 0 {
		meta.Version = 1
	}
	for _, request := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/api/v1/secrets", "", http.StatusOK},
		{http.MethodGet, "/api/v1/secrets/" + meta.ID, "", http.StatusOK},
		{http.MethodGet, "/api/v1/secrets/" + meta.ID + "/versions", "", http.StatusOK},
		{http.MethodPost, "/api/v1/secrets/" + meta.ID + "/rotate", `{"expectedVersion":1,"value":` + mustJSON(value+"v2") + `}`, http.StatusCreated},
		{http.MethodPost, "/api/v1/secrets/" + meta.ID + "/rotate", `{"expectedVersion":1,"value":"stale"}`, http.StatusConflict},
		{http.MethodPost, "/api/v1/secrets/" + meta.ID + "/rotate", `{"value":"no precondition"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/secrets/" + meta.ID + "/rotate", `{`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/secrets", `{"name":"bad","purpose":"unknown","value":"x"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/secrets", `{`, http.StatusBadRequest},
		// k0 is not in the keyring, so nothing can be rewrapped from it.
		{http.MethodPost, "/api/v1/secrets/rewrap", `{"oldKeyId":"k0","batchSize":10}`, http.StatusServiceUnavailable},
		{http.MethodPost, "/api/v1/secrets/rewrap", `{`, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/secrets/missing", "", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/secrets/" + meta.ID, "", http.StatusNoContent},
	} {
		got := record(do(e, request.method, request.path, request.body))
		if got.Code != request.want {
			t.Errorf("%s %s = %d %s, want %d", request.method, request.path, got.Code, got.Body, request.want)
		}
	}
	// Deleting retires the secret: its metadata stays for the audit trail.
	if retired := record(do(e, http.MethodGet, "/api/v1/secrets/"+meta.ID, "")); retired.Code != http.StatusOK ||
		!strings.Contains(retired.Body.String(), `"retiredAt"`) {
		t.Fatalf("after delete: %d %s, want the metadata with retiredAt", retired.Code, retired.Body)
	}
	for _, body := range responses {
		if strings.Contains(body, "TOP-SECRET-MATERIAL") || strings.Contains(body, "PRIVATE KEY") {
			t.Fatalf("a response carried secret material: %s", body)
		}
	}
}

// The service enforces the owner rule as well as the router.
func TestSecretAPIRefusesNonOwners(t *testing.T) {
	for _, principal := range []domain.Principal{
		{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator},
		{UserID: 3, WorkspaceID: 1, Role: domain.RoleReader},
		{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner, APITokenID: 9},
	} {
		e, _ := newRouter(t, principal)
		for _, request := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/secrets", ""},
			{http.MethodPost, "/api/v1/secrets", `{"name":"x","purpose":"ssh-credential","value":"v"}`},
			{http.MethodPost, "/api/v1/secrets/rewrap", `{"oldKeyId":"k0"}`},
		} {
			if got := do(e, request.method, request.path, request.body); got.Code != http.StatusForbidden {
				t.Errorf("%+v: %s %s = %d, want 403", principal, request.method, request.path, got.Code)
			}
		}
	}
}

func mustJSON(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
