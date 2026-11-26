package environments

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

// fakeEnvironments records the workspace of every call.
type fakeEnvironments struct {
	err        error
	workspaces []int
	protected  bool                // the stored environment's protection
	saved      *domain.Environment // the last environment passed to Update
}

func (f *fakeEnvironments) CreateEnvironment(_ context.Context, env *domain.Environment) error {
	f.workspaces = append(f.workspaces, env.WorkspaceID)
	env.ID = 5
	return f.err
}
func (f *fakeEnvironments) GetEnvironmentByID(_ context.Context, workspaceID, id int) (*domain.Environment, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.workspaces = append(f.workspaces, workspaceID)
	return &domain.Environment{ID: id, WorkspaceID: workspaceID, Name: "x", Protected: f.protected}, nil
}
func (f *fakeEnvironments) ListEnvironments(_ context.Context, workspaceID int) ([]*domain.Environment, error) {
	f.workspaces = append(f.workspaces, workspaceID)
	return []*domain.Environment{{ID: 1, WorkspaceID: workspaceID, Name: "prod"}}, f.err
}
func (f *fakeEnvironments) UpdateEnvironment(_ context.Context, env *domain.Environment) error {
	f.saved = env
	f.workspaces = append(f.workspaces, env.WorkspaceID)
	return f.err
}
func (f *fakeEnvironments) DeleteEnvironment(_ context.Context, workspaceID, _ int) error {
	f.workspaces = append(f.workspaces, workspaceID)
	return f.err
}

func call(store *fakeEnvironments, principal *domain.Principal, method, path, body string) *httptest.ResponseRecorder {
	e := echo.New()
	inject := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if principal != nil {
				c.Set("principal", *principal)
			}
			return next(c)
		}
	}
	RegisterRoutes(e.Group("/api/v1", inject), NewHandler(store))
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	return recorder
}

// Every call is scoped to the caller's workspace, whatever the body says.
func TestEnvironmentsAreScopedToTheCallersWorkspace(t *testing.T) {
	principal := domain.Principal{UserID: 1, WorkspaceID: 3, Role: domain.RoleOperator}
	store := &fakeEnvironments{}
	for _, request := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/api/v1/environments", `{"name":" staging ","workspaceId":99,"workspace_id":99}`, http.StatusCreated},
		{http.MethodGet, "/api/v1/environments", "", http.StatusOK},
		{http.MethodPut, "/api/v1/environments/5", `{"name":"staging","workspaceId":99}`, http.StatusOK},
		{http.MethodDelete, "/api/v1/environments/5", "", http.StatusOK},
	} {
		if got := call(store, &principal, request.method, request.path, request.body); got.Code != request.want {
			t.Fatalf("%s %s = %d %s", request.method, request.path, got.Code, got.Body)
		}
	}
	for _, workspace := range store.workspaces {
		if workspace != 3 {
			t.Fatalf("a call used workspace %d, want the caller's workspace 3 (calls: %v)", workspace, store.workspaces)
		}
	}
	if len(store.workspaces) != 5 {
		t.Fatalf("store calls = %v", store.workspaces)
	}
}

func TestEnvironmentErrors(t *testing.T) {
	principal := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	for name, tc := range map[string]struct {
		principal          *domain.Principal
		err                error
		method, path, body string
		want               int
	}{
		"no principal (list)":   {nil, nil, http.MethodGet, "/api/v1/environments", "", http.StatusUnauthorized},
		"no principal (create)": {nil, nil, http.MethodPost, "/api/v1/environments", `{"name":"x"}`, http.StatusUnauthorized},
		"no principal (update)": {nil, nil, http.MethodPut, "/api/v1/environments/1", `{"name":"x"}`, http.StatusUnauthorized},
		"no principal (delete)": {nil, nil, http.MethodDelete, "/api/v1/environments/1", "", http.StatusUnauthorized},
		"blank name":            {&principal, nil, http.MethodPost, "/api/v1/environments", `{"name":"  "}`, http.StatusBadRequest},
		"not JSON":              {&principal, nil, http.MethodPost, "/api/v1/environments", `{`, http.StatusBadRequest},
		"duplicate":             {&principal, domain.ErrEnvironmentAlreadyExists, http.MethodPost, "/api/v1/environments", `{"name":"x"}`, http.StatusConflict},
		"create failure":        {&principal, errors.New("db down"), http.MethodPost, "/api/v1/environments", `{"name":"x"}`, http.StatusInternalServerError},
		"list failure":          {&principal, errors.New("db down"), http.MethodGet, "/api/v1/environments", "", http.StatusInternalServerError},
		"bad id":                {&principal, nil, http.MethodPut, "/api/v1/environments/x", `{"name":"x"}`, http.StatusBadRequest},
		"update blank":          {&principal, nil, http.MethodPut, "/api/v1/environments/1", `{"name":""}`, http.StatusBadRequest},
		"update missing":        {&principal, domain.ErrEnvironmentNotFound, http.MethodPut, "/api/v1/environments/1", `{"name":"x"}`, http.StatusNotFound},
		"update duplicate":      {&principal, domain.ErrEnvironmentAlreadyExists, http.MethodPut, "/api/v1/environments/1", `{"name":"x"}`, http.StatusConflict},
		"update failure":        {&principal, errors.New("db down"), http.MethodPut, "/api/v1/environments/1", `{"name":"x"}`, http.StatusInternalServerError},
		"delete bad id":         {&principal, nil, http.MethodDelete, "/api/v1/environments/0", "", http.StatusBadRequest},
		"delete missing":        {&principal, domain.ErrEnvironmentNotFound, http.MethodDelete, "/api/v1/environments/1", "", http.StatusNotFound},
		"delete failure":        {&principal, errors.New("db down"), http.MethodDelete, "/api/v1/environments/1", "", http.StatusInternalServerError},
	} {
		got := call(&fakeEnvironments{err: tc.err}, tc.principal, tc.method, tc.path, tc.body)
		if got.Code != tc.want || strings.Contains(got.Body.String(), "db down") {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body, tc.want)
		}
	}
}

// Protecting is open to anyone who may write the inventory; unprotecting is
// owner-only, and a PUT that omits the field keeps the protection.
func TestUnprotectingAnEnvironmentIsOwnerOnly(t *testing.T) {
	operator := domain.Principal{UserID: 2, WorkspaceID: 1, Role: domain.RoleOperator}
	owner := domain.Principal{UserID: 1, WorkspaceID: 1, Role: domain.RoleOwner}
	store := &fakeEnvironments{protected: true}
	if got := call(store, &operator, http.MethodPut, "/api/v1/environments/1", `{"name":"x","protected":false}`); got.Code != http.StatusForbidden || store.saved != nil {
		t.Fatalf("operator unprotect = %d %s (saved %v)", got.Code, got.Body, store.saved)
	}
	if got := call(store, &operator, http.MethodPut, "/api/v1/environments/1", `{"name":"x","description":"d"}`); got.Code != http.StatusOK || !store.saved.Protected {
		t.Fatalf("PUT without the field = %d %s (saved %+v)", got.Code, got.Body, store.saved)
	}
	if got := call(store, &owner, http.MethodPut, "/api/v1/environments/1", `{"name":"x","protected":false}`); got.Code != http.StatusOK || store.saved.Protected {
		t.Fatalf("owner unprotect = %d %s (saved %+v)", got.Code, got.Body, store.saved)
	}
	open := &fakeEnvironments{}
	if got := call(open, &operator, http.MethodPut, "/api/v1/environments/1", `{"name":"x","protected":true}`); got.Code != http.StatusOK || !open.saved.Protected {
		t.Fatalf("operator protect = %d %s (saved %+v)", got.Code, got.Body, open.saved)
	}
	if got := call(open, &operator, http.MethodPost, "/api/v1/environments", `{"name":"y","protected":true}`); got.Code != http.StatusCreated || !strings.Contains(got.Body.String(), `"protected":true`) {
		t.Fatalf("create protected = %d %s", got.Code, got.Body)
	}
}
