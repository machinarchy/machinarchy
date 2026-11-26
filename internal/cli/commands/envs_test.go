package commands_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

func envsServer(t *testing.T) *apiServer {
	t.Helper()
	routes := map[string]string{
		"GET /api/v1/environments":      `[{"id":1,"name":"production","description":""},{"id":2,"name":"staging","description":"x"}]`,
		"POST /api/v1/environments":     `{"id":3,"name":"lab","description":""}`,
		"DELETE /api/v1/environments/2": `{"message":"ok"}`,
		"GET /api/v1/managed/hosts": `{"items":[{"metadata":{"id":"h1","name":"a","environmentId":1,"revision":1},"spec":{},"status":{}},` +
			`{"metadata":{"id":"h2","name":"b","environmentId":1,"revision":1},"spec":{},"status":{}},` +
			`{"metadata":{"id":"h3","name":"c","environmentId":2,"revision":1},"spec":{},"status":{}}]}`,
	}

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestEnvsListShowsHostCountsAndGuard(t *testing.T) {
	server := envsServer(t)
	config := writeConfig(t, server.URL, "tok")
	if got := execute(t, nil, "", "--config", config, "envs", "add", "production", "--protected", "--yes"); got.code != 0 {
		t.Fatalf("add = %+v", got)
	}

	got := execute(t, nil, "", "--config", config, "envs")
	lines := dataLines(got.stdout)
	if len(lines) > 0 {
		lines = lines[1:]
	}
	if got.code != 0 || len(lines) != 2 ||
		!strings.Contains(lines[0], "production") || !strings.Contains(lines[0], "2") || !strings.Contains(lines[0], "yes (local)") ||
		!strings.Contains(lines[1], "staging") || !strings.Contains(lines[1], "no") {
		t.Fatalf("list = %+v", got)
	}
	if json := execute(t, nil, "", "--config", config, "envs", "-o", "json"); !strings.Contains(json.stdout, `"protectedLocal": true`) {
		t.Fatalf("json = %+v", json)
	}
}

func TestEnvsAdd(t *testing.T) {
	server := envsServer(t)
	config := writeConfig(t, server.URL, "tok")

	t.Run("plain", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "envs", "add", "lab", "--description", "d")
		if got.code != 0 || server.last(t).Method != http.MethodPost || !strings.Contains(string(server.last(t).Body), `"name":"lab"`) ||
			strings.Contains(readFile(t, config), "protected-envs") {
			t.Fatalf("add = %+v", got)
		}
	})

	t.Run("protected needs an answer and creates nothing without one", func(t *testing.T) {
		before := server.countOf(http.MethodPost, "/api/v1/environments")
		got := execute(t, nil, "", "--config", config, "envs", "add", "prod2", "--protected")
		if got.code != clierr.ExitUsage || server.countOf(http.MethodPost, "/api/v1/environments") != before {
			t.Fatalf("add --protected without --yes = %+v (%d new creates)", got, server.countOf(http.MethodPost, "/api/v1/environments")-before)
		}
	})

	t.Run("protected with consent stores the local guard", func(t *testing.T) {
		got := execute(t, nil, "", "--config", config, "envs", "add", "prod2", "--protected", "--yes")
		if got.code != 0 || !strings.Contains(readFile(t, config), "protected-envs") || !strings.Contains(got.stdout, "local guard") {
			t.Fatalf("add --protected --yes = %+v", got)
		}
	})

	t.Run("any is reserved", func(t *testing.T) {
		if got := execute(t, nil, "", "--config", config, "envs", "add", "ANY"); got.code != clierr.ExitUsage {
			t.Fatalf("add any = %+v", got)
		}
	})
}

func TestEnvsUse(t *testing.T) {
	server := envsServer(t)
	config := writeConfig(t, server.URL, "tok")

	if got := execute(t, nil, "", "--config", config, "envs", "use", "Staging"); got.code != 0 ||
		!strings.Contains(got.stdout, "Active env: staging") || !strings.Contains(readFile(t, config), "env: staging") {
		t.Fatalf("use = %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "envs"); !strings.Contains(dataLines(got.stdout)[2], "yes") {
		t.Fatalf("active env not marked: %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "envs", "use", "nope"); got.code != clierr.ExitNotFound {
		t.Fatalf("use unknown = %+v", got)
	}
	if got := execute(t, nil, "", "--config", config, "envs", "use", "any"); got.code != 0 || strings.Contains(readFile(t, config), "env: staging") {
		t.Fatalf("use any = %+v", got)
	}
}

func TestEnvsRemove(t *testing.T) {
	server := envsServer(t)
	config := writeConfig(t, server.URL, "tok")

	t.Run("--yes is not enough", func(t *testing.T) {
		before := server.count()
		got := execute(t, nil, "", "--config", config, "envs", "remove", "staging", "--yes")
		if got.code != clierr.ExitUsage || server.last(t).Method == http.MethodDelete || server.count() > before+2 {
			t.Fatalf("remove --yes = %+v", got)
		}
	})

	t.Run("a wrong name is refused", func(t *testing.T) {
		if got := execute(t, nil, "", "--config", config, "envs", "remove", "staging", "--confirm", "other"); got.code != clierr.ExitUsage {
			t.Fatalf("remove wrong confirm = %+v", got)
		}
	})

	t.Run("typed name removes it and clears the active env and guard", func(t *testing.T) {
		execute(t, nil, "", "--config", config, "envs", "use", "staging")
		execute(t, nil, "", "--config", config, "envs", "add", "staging", "--protected", "--yes")
		got := execute(t, nil, "", "--config", config, "envs", "remove", "staging", "--confirm", "staging")
		if got.code != clierr.ExitUsage || !strings.Contains(got.stderr, "--confirm-env staging") {
			t.Fatalf("protected remove without --confirm-env = %+v", got)
		}
		got = execute(t, nil, "", "--config", config, "envs", "remove", "staging", "--confirm", "staging", "--confirm-env", "staging")
		saved := readFile(t, config)
		if got.code != 0 || server.last(t).Method != http.MethodDelete || server.last(t).Path != "/api/v1/environments/2" ||
			strings.Contains(saved, "env: staging") || strings.Contains(saved, "protected-envs: [staging]") ||
			!strings.Contains(got.stdout, "No host was deleted") {
			t.Fatalf("remove = %+v\n%s", got, saved)
		}
	})

	t.Run("unknown env", func(t *testing.T) {
		if got := execute(t, nil, "", "--config", config, "envs", "remove", "nope", "--confirm", "nope"); got.code != clierr.ExitNotFound {
			t.Fatalf("remove unknown = %+v", got)
		}
	})
}
