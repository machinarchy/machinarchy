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
	members   []fakeMember // nil until a test calls seedMembers
	invites   []fakeInvite
	noMail    bool // /members/invitations POST answers 501
	// inviteTokens maps a valid invitation token to its role; accounts maps a
	// username created by accepting one to its password.
	inviteTokens map[string]string
	accounts     map[string]string
}

type fakeInvite struct {
	ID          int
	Email, Role string
}

type fakeMember struct {
	ID       int
	Username string
	Role     string
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
	case r.URL.Path == "/api/auth/invitations/accept" && r.Method == http.MethodPost:
		s.acceptInvitation(body, reply)
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
	case s.accountPassword(identifier) != "" && s.accountPassword(identifier) == request["password"]:
		s.mu.Lock()
		secret := fmt.Sprintf("sess-%d", len(s.sessions)+1)
		s.sessions[secret] = true
		s.mu.Unlock()
		reply(200, fmt.Sprintf(`{"token":%q,"user":{"id":7,"username":%q}}`, secret, identifier))
	default:
		reply(401, `{"error":"Invalid credentials"}`)
	}
}

func (s *authServer) accountPassword(username string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.accounts[username]
}

// addInviteToken makes token a valid, unused invitation for role.
func (s *authServer) addInviteToken(token, role string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inviteTokens == nil {
		s.inviteTokens = map[string]string{}
	}
	s.inviteTokens[token] = role
}

// acceptInvitation fakes the public accept route: an unknown or used token is
// 404, a taken username 409 (the token stays valid), otherwise the account is
// created and the token is spent.
func (s *authServer) acceptInvitation(body []byte, reply func(int, string)) {
	var request struct{ Token, Username, Password string }
	_ = json.Unmarshal(body, &request)

	s.mu.Lock()
	defer s.mu.Unlock()
	role, ok := s.inviteTokens[request.Token]
	if !ok {
		reply(404, `{"error":"invitation is invalid or expired"}`)

		return
	}
	if _, taken := s.accounts[request.Username]; taken || request.Username == "maxim" {
		reply(409, `{"error":"username or email is already registered"}`)

		return
	}
	if len(request.Password) < 12 {
		reply(400, `{"error":"invalid input: password must be between 12 and 72 bytes"}`)

		return
	}
	if s.accounts == nil {
		s.accounts = map[string]string{}
	}
	s.accounts[request.Username] = request.Password
	delete(s.inviteTokens, request.Token)
	reply(201, fmt.Sprintf(`{"id":7,"username":%q,"role":%q}`, request.Username, role))
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
		strings.HasPrefix(r.URL.Path, "/api/v1/secrets") || strings.HasPrefix(r.URL.Path, "/api/v1/members") || strings.HasPrefix(r.URL.Path, "/api/v1/runtime-config")
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
	case strings.HasPrefix(r.URL.Path, "/api/v1/members"):
		s.membersRoute(r, body, reply)
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

// seedMembers gives the server a workspace with these members.
func (s *authServer) seedMembers(members ...fakeMember) {
	s.mu.Lock()
	s.members = members
	s.mu.Unlock()
}

func (s *authServer) memberList() []fakeMember {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]fakeMember(nil), s.members...)
}

// membersRoute fakes /api/v1/members: list, role change and removal, keeping at
// least one owner like the real server.
func (s *authServer) membersRoute(r *http.Request, body []byte, reply func(int, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owners := 0
	for _, m := range s.members {
		if m.Role == "owner" {
			owners++
		}
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v1/members"), "/")
	if rest == "invitations" || strings.HasPrefix(rest, "invitations/") {
		s.invitationsRoute(r, body, strings.TrimPrefix(strings.TrimPrefix(rest, "invitations"), "/"), reply)

		return
	}
	if rest == "" && r.Method == http.MethodPost {
		var request struct{ Username, Email, Role string }
		_ = json.Unmarshal(body, &request)
		for _, m := range s.members {
			if m.Username == request.Username {
				reply(409, `{"error":"username or email is already registered"}`)

				return
			}
		}
		id := 100 + len(s.members)
		s.members = append(s.members, fakeMember{ID: id, Username: request.Username, Role: request.Role})
		token := fmt.Sprintf("added-token-%d", id)
		if s.inviteTokens == nil {
			s.inviteTokens = map[string]string{}
		}
		s.inviteTokens[token] = request.Role
		reply(201, fmt.Sprintf(`{"id":%d,"username":%q,"role":%q,"token":%q,"expiresAt":%q}`, id, request.Username, request.Role, token, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)))

		return
	}
	if rest == "" && r.Method == http.MethodGet {
		var items []string
		for _, m := range s.members {
			items = append(items, fmt.Sprintf(`{"id":%d,"username":%q,"role":%q,"createdAt":%q}`, m.ID, m.Username, m.Role, time.Now().UTC().Format(time.RFC3339)))
		}
		reply(200, "["+strings.Join(items, ",")+"]")

		return
	}
	idText, action, _ := strings.Cut(rest, "/")
	id, _ := strconv.Atoi(idText)
	for i, m := range s.members {
		if m.ID != id {
			continue
		}
		switch {
		case action == "role" && r.Method == http.MethodPut:
			var request struct{ Role string }
			_ = json.Unmarshal(body, &request)
			if m.Role == "owner" && request.Role != "owner" && owners < 2 {
				reply(409, `{"error":"a workspace must keep at least one owner"}`)

				return
			}
			s.members[i].Role = request.Role
			reply(200, fmt.Sprintf(`{"id":%d,"username":%q,"role":%q,"createdAt":%q}`, m.ID, m.Username, request.Role, time.Now().UTC().Format(time.RFC3339)))
		case action == "link" && r.Method == http.MethodPost:
			if m.Role == "owner" || s.accounts[m.Username] != "" || m.ID < 100 {
				reply(409, `{"error":"the account is already active; a new link is only for accounts that have not been activated"}`)

				return
			}
			token := fmt.Sprintf("relinked-token-%d", m.ID)
			s.inviteTokens[token] = m.Role
			reply(200, fmt.Sprintf(`{"id":%d,"token":%q,"expiresAt":%q}`, m.ID, token, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)))
		case action == "" && r.Method == http.MethodDelete:
			if m.Role == "owner" && owners < 2 {
				reply(409, `{"error":"a workspace must keep at least one owner"}`)

				return
			}
			s.members = append(s.members[:i], s.members[i+1:]...)
			reply(204, "")
		default:
			reply(404, `{"error":"no route"}`)
		}

		return
	}
	reply(404, `{"error":"member not found"}`)
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

// invitationsRoute fakes /api/v1/members/invitations (the caller holds s.mu).
func (s *authServer) invitationsRoute(r *http.Request, body []byte, rest string, reply func(int, string)) {
	expires := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	switch {
	case rest == "" && r.Method == http.MethodPost:
		var request struct{ Email, Role string }
		_ = json.Unmarshal(body, &request)
		if s.noMail {
			reply(501, `{"error":"email delivery is not configured on this server"}`)

			return
		}
		s.invites = append(s.invites, fakeInvite{ID: len(s.invites) + 1, Email: request.Email, Role: request.Role})
		reply(202, fmt.Sprintf(`{"email":%q,"role":%q,"expiresAt":%q}`, request.Email, request.Role, expires))
	case rest == "" && r.Method == http.MethodGet:
		var items []string
		for _, i := range s.invites {
			items = append(items, fmt.Sprintf(`{"id":%d,"email":%q,"role":%q,"invitedBy":"maxim","createdAt":%q,"expiresAt":%q}`, i.ID, i.Email, i.Role, time.Now().UTC().Format(time.RFC3339), expires))
		}
		reply(200, "["+strings.Join(items, ",")+"]")
	case r.Method == http.MethodDelete:
		id, _ := strconv.Atoi(rest)
		for i, inv := range s.invites {
			if inv.ID == id {
				s.invites = append(s.invites[:i], s.invites[i+1:]...)
				reply(204, "")

				return
			}
		}
		reply(404, `{"error":"invitation not found"}`)
	default:
		reply(404, `{"error":"no route"}`)
	}
}

func (s *authServer) inviteList() []fakeInvite {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]fakeInvite(nil), s.invites...)
}
