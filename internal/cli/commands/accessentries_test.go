package commands_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
)

const testPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nSUPERSECRETKEYBODY\n-----END OPENSSH PRIVATE KEY-----\n"

// accessEntriesServer answers the routes of the `access` commands. When
// failProfile is set, creating an Access Profile is refused.
func accessEntriesServer(t *testing.T, failProfile bool) *apiServer {
	t.Helper()
	routes := map[string]string{
		"GET /api/v1/managed/access-profiles":        `{"items":[{"id":"prof-1","name":"agents","currentRevision":1},{"id":"prof-2","name":"deploy","currentRevision":2}]}`,
		"GET /api/v1/managed/access-profiles/prof-1": `{"id":"prof-1","name":"agents","currentRevision":1,"spec":{"connectorKind":"agent"}}`,
		"GET /api/v1/managed/access-profiles/prof-2": `{"id":"prof-2","name":"deploy","description":"CI","currentRevision":2,` +
			`"spec":{"connectorKind":"ssh","username":"deploy","authenticationMethod":"private-key","hostKeyPolicy":"approve","credential":{"id":"sec-1","version":1}}}`,
		"GET /api/v1/managed/hosts": `{"items":[{"metadata":{"id":"h1","name":"web-01","revision":1},"spec":{"accessProfileId":"prof-2"},"status":{}},` +
			`{"metadata":{"id":"h2","name":"web-02","revision":1},"spec":{"accessProfileId":"prof-2"},"status":{}}]}`,
		"POST /api/v1/secrets":                          `{"id":"sec-9","name":"x","currentVersion":1}`,
		"DELETE /api/v1/secrets/sec-9":                  ``,
		"POST /api/v1/managed/access-profiles":          `{"id":"prof-9","name":"new","currentRevision":1}`,
		"DELETE /api/v1/managed/access-profiles/prof-2": ``,
	}

	return newAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if failProfile && key == "POST /api/v1/managed/access-profiles" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"name taken","code":"conflict"}`))

			return
		}
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no route","code":"not_found"}`))

			return
		}
		if body == "" {
			w.WriteHeader(http.StatusNoContent)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = w.Write([]byte(body))
	})
}

func (s *apiServer) saw(method, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			return true
		}
	}

	return false
}

func (s *apiServer) body(method, path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			return string(r.Body)
		}
	}

	return ""
}

func writeKey(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestAccessListAndShow(t *testing.T) {
	server := accessEntriesServer(t, false)
	config := writeConfig(t, server.URL, "tok")

	list := execute(t, nil, "", "--config", config, "access")
	if list.code != 0 || !strings.Contains(list.stdout, "ssh-key") || !strings.Contains(list.stdout, "deploy") ||
		!strings.Contains(list.stdout, "agent") || !strings.Contains(list.stdout, "2") {
		t.Fatalf("list = %+v", list)
	}

	show := execute(t, nil, "", "--config", config, "access", "show", "deploy")
	for _, want := range []string{"ssh with a key", "deploy", "ask me to approve each host key", "web-01, web-02", "never shown"} {
		if !strings.Contains(show.stdout, want) {
			t.Fatalf("show lacks %q: %+v", want, show)
		}
	}
	if strings.Contains(show.stdout, "sec-1") {
		t.Fatalf("show printed the secret reference: %q", show.stdout)
	}
	if missing := execute(t, nil, "", "--config", config, "access", "show", "nope"); missing.code != clierr.ExitNotFound {
		t.Fatalf("show unknown = %+v", missing)
	}
	if doc := execute(t, nil, "", "--config", config, "access", "show", "deploy", "-o", "json"); doc.code != 0 ||
		!strings.Contains(doc.stdout, `"hostNames"`) || strings.Contains(doc.stdout, "sec-1") {
		t.Fatalf("show -o json = %+v", doc)
	}
}

func TestAccessAddKeyNeverPrintsTheKey(t *testing.T) {
	server := accessEntriesServer(t, false)
	config := writeConfig(t, server.URL, "tok")
	key := writeKey(t, testPrivateKey)

	got := execute(t, nil, "", "--config", config, "access", "add", "deploy-key", "--how", "ssh-key", "--user", "deploy", "--key-file", key)
	if got.code != 0 || !strings.Contains(got.stdout, "added") {
		t.Fatalf("add = %+v", got)
	}
	if strings.Contains(got.stdout+got.stderr, "SUPERSECRETKEYBODY") {
		t.Fatalf("the key was printed: %+v", got)
	}
	if !strings.Contains(server.body(http.MethodPost, "/api/v1/secrets"), "SUPERSECRETKEYBODY") {
		t.Fatal("the key did not reach the secret store")
	}
	profile := server.body(http.MethodPost, "/api/v1/managed/access-profiles")
	for _, want := range []string{`"private-key"`, `"approve"`, `"sec-9"`, `"deploy"`} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile body lacks %s: %s", want, profile)
		}
	}
	if strings.Contains(profile, "SUPERSECRETKEYBODY") {
		t.Fatalf("the key is in the profile body: %s", profile)
	}
}

func TestAccessAddPasswordFromStdin(t *testing.T) {
	server := accessEntriesServer(t, false)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "hunter2\n", "--config", config, "access", "add", "lab", "--how", "ssh-password", "--user", "ops",
		"--password-stdin", "--host-key-policy", "pinned")
	profile := server.body(http.MethodPost, "/api/v1/managed/access-profiles")
	if got.code != 0 || !strings.Contains(profile, `"password"`) || !strings.Contains(profile, `"pinned"`) ||
		!strings.Contains(server.body(http.MethodPost, "/api/v1/secrets"), "hunter2") || strings.Contains(got.stdout+got.stderr, "hunter2") {
		t.Fatalf("add = %+v, profile %s", got, profile)
	}
}

func TestAccessAddAgent(t *testing.T) {
	server := accessEntriesServer(t, false)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "", "--config", config, "access", "add", "agents", "--how", "agent")
	if got.code != 0 || !strings.Contains(server.body(http.MethodPost, "/api/v1/managed/access-profiles"), `"agent"`) ||
		server.saw(http.MethodPost, "/api/v1/secrets") {
		t.Fatalf("add agent = %+v", got)
	}
}

func TestAccessAddRefusals(t *testing.T) {
	server := accessEntriesServer(t, false)
	config := writeConfig(t, server.URL, "tok")
	key := writeKey(t, testPrivateKey)
	pub := writeKey(t, "ssh-ed25519 AAAA user@host\n")

	cases := map[string][]string{
		"no terminal and no --how":           {"access", "add", "x"},
		"agent with a user":                  {"access", "add", "x", "--how", "agent", "--user", "root"},
		"tofu with a password":               {"access", "add", "x", "--how", "ssh-password", "--user", "u", "--password-stdin", "--host-key-policy", "tofu"},
		"public key file":                    {"access", "add", "x", "--how", "ssh-key", "--user", "u", "--key-file", pub},
		"key file with a password entry":     {"access", "add", "x", "--how", "ssh-password", "--user", "u", "--key-file", key},
		"unknown --how":                      {"access", "add", "x", "--how", "carrier-pigeon"},
		"ssh without a user and no terminal": {"access", "add", "x", "--how", "ssh-key", "--key-file", key},
		"ssh key without a key file":         {"access", "add", "x", "--how", "ssh-key", "--user", "u"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			got := execute(t, nil, "", append([]string{"--config", config}, args...)...)
			if got.code != clierr.ExitUsage {
				t.Fatalf("exit = %d, want usage: %+v", got.code, got)
			}
		})
	}
	if server.saw(http.MethodPost, "/api/v1/secrets") || server.saw(http.MethodPost, "/api/v1/managed/access-profiles") {
		t.Fatal("a refused add reached the server")
	}
}

func TestAccessAddRetiresTheSecretWhenTheEntryFails(t *testing.T) {
	server := accessEntriesServer(t, true)
	config := writeConfig(t, server.URL, "tok")

	got := execute(t, nil, "pw\n", "--config", config, "access", "add", "lab", "--how", "ssh-password", "--user", "ops", "--password-stdin")
	if got.code == 0 || !server.saw(http.MethodDelete, "/api/v1/secrets/sec-9") {
		t.Fatalf("add = %+v; the stored credential was not retired", got)
	}
}

func TestAccessRemoveNeedsTheTypedName(t *testing.T) {
	server := accessEntriesServer(t, false)
	config := writeConfig(t, server.URL, "tok")

	if got := execute(t, nil, "", "--config", config, "access", "remove", "deploy", "--yes"); got.code != clierr.ExitUsage ||
		server.saw(http.MethodDelete, "/api/v1/managed/access-profiles/prof-2") {
		t.Fatalf("--yes alone = %+v", got)
	}
	got := execute(t, nil, "", "--config", config, "access", "remove", "deploy", "--confirm", "deploy")
	if got.code != 0 || !server.saw(http.MethodDelete, "/api/v1/managed/access-profiles/prof-2") ||
		!strings.Contains(got.stdout, "secret list") {
		t.Fatalf("remove = %+v", got)
	}
}
