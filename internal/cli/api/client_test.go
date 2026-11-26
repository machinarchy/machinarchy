package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/machinarchy/machinarchy/internal/cli/clierr"
	"github.com/machinarchy/machinarchy/internal/cli/config"
)

const testToken = "tok-0123456789"

type recordedRequest struct {
	Method        string
	Path          string
	RequestURI    string
	RawQuery      string
	Authorization string
	Accept        string
	UserAgent     string
	ContentType   string
	Body          []byte
}

type recorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *recorder) record(request *http.Request) {
	body, _ := io.ReadAll(request.Body)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.requests = append(r.requests, recordedRequest{
		Method:        request.Method,
		Path:          request.URL.Path,
		RequestURI:    request.RequestURI,
		RawQuery:      request.URL.RawQuery,
		Authorization: request.Header.Get("Authorization"),
		Accept:        request.Header.Get("Accept"),
		UserAgent:     request.Header.Get("User-Agent"),
		ContentType:   request.Header.Get("Content-Type"),
		Body:          body,
	})
}

func (r *recorder) all() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]recordedRequest(nil), r.requests...)
}

func (r *recorder) last(t *testing.T) recordedRequest {
	t.Helper()

	all := r.all()
	if len(all) == 0 {
		t.Fatal("the server received no request")
	}

	return all[len(all)-1]
}

func respond(rec *recorder, status int, body string, headers map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		for key, value := range headers {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func newClient(t *testing.T, handler http.Handler, opts ...Option) *HTTPClient {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewHTTPClient(config.Target{Server: server.URL, Token: testToken, RequestTimeout: 5 * time.Second}, opts...)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	return client
}

func asAPIError(t *testing.T, err error) *Error {
	t.Helper()

	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T (%v) is not an *Error", err, err)
	}

	return apiErr
}

func TestEveryRequestCarriesAuthenticationAndStandardHeaders(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/environments":
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))

	calls := []struct {
		name string
		call func() error
	}{
		{"ListHosts", func() error { _, err := client.ListHosts(ctx, HostQuery{}); return err }},
		{"GetHost", func() error { _, err := client.GetHost(ctx, "h1"); return err }},
		{"DeleteHost", func() error { return client.DeleteHost(ctx, "h1", 2) }},
		{"SetHostLabels", func() error { _, err := client.SetHostLabels(ctx, "h1", 2, nil); return err }},
		{"ListAccessProfiles", func() error { _, err := client.ListAccessProfiles(ctx, ""); return err }},
		{"PreviewTargets", func() error { _, err := client.PreviewTargets(ctx, TargetRule{All: true}); return err }},
		{"SubmitOperation", func() error { _, err := client.SubmitOperation(ctx, OperationInput{}); return err }},
		{"GetOperation", func() error { _, err := client.GetOperation(ctx, "o1"); return err }},
		{"ListOperations", func() error { _, err := client.ListOperations(ctx, OperationQuery{}); return err }},
		{"CancelOperation", func() error { _, err := client.CancelOperation(ctx, "o1"); return err }},
		{"TargetOutput", func() error { _, err := client.TargetOutput(ctx, "o1", "h1", "stderr", 42); return err }},
		{"ListEnvironments", func() error { _, err := client.ListEnvironments(ctx); return err }},
		{"RuntimeConfig", func() error { _, err := client.RuntimeConfig(ctx); return err }},
		{"ConfigHistory", func() error { _, err := client.ConfigHistory(ctx, 0); return err }},
		{"ConfigStatus", func() error { _, err := client.ConfigStatus(ctx); return err }},
		{"ValidateRuntimeConfig", func() error {
			_, err := client.ValidateRuntimeConfig(ctx, json.RawMessage(`{}`))
			return err
		}},
		{"PublishRuntimeConfig", func() error {
			_, err := client.PublishRuntimeConfig(ctx, 1, json.RawMessage(`{}`), "")
			return err
		}},
		{"RollbackRuntimeConfig", func() error { _, err := client.RollbackRuntimeConfig(ctx, 2, 1, ""); return err }},
		{"ListSecrets", func() error { _, err := client.ListSecrets(ctx); return err }},
		{"CreateSecret", func() error { _, err := client.CreateSecret(ctx, json.RawMessage(`{"name":"n"}`)); return err }},
		{"SecretVersions", func() error { _, err := client.SecretVersions(ctx, "s1"); return err }},
		{"RotateSecret", func() error { _, err := client.RotateSecret(ctx, "s1", 1, "v"); return err }},
		{"RewrapSecrets", func() error { _, err := client.RewrapSecrets(ctx, "k1", 10); return err }},
		{"DeleteSecret", func() error { return client.DeleteSecret(ctx, "s1") }},
	}

	for _, test := range calls {
		if err := test.call(); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
	}

	requests := rec.all()
	if len(requests) != len(calls) {
		t.Fatalf("server saw %d requests, want %d", len(requests), len(calls))
	}
	for index, request := range requests {
		name := calls[index].name
		if request.Authorization != "Bearer "+testToken {
			t.Errorf("%s: Authorization = %q", name, request.Authorization)
		}
		if request.Accept != "application/json" {
			t.Errorf("%s: Accept = %q", name, request.Accept)
		}
		if !strings.HasPrefix(request.UserAgent, "machinarchy-cli/") {
			t.Errorf("%s: User-Agent = %q", name, request.UserAgent)
		}
		hasBody := len(request.Body) > 0
		if hasBody && request.ContentType != "application/json" {
			t.Errorf("%s: Content-Type = %q for a request with a body", name, request.ContentType)
		}
		if !hasBody && request.ContentType != "" {
			t.Errorf("%s: Content-Type = %q for a request without a body", name, request.ContentType)
		}
	}
}

func TestRequestMethodsPathsAndBodies(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	client := newClient(t, respond(rec, http.StatusOK, `{}`, nil))

	tests := []struct {
		name       string
		call       func() error
		wantMethod string
		wantURI    string
		wantBody   map[string]any
	}{
		{
			name: "publish",
			call: func() error {
				_, err := client.PublishRuntimeConfig(ctx, 3, json.RawMessage(`{"a":1}`), "why")
				return err
			},
			wantMethod: http.MethodPut,
			wantURI:    "/api/v1/runtime-config",
			wantBody:   map[string]any{"expectedRevision": float64(3), "config": map[string]any{"a": float64(1)}, "changeNote": "why"},
		},
		{
			name:       "validate",
			call:       func() error { _, err := client.ValidateRuntimeConfig(ctx, json.RawMessage(`{"a":1}`)); return err },
			wantMethod: http.MethodPost,
			wantURI:    "/api/v1/runtime-config/validate",
			wantBody:   map[string]any{"config": map[string]any{"a": float64(1)}},
		},
		{
			name:       "rollback",
			call:       func() error { _, err := client.RollbackRuntimeConfig(ctx, 5, 2, "undo"); return err },
			wantMethod: http.MethodPost,
			wantURI:    "/api/v1/runtime-config/rollback",
			wantBody:   map[string]any{"expectedRevision": float64(5), "targetRevision": float64(2), "changeNote": "undo"},
		},
		{
			name: "create secret passes the document through",
			call: func() error {
				_, err := client.CreateSecret(ctx, json.RawMessage(`{"name":"n","value":"v"}`))
				return err
			},
			wantMethod: http.MethodPost,
			wantURI:    "/api/v1/secrets",
			wantBody:   map[string]any{"name": "n", "value": "v"},
		},
		{
			name:       "rotate",
			call:       func() error { _, err := client.RotateSecret(ctx, "abc", 4, "new"); return err },
			wantMethod: http.MethodPost,
			wantURI:    "/api/v1/secrets/abc/rotate",
			wantBody:   map[string]any{"expectedVersion": float64(4), "value": "new"},
		},
		{
			name:       "rewrap",
			call:       func() error { _, err := client.RewrapSecrets(ctx, "k1", 25); return err },
			wantMethod: http.MethodPost,
			wantURI:    "/api/v1/secrets/rewrap",
			wantBody:   map[string]any{"oldKeyId": "k1", "batchSize": float64(25)},
		},
		{
			name:       "versions",
			call:       func() error { _, err := client.SecretVersions(ctx, "abc"); return err },
			wantMethod: http.MethodGet,
			wantURI:    "/api/v1/secrets/abc/versions",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err != nil {
				t.Fatalf("call: %v", err)
			}

			request := rec.last(t)
			if request.Method != test.wantMethod {
				t.Errorf("method = %s, want %s", request.Method, test.wantMethod)
			}
			if request.RequestURI != test.wantURI {
				t.Errorf("URI = %s, want %s", request.RequestURI, test.wantURI)
			}
			if test.wantBody == nil {
				if len(request.Body) != 0 {
					t.Errorf("unexpected body %q", request.Body)
				}
				return
			}

			var got map[string]any
			if err := json.Unmarshal(request.Body, &got); err != nil {
				t.Fatalf("request body %q is not JSON: %v", request.Body, err)
			}
			wantJSON, _ := json.Marshal(test.wantBody)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Errorf("body = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestConfigHistoryQuery(t *testing.T) {
	rec := &recorder{}
	client := newClient(t, respond(rec, http.StatusOK, `[]`, nil))

	if _, err := client.ConfigHistory(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t).RawQuery; got != "" {
		t.Errorf("query for before=0 is %q, want empty", got)
	}

	if _, err := client.ConfigHistory(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t).RawQuery; got != "before=7" {
		t.Errorf("query = %q, want before=7", got)
	}
}

func TestManagedHostsDecodeAndQuery(t *testing.T) {
	body := `{"items":[{"metadata":{"id":"0f3c","name":"web-01","environmentId":2,"labels":{"role":"web"},"revision":3},` +
		`"spec":{"accessProfileId":"p1","accessTarget":{"endpoint":"10.0.0.7:22"}},"status":{"phase":"Active"}}],"nextCursor":"0f3c"}`
	rec := &recorder{}
	client := newClient(t, respond(rec, http.StatusOK, body, nil))

	page, err := client.ListHosts(context.Background(), HostQuery{Selector: "role=web", EnvironmentID: 2, Cursor: "a b"})
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor != "0f3c" {
		t.Fatalf("page = %+v", page)
	}
	host := page.Items[0]
	if host.Metadata.Name != "web-01" || host.Metadata.Revision != 3 || host.Spec.AccessTarget.Endpoint != "10.0.0.7:22" || host.Status.Phase != "Active" {
		t.Fatalf("host = %+v", host)
	}
	got := rec.last(t)
	if got.Path != "/api/v1/managed/hosts" || got.RawQuery != "cursor=a+b&environment=2&selector=role%3Dweb" {
		t.Fatalf("request = %s?%s", got.Path, got.RawQuery)
	}
}

func TestAPIErrorsAreTypedClassifiedAndHelpful(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		headers      map[string]string
		wantExit     int
		wantMessage  string
		wantHintPart string
	}{
		{"unauthorized", 401, `{"error":"invalid token"}`, nil, clierr.ExitAuth, "invalid token", "machinarchy login"},
		{"forbidden", 403, `{"error":"owner role required"}`, nil, clierr.ExitAuth, "owner role required", "human owner session"},
		{"not found via framework message", 404, `{"message":"Not Found"}`, nil, clierr.ExitNotFound, "Not Found", "Check the identifier"},
		{"conflict", 409, `{"error":"stale revision"}`, nil, clierr.ExitConflict, "stale revision", "up-to-date revision"},
		{"precondition failed", 412, `{"error":"untrusted host key"}`, nil, clierr.ExitConflict, "untrusted host key", ""},
		{"bad request", 400, `{"error":"name is required"}`, nil, clierr.ExitUsage, "name is required", ""},
		{"unprocessable", 422, `{"error":"invalid selector"}`, nil, clierr.ExitUsage, "invalid selector", ""},
		{"rate limited", 429, `{"error":"slow down"}`, map[string]string{"Retry-After": "7"}, clierr.ExitUnavailable, "slow down", "7s"},
		{"unavailable", 503, `{"error":"draining"}`, map[string]string{"Retry-After": "3"}, clierr.ExitUnavailable, "draining", "3s"},
		{"internal error with html body", 500, `<html>oops</html>`, nil, clierr.ExitUnavailable, "Internal Server Error", "internal error"},
		{"problem details", 400, `{"title":"Bad Request","detail":"selector is malformed"}`, nil, clierr.ExitUsage, "selector is malformed", ""},
		{"empty body", 502, ``, nil, clierr.ExitUnavailable, "Bad Gateway", ""},
		{"name taken", 409, `{"error":"resource already exists","code":"already_exists"}`, nil, clierr.ExitConflict, "resource already exists", "already exists. Choose another name"},
		{"execution policy", 403, `{"error":"command execution is not permitted: program \"rm\" is not on the allowed list","code":"execution_not_allowed"}`, nil,
			clierr.ExitAuth, `command execution is not permitted: program "rm" is not on the allowed list`, "allowedPrograms"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, respond(&recorder{}, test.status, test.body, test.headers))

			_, err := client.ListEnvironments(context.Background())
			if err == nil {
				t.Fatal("ListHosts succeeded for an error status")
			}

			apiErr := asAPIError(t, err)
			if apiErr.Status != test.status {
				t.Errorf("status = %d, want %d", apiErr.Status, test.status)
			}
			if apiErr.Message != test.wantMessage {
				t.Errorf("message = %q, want %q", apiErr.Message, test.wantMessage)
			}
			if got := clierr.ExitCode(err); got != test.wantExit {
				t.Errorf("exit code = %d, want %d", got, test.wantExit)
			}
			if !strings.HasPrefix(err.Error(), "API returned HTTP ") {
				t.Errorf("error text = %q", err.Error())
			}
			if apiErr.Method != http.MethodGet || apiErr.Path != "/api/v1/environments" {
				t.Errorf("request = %s %s", apiErr.Method, apiErr.Path)
			}
			hint := strings.TrimSpace(clierr.Why(err) + " " + clierr.Hint(err))
			if test.wantHintPart != "" && !strings.Contains(hint, test.wantHintPart) {
				t.Errorf("hint = %q, want it to contain %q", hint, test.wantHintPart)
			}
			if test.wantHintPart == "" && (test.status == 400 || test.status == 422) && hint != "" {
				t.Errorf("validation errors should speak for themselves, got hint %q", hint)
			}
			if strings.Contains(err.Error(), testToken) || strings.Contains(hint, testToken) {
				t.Error("the credential leaked into the error")
			}
		})
	}
}

func TestAPIErrorCarriesRequestIDRetryAfterAndFields(t *testing.T) {
	client := newClient(t, respond(&recorder{}, http.StatusPreconditionFailed,
		`{"error":"untrusted host key","fingerprint":"SHA256:abc","algorithm":"ssh-ed25519","count":3}`,
		map[string]string{"X-Request-Id": "req-42", "Retry-After": "90000000"}))

	_, err := client.ListEnvironments(context.Background())
	apiErr := asAPIError(t, err)

	if apiErr.RequestID != "req-42" || !strings.Contains(err.Error(), "(request ID req-42)") {
		t.Errorf("request ID not surfaced: %q / %q", apiErr.RequestID, err.Error())
	}
	if apiErr.Field("fingerprint") != "SHA256:abc" || apiErr.Field("algorithm") != "ssh-ed25519" {
		t.Errorf("fields = %v", apiErr.Fields)
	}
	if _, present := apiErr.Fields["count"]; present {
		t.Error("non-string fields must not be copied")
	}
	if apiErr.RetryAfter != maxRetryAfter {
		t.Errorf("RetryAfter = %s, want it capped at %s", apiErr.RetryAfter, maxRetryAfter)
	}
}

func TestServerMessagesAreSanitised(t *testing.T) {
	body := `{"error":"first\u001b[31m red\nsecond\u0007"}`
	client := newClient(t, respond(&recorder{}, http.StatusBadRequest, body, nil))

	_, err := client.ListEnvironments(context.Background())
	apiErr := asAPIError(t, err)

	for _, r := range apiErr.Message {
		if unicode.IsControl(r) {
			t.Fatalf("message %q contains control character %U", apiErr.Message, r)
		}
	}

	long := `{"error":"` + strings.Repeat("x", 5000) + `"}`
	client = newClient(t, respond(&recorder{}, http.StatusBadRequest, long, nil))

	_, err = client.ListEnvironments(context.Background())
	if got := len([]rune(asAPIError(t, err).Message)); got > maxMessageRunes+3 {
		t.Fatalf("message is %d runes, want at most %d", got, maxMessageRunes+3)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var followed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/environments", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	})
	client := newClient(t, mux)

	_, err := client.ListEnvironments(context.Background())
	apiErr := asAPIError(t, err)

	if followed.Load() {
		t.Fatal("the client followed a redirect and would have replayed the credential")
	}
	if apiErr.Status != http.StatusFound || apiErr.Location != "/elsewhere" {
		t.Fatalf("status = %d, location = %q", apiErr.Status, apiErr.Location)
	}
	if clierr.ExitCode(err) != clierr.ExitUsage {
		t.Errorf("exit code = %d, want %d", clierr.ExitCode(err), clierr.ExitUsage)
	}
	if !strings.Contains(clierr.Hint(err), "/elsewhere") {
		t.Errorf("hint = %q, want the redirect target", clierr.Hint(err))
	}
}

func TestContextCancellationPropagates(t *testing.T) {
	t.Run("cancelled while the request is in flight", func(t *testing.T) {
		started := make(chan struct{})
		var once sync.Once
		client := newClient(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			once.Do(func() { close(started) })
			<-r.Context().Done()
		}))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-started
			cancel()
		}()

		_, err := client.ListEnvironments(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if got := clierr.ExitCode(err); got != clierr.ExitInterrupted {
			t.Fatalf("exit code = %d, want %d", got, clierr.ExitInterrupted)
		}
	})

	t.Run("already cancelled sends nothing", func(t *testing.T) {
		rec := &recorder{}
		client := newClient(t, respond(rec, http.StatusOK, `[]`, nil))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.ListEnvironments(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if n := len(rec.all()); n != 0 {
			t.Fatalf("server received %d requests, want 0", n)
		}
	})
}

func TestRequestTimeouts(t *testing.T) {
	block := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	})

	t.Run("client request timeout", func(t *testing.T) {
		server := httptest.NewServer(block)
		t.Cleanup(server.Close)

		client, err := NewHTTPClient(config.Target{Server: server.URL, Token: testToken, RequestTimeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}

		_, err = client.ListEnvironments(context.Background())
		if err == nil {
			t.Fatal("ListHosts succeeded against a stalled server")
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("a timeout must not be reported as an interrupt: %v", err)
		}
		if got := clierr.ExitCode(err); got != clierr.ExitUnavailable {
			t.Errorf("exit code = %d, want %d", got, clierr.ExitUnavailable)
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("message = %q", err.Error())
		}
		if !strings.Contains(clierr.Hint(err), "--request-timeout") {
			t.Errorf("hint = %q", clierr.Hint(err))
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		client := newClient(t, block)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_, err := client.ListEnvironments(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
		if got := clierr.ExitCode(err); got != clierr.ExitUnavailable {
			t.Errorf("exit code = %d, want %d", got, clierr.ExitUnavailable)
		}
		if !strings.Contains(err.Error(), "command deadline") {
			t.Errorf("message = %q", err.Error())
		}
	})
}

func TestUnreachableServerErrorsAreClearAndSafe(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()

	client, err := NewHTTPClient(config.Target{Server: address, Token: testToken, RequestTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.ListEnvironments(context.Background())
	if err == nil {
		t.Fatal("ListHosts succeeded against a closed server")
	}
	if got := clierr.ExitCode(err); got != clierr.ExitUnavailable {
		t.Errorf("exit code = %d, want %d", got, clierr.ExitUnavailable)
	}
	if !strings.Contains(err.Error(), "cannot reach the API server at "+address) {
		t.Errorf("message = %q", err.Error())
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "/api/v1/environments") {
		t.Errorf("message should neither leak the credential nor repeat the URL: %q", err.Error())
	}
	if clierr.Hint(err) == "" {
		t.Error("expected a hint")
	}
	// The hint must name only commands that exist (get-contexts was removed in M2).
	if strings.Contains(clierr.Hint(err), "get-contexts") || !strings.Contains(clierr.Hint(err), "machinarchy whoami") {
		t.Errorf("hint = %q", clierr.Hint(err))
	}
}

type closeTracker struct {
	io.Reader
	closes atomic.Int32
}

func (c *closeTracker) Close() error {
	c.closes.Add(1)
	return nil
}

type stubTransport struct {
	status  int
	body    string
	tracker *closeTracker
}

func (s *stubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.tracker = &closeTracker{Reader: strings.NewReader(s.body)}

	return &http.Response{
		StatusCode: s.status,
		Header:     make(http.Header),
		Body:       s.tracker,
		Request:    request,
	}, nil
}

func TestResponseBodiesAreAlwaysClosed(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"success", 200, `[]`, false},
		{"error status", 500, `{"error":"boom"}`, true},
		{"invalid JSON", 200, `<html>`, true},
		{"empty body where one is required", 200, ``, true},
		{"oversized success", 200, strings.Repeat("x", 64), true},
		{"oversized error", 500, strings.Repeat("x", maxErrorBodyBytes*2), true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &stubTransport{status: test.status, body: test.body}
			client, err := NewHTTPClient(
				config.Target{Server: "http://api.example.test", Token: testToken},
				WithHTTPClient(&http.Client{Transport: transport}),
				withMaxResponseBytes(32),
			)
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.ListEnvironments(context.Background())
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
			if transport.tracker == nil {
				t.Fatal("transport was never used")
			}
			if got := transport.tracker.closes.Load(); got != 1 {
				t.Fatalf("response body closed %d times, want exactly 1", got)
			}
		})
	}
}

func TestOversizedResponses(t *testing.T) {
	t.Run("successful response over the limit is rejected", func(t *testing.T) {
		client := newClient(t, respond(&recorder{}, http.StatusOK, `[`+strings.Repeat(" ", 100)+`]`, nil), withMaxResponseBytes(50))

		_, err := client.ListEnvironments(context.Background())
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v, want a size error", err)
		}
		if got := clierr.ExitCode(err); got != clierr.ExitUnavailable {
			t.Errorf("exit code = %d", got)
		}
	})

	t.Run("an oversized error body still yields the API error", func(t *testing.T) {
		body := strings.Repeat("x", maxErrorBodyBytes*2)
		client := newClient(t, respond(&recorder{}, http.StatusInternalServerError, body, nil))

		_, err := client.ListEnvironments(context.Background())
		if apiErr := asAPIError(t, err); apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("status = %d", apiErr.Status)
		}
	})
}

func TestEmptyAndInvalidSuccessBodies(t *testing.T) {
	ctx := context.Background()

	t.Run("accepted with no body", func(t *testing.T) {
		client := newClient(t, respond(&recorder{}, http.StatusAccepted, ``, nil))

		raw, err := client.RuntimeConfig(ctx)
		if err != nil || len(raw) != 0 {
			t.Fatalf("raw = %q, err = %v, want empty and nil", raw, err)
		}
	})

	t.Run("no content from a delete", func(t *testing.T) {
		client := newClient(t, respond(&recorder{}, http.StatusNoContent, ``, nil))

		if err := client.DeleteSecret(ctx, "s1"); err != nil {
			t.Fatalf("DeleteSecret: %v", err)
		}
	})

	t.Run("ok with no body is an error", func(t *testing.T) {
		client := newClient(t, respond(&recorder{}, http.StatusOK, ``, nil))

		_, err := client.ListEnvironments(ctx)
		if err == nil || clierr.ExitCode(err) != clierr.ExitUnavailable {
			t.Fatalf("error = %v (exit %d)", err, clierr.ExitCode(err))
		}
	})

	t.Run("html where JSON is expected", func(t *testing.T) {
		client := newClient(t, respond(&recorder{}, http.StatusOK, `<html>login</html>`, nil))

		_, err := client.ListEnvironments(ctx)
		if err == nil || clierr.ExitCode(err) != clierr.ExitUnavailable {
			t.Fatalf("error = %v (exit %d)", err, clierr.ExitCode(err))
		}
		if !strings.Contains(clierr.Hint(err), "Machinarchy API") {
			t.Errorf("hint = %q", clierr.Hint(err))
		}
	})
}

func TestIdentifiersCannotChangeTheRoute(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	client := newClient(t, respond(rec, http.StatusOK, `{}`, nil))

	t.Run("reserved characters stay inside one segment", func(t *testing.T) {
		tests := map[string]string{
			"a/b":    "/api/v1/secrets/a%2Fb/versions",
			"a b?c":  "/api/v1/secrets/a%20b%3Fc/versions",
			"a#frag": "/api/v1/secrets/a%23frag/versions",
			"100%":   "/api/v1/secrets/100%25/versions",
		}
		for id, wantURI := range tests {
			if _, err := client.SecretVersions(ctx, id); err != nil {
				t.Fatalf("SecretVersions(%q): %v", id, err)
			}
			if got := rec.last(t).RequestURI; got != wantURI {
				t.Errorf("id %q produced %s, want %s", id, got, wantURI)
			}
		}
	})

	t.Run("dot and empty identifiers are rejected before any request", func(t *testing.T) {
		before := len(rec.all())
		for _, id := range []string{"", ".", ".."} {
			_, err := client.SecretVersions(ctx, id)
			if err == nil {
				t.Errorf("SecretVersions(%q) succeeded", id)
				continue
			}
			if got := clierr.ExitCode(err); got != clierr.ExitUsage {
				t.Errorf("SecretVersions(%q) exit code = %d, want %d", id, got, clierr.ExitUsage)
			}
		}
		if after := len(rec.all()); after != before {
			t.Errorf("%d requests were sent for invalid identifiers", after-before)
		}
	})
}

func TestServerPathPrefixIsPreserved(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(respond(rec, http.StatusOK, `[]`, nil))
	t.Cleanup(server.Close)

	client, err := NewHTTPClient(config.Target{Server: server.URL + "/gateway/", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConfigHistory(context.Background(), 4); err != nil {
		t.Fatal(err)
	}

	request := rec.last(t)
	if request.Path != "/gateway/api/v1/runtime-config/revisions" || request.RawQuery != "before=4" {
		t.Fatalf("request = %s ?%s", request.Path, request.RawQuery)
	}
}

func TestNewHTTPClientValidation(t *testing.T) {
	valid := config.Target{Server: "http://localhost:8080", Token: testToken}

	t.Run("rejects bad input as usage errors", func(t *testing.T) {
		tests := map[string]config.Target{
			"bad server":      {Server: "localhost", Token: testToken},
			"empty token":     {Server: valid.Server},
			"spaced token":    {Server: valid.Server, Token: "a b"},
			"newline token":   {Server: valid.Server, Token: "abc\r\nX-Injected: 1"},
			"negative budget": {Server: valid.Server, Token: testToken, RequestTimeout: -time.Second},
		}
		for name, target := range tests {
			_, err := NewHTTPClient(target)
			if err == nil {
				t.Errorf("%s: NewHTTPClient succeeded", name)
				continue
			}
			if got := clierr.ExitCode(err); got != clierr.ExitUsage {
				t.Errorf("%s: exit code = %d, want %d", name, got, clierr.ExitUsage)
			}
		}
	})

	t.Run("timeout defaults and overrides", func(t *testing.T) {
		client, err := NewHTTPClient(valid)
		if err != nil {
			t.Fatal(err)
		}
		if client.http.Timeout != config.DefaultRequestTimeout {
			t.Errorf("default timeout = %s", client.http.Timeout)
		}

		target := valid
		target.RequestTimeout = 3 * time.Second
		client, err = NewHTTPClient(target)
		if err != nil {
			t.Fatal(err)
		}
		if client.http.Timeout != 3*time.Second {
			t.Errorf("timeout = %s, want 3s", client.http.Timeout)
		}
	})

	t.Run("a supplied http.Client is copied, not mutated", func(t *testing.T) {
		custom := &http.Client{Timeout: 9 * time.Second}

		client, err := NewHTTPClient(valid, WithHTTPClient(custom))
		if err != nil {
			t.Fatal(err)
		}
		if custom.CheckRedirect != nil {
			t.Error("the caller's http.Client was modified")
		}
		if client.http == custom {
			t.Error("the client must work on a copy")
		}
		if client.http.Timeout != 9*time.Second {
			t.Errorf("timeout = %s, want the supplied 9s", client.http.Timeout)
		}
		if client.http.CheckRedirect == nil {
			t.Error("redirect following must be disabled")
		}
	})

	t.Run("user agent can be overridden", func(t *testing.T) {
		rec := &recorder{}
		client := newClient(t, respond(rec, http.StatusOK, `[]`, nil), WithUserAgent("custom/1.0"))
		if _, err := client.ListEnvironments(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := rec.last(t).UserAgent; got != "custom/1.0" {
			t.Errorf("User-Agent = %q", got)
		}
	})
}

func TestMisuseIsReportedNotPanicked(t *testing.T) {
	var uninitialised *HTTPClient
	if _, err := uninitialised.ListEnvironments(context.Background()); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("nil client error = %v", err)
	}

	client := newClient(t, respond(&recorder{}, http.StatusOK, `[]`, nil))

	var noContext context.Context
	if _, err := client.ListEnvironments(noContext); err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Errorf("nil context error = %v", err)
	}
}

func TestAPIErrorExitCodes(t *testing.T) {
	tests := map[int]int{
		301: clierr.ExitUsage,
		400: clierr.ExitUsage,
		401: clierr.ExitAuth,
		403: clierr.ExitAuth,
		404: clierr.ExitNotFound,
		405: clierr.ExitUsage,
		408: clierr.ExitUnavailable,
		409: clierr.ExitConflict,
		412: clierr.ExitConflict,
		422: clierr.ExitUsage,
		429: clierr.ExitUnavailable,
		500: clierr.ExitUnavailable,
		502: clierr.ExitUnavailable,
		503: clierr.ExitUnavailable,
	}

	for status, want := range tests {
		if got := (&Error{Status: status}).ExitCode(); got != want {
			t.Errorf("status %d: exit code = %d, want %d", status, got, want)
		}
	}
}

func TestTargetOutputRequest(t *testing.T) {
	var got *http.Request
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"text":"tail\n","from":42,"next":47,"live":true}`))
	}))
	out, err := client.TargetOutput(context.Background(), "o 1", "h1", "stderr", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.EscapedPath() != "/api/v1/managed/operations/o%201/targets/h1/output" ||
		got.URL.Query().Get("stream") != "stderr" || got.URL.Query().Get("from") != "42" {
		t.Fatalf("request = %s", got.URL)
	}
	if out.Text != "tail\n" || out.Next != 47 || !out.Live {
		t.Fatalf("decoded = %+v", out)
	}
}

// K-04 (version skew, no C-11): JSON of the wrong shape names an update; text
// that is not JSON names the address. Both are exit 4.
func TestDecodeFailureSaysWhetherToUpdateOrCheckTheAddress(t *testing.T) {
	var target struct {
		Items []string `json:"items"`
	}
	skew := decodeResponse(200, "/api/v1/x", []byte(`{"items":{"a":1}}`), &target)
	page := decodeResponse(200, "/api/v1/x", []byte(`<html>sign in</html>`), &target)

	for name, tc := range map[string]struct {
		err  error
		want string
	}{"wrong shape": {skew, "Update the CLI"}, "not JSON": {page, "points at the Machinarchy API"}} {
		var coded *clierr.Error
		if !errors.As(tc.err, &coded) || coded.ExitCode() != clierr.ExitUnavailable || !strings.Contains(coded.Hint, tc.want) {
			t.Errorf("%s: %v (want hint containing %q)", name, tc.err, tc.want)
		}
	}
}
