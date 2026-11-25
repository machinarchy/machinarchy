package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

func serverCA(t *testing.T, server *httptest.Server) string {
	t.Helper()

	encoded := base64.StdEncoding.EncodeToString(server.Certificate().Raw)
	var b strings.Builder
	b.WriteString("-----BEGIN CERTIFICATE-----\n")
	for len(encoded) > 64 {
		b.WriteString(encoded[:64] + "\n")
		encoded = encoded[64:]
	}
	b.WriteString(encoded + "\n-----END CERTIFICATE-----\n")

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestPing(t *testing.T) {
	t.Parallel()

	t.Run("any HTTP answer means the server was reached", func(t *testing.T) {
		t.Parallel()
		for _, status := range []int{200, 404, 503} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			if err := Ping(context.Background(), Endpoint{URL: server.URL}); err != nil {
				t.Errorf("status %d: %v", status, err)
			}
			server.Close()
		}
	})

	t.Run("a closed port is unavailable, with the address and a next step", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.NotFoundHandler())
		address := server.URL
		server.Close()

		err := Ping(context.Background(), Endpoint{URL: address})
		if err == nil || !strings.Contains(err.Error(), "cannot reach "+address) || clierr.Hint(err) == "" {
			t.Fatalf("err = %v", err)
		}
		if code, _ := clierr.Classify(err); code != clierr.ExitUnavailable {
			t.Fatalf("exit code = %d", code)
		}
	})

	t.Run("a certificate nobody trusts names --ca-file", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewTLSServer(http.NotFoundHandler())
		defer server.Close()

		err := Ping(context.Background(), Endpoint{URL: server.URL})
		if err == nil || !strings.Contains(err.Error(), "is not trusted (certificate signed by unknown authority)") ||
			!strings.Contains(clierr.Hint(err), "--ca-file ca.pem") {
			t.Fatalf("err = %v / %q", err, clierr.Hint(err))
		}

		if err := Ping(context.Background(), Endpoint{URL: server.URL, CAFile: serverCA(t, server)}); err != nil {
			t.Fatalf("the server's own CA must be trusted: %v", err)
		}
	})

	t.Run("a slow server times out", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		defer server.Close()
		defer close(release)

		err := Ping(context.Background(), Endpoint{URL: server.URL, Timeout: 50 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestEndpointCAFileErrors(t *testing.T) {
	t.Parallel()

	if err := Ping(context.Background(), Endpoint{URL: "https://x.example", CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil || !strings.Contains(err.Error(), "read the CA file") {
		t.Fatalf("a missing CA file: %v", err)
	}
	notPEM := filepath.Join(t.TempDir(), "x.pem")
	if err := os.WriteFile(notPEM, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ping(context.Background(), Endpoint{URL: "https://x.example", CAFile: notPEM}); err == nil || !strings.Contains(err.Error(), "holds no PEM certificate") {
		t.Fatalf("a CA file without a certificate: %v", err)
	}
}

func loginServer(t *testing.T, handler http.HandlerFunc) Endpoint {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return Endpoint{URL: server.URL}
}

func TestLogin(t *testing.T) {
	t.Parallel()

	t.Run("an email goes as email, a name as username, the password only in the body", func(t *testing.T) {
		t.Parallel()
		var bodies []map[string]string
		endpoint := loginServer(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies = append(bodies, body)
			if r.Header.Get("Authorization") != "" || strings.Contains(r.URL.String(), "pw") {
				t.Errorf("the password or a credential leaked into the request: %q %q", r.URL, r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"token":"sess-1","user":{"id":4,"username":"maxim","email":"maxim@example.com"}}`))
		})

		result, err := Login(context.Background(), endpoint, "maxim@example.com", "pw")
		if err != nil || result.Token != "sess-1" || result.User.Label() != "maxim" || result.User.ID != 4 {
			t.Fatalf("result = %+v, %v", result, err)
		}
		if _, err := Login(context.Background(), endpoint, "maxim", "pw"); err != nil {
			t.Fatal(err)
		}
		if bodies[0]["email"] != "maxim@example.com" || bodies[0]["username"] != "" || bodies[1]["username"] != "maxim" || bodies[1]["email"] != "" || bodies[0]["password"] != "pw" {
			t.Fatalf("bodies = %v", bodies)
		}
	})

	t.Run("a wrong password is a 401 the caller can classify", func(t *testing.T) {
		t.Parallel()
		endpoint := loginServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"Invalid credentials"}`))
		})
		_, err := Login(context.Background(), endpoint, "x", "pw")
		var apiErr *Error
		if !asError(err, &apiErr) || apiErr.Status != 401 || strings.Contains(err.Error(), "pw") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an answer that is not Machinarchy's", func(t *testing.T) {
		t.Parallel()
		for name, body := range map[string]string{"html": "<html>hello</html>", "no token": `{"user":{}}`, "empty": ``} {
			endpoint := loginServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			_, err := Login(context.Background(), endpoint, "x", "pw")
			if err == nil || !strings.Contains(err.Error(), "that address did not answer like a Machinarchy server") {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})

	t.Run("redirects are not followed", func(t *testing.T) {
		t.Parallel()
		var followed bool
		elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
		defer elsewhere.Close()
		endpoint := loginServer(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, elsewhere.URL, http.StatusFound) })

		if _, err := Login(context.Background(), endpoint, "x", "pw"); err == nil || followed {
			t.Fatalf("err = %v, followed = %v", err, followed)
		}
	})

	t.Run("an unreachable server", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.NotFoundHandler())
		address := server.URL
		server.Close()
		if _, err := Login(context.Background(), Endpoint{URL: address}, "x", "pw"); err == nil || !strings.Contains(err.Error(), "cannot reach") {
			t.Fatalf("err = %v", err)
		}
	})
}

func asError(err error, target **Error) bool {
	for err != nil {
		if apiErr, ok := err.(*Error); ok { //nolint:errorlint // walking by hand keeps the helper dependency-free
			*target = apiErr

			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error }) //nolint:errorlint
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}

	return false
}

func TestUserLabel(t *testing.T) {
	t.Parallel()

	if (User{Username: "maxim", Email: "m@x"}).Label() != "maxim" || (User{Email: "m@x"}).Label() != "m@x" || (User{}).Label() != "" {
		t.Fatal("Label is wrong")
	}
}

func TestAuthClientMethods(t *testing.T) {
	t.Parallel()

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/me":
			_, _ = w.Write([]byte(`{"id":1,"username":"maxim","email":"m@x"}`))
		case "GET /api/v1/sessions":
			_, _ = w.Write([]byte(`{"items":[{"id":"a","current":false,"expiresAt":"2030-01-02T03:04:05Z"},{"id":"b","current":true,"expiresAt":"2030-01-03T03:04:05Z"}]}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	client, err := NewHTTPClient(config.Target{Server: server.URL, Token: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if me, err := client.Me(ctx); err != nil || me.Label() != "maxim" {
		t.Fatalf("Me = %+v, %v", me, err)
	}
	sessions, err := client.Sessions(ctx)
	if err != nil || len(sessions) != 2 || sessions[0].Current || !sessions[1].Current || sessions[1].ExpiresAt.Year() != 2030 {
		t.Fatalf("Sessions = %+v, %v", sessions, err)
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.RevokeSessions(ctx); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"GET /api/v1/me Bearer sess-1", "GET /api/v1/sessions Bearer sess-1",
		"POST /api/auth/logout Bearer sess-1", "DELETE /api/v1/sessions Bearer sess-1",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func TestHTTPClientTrustsAConfiguredCA(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"id":1}`)) }))
	defer server.Close()

	plain, err := NewHTTPClient(config.Target{Server: server.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Me(context.Background()); err == nil {
		t.Fatal("a certificate nobody trusts was accepted")
	}

	trusted, err := NewHTTPClient(config.Target{Server: server.URL, Token: "t", CAFile: serverCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.Me(context.Background()); err != nil {
		t.Fatalf("the configured CA must be trusted: %v", err)
	}

	if _, err := NewHTTPClient(config.Target{Server: server.URL, Token: "t", CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
}

// An issued token must marshal as YAML: its embedded metadata is inlined, so
// the "token" key is not defined twice.
func TestIssuedTokensMarshalAsYAML(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{
		"API token":       IssuedToken{Token: Token{ID: 7, Name: "ci", Role: "operator"}, Secret: "mch_pat_x"},
		"enrolment token": IssuedEnrollmentToken{EnrollmentToken: EnrollmentToken{ID: "e1"}, Token: "mae_x"},
	} {
		data, err := yaml.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Count(string(data), "token:") != 1 || !strings.Contains(string(data), "id:") {
			t.Fatalf("%s:\n%s", name, data)
		}
	}
}
