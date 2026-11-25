package tokens

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/machinarchy/machinarchy/internal/domain"
)

type fakeTokens struct {
	err       error
	principal domain.Principal
	role      domain.Role
	revoked   int
}

func (f *fakeTokens) Create(_ context.Context, p domain.Principal, name string, role domain.Role, _ int) (*domain.IssuedAPIToken, error) {
	f.principal, f.role = p, role
	if f.err != nil {
		return nil, f.err
	}
	return &domain.IssuedAPIToken{APIToken: domain.APIToken{ID: 1, Name: name, Role: role, TokenHash: "stored-hash"}, Token: "mch_pat_secret"}, nil
}

func (f *fakeTokens) List(_ context.Context, p domain.Principal) ([]domain.APIToken, error) {
	f.principal = p
	return []domain.APIToken{{ID: 1, Name: "ci", Role: domain.RoleReader, TokenHash: "stored-hash"}}, f.err
}

func (f *fakeTokens) Revoke(_ context.Context, p domain.Principal, id int) error {
	f.principal, f.revoked = p, id
	return f.err
}

var owner = domain.Principal{UserID: 4, WorkspaceID: 1, Role: domain.RoleOwner}

func call(service *fakeTokens, principal *domain.Principal, method, path, body string) *httptest.ResponseRecorder {
	e := echo.New()
	group := e.Group("/api/v1/tokens", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if principal != nil {
				c.Set("principal", *principal)
			}
			return next(c)
		}
	})
	RegisterRoutes(group, NewHandler(service))
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

func TestTokenEndpoints(t *testing.T) {
	service := &fakeTokens{}
	created := call(service, &owner, http.MethodPost, "/api/v1/tokens", `{"name":"ci","role":" Operator "}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), "mch_pat_secret") ||
		service.role != domain.RoleOperator || service.principal != owner {
		t.Fatalf("create = %d %s (role %q)", created.Code, created.Body, service.role)
	}
	listed := call(service, &owner, http.MethodGet, "/api/v1/tokens", "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "stored-hash") || strings.Contains(created.Body.String(), "stored-hash") {
		t.Fatalf("list = %d %s (the stored hash must never be returned)", listed.Code, listed.Body)
	}
	if revoked := call(service, &owner, http.MethodDelete, "/api/v1/tokens/7", ""); revoked.Code != http.StatusOK || service.revoked != 7 {
		t.Fatalf("revoke = %d (id %d)", revoked.Code, service.revoked)
	}
	for name, tc := range map[string]struct {
		principal    *domain.Principal
		err          error
		method, path string
		body         string
		want         int
	}{
		"no principal":   {nil, nil, http.MethodGet, "/api/v1/tokens", "", http.StatusUnauthorized},
		"no principal 2": {nil, nil, http.MethodPost, "/api/v1/tokens", `{}`, http.StatusUnauthorized},
		"no principal 3": {nil, nil, http.MethodDelete, "/api/v1/tokens/1", "", http.StatusUnauthorized},
		"bad body":       {&owner, nil, http.MethodPost, "/api/v1/tokens", `{`, http.StatusBadRequest},
		"bad id":         {&owner, nil, http.MethodDelete, "/api/v1/tokens/abc", "", http.StatusBadRequest},
		"zero id":        {&owner, nil, http.MethodDelete, "/api/v1/tokens/0", "", http.StatusBadRequest},
		"invalid":        {&owner, domain.ErrInvalidInput, http.MethodPost, "/api/v1/tokens", `{}`, http.StatusBadRequest},
		"denied":         {&owner, domain.ErrPermissionDenied, http.MethodGet, "/api/v1/tokens", "", http.StatusForbidden},
		"duplicate":      {&owner, domain.ErrAPITokenAlreadyExists, http.MethodPost, "/api/v1/tokens", `{}`, http.StatusConflict},
		"not found":      {&owner, domain.ErrAPITokenNotFound, http.MethodDelete, "/api/v1/tokens/3", "", http.StatusNotFound},
		"store failure":  {&owner, errors.New("db down"), http.MethodGet, "/api/v1/tokens", "", http.StatusInternalServerError},
	} {
		got := call(&fakeTokens{err: tc.err}, tc.principal, tc.method, tc.path, tc.body)
		if got.Code != tc.want || strings.Contains(got.Body.String(), "db down") {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body, tc.want)
		}
		if name == "duplicate" && !strings.Contains(got.Body.String(), `"code":"already_exists"`) {
			t.Errorf("duplicate: body %s, want code already_exists", got.Body)
		}
	}
}
