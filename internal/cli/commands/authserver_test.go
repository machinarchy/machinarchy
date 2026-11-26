package commands_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/cli"
	"github.com/machinarchy/machinarchy/internal/cli/commands"
	"github.com/machinarchy/machinarchy/internal/cli/config"
	"github.com/machinarchy/machinarchy/internal/cli/cred"
)

const (
	testPassword = "correct horse battery staple"
	testEmail    = "maxim@example.com"
)

// recorded is one request the fake server received.
type recorded struct {
	Method, Path, Auth string
	Body               []byte
}

// authServer is a small fake of the parts of the Machinarchy API the sign-in
// commands use. It enforces what the real one does: a session may do
// everything, an API token (mch_pat_...) may not manage sessions or tokens,
// and a revoked credential stops working.
type authServer struct {
	*httptest.Server

	mu        sync.Mutex
	requests  []recorded
	sessions  map[string]bool // session token -> active
	tokens    map[string]*issued
	nextID    int
	redirect  string // when set, every request is answered with a redirect there
	hostCalls int
}

type issued struct {
	ID      int
	Name    string
	Role    string
	Secret  string
	Expires time.Time
	Revoked bool
}

func newAuthServer(t *testing.T, tls bool) *authServer {
	t.Helper()

	s := &authServer{sessions: map[string]bool{}, tokens: map[string]*issued{}, nextID: 1}
	handler := http.HandlerFunc(s.serve)
	if tls {
		s.Server = httptest.NewTLSServer(handler)
	} else {
		s.Server = httptest.NewServer(handler)
	}
	t.Cleanup(s.Close)

	return s
}

// addToken registers a token the server knows, as if an owner had created it.
func (s *authServer) addToken(secret, name, role string) *issued {
	s.mu.Lock()
	defer s.mu.Unlock()

	token := &issued{ID: s.nextID, Name: name, Role: role, Secret: secret, Expires: time.Now().Add(90 * 24 * time.Hour)}
	s.nextID++
	s.tokens[secret] = token

	return token
}

// addSession registers a login session the server knows.
func (s *authServer) addSession(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[secret] = true
}

func (s *authServer) log() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]recorded(nil), s.requests...)
}

func (s *authServer) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// saw returns the requests with the given method and path.
func (s *authServer) saw(method, path string) []recorded {
	var out []recorded
	for _, r := range s.log() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}

	return out
}

func (s *authServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	auth := r.Header.Get("Authorization")

	s.mu.Lock()
	s.requests = append(s.requests, recorded{Method: r.Method, Path: r.URL.Path, Auth: auth, Body: body})
	redirect := s.redirect
	s.mu.Unlock()

	if redirect != "" {
		http.Redirect(w, r, redirect, http.StatusFound)

		return
	}

	reply := func(status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	secret := strings.TrimPrefix(auth, "Bearer ")

	switch {
	case r.URL.Path == "/healthz/ready":
		reply(200, `{"status":"ok"}`)
	case r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost:
		s.login(w, body, reply)
	case r.URL.Path == "/api/auth/logout" && r.Method == http.MethodPost:
		s.mu.Lock()
		delete(s.sessions, secret)
		s.mu.Unlock()
		reply(200, `{"message":"ok"}`)
	case strings.HasPrefix(r.URL.Path, "/api/v1/"):
		s.v1(w, r, secret, body, reply)
	default:
		reply(404, `{"error":"no route"}`)
	}
}

func (s *authServer) login(w http.ResponseWriter, body []byte, reply func(int, string)) {
	var request map[string]string
	_ = json.Unmarshal(body, &request)
	identifier := request["email"] + request["username"]

	switch {
	case identifier == "throttled":
		w.Header().Set("Retry-After", "40")
		reply(429, `{"error":"too many attempts"}`)
	case (identifier == testEmail || identifier == "maxim") && request["password"] == testPassword:
		s.mu.Lock()
		secret := fmt.Sprintf("sess-%d", len(s.sessions)+1)
		s.sessions[secret] = true
		s.mu.Unlock()
		reply(200, fmt.Sprintf(`{"token":%q,"user":{"id":1,"username":"maxim","email":%q}}`, secret, testEmail))
	default:
		reply(401, `{"error":"Invalid credentials"}`)
	}
}

// identify says who a credential is: "session", "token" or "" for none.
func (s *authServer) identify(secret string) (kind string, token *issued) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessions[secret] {
		return "session", nil
	}
	if t, ok := s.tokens[secret]; ok && !t.Revoked && t.Expires.After(time.Now()) {
		return "token", t
	}

	return "", nil
}

func (s *authServer) v1(_ http.ResponseWriter, r *http.Request, secret string, body []byte, reply func(int, string)) {
	kind, token := s.identify(secret)
	if kind == "" {
		reply(401, `{"error":"invalid or expired authorization token"}`)

		return
	}
	// Only a login session may manage sessions and tokens.
	humanOnly := strings.HasPrefix(r.URL.Path, "/api/v1/sessions") || strings.HasPrefix(r.URL.Path, "/api/v1/tokens") ||
		strings.HasPrefix(r.URL.Path, "/api/v1/secrets") || strings.HasPrefix(r.URL.Path, "/api/v1/runtime-config")
	if humanOnly && kind != "session" {
		reply(403, `{"error":"a user session is required"}`)

		return
	}

	switch {
	case r.URL.Path == "/api/v1/me":
		reply(200, fmt.Sprintf(`{"id":1,"username":"maxim","email":%q}`, testEmail))
	case r.URL.Path == "/api/v1/sessions" && r.Method == http.MethodGet:
		expires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
		reply(200, fmt.Sprintf(`{"items":[{"id":"s1","current":true,"createdAt":%q,"expiresAt":%q}]}`, time.Now().UTC().Format(time.RFC3339), expires))
	case r.URL.Path == "/api/v1/sessions" && r.Method == http.MethodDelete:
		s.mu.Lock()
		n := len(s.sessions)
		s.sessions = map[string]bool{}
		s.mu.Unlock()
		reply(200, fmt.Sprintf(`{"revoked":%d}`, n))
	case r.URL.Path == "/api/v1/tokens" && r.Method == http.MethodPost:
		s.createToken(body, reply)
	case r.URL.Path == "/api/v1/tokens" && r.Method == http.MethodGet:
		s.listTokens(reply)
	case strings.HasPrefix(r.URL.Path, "/api/v1/tokens/") && r.Method == http.MethodDelete:
		s.revokeToken(strings.TrimPrefix(r.URL.Path, "/api/v1/tokens/"), reply)
	case r.URL.Path == "/api/v1/managed/hosts":
		s.mu.Lock()
		s.hostCalls++
		s.mu.Unlock()
		_ = token
		reply(200, `{"items":[]}`)
	default:
		reply(404, `{"error":"no route"}`)
	}
}

func (s *authServer) createToken(body []byte, reply func(int, string)) {
	var request struct {
		Name           string `json:"name"`
		Role           string `json:"role"`
		ExpiresInHours int    `json:"expiresInHours"`
	}
	_ = json.Unmarshal(body, &request)
	if request.Role != "reader" && request.Role != "operator" {
		reply(400, `{"error":"role must be reader or operator"}`)

		return
	}

	s.mu.Lock()
	token := &issued{ID: s.nextID, Name: request.Name, Role: request.Role, Secret: fmt.Sprintf("mch_pat_%s-%d-SECRET", request.Name, s.nextID),
		Expires: time.Now().Add(time.Duration(request.ExpiresInHours) * time.Hour)}
	s.nextID++
	s.tokens[token.Secret] = token
	s.mu.Unlock()

	reply(201, fmt.Sprintf(`{"id":%d,"name":%q,"role":%q,"createdAt":%q,"expiresAt":%q,"token":%q}`,
		token.ID, token.Name, token.Role, time.Now().UTC().Format(time.RFC3339), token.Expires.UTC().Format(time.RFC3339), token.Secret))
}

func (s *authServer) listTokens(reply func(int, string)) {
	s.mu.Lock()
	var items []string
	for _, t := range s.tokens {
		revoked := ""
		if t.Revoked {
			revoked = fmt.Sprintf(`,"revokedAt":%q`, time.Now().UTC().Format(time.RFC3339))
		}
		items = append(items, fmt.Sprintf(`{"id":%d,"name":%q,"role":%q,"createdAt":%q,"expiresAt":%q%s}`,
			t.ID, t.Name, t.Role, time.Now().UTC().Format(time.RFC3339), t.Expires.UTC().Format(time.RFC3339), revoked))
	}
	s.mu.Unlock()

	reply(200, "["+strings.Join(items, ",")+"]")
}

func (s *authServer) revokeToken(id string, reply func(int, string)) {
	n, _ := strconv.Atoi(id)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens {
		if t.ID == n {
			t.Revoked = true
			reply(204, "")

			return
		}
	}
	reply(404, `{"error":"not found"}`)
}

// ---- running the CLI against it ---------------------------------------------

type authRun struct {
	stdin    string
	terminal bool
	secrets  []string // answers to hidden prompts, in order
	env      map[string]string
	config   string
	now      time.Time
}

type authResult struct {
	result
	secretsAsked []string
}

// configDir returns a fresh config path inside a temporary directory.
func configPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "config.yaml")
}

// runAuth runs the CLI the way main does, with an injected terminal.
func runAuth(t *testing.T, o authRun, args ...string) authResult {
	t.Helper()
	isolate(t)
	for name, value := range o.env {
		t.Setenv(name, value)
	}

	var out, errOut bytes.Buffer
	var asked []string
	secrets := append([]string(nil), o.secrets...)

	options := cli.Options{
		Streams:    cli.IOStreams{In: strings.NewReader(o.stdin), Out: &out, ErrOut: &errOut},
		Groups:     commands.DefaultGroups(),
		IsTerminal: func(io.Reader) bool { return o.terminal },
		ReadSecret: func(prompt string) (string, error) {
			asked = append(asked, prompt)
			if len(secrets) == 0 {
				return "", fmt.Errorf("unexpected hidden prompt %q", prompt)
			}
			answer := secrets[0]
			secrets = secrets[1:]

			return answer, nil
		},
		Hostname: func() (string, error) { return "Maxims-MacBook.local", nil },
	}
	if !o.now.IsZero() {
		options.Now = func() time.Time { return o.now }
	}

	path := o.config
	if path == "" {
		path = configPath(t)
	}
	code := cli.Run(context.Background(), options, append([]string{"--config", path}, args...))

	return authResult{result: result{code: code, stdout: out.String(), stderr: errOut.String()}, secretsAsked: asked}
}

// savedCredentials reads what is saved for a server straight from the file.
func savedCredentials(t *testing.T, configFile, server string) cred.Entry {
	t.Helper()

	entry, err := cred.NewFile(config.CredentialsPath(configFile)).Get(server)
	if err != nil {
		t.Fatalf("read saved credentials: %v", err)
	}

	return entry
}

// seed writes a config with one saved server and the given credentials.
func seed(t *testing.T, server string, entries ...cred.Credential) string {
	t.Helper()

	path := configPath(t)
	if err := os.WriteFile(path, []byte(fmt.Sprintf("version: 2\nactive: lab\nservers:\n  lab:\n    url: %s\n", server)), 0o600); err != nil {
		t.Fatal(err)
	}
	store := cred.NewFile(config.CredentialsPath(path))
	for _, c := range entries {
		if err := store.Put("lab", c); err != nil {
			t.Fatal(err)
		}
	}

	return path
}

// allText is every byte a run printed or saved, for secret scans.
func allText(t *testing.T, configFile string, results ...authResult) string {
	t.Helper()

	var b strings.Builder
	for _, r := range results {
		b.WriteString(r.stdout)
		b.WriteString(r.stderr)
	}
	for _, path := range []string{configFile} {
		data, err := os.ReadFile(path) // #nosec G304 -- test path
		if err == nil {
			b.Write(data)
		}
	}

	return b.String()
}

// writeServerCA writes the certificate of a TLS test server as a PEM file.
func writeServerCA(t *testing.T, server *authServer) string {
	t.Helper()

	cert := server.Certificate()
	block := "-----BEGIN CERTIFICATE-----\n" + base64Lines(cert.Raw) + "-----END CERTIFICATE-----\n"
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func base64Lines(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(encoded) > 64 {
		b.WriteString(encoded[:64] + "\n")
		encoded = encoded[64:]
	}
	b.WriteString(encoded + "\n")

	return b.String()
}
