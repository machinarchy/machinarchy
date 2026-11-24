package agents

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain"
)

type fakeStore struct {
	mu        sync.Mutex
	tokens    []agentgateway.EnrollmentToken
	revokedBy int
}

func (s *fakeStore) ListAgents(context.Context) ([]agentgateway.Agent, error) { return nil, nil }

func (s *fakeStore) CreateEnrollmentToken(_ context.Context, token agentgateway.EnrollmentToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, token)
	return nil
}

func (s *fakeStore) ListEnrollmentTokens(context.Context) ([]agentgateway.EnrollmentToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agentgateway.EnrollmentToken(nil), s.tokens...), nil
}

func (s *fakeStore) RevokeEnrollmentToken(_ context.Context, id string, actorID int, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.tokens {
		if token.ID == id {
			s.revokedBy = actorID
			return nil
		}
	}
	return agentgateway.ErrEnrollmentTokenNotFound
}

// router mounts the routes with the principal injected; the real router adds
// RequireOwner and RequireHuman to the owner group.
func router(store *fakeStore) *echo.Echo {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", domain.Principal{UserID: 42, WorkspaceID: 1, Role: domain.RoleOwner})
			return next(c)
		}
	}
	group := e.Group("/api/v1/agents", inject)
	RegisterRoutes(group, group, NewHandler(store, nil, nil))
	return e
}

func call(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

func TestEnrollmentTokenEndpoints(t *testing.T) {
	store := &fakeStore{}
	e := router(store)

	created := call(e, http.MethodPost, "/api/v1/agents/enrollment-tokens", `{"hostname":"Web-01.Example","ttlSeconds":600}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var response struct {
		ID        string    `json:"id"`
		Token     string    `json:"token"`
		Hostname  string    `json:"hostname"`
		CreatedBy int       `json:"createdBy"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	stored := store.tokens[0]
	switch {
	case !strings.HasPrefix(response.Token, "mae_") || response.ID != stored.ID:
		t.Fatalf("response = %+v", response)
	case stored.Hash != agentgateway.HashEnrollmentToken(response.Token):
		t.Fatal("the stored hash does not match the returned secret")
	case response.Hostname != "web-01.example" || response.CreatedBy != 42:
		t.Fatalf("hostname %q createdBy %d", response.Hostname, response.CreatedBy)
	case time.Until(response.ExpiresAt) > 10*time.Minute || time.Until(response.ExpiresAt) < 9*time.Minute:
		t.Fatalf("expiresAt %v, want in 10 minutes", response.ExpiresAt)
	}

	if empty := call(e, http.MethodPost, "/api/v1/agents/enrollment-tokens", ""); empty.Code != http.StatusCreated {
		t.Fatalf("create without a body = %d %s", empty.Code, empty.Body)
	}
	for name, body := range map[string]string{
		"bad hostname":  `{"hostname":"web_01"}`,
		"too long":      `{"ttlSeconds":90000}`,
		"negative":      `{"ttlSeconds":-1}`,
		"not JSON":      `{`,
		"unknown types": `{"ttlSeconds":"soon"}`,
	} {
		if got := call(e, http.MethodPost, "/api/v1/agents/enrollment-tokens", body); got.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, got.Code)
		}
	}

	list := call(e, http.MethodGet, "/api/v1/agents/enrollment-tokens", "")
	if list.Code != http.StatusOK || bytes.Contains(list.Body.Bytes(), []byte(response.Token)) ||
		bytes.Contains(list.Body.Bytes(), []byte(stored.Hash)) || !bytes.Contains(list.Body.Bytes(), []byte(stored.ID)) {
		t.Fatalf("list = %d %s (it must name the token but never show its secret or hash)", list.Code, list.Body)
	}

	if got := call(e, http.MethodDelete, "/api/v1/agents/enrollment-tokens/"+stored.ID, ""); got.Code != http.StatusNoContent || store.revokedBy != 42 {
		t.Fatalf("revoke = %d (by %d)", got.Code, store.revokedBy)
	}
	if got := call(e, http.MethodDelete, "/api/v1/agents/enrollment-tokens/enr-unknown", ""); got.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown = %d", got.Code)
	}
}

// fakeKeys is a SigningKeys with an active and, optionally, a next key.
type fakeKeys struct {
	pins   []string
	staged bool
	calls  []string
}

func (f *fakeKeys) PublicKeys() []string { return f.pins }
func (f *fakeKeys) List(context.Context) ([]agentgateway.SigningKey, error) {
	return []agentgateway.SigningKey{{ID: "k1", State: agentgateway.SigningKeyActive, PublicKey: make(ed25519.PublicKey, 32), SecretID: "secret-must-not-leak"}}, nil
}
func (f *fakeKeys) Stage(_ context.Context, p domain.Principal) (agentgateway.SigningKey, error) {
	f.calls = append(f.calls, fmt.Sprintf("stage by %d", p.UserID))
	if f.staged {
		return agentgateway.SigningKey{}, agentgateway.ErrSigningKeyStaged
	}
	f.staged = true
	return agentgateway.SigningKey{ID: "k2", State: agentgateway.SigningKeyNext, PublicKey: make(ed25519.PublicKey, 32)}, nil
}
func (f *fakeKeys) Activate(context.Context, domain.Principal) (agentgateway.SigningKey, error) {
	f.calls = append(f.calls, "activate")
	if !f.staged {
		return agentgateway.SigningKey{}, agentgateway.ErrNoStagedSigningKey
	}
	f.staged = false
	return agentgateway.SigningKey{ID: "k2", State: agentgateway.SigningKeyActive, PublicKey: make(ed25519.PublicKey, 32)}, nil
}
func (f *fakeKeys) Discard(context.Context, domain.Principal) (agentgateway.SigningKey, error) {
	f.calls = append(f.calls, "discard")
	if !f.staged {
		return agentgateway.SigningKey{}, agentgateway.ErrNoStagedSigningKey
	}
	f.staged = false
	return agentgateway.SigningKey{ID: "k2", State: agentgateway.SigningKeyRetired, PublicKey: make(ed25519.PublicKey, 32)}, nil
}

// AUDIT M-3: during a rotation the endpoint announces the next key, and
// publicKeys is the value agents pin.
func TestControllerKeyListsTheNextKeyDuringARotation(t *testing.T) {
	for _, tc := range []struct {
		keys SigningKeys
		want int
		body string
	}{
		{nil, http.StatusServiceUnavailable, ""},
		{&fakeKeys{pins: []string{"current"}}, http.StatusOK, `{"publicKey":"current","publicKeys":["current"]}`},
		{&fakeKeys{pins: []string{"current", "next"}}, http.StatusOK, `{"publicKey":"current","nextPublicKey":"next","publicKeys":["current","next"]}`},
	} {
		e := echo.New()
		group := e.Group("/api/v1/agents")
		RegisterRoutes(group, group, NewHandler(&fakeStore{}, nil, tc.keys))
		recorder := call(e, http.MethodGet, "/api/v1/agents/controller-key", "")
		if recorder.Code != tc.want || (tc.body != "" && strings.TrimSpace(recorder.Body.String()) != tc.body) {
			t.Errorf("keys %v: %d %s", tc.keys, recorder.Code, recorder.Body)
		}
	}
}

// Phase 16: the owner stages, activates and discards signing keys; the
// secret reference never appears in a response.
func TestSigningKeyEndpoints(t *testing.T) {
	keys := &fakeKeys{pins: []string{"current"}}
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("principal", domain.Principal{UserID: 42, WorkspaceID: 1, Role: domain.RoleOwner})
			return next(c)
		}
	}
	group := e.Group("/api/v1/agents", inject)
	RegisterRoutes(group, group, NewHandler(&fakeStore{}, nil, keys))
	for _, step := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/v1/agents/signing-keys/next/activate", http.StatusConflict},
		{http.MethodDelete, "/api/v1/agents/signing-keys/next", http.StatusConflict},
		{http.MethodPost, "/api/v1/agents/signing-keys/next", http.StatusCreated},
		{http.MethodPost, "/api/v1/agents/signing-keys/next", http.StatusConflict},
		{http.MethodPost, "/api/v1/agents/signing-keys/next/activate", http.StatusOK},
		{http.MethodPost, "/api/v1/agents/signing-keys/next", http.StatusCreated},
		{http.MethodDelete, "/api/v1/agents/signing-keys/next", http.StatusOK},
	} {
		if got := call(e, step.method, step.path, ""); got.Code != step.want {
			t.Fatalf("%s %s = %d %s, want %d", step.method, step.path, got.Code, got.Body, step.want)
		}
	}
	if keys.calls[2] != "stage by 42" {
		t.Fatalf("calls = %v", keys.calls)
	}
	list := call(e, http.MethodGet, "/api/v1/agents/signing-keys", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"publicKey":"`) || strings.Contains(list.Body.String(), "secret-must-not-leak") {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	off := echo.New()
	offGroup := off.Group("/api/v1/agents", inject)
	RegisterRoutes(offGroup, offGroup, NewHandler(&fakeStore{}, nil, nil))
	if got := call(off, http.MethodPost, "/api/v1/agents/signing-keys/next", ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a gateway = %d", got.Code)
	}
}
