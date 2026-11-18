package v1_test

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/machinarchy/machinarchy/docs"
	"github.com/machinarchy/machinarchy/internal/runtimeconfig"
	"github.com/machinarchy/machinarchy/internal/secrets"
	"github.com/machinarchy/machinarchy/internal/service"
	v1 "github.com/machinarchy/machinarchy/internal/transport/http/v1"
)

// TestOpenAPISpecCoversEveryRoute keeps docs/swagger.json in step with the
// router (AUDIT L-12): every registered API route and method must be
// documented. Regenerate with `make api-docs` after adding a route.
func TestOpenAPISpecCoversEveryRoute(t *testing.T) {
	recorder := httptest.NewRecorder()
	docs.SpecHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	v1.RegisterRoutes(e.Group("/api"), v1.Repositories{
		Configuration: &runtimeconfig.Service{}, Secrets: &secrets.Service{}, ManagedResources: &service.ManagedResourceService{},
	}, nil, []byte("x"), false, false, nil, nil, nil)

	param := regexp.MustCompile(`:([A-Za-z]+)`)
	var missing []string
	for _, route := range e.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") || route.Method == "echo_route_not_found" || strings.HasSuffix(route.Path, "/*") {
			continue
		}
		path := strings.TrimSuffix(param.ReplaceAllString(route.Path, "{$1}"), "/")
		if _, ok := spec.Paths[path][strings.ToLower(route.Method)]; !ok {
			missing = append(missing, route.Method+" "+path)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("docs/swagger.json is missing %d routes; run make api-docs:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}
