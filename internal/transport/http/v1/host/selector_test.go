package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/internal/domain"
)

type listFakeHosts struct {
	domain.HostStore
	hosts []*domain.Host

	patchWorkspace, patchID int
	patchSet                map[string]string
	patchRemove             []string
	patchErr                error
	patchResult             map[string]string
}

func (f *listFakeHosts) ListAllHosts(context.Context, int) ([]*domain.Host, error) {
	return f.hosts, nil
}

func (f *listFakeHosts) PatchHostLabels(_ context.Context, workspaceID, id int, set map[string]string, remove []string) (map[string]string, error) {
	f.patchWorkspace, f.patchID, f.patchSet, f.patchRemove = workspaceID, id, set, remove
	return f.patchResult, f.patchErr
}

type listFakeEnvs struct {
	domain.EnvironmentStore
}

func (listFakeEnvs) ListEnvironments(context.Context, int) ([]*domain.Environment, error) {
	return []*domain.Environment{{ID: 1, Name: "production"}, {ID: 2, Name: "staging"}}, nil
}

func listHosts() []*domain.Host {
	mk := func(id int, name, role string, env int) *domain.Host {
		return &domain.Host{ID: strconv.Itoa(id), Name: name, EnvironmentID: env, Labels: map[string]string{"role": role}}
	}
	return []*domain.Host{mk(1, "web-01", "web", 1), mk(2, "web-02", "web", 2), mk(3, "db-01", "db", 1)}
}

func call(h *Handler, handler func(echo.Context) error, method, target, body string, params map[string]string) *httptest.ResponseRecorder {
	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("principal", domain.Principal{UserID: 1, WorkspaceID: 7, Role: domain.RoleOwner})
	for name, value := range params {
		c.SetParamNames(name)
		c.SetParamValues(value)
	}
	_ = handler(c)
	return rec
}

func TestListFiltersWithASelector(t *testing.T) {
	h := NewHandler(&listFakeHosts{hosts: listHosts()}, nil, listFakeEnvs{}, nil)
	names := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var hosts []domain.Host
		if err := json.Unmarshal(rec.Body.Bytes(), &hosts); err != nil {
			t.Fatalf("body %s: %v", rec.Body.String(), err)
		}
		out := make([]string, len(hosts))
		for i, host := range hosts {
			out[i] = host.Name
		}
		return strings.Join(out, ",")
	}

	for _, tc := range []struct{ selector, want string }{
		{"role=web", "web-01,web-02"},
		{"environment=production", "web-01,db-01"},
		{"environment=production,role=web", "web-01"},
		{"role in (db,cache)", "db-01"},
		{"name=web-*,environment!=production", "web-02"},
		{"role=none", ""},
	} {
		rec := call(h, h.List, http.MethodGet, "/hosts?selector="+url.QueryEscape(tc.selector), "", nil)
		if rec.Code != http.StatusOK || names(rec) != tc.want {
			t.Errorf("selector %q: status %d, hosts %q, want %q", tc.selector, rec.Code, names(rec), tc.want)
		}
	}

	if rec := call(h, h.List, http.MethodGet, "/hosts", "", nil); names(rec) != "web-01,web-02,db-01" {
		t.Errorf("no selector must return every host, got %q", names(rec))
	}
	for _, bad := range []string{"", "role in (", "a,,b"} {
		rec := call(h, h.List, http.MethodGet, "/hosts?selector="+url.QueryEscape(bad), "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("selector %q: status = %d, want 400; an empty or invalid selector must never mean all hosts", bad, rec.Code)
		}
	}
}

func TestPatchLabelsHandler(t *testing.T) {
	t.Run("applies a patch in the caller's workspace", func(t *testing.T) {
		store := &listFakeHosts{patchResult: map[string]string{"role": "api"}}
		h := NewHandler(store, nil, listFakeEnvs{}, nil)
		rec := call(h, h.PatchLabels, http.MethodPatch, "/hosts/5/labels", `{"set":{"role":"api"},"remove":["old"]}`, map[string]string{"id": "5"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
		if store.patchWorkspace != 7 || store.patchID != 5 || store.patchSet["role"] != "api" || store.patchRemove[0] != "old" {
			t.Errorf("store call = ws %d id %d set %v remove %v", store.patchWorkspace, store.patchID, store.patchSet, store.patchRemove)
		}
		var body map[string]map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["labels"]["role"] != "api" {
			t.Errorf("body = %s (%v)", rec.Body.String(), err)
		}
	})

	t.Run("rejects bad input before touching the store", func(t *testing.T) {
		for name, tc := range map[string]struct{ id, body string }{
			"bad id":         {"abc", `{"set":{"a":"b"}}`},
			"zero id":        {"0", `{"set":{"a":"b"}}`},
			"bad json":       {"5", `{`},
			"empty patch":    {"5", `{}`},
			"reserved key":   {"5", `{"set":{"name":"x"}}`},
			"invalid value":  {"5", `{"set":{"a":"has space"}}`},
			"set and remove": {"5", `{"set":{"a":"b"},"remove":["a"]}`},
		} {
			store := &listFakeHosts{}
			h := NewHandler(store, nil, listFakeEnvs{}, nil)
			rec := call(h, h.PatchLabels, http.MethodPatch, "/hosts/"+tc.id+"/labels", tc.body, map[string]string{"id": tc.id})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", name, rec.Code)
			}
			if store.patchID != 0 {
				t.Errorf("%s: the store was called", name)
			}
		}
	})

	t.Run("maps store errors", func(t *testing.T) {
		for name, tc := range map[string]struct {
			err    error
			status int
		}{
			"not found":  {domain.ErrHostNotFound, http.StatusNotFound},
			"too many":   {domain.ErrInvalidInput, http.StatusBadRequest},
			"unexpected": {errors.New("pq: connection refused"), http.StatusInternalServerError},
		} {
			store := &listFakeHosts{patchErr: tc.err}
			h := NewHandler(store, nil, listFakeEnvs{}, nil)
			rec := call(h, h.PatchLabels, http.MethodPatch, "/hosts/5/labels", `{"set":{"a":"b"}}`, map[string]string{"id": "5"})
			if rec.Code != tc.status {
				t.Errorf("%s: status = %d, want %d", name, rec.Code, tc.status)
			}
			if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "pq") {
				t.Errorf("%s: internal details leaked: %s", name, rec.Body.String())
			}
		}
	})
}
