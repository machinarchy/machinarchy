package agents

import (
	"bytes"
	"context"
	"encoding/json"
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
	RegisterRoutes(group, group, group, NewHandler(store, nil, nil))
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

// AUDIT M-3: during a rotation the endpoint announces the next key, and
// publicKeys is the value agents pin.
func TestControllerKeyListsTheNextKeyDuringARotation(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want int
		body string
	}{
		{nil, http.StatusServiceUnavailable, ""},
		{[]string{"current"}, http.StatusOK, `{"publicKey":"current","publicKeys":["current"]}`},
		{[]string{"current", "next"}, http.StatusOK, `{"publicKey":"current","nextPublicKey":"next","publicKeys":["current","next"]}`},
	} {
		e := echo.New()
		group := e.Group("/api/v1/agents")
		RegisterRoutes(group, group, group, NewHandler(&fakeStore{}, nil, tc.keys))
		recorder := call(e, http.MethodGet, "/api/v1/agents/controller-key", "")
		if recorder.Code != tc.want || (tc.body != "" && strings.TrimSpace(recorder.Body.String()) != tc.body) {
			t.Errorf("keys %v: %d %s", tc.keys, recorder.Code, recorder.Body)
		}
	}
}
